package tui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mlliarm/learnx/internal/llm"
	"github.com/mlliarm/learnx/internal/pack"
	"github.com/mlliarm/learnx/internal/practice"
	"github.com/mlliarm/learnx/internal/store"
	"github.com/mlliarm/learnx/internal/tutor"
)

const countSetup = `PUT t
{"mappings": {"properties": {"x": {"type": "integer"}}}}

POST t/_bulk
{"index": {}}
{"x": 1}
{"index": {}}
{"x": 2}
{"index": {}}
{"x": 3}`

func newTestApp(t *testing.T) (*app, *store.Store) {
	t.Helper()
	p, err := pack.Load("elastic-security")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	syl := &tutor.Syllabus{Title: "Test course", Modules: []tutor.Module{{ID: "1", Title: "Basics", Lessons: []tutor.LessonPlan{
		{ID: "1.1", Title: "Counting"}, {ID: "1.2", Title: "Not written"},
	}}}}
	lesson := &tutor.Lesson{ID: "1.1", Title: "Counting", Body: "Use `COUNT(*)`.", Exercises: []tutor.Exercise{
		{Kind: "multiple_choice", Prompt: "Which query language?", Choices: []string{"KQL", "ES|QL"}, Answer: "ES|QL",
			Hints: []string{"Think about pipes.", "Its name mentions ES."}},
		{Kind: "query", Prompt: "Count the documents.", SetupSQL: countSetup, SolutionSQL: "FROM t | STATS n = COUNT(*)"},
	}}
	if err := st.Save("elastic-security", practice.LessonKey("1.1"), lesson); err != nil {
		t.Fatal(err)
	}
	a := &app{ctx: context.Background(), style: "dark", opt: Options{Pack: p, Store: st, Syllabus: syl},
		checker: practice.NewChecker(p, "", io.Discard)}
	a.initList()
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return a, st
}

// press sends a key and runs the resulting commands, feeding their messages
// back in; spinner ticks and cursor blinks are dropped.
func press(t *testing.T, a *app, keys ...string) {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "ctrl+s":
			msg = tea.KeyMsg{Type: tea.KeyCtrlS}
		case "ctrl+g":
			msg = tea.KeyMsg{Type: tea.KeyCtrlG}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd := a.Update(msg)
		drain(t, a, cmd)
	}
}

func drain(t *testing.T, a *app, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("command did not finish")
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			drain(t, a, c)
		}
	case checkedMsg, lessonWrittenMsg, hintsMsg:
		_, next := a.Update(msg)
		drain(t, a, next)
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// view is the screen as plain text, with trailing blanks trimmed.
func view(a *app) string {
	lines := strings.Split(ansi.ReplaceAllString(a.View(), ""), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func TestSyllabusShowsProgress(t *testing.T) {
	a, _ := newTestApp(t)
	v := view(a)
	for _, want := range []string{"Test course", "1.1  Counting", "0 of 2 exercises done", "1.2  Not written", "not written yet"} {
		if !strings.Contains(v, want) {
			t.Errorf("syllabus lacks %q:\n%s", want, v)
		}
	}
	press(t, a, "r")
	if !strings.Contains(view(a), "Nothing to review") {
		t.Errorf("no review notice:\n%s", view(a))
	}
}

func TestPracticeFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("runs Elasticsearch")
	}
	a, st := newTestApp(t)
	if err := a.checker.CanRun(); err != nil {
		t.Skip(err)
	}

	press(t, a, "enter") // open lesson 1.1
	if a.screen != scrLesson || !strings.Contains(view(a), "COUNT(*)") {
		t.Fatalf("lesson not shown:\n%s", view(a))
	}
	press(t, a, "p")
	if a.screen != scrPractice || !strings.Contains(view(a), "Which query language?") {
		t.Fatalf("practice not shown:\n%s", view(a))
	}

	press(t, a, "a") // wrong choice
	if !strings.Contains(view(a), "Not quite") || !strings.Contains(view(a), "Answer:") {
		t.Fatalf("no feedback after a wrong choice:\n%s", view(a))
	}
	press(t, a, "enter")
	if !strings.Contains(view(a), "Count the documents") {
		t.Fatalf("query exercise not shown:\n%s", view(a))
	}

	a.prac.editor.SetValue("FROM t | WHERE x > 1 | STATS n = COUNT(*)")
	press(t, a, "ctrl+s")
	if testing.Verbose() {
		t.Logf("query exercise after a wrong answer:\n%s", view(a))
	}
	if !strings.Contains(view(a), "Not yet") || a.prac.revealed {
		t.Fatalf("wrong query not reported, or solution revealed:\n%s", view(a))
	}
	a.prac.editor.SetValue("FROM t | STATS c = COUNT(*)")
	press(t, a, "ctrl+s")
	if !strings.Contains(view(a), "Correct") || !a.prac.revealed {
		t.Fatalf("right query not accepted:\n%s", view(a))
	}
	press(t, a, "enter")
	if !strings.Contains(view(a), "Done: 1 of 2") {
		t.Fatalf("no summary:\n%s", view(a))
	}
	press(t, a, "enter")
	if a.screen != scrSyllabus || !strings.Contains(view(a), "1 of 2 exercises done") || !strings.Contains(view(a), "1 to review") {
		t.Fatalf("syllabus not refreshed:\n%s", view(a))
	}

	stats, _ := st.LessonStats("elastic-security", "1.1")
	if stats[0] != (store.ExerciseStat{Attempts: 1}) || stats[1] != (store.ExerciseStat{Attempts: 2, Correct: true}) {
		t.Errorf("attempts = %+v", stats)
	}

	if testing.Verbose() {
		t.Logf("syllabus after practice:\n%s", view(a))
	}
	press(t, a, "r") // the missed choice is due now
	if a.screen != scrPractice || !strings.Contains(view(a), "Review") || !strings.Contains(view(a), "Which query language?") {
		t.Fatalf("review not started:\n%s", view(a))
	}
	press(t, a, "b", "enter")
	if !strings.Contains(view(a), "Done: 1 of 1") {
		t.Fatalf("review summary:\n%s", view(a))
	}
}

func TestChatOpensAndCloses(t *testing.T) {
	a, _ := newTestApp(t)
	press(t, a, "a")
	if a.screen != scrChat || !strings.Contains(view(a), "Ask the Elastic Security docs") {
		t.Fatalf("chat not shown:\n%s", view(a))
	}
	press(t, a, "ctrl+s") // empty question: nothing happens
	if a.chat.busy {
		t.Error("an empty question was sent")
	}
	press(t, a, "esc")
	if a.screen != scrSyllabus || a.chat != nil {
		t.Errorf("chat not closed:\n%s", view(a))
	}
}

func TestHints(t *testing.T) {
	a, st := newTestApp(t)
	press(t, a, "enter", "p")
	if !strings.Contains(view(a), "ctrl+g hint") {
		t.Fatalf("no hint key in help:\n%s", view(a))
	}
	press(t, a, "ctrl+g")
	if v := view(a); !strings.Contains(v, "Hint 1/2: Think about pipes.") || strings.Contains(v, "Hint 2/2") {
		t.Fatalf("first hint:\n%s", v)
	}
	press(t, a, "ctrl+g", "ctrl+g")
	if v := view(a); !strings.Contains(v, "Hint 2/2: Its name mentions ES.") || !strings.Contains(v, "No more hints") {
		t.Fatalf("second hint:\n%s", v)
	}
	if stats, _ := st.LessonStats("elastic-security", "1.1"); len(stats) != 0 {
		t.Errorf("hints counted as attempts: %+v", stats)
	}

	// The query exercise has no hints, so the model writes some and they are saved.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"hints\":[\"Use an aggregate.\"]}"}}]}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENROUTER_BASE_URL", srv.URL)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	a.checker = practice.NewChecker(a.opt.Pack, llm.OpenRouterModel, io.Discard)
	press(t, a, "esc", "enter") // skip to the query exercise
	press(t, a, "ctrl+g")
	if v := view(a); !strings.Contains(v, "Hint 1/1: Use an aggregate.") {
		t.Fatalf("generated hint:\n%s", v)
	}
	var saved tutor.Lesson
	st.Load("elastic-security", practice.LessonKey("1.1"), &saved)
	if len(saved.Exercises[1].Hints) != 1 {
		t.Errorf("generated hints not saved: %+v", saved.Exercises[1])
	}
}

func TestQueryPlaceholder(t *testing.T) {
	for solution, want := range map[string]string{
		"FROM auth-events\n| STATS n = COUNT(*)":  "FROM …",
		"-- top hosts\nSELECT host FROM t":        "SELECT …",
		"POST collections/books/points/query\n{}": "POST …",
		"":                     "Write your query…",
		"  \n# only a comment": "Write your query…",
	} {
		if got := queryPlaceholder(solution); got != want {
			t.Errorf("queryPlaceholder(%q) = %q, want %q", solution, got, want)
		}
	}
}
