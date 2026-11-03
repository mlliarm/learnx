package tutor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/mlliarm/learnx/internal/docs"
	"github.com/mlliarm/learnx/internal/llm"
	"github.com/mlliarm/learnx/internal/pack"
)

// fakeOpenRouter replies with scripted chat-completion messages and records requests.
type fakeOpenRouter struct {
	mu      sync.Mutex
	replies []string // choices[0] JSON per request
	bodies  []map[string]any
	auth    []string
}

func (f *fakeOpenRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	n := len(f.bodies)
	f.bodies = append(f.bodies, body)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if r.URL.Path != "/chat/completions" || n >= len(f.replies) {
		http.Error(w, `{"error":{"message":"unexpected request"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"choices":[`+f.replies[n]+`],"usage":{"prompt_tokens":100,"completion_tokens":20,"cost":0.0012}}`)
}

func TestWriteLessonOnOpenRouter(t *testing.T) {
	ctx := context.Background()
	want := Lesson{Title: "ES|QL basics", Body: "b", Exercises: []Exercise{}, Sources: []Source{}}
	final, _ := json.Marshal(want)

	or := &fakeOpenRouter{replies: []string{
		`{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"reasoning_details":[{"type":"reasoning.encrypted","data":"xyz"}],
		  "tool_calls":[{"id":"call_1","type":"function","function":{"name":"search-docs","arguments":"{\"query\":\"ES|QL STATS\"}"}}]}}`,
		`{"finish_reason":"stop","message":{"role":"assistant","content":` + jsonString(string(final)) + `}}`,
	}}
	srv := httptest.NewServer(or)
	t.Cleanup(srv.Close)
	t.Setenv("OPENROUTER_BASE_URL", srv.URL)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")

	docsSrv := fakeDocs(t)
	p := &pack.Pack{Name: "fake", Title: "FakeDB", Docs: pack.Docs{MCP: &pack.MCPDocs{Endpoint: docsSrv.URL}}}
	src, err := docs.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var status strings.Builder
	tools, err := llm.DocsTools(ctx, src, &status)
	if err != nil {
		t.Fatal(err)
	}
	tu := &Tutor{Pack: p, LLM: llm.New(llm.OpenRouterModel, &status), Tools: tools, Effort: anthropic.BetaOutputConfigEffortMax}
	syl := &Syllabus{Title: "C", Modules: []Module{{ID: "1", Title: "M", Lessons: []LessonPlan{{ID: "1.1", Title: "ES|QL basics"}}}}}

	got, err := tu.WriteLesson(ctx, syl, "1.1")
	if err != nil {
		t.Fatalf("WriteLesson: %v\nstatus:\n%s", err, status.String())
	}
	if got.Title != want.Title || got.Model != llm.OpenRouterModel {
		t.Errorf("lesson = %+v", got)
	}
	if !strings.Contains(status.String(), "search-docs: ES|QL STATS") || !strings.Contains(status.String(), "$0.0024") {
		t.Errorf("status:\n%s", status.String())
	}

	if len(or.bodies) != 2 || or.auth[0] != "Bearer sk-or-test" {
		t.Fatalf("requests = %d, auth = %q", len(or.bodies), or.auth)
	}
	first := or.bodies[0]
	if first["model"] != "openai/gpt-6-luna" {
		t.Errorf("model = %v", first["model"])
	}
	if r, _ := first["reasoning"].(map[string]any); r["effort"] != "xhigh" {
		t.Errorf("reasoning = %v", first["reasoning"])
	}
	toolsJSON, _ := json.Marshal(first["tools"])
	if !strings.Contains(string(toolsJSON), `"name":"search-docs"`) || !strings.Contains(string(toolsJSON), `"parameters":{`) {
		t.Errorf("tools = %s", toolsJSON)
	}
	rf, _ := json.Marshal(first["response_format"])
	if testing.Verbose() {
		t.Log(string(rf))
	}
	for _, s := range []string{`"strict":true`, `"additionalProperties":false`, `"required":["title","body","exercises","sources"]`} {
		if !strings.Contains(string(rf), s) {
			t.Errorf("response_format lacks %s: %s", s, rf)
		}
	}
	if strings.Contains(string(rf), `"$schema"`) || strings.Contains(string(rf), `"$ref"`) || strings.Contains(string(rf), `"id"`) {
		t.Errorf("response_format has $schema, $ref or the local id field: %s", rf)
	}

	// The second request echoes the assistant turn (with reasoning) and adds the tool result.
	msgs, _ := json.Marshal(or.bodies[1]["messages"])
	for _, s := range []string{`"reasoning.encrypted"`, `"role":"tool"`, `"tool_call_id":"call_1"`, "query was ES|QL STATS"} {
		if !strings.Contains(string(msgs), s) {
			t.Errorf("second request lacks %s: %s", s, msgs)
		}
	}
}
