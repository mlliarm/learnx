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

// Elasticsearch runs ES|QL exercises on a single-node cluster in Docker, or
// on the cluster at LEARNX_ES_URL (which must have security disabled and is
// wiped before every run). Setup is written as Kibana Dev Tools console
// requests; the query is ES|QL.
type Elasticsearch struct {
	image  string
	status io.Writer
	http   *http.Client

	mu       sync.Mutex // runs share one cluster, so they take turns
	url      string     // set once the cluster is reachable
	baseline map[string]bool
}

func NewElasticsearch(image string, status io.Writer) (*Elasticsearch, error) {
	es := &Elasticsearch{image: image, status: status, http: &http.Client{Timeout: 60 * time.Second}}
	if u := os.Getenv("LEARNX_ES_URL"); u != "" {
		es.url = strings.TrimSuffix(u, "/")
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
		if err != nil {
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

// reset deletes everything earlier runs created.
func (es *Elasticsearch) reset(ctx context.Context) error {
	if _, err := es.do(ctx, http.MethodDelete, "/_data_stream/*?expand_wildcards=all", "", nil); err != nil && !IsQueryError(err) {
		return err
	}
	if _, err := es.do(ctx, http.MethodDelete, "/*?expand_wildcards=open,closed", "", nil); err != nil && !IsQueryError(err) {
		return err
	}
	names, err := es.templates(ctx)
	if err != nil {
		return err
	}
	for _, n := range names {
		if !es.baseline[n] {
			if _, err := es.do(ctx, http.MethodDelete, "/_index_template/"+n, "", nil); err != nil && !IsQueryError(err) {
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
	req, err := http.NewRequestWithContext(ctx, method, es.url+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := es.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 && res.StatusCode < 500 {
		return nil, &QueryError{Msg: esReason(raw)}
	}
	if res.StatusCode >= 500 {
		return nil, fmt.Errorf("elasticsearch %s %s: HTTP %d: %s", method, path, res.StatusCode, esReason(raw))
	}
	return raw, nil
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
