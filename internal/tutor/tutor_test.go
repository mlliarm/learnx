package tutor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mlliarm/learnx/internal/docs"
	"github.com/mlliarm/learnx/internal/llm"
	"github.com/mlliarm/learnx/internal/pack"
)

// fakeDocs is an MCP docs server with a single search tool.
func fakeDocs(t *testing.T) *httptest.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-docs", Version: "0"}, nil)
	type in struct {
		Query string `json:"query"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "search-docs", Description: "Search the docs"},
		func(_ context.Context, _ *mcp.CallToolRequest, in in) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
				Text: "ES|QL pipes data through commands. https://docs.example/esql (query was " + in.Query + ")",
			}}}, nil, nil
		})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	t.Cleanup(srv.Close)
	return srv
}

// fakeClaude streams scripted turns and records every request body and header.
type fakeClaude struct {
	mu       sync.Mutex
	turns    [][]string // SSE data payloads per turn
	bodies   []map[string]any
	betaHdrs []string
}

func (f *fakeClaude) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	f.mu.Lock()
	n := len(f.bodies)
	f.bodies = append(f.bodies, body)
	f.betaHdrs = append(f.betaHdrs, r.Header.Get("anthropic-beta"))
	f.mu.Unlock()

	if n >= len(f.turns) {
		http.Error(w, "unexpected extra request", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, data := range f.turns[n] {
		var ev struct{ Type string }
		_ = json.Unmarshal([]byte(data), &ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
	}
}

func turn(stopReason string, block string, deltas ...string) []string {
	evs := []string{
		`{"type":"message_start","message":{"id":"msg_x","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":` + block + `}`,
	}
	for _, d := range deltas {
		evs = append(evs, `{"type":"content_block_delta","index":0,"delta":`+d+`}`)
	}
	return append(evs,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"`+stopReason+`"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestWriteLessonResearchesDocsThenReturnsStructuredLesson(t *testing.T) {
	ctx := context.Background()
	docsSrv := fakeDocs(t)

	want := Lesson{
		Title: "ES|QL basics",
		Body:  "ES|QL pipes data through commands.",
		Exercises: []Exercise{{
			Kind:        "query",
			Prompt:      "Count rows.",
			SetupSQL:    "PUT t\n{}",
			SolutionSQL: "FROM t | STATS n = COUNT(*)",
			Choices:     []string{},
			Explanation: "COUNT(*) counts documents.",
		}},
		Sources: []Source{{Title: "ES|QL", URL: "https://docs.example/esql"}},
	}
	final, _ := json.Marshal(want)

	claude := &fakeClaude{turns: [][]string{
		turn("tool_use",
			`{"type":"tool_use","id":"toolu_1","name":"search-docs","input":{}}`,
			`{"type":"input_json_delta","partial_json":"{\"query\":\"ES|QL STATS\"}"}`),
		turn("end_turn",
			`{"type":"text","text":""}`,
			`{"type":"text_delta","text":`+jsonString(string(final))+`}`),
	}}
	apiSrv := httptest.NewServer(claude)
	t.Cleanup(apiSrv.Close)

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
	tu := &Tutor{
		Pack:   p,
		LLM:    llm.New("", &status, option.WithBaseURL(apiSrv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0)),
		Tools:  tools,
		Effort: anthropic.BetaOutputConfigEffortMedium,
	}
	syl := &Syllabus{Title: "FakeDB course", Profile: Profile{Level: "beginner"}, Modules: []Module{{
		ID: "1", Title: "Storage", Lessons: []LessonPlan{{ID: "1.1", Title: "ES|QL basics", Objectives: []string{"Explain STATS"}}},
	}}}

	got, err := tu.WriteLesson(ctx, syl, "1.1")
	if err != nil {
		t.Fatalf("WriteLesson: %v\nstatus:\n%s", err, status.String())
	}

	if got.ID != "1.1" || got.Model != llm.DefaultModel || got.Title != want.Title || len(got.Exercises) != 1 {
		t.Errorf("unexpected lesson: %+v", got)
	}
	if !strings.Contains(status.String(), "search-docs: ES|QL STATS") {
		t.Errorf("tool call not reported in status:\n%s", status.String())
	}

	if len(claude.bodies) != 2 {
		t.Fatalf("want 2 API calls, got %d", len(claude.bodies))
	}
	first := claude.bodies[0]
	if first["model"] != llm.DefaultModel {
		t.Errorf("model = %v", first["model"])
	}
	if first["fallbacks"] != "default" || !strings.Contains(claude.betaHdrs[0], "server-side-fallback-2026-07-01") {
		t.Errorf("refusal fallbacks not enabled: fallbacks=%v beta=%q", first["fallbacks"], claude.betaHdrs[0])
	}
	oc, _ := first["output_config"].(map[string]any)
	if testing.Verbose() {
		b, _ := json.MarshalIndent(oc["format"], "", " ")
		t.Log(string(b))
	}
	if oc["effort"] != "medium" || oc["format"] == nil {
		t.Errorf("output_config = %v", oc)
	}
	toolsJSON, _ := json.Marshal(first["tools"])
	if !strings.Contains(string(toolsJSON), `"name":"search-docs"`) {
		t.Errorf("MCP tool not exposed to the model: %s", toolsJSON)
	}

	// The second request must carry the MCP server's answer back as a tool_result.
	second, _ := json.Marshal(claude.bodies[1]["messages"])
	if !strings.Contains(string(second), `"tool_use_id":"toolu_1"`) || !strings.Contains(string(second), "query was ES|QL STATS") {
		t.Errorf("tool result not sent back: %s", second)
	}
}

func TestAssignIDs(t *testing.T) {
	var out syllabusOut
	if err := json.Unmarshal([]byte(`{"title":"C","summary":"S","modules":[
		{"title":"1. A","summary":"","lessons":[{"title":"1.1 a1","objectives":[],"doc_urls":[]},{"title":"a2","objectives":[],"doc_urls":[]}]},
		{"title":"B","summary":"","lessons":[{"title":"b1","objectives":[],"doc_urls":[]}]}]}`), &out); err != nil {
		t.Fatal(err)
	}
	s := &Syllabus{Modules: assignIDs(&out)}
	if got := strings.Join(s.LessonIDs(), ","); got != "1.1,1.2,2.1" {
		t.Errorf("ids = %s", got)
	}
	if s.Modules[0].Title != "A" || s.Modules[0].Lessons[0].Title != "a1" {
		t.Errorf("leading numbers kept: %q, %q", s.Modules[0].Title, s.Modules[0].Lessons[0].Title)
	}
	if _, l, ok := s.Lesson("2.1"); !ok || l.Title != "b1" {
		t.Errorf("Lesson(2.1) = %v %v", l, ok)
	}
}

func TestDropTitleHeading(t *testing.T) {
	cases := map[string]string{
		"# 1.1 Title\n\nBody":  "Body",
		"\n# Title\nBody":      "Body",
		"## Section\nBody":     "## Section\nBody",
		"Intro\n# Not a title": "Intro\n# Not a title",
	}
	for in, want := range cases {
		if got := dropTitleHeading(in); got != want {
			t.Errorf("dropTitleHeading(%q) = %q, want %q", in, got, want)
		}
	}
}
