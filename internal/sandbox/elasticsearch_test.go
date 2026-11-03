package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mlliarm/learnx/internal/pack"
)

func TestParseConsole(t *testing.T) {
	reqs, err := parseConsole(`
# comments and blank lines are ignored
PUT auth-events
{
  "mappings": {"properties": {"user.name": {"type": "keyword"}}}
}

POST auth-events/_bulk
{"index": {}}
{
  "user": {"name": "alice"}
}
{"index": {}}
{"user": {"name": "bob"}}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[0].method != "PUT" || reqs[0].path != "/auth-events" || reqs[1].path != "/auth-events/_bulk" {
		t.Fatalf("reqs = %+v", reqs)
	}
	if got := string(reqs[0].body); got != `{"mappings":{"properties":{"user.name":{"type":"keyword"}}}}` {
		t.Errorf("PUT body = %s", got)
	}
	want := "{\"index\":{}}\n{\"user\":{\"name\":\"alice\"}}\n{\"index\":{}}\n{\"user\":{\"name\":\"bob\"}}\n"
	if got := string(reqs[1].body); got != want {
		t.Errorf("bulk body = %q, want %q", got, want)
	}

	for _, bad := range []string{`{"orphan": true}`, "PUT x\n{not json}", "PUT x\n{}\n{}"} {
		if _, err := parseConsole(bad); err == nil {
			t.Errorf("parseConsole(%q) accepted", bad)
		}
	}
}

func TestESQLOrdered(t *testing.T) {
	es := &Elasticsearch{}
	if !es.Ordered("FROM logs | STATS c = COUNT(*) BY host.name | SORT c DESC") || es.Ordered("FROM logs | KEEP host.name") {
		t.Error("SORT detection is wrong")
	}
}

// Runs a real single-node cluster in Docker (or LEARNX_ES_URL); slow on the
// first run, skipped with -short.
func TestElasticsearchRun(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Elasticsearch")
	}
	p, err := pack.Load("elastic-security")
	if err != nil {
		t.Fatal(err)
	}
	var status strings.Builder
	sb, err := Open(p, &status)
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	setup := `PUT auth-events
{"mappings": {"properties": {"@timestamp": {"type": "date"}, "user.name": {"type": "keyword"}, "source.ip": {"type": "ip"}, "event.outcome": {"type": "keyword"}}}}

POST auth-events/_bulk
{"index": {}}
{"@timestamp": "2026-10-01T10:00:00Z", "user": {"name": "alice"}, "source": {"ip": "10.0.0.5"}, "event": {"outcome": "failure"}}
{"index": {}}
{"@timestamp": "2026-10-01T10:01:00Z", "user": {"name": "alice"}, "source": {"ip": "10.0.0.5"}, "event": {"outcome": "failure"}}
{"index": {}}
{"@timestamp": "2026-10-01T10:02:00Z", "user": {"name": "bob"}, "source": {"ip": "10.0.0.9"}, "event": {"outcome": "success"}}`

	res, err := sb.Run(ctx, setup, `FROM auth-events | WHERE event.outcome == "failure" | STATS failures = COUNT(*) BY user.name | SORT failures DESC`)
	if err != nil {
		t.Fatalf("%s: %v\nstatus:\n%s", sb.Name(), err, status.String())
	}
	if len(res.Columns) != 2 || res.Columns[0].Name != "failures" || len(res.Rows) != 1 ||
		fmt.Sprint(res.Rows[0]) != "[2 alice]" {
		t.Errorf("result = %+v", res)
	}

	_, err = sb.Run(ctx, setup, "FROM auth-events | STATS c = COUNT(*) BY nosuch")
	if !IsQueryError(err) || !strings.Contains(err.Error(), "Unknown column") {
		t.Errorf("bad ES|QL: %v", err)
	}
	_, err = sb.Run(ctx, "POST auth-events/_bulk\n{\"index\": {}}\n{\"n\": \"x\"}\n{\"index\": {}}\n{\"n\": {\"nested\": 1}}", "FROM auth-events")
	if !IsQueryError(err) || !strings.Contains(err.Error(), "bulk item 2") {
		t.Errorf("failing bulk item not reported: %v", err)
	}

	// Every run starts clean: indices and templates from earlier runs are gone.
	_, err = sb.Run(ctx, `PUT _index_template/exercise
{"index_patterns": ["ex-*"], "template": {"mappings": {"properties": {"n": {"type": "keyword"}}}}}`, "ROW a = 1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Run(ctx, "", "FROM auth-events | LIMIT 1"); !IsQueryError(err) {
		t.Errorf("index survived a reset: %v", err)
	}
	es := sb.(*Elasticsearch)
	names, _ := es.templates(ctx)
	for _, n := range names {
		if n == "exercise" {
			t.Error("index template survived a reset")
		}
	}
}

// fakeES answers the requests the cluster guard and reset send.
type fakeES struct {
	settings  string // body of GET /_cluster/settings
	indices   []string
	marked    bool
	deleteErr bool   // refuse wildcard deletes
	vanished  string // an index template that is listed but already deleted
	requests  []string
}

func (f *fakeES) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	switch {
	case r.URL.Path == "/_cluster/health":
		fmt.Fprint(w, `{"status":"green"}`)
	case r.URL.Path == "/_cluster/settings":
		fmt.Fprint(w, f.settings)
	case r.URL.Path == "/"+sandboxMarker && r.Method == http.MethodHead:
		if !f.marked {
			w.WriteHeader(http.StatusNotFound)
		}
	case r.URL.Path == "/"+sandboxMarker && r.Method == http.MethodPut:
		f.marked = true
		fmt.Fprint(w, `{"acknowledged":true}`)
	case strings.HasPrefix(r.URL.Path, "/_resolve/index/"):
		var ix []string
		for _, n := range f.indices {
			ix = append(ix, fmt.Sprintf(`{"name":%q}`, n))
		}
		fmt.Fprintf(w, `{"indices":[%s],"aliases":[],"data_streams":[]}`, strings.Join(ix, ","))
	case r.URL.Path == "/_index_template" && f.vanished != "":
		fmt.Fprintf(w, `{"index_templates":[{"name":%q}]}`, f.vanished)
	case r.URL.Path == "/_index_template":
		fmt.Fprint(w, `{"index_templates":[]}`)
	case r.URL.Path == "/_index_template/"+f.vanished:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"type":"index_template_missing_exception","reason":"index_template [x] missing"}}`)
	case r.URL.Path == "/_refresh":
		fmt.Fprint(w, `{"_shards":{}}`)
	case r.URL.Path == "/_query":
		fmt.Fprint(w, `{"columns":[{"name":"a","type":"integer"}],"values":[[1]]}`)
	case r.Method == http.MethodDelete && f.deleteErr:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"type":"illegal_argument_exception","reason":"Wildcard expressions or all indices are not allowed"}}`)
	case r.Method == http.MethodDelete:
		fmt.Fprint(w, `{"acknowledged":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

const wildcardDeletesAllowed = `{"defaults":{"action":{"destructive_requires_name":"false"}}}`

func externalES(t *testing.T, f *fakeES) *Elasticsearch {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("LEARNX_ES_URL", srv.URL)
	es, err := NewElasticsearch("", nil)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestExternalClusterGuard(t *testing.T) {
	ctx := context.Background()

	t.Run("refuses a cluster with data", func(t *testing.T) {
		f := &fakeES{settings: wildcardDeletesAllowed, indices: []string{"orders", "customers"}}
		err := externalES(t, f).start(ctx)
		if err == nil || !strings.Contains(err.Error(), "orders, customers") || !strings.Contains(err.Error(), "LEARNX_ES_WIPE=1") {
			t.Fatalf("err = %v", err)
		}
		if f.marked {
			t.Error("cluster marked despite the refusal")
		}
	})

	t.Run("LEARNX_ES_WIPE=1 allows it", func(t *testing.T) {
		t.Setenv("LEARNX_ES_WIPE", "1")
		f := &fakeES{settings: wildcardDeletesAllowed, indices: []string{"orders"}}
		if err := externalES(t, f).start(ctx); err != nil {
			t.Fatal(err)
		}
		if !f.marked {
			t.Error("cluster not marked")
		}
	})

	t.Run("an empty cluster is claimed", func(t *testing.T) {
		f := &fakeES{settings: wildcardDeletesAllowed}
		if err := externalES(t, f).start(ctx); err != nil || !f.marked {
			t.Fatalf("err = %v, marked = %v", err, f.marked)
		}
	})

	t.Run("a marked cluster's leftovers are learnx's own", func(t *testing.T) {
		f := &fakeES{settings: wildcardDeletesAllowed, indices: []string{"auth-events"}, marked: true}
		if err := externalES(t, f).start(ctx); err != nil {
			t.Fatal(err)
		}
		for _, r := range f.requests {
			if strings.HasPrefix(r, "GET /_resolve") || strings.HasPrefix(r, "PUT ") {
				t.Errorf("unexpected request %s", r)
			}
		}
	})

	for name, settings := range map[string]string{
		"unset (the default)":    `{}`,
		"set to true":            `{"persistent":{"action":{"destructive_requires_name":"true"}}}`,
		"transient overrides it": `{"defaults":{"action":{"destructive_requires_name":"false"}},"transient":{"action":{"destructive_requires_name":"true"}}}`,
	} {
		t.Run("refuses wildcard deletes "+name, func(t *testing.T) {
			f := &fakeES{settings: settings, marked: true}
			err := externalES(t, f).start(ctx)
			if err == nil || !strings.Contains(err.Error(), "destructive_requires_name") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestResetFailureStopsTheRun(t *testing.T) {
	f := &fakeES{settings: wildcardDeletesAllowed, marked: true}
	es := externalES(t, f)
	f.deleteErr = true // e.g. the setting changed after learnx connected
	_, err := es.Run(context.Background(), "", "ROW a = 1")
	if err == nil || IsQueryError(err) || !strings.Contains(err.Error(), "Wildcard expressions") {
		t.Fatalf("err = %v (query error: %v)", err, IsQueryError(err))
	}
	for _, r := range f.requests {
		if r == "POST /_query" {
			t.Error("the query ran after a failed reset")
		}
	}
}

// Another learnx sharing the cluster may delete a template between this
// reset listing it and deleting it; that is not a failure.
func TestResetToleratesVanishedObjects(t *testing.T) {
	f := &fakeES{settings: wildcardDeletesAllowed, marked: true}
	es := externalES(t, f)
	if err := es.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.vanished = "exercise"
	if _, err := es.Run(context.Background(), "", "ROW a = 1"); err != nil {
		t.Fatal(err)
	}
}
