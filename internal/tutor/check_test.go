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
	"github.com/mlliarm/learnx/internal/sandbox"
)

// fakeSandbox accepts solutions containing "good" and rejects the rest.
type fakeSandbox struct{ runs []string }

func (f *fakeSandbox) Name() string { return "fake" }

func (f *fakeSandbox) Ordered(string) bool { return false }

func (f *fakeSandbox) Run(_ context.Context, _, query string) (*sandbox.Result, error) {
	f.runs = append(f.runs, query)
	if strings.Contains(query, "good") {
		return &sandbox.Result{Columns: []sandbox.Column{{Name: "x"}}, Rows: [][]any{{json.Number("1")}}}, nil
	}
	return nil, &sandbox.QueryError{Msg: "Code: 46. DB::Exception: Unknown function"}
}

func textTurn(v any) []string {
	b, _ := json.Marshal(v)
	return turn("end_turn", `{"type":"text","text":""}`, `{"type":"text_delta","text":`+jsonString(string(b))+`}`)
}

func newTestTutor(t *testing.T, claude *fakeClaude, sb sandbox.Sandbox, status *strings.Builder) *Tutor {
	srv := httptest.NewServer(claude)
	t.Cleanup(srv.Close)
	return &Tutor{
		Pack:    &pack.Pack{Name: "fake", Title: "FakeDB"},
		LLM:     llm.New("", status, option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0)),
		Sandbox: sb,
	}
}

func TestCheckExercisesRepairsAndDrops(t *testing.T) {
	fixed := Exercise{Kind: "query", Prompt: "Fixed", SolutionSQL: "SELECT good", Choices: []string{}}
	// One repair succeeds for exercise 2; exercise 3 fails both repairs.
	claude := &fakeClaude{turns: [][]string{
		textTurn(fixed),
		textTurn(Exercise{Kind: "query", SolutionSQL: "SELECT bad again", Choices: []string{}}),
		textTurn(Exercise{Kind: "query", SolutionSQL: "SELECT still bad", Choices: []string{}}),
	}}
	sb := &fakeSandbox{}
	var status strings.Builder
	tu := newTestTutor(t, claude, sb, &status)

	l := &Lesson{Title: "L", Exercises: []Exercise{
		{Kind: "query", SolutionSQL: "SELECT good"},
		{Kind: "query", SolutionSQL: "SELECT bad"},
		{Kind: "query", SolutionSQL: "SELECT also bad"},
		{Kind: "multiple_choice", Choices: []string{"a", "b"}, Answer: "a"},
	}}
	if err := tu.checkExercises(context.Background(), l); err != nil {
		t.Fatal(err)
	}

	var kinds []string
	for _, ex := range l.Exercises {
		kinds = append(kinds, ex.Kind+":"+ex.Prompt)
	}
	if got := strings.Join(kinds, ","); got != "query:,query:Fixed,multiple_choice:" {
		t.Errorf("exercises after check = %s\nstatus:\n%s", got, status.String())
	}
	if len(claude.bodies) != 3 {
		t.Errorf("want 3 repair calls, got %d", len(claude.bodies))
	}
	msgs, _ := json.Marshal(claude.bodies[0]["messages"])
	if !strings.Contains(string(msgs), "Unknown function") {
		t.Errorf("repair prompt lacks the sandbox error: %s", msgs)
	}
	if !strings.Contains(status.String(), "dropped exercise 3") {
		t.Errorf("drop not reported:\n%s", status.String())
	}
}

func TestGradeShortAnswer(t *testing.T) {
	claude := &fakeClaude{turns: [][]string{textTurn(Feedback{Correct: true, Feedback: "Right."})}}
	var status strings.Builder
	tu := newTestTutor(t, claude, nil, &status)

	ex := &Exercise{Kind: "short_answer", Prompt: "Why add SORT to an ES|QL query?", Answer: "It fixes the row order."}
	fb, err := tu.GradeShortAnswer(context.Background(), ex, "it puts the results in order")
	if err != nil {
		t.Fatal(err)
	}
	if !fb.Correct || fb.Feedback != "Right." {
		t.Errorf("feedback = %+v", fb)
	}
	body := claude.bodies[0]
	if tools, _ := body["tools"].([]any); len(tools) > 0 {
		t.Errorf("grading should not offer docs tools: %v", body["tools"])
	}
	msgs, _ := json.Marshal(body["messages"])
	if !strings.Contains(string(msgs), "puts the results in order") || !strings.Contains(string(msgs), "fixes the row order") {
		t.Errorf("prompt lacks the answers: %s", msgs)
	}
}
