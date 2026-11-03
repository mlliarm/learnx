package tutor

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/mlliarm/learnx/internal/llm"
	"github.com/mlliarm/learnx/internal/pack"
)

var history = []llm.Turn{
	{Role: "user", Text: "What is EQL?"},
	{Role: "assistant", Text: "Event Query Language."},
}

func TestAskSendsHistoryToClaude(t *testing.T) {
	claude := &fakeClaude{turns: [][]string{
		turn("end_turn", `{"type":"text","text":""}`, `{"type":"text_delta","text":"Use sequence. [EQL](https://docs.example/eql)"}`),
	}}
	srv := httptest.NewServer(claude)
	t.Cleanup(srv.Close)
	tu := &Tutor{Pack: &pack.Pack{Title: "FakeDB"},
		LLM: llm.New("", nil, option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))}

	answer, err := tu.Ask(context.Background(), history, "How do I match a sequence?")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "https://docs.example/eql") {
		t.Errorf("answer = %q", answer)
	}
	msgs, _ := json.Marshal(claude.bodies[0]["messages"])
	want := `[{"content":[{"text":"What is EQL?","type":"text"}],"role":"user"},{"content":[{"text":"Event Query Language.","type":"text"}],"role":"assistant"},{"content":[{"text":"How do I match a sequence?","type":"text"}],"role":"user"}]`
	if string(msgs) != want {
		t.Errorf("messages =\n%s\nwant\n%s", msgs, want)
	}
	if _, schema := claude.bodies[0]["output_config"].(map[string]any)["format"]; schema {
		t.Error("chat answers should be free text, not structured output")
	}
}

func TestAskSendsHistoryToOpenRouter(t *testing.T) {
	or := &fakeOpenRouter{replies: []string{`{"finish_reason":"stop","message":{"role":"assistant","content":"Use sequence."}}`}}
	srv := httptest.NewServer(or)
	t.Cleanup(srv.Close)
	t.Setenv("OPENROUTER_BASE_URL", srv.URL)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	tu := &Tutor{Pack: &pack.Pack{Title: "FakeDB", Docs: pack.Docs{MCP: &pack.MCPDocs{BaseURL: "https://docs.example"}}},
		LLM: llm.New(llm.OpenRouterModel, nil)}

	if _, err := tu.Ask(context.Background(), history, "How do I match a sequence?"); err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range or.bodies[0]["messages"].([]any) {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if got := strings.Join(roles, ","); got != "system,user,assistant,user" {
		t.Errorf("roles = %s", got)
	}
	system := or.bodies[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "relative to https://docs.example") {
		t.Errorf("system prompt lacks the base URL note: %s", system)
	}
	if _, ok := or.bodies[0]["response_format"]; ok {
		t.Error("chat answers should be free text")
	}
}
