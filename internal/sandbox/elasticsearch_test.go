package sandbox

import (
	"context"
	"fmt"
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
