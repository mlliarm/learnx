package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mlliarm/learnx/internal/pack"
	"github.com/mlliarm/learnx/internal/practice"
	"github.com/mlliarm/learnx/internal/store"
	"github.com/mlliarm/learnx/internal/tutor"
)

func newPracticeCmd() *cobra.Command {
	var model string
	var all bool
	cmd := &cobra.Command{
		Use:   "practice <pack> [lesson-id]",
		Short: "Do a lesson's exercises; queries are run and checked (default: the next unfinished lesson)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			st, syl, err := loadSyllabus(name)
			if err != nil {
				return err
			}
			defer st.Close()
			id := ""
			if len(args) == 2 {
				id = args[1]
			} else if id, err = practice.NextUnfinished(st, name, syl); err != nil {
				return err
			}
			lesson, err := loadLesson(st, name, id)
			if err != nil {
				return err
			}
			items, err := practice.LessonItems(st, name, lesson, all)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Every exercise in lesson %s is done; pass --all to do them again.\n", id)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Lesson %s %s: %d exercise(s).", id, lesson.Title, len(items))
			if err := runItems(cmd, st, name, model, items); err != nil {
				return err
			}
			done, total, err := practice.LessonProgress(st, name, lesson)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Lesson %s: %d of %d exercises correct.\n", id, done, total)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "redo exercises you already got right")
	addModelFlag(cmd, &model)
	return cmd
}

func newReviewCmd() *cobra.Command {
	var model string
	var all bool
	cmd := &cobra.Command{
		Use:   "review <pack>",
		Short: "Review exercises you missed, spaced out over time",
		Long: `Exercises you miss join a review queue. Answer one correctly and it comes back
after 1, 3, 7, 16 and then 35 days; miss it and it starts over. After the last
interval it leaves the queue.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if _, err := pack.Load(name); err != nil {
				return err
			}
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			now := time.Now()
			if all {
				now = time.Time{}
			}
			items, err := practice.DueItems(st, name, now)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to review right now.")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d exercise(s) to review.", len(items))
			return runItems(cmd, st, name, model, items)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "review every queued exercise, not only those due")
	addModelFlag(cmd, &model)
	return cmd
}

// runItems works through items on the terminal, recording every attempt.
func runItems(cmd *cobra.Command, st *store.Store, packName, model string, items []practice.Item) error {
	p, err := pack.Load(packName)
	if err != nil {
		return err
	}
	s := &session{
		in:      bufio.NewReader(cmd.InOrStdin()),
		out:     cmd.OutOrStdout(),
		checker: practice.NewChecker(p, model, cmd.ErrOrStderr()),
		record: func(it practice.Item, correct bool, answer string) error {
			return st.RecordAttempt(store.Attempt{Pack: packName, Lesson: it.Lesson.ID, Exercise: it.Index, Correct: correct, Answer: answer})
		},
		saveLesson: func(l *tutor.Lesson) error {
			return st.Save(packName, practice.LessonKey(l.ID), l)
		},
	}
	fmt.Fprintln(s.out, " Type ? for a hint; submit an empty answer to skip and see the solution; Ctrl-D stops.")
	fmt.Fprintln(s.out)
	for _, it := range items {
		err := s.exercise(cmd.Context(), it)
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(s.out)
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// session runs exercises on a line-based terminal.
type session struct {
	in         *bufio.Reader
	out        io.Writer
	checker    *practice.Checker
	record     func(it practice.Item, correct bool, answer string) error
	saveLesson func(l *tutor.Lesson) error // after hints are added to it
}

// exercise returns io.EOF when the learner closes input.
func (s *session) exercise(ctx context.Context, it practice.Item) error {
	ex := it.Exercise()
	heading := ex.Markdown(it.Index+1, false)
	if len(it.Lesson.ID) > 0 {
		heading = fmt.Sprintf("*Lesson %s %s*\n\n%s", it.Lesson.ID, it.Lesson.Title, heading)
	}
	if err := render(s.out, heading, false); err != nil {
		return err
	}
	if ex.Kind == "query" {
		if err := s.checker.CanRun(); err != nil {
			fmt.Fprintf(s.out, "Can't run query exercises: %v\n\n", err)
			return s.reveal(ex.SolutionMarkdown())
		}
	}
	hints := 0 // shown so far
	for {
		answer, err := s.read(ex.Kind)
		if err != nil {
			return err
		}
		if answer == "" {
			return s.reveal(ex.SolutionMarkdown())
		}
		if answer == "?" {
			s.hint(ctx, it, &hints)
			continue
		}
		out, err := s.checker.Check(ctx, ex, answer)
		if err != nil {
			return err
		}
		if out.Invalid {
			fmt.Fprintln(s.out, out.Feedback)
			continue
		}
		if err := s.record(it, out.Correct, answer); err != nil {
			return err
		}
		mark := "✗"
		if out.Correct {
			mark = "✓"
		}
		fmt.Fprintf(s.out, "%s %s\n", mark, out.Feedback)
		if out.Final {
			fmt.Fprintln(s.out)
			return s.reveal(ex.SolutionMarkdown())
		}
		fmt.Fprintln(s.out, "Try again, or submit an empty answer to see the solution.")
		fmt.Fprintln(s.out)
	}
}

// hint shows the next hint, writing hints first if the exercise has none.
func (s *session) hint(ctx context.Context, it practice.Item, shown *int) {
	hints, added, err := s.checker.Hints(ctx, it.Exercise())
	if err != nil {
		fmt.Fprintf(s.out, "%v\n\n", err)
		return
	}
	if added {
		if err := s.saveLesson(it.Lesson); err != nil {
			fmt.Fprintf(s.out, "(couldn't save the hints: %v)\n", err)
		}
	}
	if *shown >= len(hints) {
		fmt.Fprintln(s.out, "No more hints; submit an empty answer to see the solution.")
		fmt.Fprintln(s.out)
		return
	}
	*shown++
	fmt.Fprintf(s.out, "💡 Hint %d/%d: %s\n\n", *shown, len(hints), hints[*shown-1])
}

func (s *session) read(kind string) (string, error) {
	switch kind {
	case "query":
		fmt.Fprintln(s.out, "Your query (end with ; or an empty line):")
		return s.readBlock("query> ", "  ...> ", true)
	case "multiple_choice":
		return s.readLine("answer> ")
	default:
		fmt.Fprintln(s.out, "Your answer (end with an empty line):")
		return s.readBlock("> ", "> ", false)
	}
}

func (s *session) reveal(md string) error {
	return render(s.out, md+"---\n\n", false)
}

func (s *session) readLine(prompt string) (string, error) {
	fmt.Fprint(s.out, prompt)
	line, err := s.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readBlock reads lines until an empty one or, for SQL, a line ending in ";".
// An empty first line returns "", and a first line of "?" returns "?".
func (s *session) readBlock(first, more string, sql bool) (string, error) {
	var lines []string
	prompt := first
	for {
		line, err := s.readLine(prompt)
		if err != nil {
			if errors.Is(err, io.EOF) && len(lines) > 0 {
				return strings.Join(lines, "\n"), nil
			}
			return "", err
		}
		if line == "" {
			return strings.Join(lines, "\n"), nil
		}
		if line == "?" && len(lines) == 0 {
			return line, nil // a hint request, not the start of an answer
		}
		lines = append(lines, line)
		if sql && strings.HasSuffix(line, ";") {
			return strings.Join(lines, "\n"), nil
		}
		prompt = more
	}
}
