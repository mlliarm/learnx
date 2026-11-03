package tutor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/mlliarm/learnx/internal/sandbox"
)

// maxRepairs is how many times a failing query exercise goes back to the model.
const maxRepairs = 2

// checkExercises runs every query exercise's setup and solution. Failing ones
// are repaired by the model; those still failing after maxRepairs are dropped.
func (t *Tutor) checkExercises(ctx context.Context, l *Lesson) error {
	if t.Sandbox == nil {
		return nil
	}
	var kept []Exercise
	for i, ex := range l.Exercises {
		if ex.Kind != "query" {
			kept = append(kept, ex)
			continue
		}
		ok, err := t.checkQuery(ctx, l.Title, i+1, &ex)
		if err != nil {
			// The sandbox itself is broken; keep the lesson rather than lose it.
			t.LLM.Logf("· couldn't check exercises with %s: %v", t.Sandbox.Name(), err)
			return nil
		}
		if ok {
			kept = append(kept, ex)
		} else {
			t.LLM.Logf("· dropped exercise %d: still failing after %d fixes", i+1, maxRepairs)
		}
	}
	l.Exercises = kept
	return nil
}

func (t *Tutor) checkQuery(ctx context.Context, lessonTitle string, n int, ex *Exercise) (bool, error) {
	for attempt := 0; ; attempt++ {
		if ex.Kind != "query" {
			return true, nil // the fix turned it into another kind
		}
		problem, err := t.tryQuery(ctx, ex)
		if err != nil || problem == "" {
			return err == nil, err
		}
		if attempt == maxRepairs {
			return false, nil
		}
		t.LLM.Logf("· exercise %d failed its check (%s); asking for a fix", n, firstLine(problem, 120))
		fixed, err := t.repair(ctx, lessonTitle, ex, problem)
		if err != nil {
			return false, err
		}
		*ex = *fixed
	}
}

// tryQuery returns what is wrong with a query exercise, or "" if it runs.
func (t *Tutor) tryQuery(ctx context.Context, ex *Exercise) (string, error) {
	res, err := t.Sandbox.Run(ctx, ex.SetupSQL, ex.SolutionSQL)
	if sandbox.IsQueryError(err) {
		return err.Error(), nil
	}
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "solution_sql returned no rows", nil
	}
	return "", nil
}

func (t *Tutor) repair(ctx context.Context, lessonTitle string, ex *Exercise, problem string) (*Exercise, error) {
	exJSON, _ := json.MarshalIndent(ex, "", "  ")
	prompt := fmt.Sprintf(`An exercise for the lesson "%s" failed when its setup_sql and then its solution_sql were run in a fresh session.

<exercise>
%s
</exercise>

<error>
%s
</error>

Exercise environment: %s

Fix the exercise so it runs and checks the same skill. Look things up in the docs if the error suggests a syntax or feature mistake. Return the whole corrected exercise.`,
		lessonTitle, exJSON, problem, orNone(t.Pack.Tutor.ExerciseEnv))

	var fixed Exercise
	if err := t.run(ctx, prompt, t.Tools, anthropic.BetaOutputConfigEffortMedium, 10, &fixed); err != nil {
		return nil, fmt.Errorf("fixing an exercise: %w", err)
	}
	return &fixed, nil
}

// Feedback is the verdict on a short answer.
type Feedback struct {
	Correct  bool   `json:"correct" jsonschema:"description=True when the answer shows the understanding the model answer shows; wording may differ"`
	Feedback string `json:"feedback" jsonschema:"description=One to three sentences to the learner on what was right and what was missing or wrong"`
}

// GradeShortAnswer judges a learner's answer against the exercise's model answer.
func (t *Tutor) GradeShortAnswer(ctx context.Context, ex *Exercise, answer string) (*Feedback, error) {
	prompt := fmt.Sprintf(`Grade a learner's answer to a short-answer exercise. Judge it against the model answer and explanation below; no research is needed.

<question>
%s
</question>

<model_answer>
%s
</model_answer>

<explanation>
%s
</explanation>

<learner_answer>
%s
</learner_answer>

Mark it correct if it gets the essential points right even if it is phrased differently or less complete. Mark it incorrect if it misses the central idea or states something false. Address the learner directly in the feedback.`,
		ex.Prompt, ex.Answer, orNone(ex.Explanation), answer)

	var fb Feedback
	if err := t.run(ctx, prompt, nil, anthropic.BetaOutputConfigEffortLow, 2, &fb); err != nil {
		return nil, err
	}
	return &fb, nil
}

type hintsOut struct {
	Hints []string `json:"hints" jsonschema:"description=One to three hints from a gentle nudge to a strong pointer; never the answer itself"`
}

// WriteHints writes hints for an exercise that has none, e.g. one from a
// lesson written before exercises carried hints.
func (t *Tutor) WriteHints(ctx context.Context, ex *Exercise) ([]string, error) {
	exJSON, _ := json.MarshalIndent(ex, "", "  ")
	prompt := fmt.Sprintf(`A learner is stuck on this exercise. Write one to three hints they can reveal one at a time: start with a gentle nudge (which concept or feature applies), end with a strong pointer (the function or clause to use), and never give away the answer. No research is needed.

<exercise>
%s
</exercise>`, exJSON)

	var out hintsOut
	if err := t.run(ctx, prompt, nil, anthropic.BetaOutputConfigEffortLow, 2, &out); err != nil {
		return nil, err
	}
	if len(out.Hints) == 0 {
		return nil, fmt.Errorf("the model wrote no hints")
	}
	return out.Hints, nil
}

func firstLine(s string, n int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(line); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return line
}
