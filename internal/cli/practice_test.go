package cli

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mlliarm/learnx/internal/pack"
	"github.com/mlliarm/learnx/internal/practice"
	"github.com/mlliarm/learnx/internal/tutor"
)

func TestSessionHints(t *testing.T) {
	p, err := pack.Load("elastic-security")
	if err != nil {
		t.Fatal(err)
	}
	lesson := &tutor.Lesson{ID: "1.1", Exercises: []tutor.Exercise{{
		Kind: "multiple_choice", Prompt: "Which query language?", Choices: []string{"KQL", "ES|QL"}, Answer: "ES|QL",
		Hints: []string{"Think about pipes.", "Its name mentions ES."},
	}}}
	var out strings.Builder
	var attempts []string
	s := &session{
		in:      bufio.NewReader(strings.NewReader("?\n?\n?\nb\n")),
		out:     &out,
		checker: practice.NewChecker(p, "", io.Discard),
		record: func(_ practice.Item, correct bool, answer string) error {
			attempts = append(attempts, answer)
			return nil
		},
		saveLesson: func(*tutor.Lesson) error { t.Error("stored hints should not be saved again"); return nil },
	}
	if err := s.exercise(context.Background(), practice.Item{Lesson: lesson, Index: 0}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Hint 1/2: Think about pipes.", "Hint 2/2: Its name mentions ES.", "No more hints", "✓ Correct!"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if len(attempts) != 1 || attempts[0] != "b" {
		t.Errorf("attempts = %q; hint requests must not count", attempts)
	}
}

func TestReadBlockHintRequest(t *testing.T) {
	s := &session{in: bufio.NewReader(strings.NewReader("?\nSELECT 1\n?\n;\n")), out: io.Discard}
	if got, _ := s.readBlock("> ", "> ", true); got != "?" {
		t.Errorf("first block = %q, want the hint request", got)
	}
	// A "?" inside an answer is part of the answer.
	if got, _ := s.readBlock("> ", "> ", true); got != "SELECT 1\n?\n;" {
		t.Errorf("second block = %q", got)
	}
}
