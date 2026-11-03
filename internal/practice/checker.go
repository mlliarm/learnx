// Package practice checks learner answers to exercises. It holds no UI, so
// the line-based commands and the TUI share it.
package practice

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/mlliarm/learnx/internal/grade"
	"github.com/mlliarm/learnx/internal/llm"
	"github.com/mlliarm/learnx/internal/pack"
	"github.com/mlliarm/learnx/internal/sandbox"
	"github.com/mlliarm/learnx/internal/tutor"
)

// Outcome is the verdict on one answer.
type Outcome struct {
	Correct bool
	// Feedback explains the verdict: a SQL error, a result mismatch or the
	// grader's comments.
	Feedback string
	// Final is false when the learner may try again (query exercises) or
	// gave an unusable answer (a choice that names no option).
	Final bool
	// Invalid marks an answer that wasn't counted as an attempt.
	Invalid bool
}

type Checker struct {
	Pack   *pack.Pack
	Model  string
	Status io.Writer // LLM progress lines; may be nil

	sb    sandbox.Sandbox
	sbErr error

	mu       sync.Mutex
	expected map[string]*sandbox.Result // reference results by setup+solution
	grader   *tutor.Tutor               // built on first use, for grading and hints
}

func NewChecker(p *pack.Pack, model string, status io.Writer) *Checker {
	c := &Checker{Pack: p, Model: model, Status: status, expected: map[string]*sandbox.Result{}}
	c.sb, c.sbErr = sandbox.Open(p, status)
	return c
}

// CanRun reports whether query exercises can be checked, and why not.
func (c *Checker) CanRun() error { return c.sbErr }

// Check grades an answer. An empty answer is not passed here: skipping is
// the caller's business.
func (c *Checker) Check(ctx context.Context, ex *tutor.Exercise, answer string) (Outcome, error) {
	switch ex.Kind {
	case "query":
		return c.query(ctx, ex, answer)
	case "multiple_choice":
		correct, ok := grade.Choice(ex.Choices, ex.Answer, answer)
		if !ok {
			return Outcome{Invalid: true, Feedback: fmt.Sprintf("Pick one of a–%c.", 'a'+len(ex.Choices)-1)}, nil
		}
		if correct {
			return Outcome{Correct: true, Final: true, Feedback: "Correct!"}, nil
		}
		return Outcome{Final: true, Feedback: "Not quite."}, nil
	default:
		return c.shortAnswer(ctx, ex, answer)
	}
}

func (c *Checker) query(ctx context.Context, ex *tutor.Exercise, q string) (Outcome, error) {
	if c.sb == nil {
		return Outcome{}, fmt.Errorf("can't run query exercises: %w", c.sbErr)
	}
	want, err := c.reference(ctx, ex)
	if err != nil {
		return Outcome{}, fmt.Errorf("the reference solution doesn't run, so this exercise can't be checked: %w", err)
	}
	got, err := c.sb.Run(ctx, ex.SetupSQL, q)
	if sandbox.IsQueryError(err) {
		return Outcome{Feedback: err.Error()}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if v := grade.Results(want, got, c.sb.Ordered(ex.SolutionSQL)); !v.Correct {
		return Outcome{Feedback: "Not yet: " + v.Reason + "."}, nil
	}
	return Outcome{Correct: true, Final: true, Feedback: "Correct!"}, nil
}

func (c *Checker) reference(ctx context.Context, ex *tutor.Exercise) (*sandbox.Result, error) {
	key := ex.SetupSQL + "\x00" + ex.SolutionSQL
	c.mu.Lock()
	res, ok := c.expected[key]
	c.mu.Unlock()
	if ok {
		return res, nil
	}
	res, err := c.sb.Run(ctx, ex.SetupSQL, ex.SolutionSQL)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.expected[key] = res
	c.mu.Unlock()
	return res, nil
}

// Hints returns the exercise's hints. An exercise without any (from a lesson
// written before exercises had hints) gets some written by the model and
// stored on ex; added reports that, so the caller can save the lesson.
func (c *Checker) Hints(ctx context.Context, ex *tutor.Exercise) (hints []string, added bool, err error) {
	if len(ex.Hints) > 0 {
		return ex.Hints, false, nil
	}
	hints, err = c.tutor().WriteHints(ctx, ex)
	if err != nil {
		return nil, false, fmt.Errorf("couldn't write hints: %w", err)
	}
	ex.Hints = hints
	return hints, true, nil
}

// tutor is an LLM-only Tutor (no docs connection) for grading and hints.
func (c *Checker) tutor() *tutor.Tutor {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.grader == nil {
		c.grader = &tutor.Tutor{Pack: c.Pack, LLM: llm.New(c.Model, c.Status)}
	}
	return c.grader
}

func (c *Checker) shortAnswer(ctx context.Context, ex *tutor.Exercise, answer string) (Outcome, error) {
	fb, err := c.tutor().GradeShortAnswer(ctx, ex, answer)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Correct: fb.Correct, Final: true, Feedback: strings.TrimSpace(fb.Feedback)}, nil
}
