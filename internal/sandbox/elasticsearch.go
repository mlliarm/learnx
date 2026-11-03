package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// esContainer is reused across learnx runs: Elasticsearch takes ~30s to
// start, so it is left running. `docker rm -f learnx-elasticsearch` stops it.
const esContainer = "learnx-elasticsearch"

// sandboxMarker is a hidden index that marks a cluster as learnx's sandbox.
// Resets leave it alone (they only expand to visible indices), so a cluster
// that has it holds nothing but learnx's own leftovers.
const sandboxMarker = "learnx-sandbox"

// Elasticsearch runs ES|QL exercises on a single-node cluster in Docker, or
// on the cluster at LEARNX_ES_URL (which must have security disabled, allow
// wildcard deletes, and be empty or LEARNX_ES_WIPE=1, since it is wiped before
// every run). Setup is written as Kibana Dev Tools console
// requests; the query is ES|QL.
type Elasticsearch struct {
	image    string
	status   io.Writer
	http     *http.Client
	external bool // the cluster at LEARNX_ES_URL, not learnx's container

	mu       sync.Mutex // runs share one cluster, so they take turns
	url      string     // set once the cluster is reachable
	baseline map[string]bool
}

func NewElasticsearch(image string, status io.Writer) (*Elasticsearch, error) {
	es := &Elasticsearch{image: image, status: status, http: &http.Client{Timeout: 60 * time.Second}}
	if u := os.Getenv("LEARNX_ES_URL"); u != "" {
		es.url, es.external = strings.TrimSuffix(u, "/"), true
		return es, nil
	}
	if image == "" {
		return nil, errors.New("the pack names no Elasticsearch image and LEARNX_ES_URL is not set")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, errors.New("exercises need Docker to run Elasticsearch, or LEARNX_ES_URL pointing at a disposable cluster")
	}
	return es, nil
}

func (es *Elasticsearch) Name() string {
	if os.Getenv("LEARNX_ES_URL") != "" {
		return "elasticsearch at " + os.Getenv("LEARNX_ES_URL")
	}
	return "elasticsearch (docker)"
}

var esqlSort = regexp.MustCompile(`(?i)\|\s*SORT\b`)

// Ordered: ES|QL rows have no defined order unless the query sorts them.
func (es *Elasticsearch) Ordered(query string) bool { return esqlSort.MatchString(query) }

func (es *Elasticsearch) Run(ctx context.Context, setup, query string) (*Result, error) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if err := es.start(ctx); err != nil {
		return nil, err
	}
	release, err := es.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := es.reset(ctx); err != nil {
		return nil, fmt.Errorf("reset the sandbox: %w", err)
	}
	reqs, err := parseConsole(setup)
	if err != nil {
		return nil, &QueryError{Msg: "setup: " + err.Error()}
	}
	for i, r := range reqs {
		if err := es.setupRequest(ctx, r); err != nil {
			if IsQueryError(err) {
				return nil, &QueryError{Msg: fmt.Sprintf("setup request %d (%s %s): %v", i+1, r.method, r.path, err)}
			}
			return nil, err
		}
	}
	if _, err := es.do(ctx, http.MethodPost, "/_refresh", "", nil); err != nil {
		return nil, err
	}

	q := strings.TrimSpace(query)
	for strings.HasSuffix(q, ";") {
		q = strings.TrimSpace(strings.TrimSuffix(q, ";"))
	}
	if q == "" {
		return nil, &QueryError{Msg: "the query is empty"}
	}
	body, _ := json.Marshal(map[string]string{"query": q})
	raw, err := es.do(ctx, http.MethodPost, "/_query?format=json", "application/json", body)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out struct {
		Columns []Column `json:"columns"`
		Values  [][]any  `json:"values"`
	}
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("unreadable ES|QL response: %w", err)
	}
	return &Result{Columns: out.Columns, Rows: out.Values}, nil
}

// start finds or launches the cluster and waits until it answers.
func (es *Elasticsearch) start(ctx context.Context) error {
	if es.url != "" && es.baseline != nil {
		return nil
	}
	if es.url == "" {
		port, err := es.container(ctx)
		if err != nil {
			return err
		}
		es.url = "http://127.0.0.1:" + port
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		_, err := es.do(ctx, http.MethodGet, "/_cluster/health?wait_for_status=yellow&timeout=2s", "", nil)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("elasticsearch at %s did not become ready: %w", es.url, err)
		}
		time.Sleep(time.Second)
	}
	if err := es.claim(ctx); err != nil {
		return err
	}
	// Index templates that exist now are the cluster's own; any others were
	// made by an exercise and get removed between runs.
	names, err := es.templates(ctx)
	if err != nil {
		return err
	}
	es.baseline = map[string]bool{}
	for _, n := range names {
		es.baseline[n] = true
	}
	return nil
}

// lockTTL bounds how long a run may hold the cluster lock; an older lock is
// taken to belong to a learnx that died, and is taken over.
const lockTTL = 3 * time.Minute

const lockDoc = "/" + sandboxMarker + "/_doc/lock"

// lock takes the cluster-wide lock that keeps learnx processes sharing a
// cluster from wiping each other's exercise data mid-run. It is a document
// in the marker index, which only one process can create. Release deletes
// it only if it is still the same document, so a lock taken over as stale
// is never released by its old owner.
func (es *Elasticsearch) lock(ctx context.Context) (release func(), err error) {
	body, _ := json.Marshal(map[string]string{"owner": es.Name(), "at": time.Now().UTC().Format(time.RFC3339Nano)})
	waiting := false
	for {
		status, raw, err := es.send(ctx, http.MethodPut, "/"+sandboxMarker+"/_create/lock", "application/json", body)
		if err != nil {
			return nil, fmt.Errorf("lock the sandbox: %v", err)
		}
		switch {
		case status == http.StatusCreated:
			var doc struct {
				SeqNo       int64 `json:"_seq_no"`
				PrimaryTerm int64 `json:"_primary_term"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				return nil, fmt.Errorf("lock the sandbox: %w", err)
			}
			return func() {
				// A fresh context: the run's may already be cancelled.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				es.send(ctx, http.MethodDelete, fmt.Sprintf("%s?if_seq_no=%d&if_primary_term=%d", lockDoc, doc.SeqNo, doc.PrimaryTerm), "", nil)
			}, nil
		case status != http.StatusConflict:
			return nil, fmt.Errorf("lock the sandbox: HTTP %d: %s", status, esReason(raw))
		}
		if err := es.breakStaleLock(ctx); err != nil {
			return nil, err
		}
		if !waiting {
			es.logf("· waiting for another learnx using the sandbox…")
			waiting = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// breakStaleLock deletes the lock if it is older than lockTTL.
func (es *Elasticsearch) breakStaleLock(ctx context.Context) error {
	status, raw, err := es.send(ctx, http.MethodGet, lockDoc, "", nil)
	if err != nil {
		return fmt.Errorf("read the sandbox lock: %v", err)
	}
	if status == http.StatusNotFound {
		return nil // just released
	}
	var doc struct {
		SeqNo       int64 `json:"_seq_no"`
		PrimaryTerm int64 `json:"_primary_term"`
		Source      struct {
			At time.Time `json:"at"`
		} `json:"_source"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &doc) != nil {
		return fmt.Errorf("read the sandbox lock: HTTP %d: %s", status, esReason(raw))
	}
	if time.Since(doc.Source.At) < lockTTL {
		return nil
	}
	es.logf("· taking over a sandbox lock left since %s", doc.Source.At.Local().Format(time.TimeOnly))
	// Conditional, so that only one waiter breaks it.
	_, _, err = es.send(ctx, http.MethodDelete, fmt.Sprintf("%s?if_seq_no=%d&if_primary_term=%d", lockDoc, doc.SeqNo, doc.PrimaryTerm), "", nil)
	return err
}

// claim makes sure the cluster can be wiped before every run, then marks it
// as learnx's sandbox. A cluster at LEARNX_ES_URL that isn't marked yet must
// hold no indices or data streams, unless LEARNX_ES_WIPE=1 allows deleting
// them; it must also allow wildcard deletes, or resets would fail.
func (es *Elasticsearch) claim(ctx context.Context) error {
	if es.external {
		if err := es.checkWildcardDeletes(ctx); err != nil {
			return err
		}
	}
	marked, err := es.exists(ctx, "/"+sandboxMarker)
	if err != nil || marked {
		return err
	}
	if es.external && os.Getenv("LEARNX_ES_WIPE") != "1" {
		names, err := es.userData(ctx)
		if err != nil {
			return err
		}
		if len(names) > 0 {
			if len(names) > 5 {
				names = append(names[:5], "…")
			}
			return fmt.Errorf("the cluster at LEARNX_ES_URL holds indices or data streams (%s), and learnx deletes all of them before every exercise; "+
				"point LEARNX_ES_URL at an empty, disposable cluster, or set LEARNX_ES_WIPE=1 to let learnx delete them", strings.Join(names, ", "))
		}
	}
	body := []byte(`{"settings": {"index.hidden": true, "index.number_of_replicas": 0}}`)
	if _, err := es.do(ctx, http.MethodPut, "/"+sandboxMarker, "application/json", body); err != nil &&
		!strings.Contains(err.Error(), "resource_already_exists_exception") {
		return fmt.Errorf("mark the cluster as learnx's sandbox: %v", err)
	}
	return nil
}

// checkWildcardDeletes fails unless action.destructive_requires_name is
// false, which the reset's DELETE /* needs.
func (es *Elasticsearch) checkWildcardDeletes(ctx context.Context) error {
	raw, err := es.do(ctx, http.MethodGet, "/_cluster/settings?include_defaults=true&filter_path=*.action.destructive_requires_name", "", nil)
	if err != nil {
		return fmt.Errorf("read the cluster settings at LEARNX_ES_URL: %v", err)
	}
	var out map[string]struct {
		Action struct {
			DestructiveRequiresName string `json:"destructive_requires_name"`
		} `json:"action"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("unreadable cluster settings: %w", err)
	}
	// Unset means true, the default since Elasticsearch 8; transient beats
	// persistent, which beats the node's own settings.
	value := "true"
	for _, level := range []string{"defaults", "persistent", "transient"} {
		if v := out[level].Action.DestructiveRequiresName; v != "" {
			value = v
		}
	}
	if value != "false" {
		return errors.New("the cluster at LEARNX_ES_URL refuses wildcard deletes, which learnx uses to wipe it before every exercise; " +
			"use a disposable cluster with action.destructive_requires_name set to false")
	}
	return nil
}

// userData lists the visible indices and data streams on the cluster.
func (es *Elasticsearch) userData(ctx context.Context) ([]string, error) {
	raw, err := es.do(ctx, http.MethodGet, "/_resolve/index/*?expand_wildcards=open,closed", "", nil)
	if err != nil {
		return nil, fmt.Errorf("list the indices at LEARNX_ES_URL: %v", err)
	}
	var out struct {
		Indices     []struct{ Name string } `json:"indices"`
		DataStreams []struct{ Name string } `json:"data_streams"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unreadable index list: %w", err)
	}
	var names []string
	for _, ds := range out.DataStreams {
		names = append(names, ds.Name)
	}
	for _, ix := range out.Indices {
		names = append(names, ix.Name)
	}
	return names, nil
}

// exists reports whether a HEAD request for path finds something.
func (es *Elasticsearch) exists(ctx context.Context, path string) (bool, error) {
	status, _, err := es.send(ctx, http.MethodHead, path, "", nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("elasticsearch HEAD %s: HTTP %d", path, status)
}

// container starts (or reuses) the sandbox container and returns its host port.
func (es *Elasticsearch) container(ctx context.Context) (string, error) {
	state, _ := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", esContainer).Output()
	switch strings.TrimSpace(string(state)) {
	case "true":
	case "false":
		if out, err := exec.CommandContext(ctx, "docker", "start", esContainer).CombinedOutput(); err != nil {
			return "", fmt.Errorf("docker start: %v: %s", err, out)
		}
		es.logf("· restarting the Elasticsearch sandbox…")
	default:
		es.logf("· starting the Elasticsearch sandbox (%s); the first start takes about 30s…", es.image)
		out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", esContainer,
			"-p", "127.0.0.1::9200",
			"-e", "discovery.type=single-node",
			"-e", "xpack.security.enabled=false",
			"-e", "xpack.ml.enabled=false",
			"-e", "action.destructive_requires_name=false",
			"-e", "ES_JAVA_OPTS=-Xms512m -Xmx512m",
			es.image).CombinedOutput()
		// Another learnx starting it at the same moment is fine.
		if err != nil && !strings.Contains(string(out), "is already in use") {
			return "", fmt.Errorf("docker run %s: %v: %s", es.image, err, out)
		}
	}
	out, err := exec.CommandContext(ctx, "docker", "port", esContainer, "9200/tcp").Output()
	if err != nil {
		return "", fmt.Errorf("docker port: %w", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	i := strings.LastIndex(line, ":")
	if i < 0 {
		return "", fmt.Errorf("unexpected docker port output %q", out)
	}
	return line[i+1:], nil
}

// reset deletes everything earlier runs created. Any failure stops the run:
// left in place, old data would mix with the exercise's. Errors are never
// a *QueryError, which would blame the learner's query.
func (es *Elasticsearch) reset(ctx context.Context) error {
	del := func(path string) error {
		status, raw, err := es.send(ctx, http.MethodDelete, path, "", nil)
		switch {
		case err != nil:
			return fmt.Errorf("DELETE %s: %v", path, err)
		case status == http.StatusNotFound:
			// Already gone, e.g. removed by another learnx sharing the cluster.
		case status >= 400:
			return fmt.Errorf("DELETE %s: HTTP %d: %s", path, status, esReason(raw))
		}
		return nil
	}
	if err := del("/_data_stream/*?expand_wildcards=all"); err != nil {
		return err
	}
	if err := del("/*?expand_wildcards=open,closed"); err != nil {
		return err
	}
	names, err := es.templates(ctx)
	if err != nil {
		return fmt.Errorf("list index templates: %v", err)
	}
	for _, n := range names {
		if !es.baseline[n] {
			if err := del("/_index_template/" + n); err != nil {
				return err
			}
		}
	}
	return nil
}

func (es *Elasticsearch) templates(ctx context.Context) ([]string, error) {
	raw, err := es.do(ctx, http.MethodGet, "/_index_template", "", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		IndexTemplates []struct {
			Name string `json:"name"`
		} `json:"index_templates"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	names := make([]string, len(out.IndexTemplates))
	for i, t := range out.IndexTemplates {
		names[i] = t.Name
	}
	return names, nil
}

func (es *Elasticsearch) setupRequest(ctx context.Context, r consoleRequest) error {
	ctype := "application/json"
	if r.bulk() {
		ctype = "application/x-ndjson"
	}
	raw, err := es.do(ctx, r.method, r.path, ctype, r.body)
	if err != nil {
		return err
	}
	if r.bulk() {
		// _bulk answers 200 even when items fail.
		var out struct {
			Errors bool `json:"errors"`
			Items  []map[string]struct {
				Error *struct {
					Type   string `json:"type"`
					Reason string `json:"reason"`
				} `json:"error"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &out) == nil && out.Errors {
			for i, item := range out.Items {
				for _, res := range item {
					if res.Error != nil {
						return &QueryError{Msg: fmt.Sprintf("bulk item %d: %s: %s", i+1, res.Error.Type, res.Error.Reason)}
					}
				}
			}
			return &QueryError{Msg: "some bulk items failed"}
		}
	}
	return nil
}

// do sends one request. 4xx answers are mistakes in what was sent and come
// back as *QueryError with Elasticsearch's reason.
func (es *Elasticsearch) do(ctx context.Context, method, path, ctype string, body []byte) ([]byte, error) {
	status, raw, err := es.send(ctx, method, path, ctype, body)
	if err != nil {
		return nil, err
	}
	if status >= 400 && status < 500 {
		return nil, &QueryError{Msg: esReason(raw)}
	}
	if status >= 500 {
		return nil, fmt.Errorf("elasticsearch %s %s: HTTP %d: %s", method, path, status, esReason(raw))
	}
	return raw, nil
}

// send sends one request and returns the status and body as they are.
func (es *Elasticsearch) send(ctx context.Context, method, path, ctype string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, es.url+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := es.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, raw, nil
}

func esReason(raw []byte) string {
	var out struct {
		Error struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &out) == nil && out.Error.Reason != "" {
		return out.Error.Type + ": " + out.Error.Reason
	}
	return strings.TrimSpace(string(raw))
}

func (es *Elasticsearch) logf(format string, args ...any) {
	if es.status != nil {
		fmt.Fprintf(es.status, format+"\n", args...)
	}
}

// consoleRequest is one request in Kibana Dev Tools console syntax:
//
//	PUT auth-events
//	{"mappings": {...}}
//
//	POST auth-events/_bulk
//	{"index": {}}
//	{"user": {"name": "alice"}}
type consoleRequest struct {
	method, path string
	body         []byte
}

func (r consoleRequest) bulk() bool { return strings.Contains(r.path, "_bulk") }

var consoleLine = regexp.MustCompile(`^(GET|PUT|POST|DELETE|HEAD)\s+(\S+)\s*$`)

func parseConsole(s string) ([]consoleRequest, error) {
	var reqs []consoleRequest
	var body []string
	flush := func() error {
		if len(reqs) == 0 {
			if strings.TrimSpace(strings.Join(body, "")) != "" {
				return errors.New("a request body comes before any request line such as `PUT my-index`")
			}
			return nil
		}
		r := &reqs[len(reqs)-1]
		b, err := normalizeBody(strings.Join(body, "\n"), r.bulk())
		if err != nil {
			return fmt.Errorf("%s %s: %w", r.method, r.path, err)
		}
		r.body = b
		body = nil
		return nil
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		if m := consoleLine.FindStringSubmatch(t); m != nil {
			if err := flush(); err != nil {
				return nil, err
			}
			path := m[2]
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			reqs = append(reqs, consoleRequest{method: m[1], path: path})
			continue
		}
		body = append(body, line)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return reqs, nil
}

// normalizeBody re-encodes the JSON in a body: one compact document per line
// for _bulk (so pretty-printed bulk lines still work), one document otherwise.
func normalizeBody(text string, bulk bool) ([]byte, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	var out bytes.Buffer
	n := 0
	for {
		var v json.RawMessage
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid JSON body: %w", err)
		}
		n++
		if !bulk && n > 1 {
			return nil, errors.New("more than one JSON document in the body")
		}
		if err := json.Compact(&out, v); err != nil {
			return nil, err
		}
		if bulk {
			out.WriteByte('\n')
		}
	}
	if n == 0 {
		return nil, nil
	}
	return out.Bytes(), nil
}
