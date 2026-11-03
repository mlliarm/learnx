package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/glamour"
	"github.com/spf13/cobra"

	"github.com/mlliarm/learnx/internal/llm"
	"github.com/mlliarm/learnx/internal/pack"
	"github.com/mlliarm/learnx/internal/practice"
	"github.com/mlliarm/learnx/internal/store"
	"github.com/mlliarm/learnx/internal/tutor"
)

var (
	syllabusKey = practice.SyllabusKey
	lessonKey   = practice.LessonKey
)

func newPlanCmd() *cobra.Command {
	var p tutor.Profile
	var model string
	var force bool
	cmd := &cobra.Command{
		Use:   "plan <pack>",
		Short: "Research the docs and generate a syllabus tailored to you",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open()
			if err != nil {
				return err
			}
			defer st.Close()
			if st.Exists(args[0], syllabusKey) && !force {
				return fmt.Errorf("a syllabus for %s already exists; run `learnx syllabus %s` to see it or pass --force to replace it (generated lessons are kept)", args[0], args[0])
			}
			return withTutor(cmd, args[0], model, "", func(ctx context.Context, t *tutor.Tutor) error {
				fmt.Fprintf(cmd.ErrOrStderr(), "Planning a %s course with %s…\n", t.Pack.Title, t.LLM.Model)
				syl, err := t.Plan(ctx, p)
				if err != nil {
					return err
				}
				if err := st.Save(args[0], syllabusKey, syl); err != nil {
					return err
				}
				return render(cmd.OutOrStdout(), syl.Outline(nil), false)
			})
		},
	}
	cmd.Flags().StringVar(&p.Level, "level", "beginner", "your current level: beginner, intermediate or advanced")
	cmd.Flags().StringVar(&p.Goal, "goal", "", `what you want to be able to do, e.g. "build a real-time analytics dashboard"`)
	cmd.Flags().StringVar(&p.Background, "background", "", `what you already know, e.g. "comfortable with PostgreSQL and SQL"`)
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing syllabus")
	addModelFlag(cmd, &model)
	return cmd
}

func newSyllabusCmd() *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "syllabus <pack>",
		Short: "Show your syllabus and which lessons are written",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, syl, err := loadSyllabus(args[0])
			if err != nil {
				return err
			}
			defer st.Close()
			done := func(id string) bool { return st.Exists(args[0], lessonKey(id)) }
			return render(cmd.OutOrStdout(), syl.Outline(done), raw)
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "print plain Markdown")
	return cmd
}

func newLessonCmd() *cobra.Command {
	var model, effort string
	var regen, raw, solutions bool
	cmd := &cobra.Command{
		Use:   "lesson <pack> [lesson-id]",
		Short: "Show a lesson, writing it first if needed (default: the next unwritten one)",
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
			} else if id = nextUnwritten(st, name, syl); id == "" {
				return fmt.Errorf("every lesson is written; pass a lesson id to reread one")
			}
			if _, _, ok := syl.Lesson(id); !ok {
				return fmt.Errorf("no lesson %q; run `learnx syllabus %s` to see the ids", id, name)
			}

			var lesson tutor.Lesson
			found, err := st.Load(name, lessonKey(id), &lesson)
			if err != nil {
				return err
			}
			if !found || regen {
				err := withTutor(cmd, name, model, effort, func(ctx context.Context, t *tutor.Tutor) error {
					fmt.Fprintf(cmd.ErrOrStderr(), "Writing lesson %s with %s…\n", id, t.LLM.Model)
					l, err := t.WriteLesson(ctx, syl, id)
					if err != nil {
						return err
					}
					lesson = *l
					if err := st.Save(name, lessonKey(id), l); err != nil {
						return err
					}
					// Attempts are keyed by exercise index, which a rewrite invalidates.
					return st.ResetLesson(name, id)
				})
				if err != nil {
					return err
				}
			}
			return render(cmd.OutOrStdout(), lesson.Markdown(solutions), raw)
		},
	}
	cmd.Flags().BoolVar(&regen, "regen", false, "rewrite the lesson even if it is cached")
	cmd.Flags().BoolVar(&solutions, "solutions", false, "show exercise solutions")
	cmd.Flags().BoolVar(&raw, "raw", false, "print plain Markdown")
	cmd.Flags().StringVar(&effort, "effort", "high", "model effort for lesson writing: low, medium, high, xhigh or max")
	addModelFlag(cmd, &model)
	return cmd
}

func addModelFlag(cmd *cobra.Command, model *string) {
	cmd.Flags().StringVar(model, "model", llm.ModelFromEnv(),
		`Claude model, or an OpenRouter model as "vendor/model" (env LEARNX_MODEL)`)
}

func withTutor(cmd *cobra.Command, packName, model, effort string, fn func(context.Context, *tutor.Tutor) error) error {
	p, err := pack.Load(packName)
	if err != nil {
		return err
	}
	t, closeDocs, err := tutor.Open(cmd.Context(), p, model, effort, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer closeDocs()
	return fn(cmd.Context(), t)
}

func loadSyllabus(name string) (*store.Store, *tutor.Syllabus, error) {
	if _, err := pack.Load(name); err != nil {
		return nil, nil, err
	}
	st, err := store.Open()
	if err != nil {
		return nil, nil, err
	}
	var syl tutor.Syllabus
	found, err := st.Load(name, syllabusKey, &syl)
	if err == nil && !found {
		err = fmt.Errorf("no syllabus for %s yet; run `learnx plan %s` first", name, name)
	}
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, &syl, nil
}

func loadLesson(st *store.Store, name, id string) (*tutor.Lesson, error) {
	var l tutor.Lesson
	found, err := st.Load(name, lessonKey(id), &l)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("lesson %s isn't written yet; run `learnx lesson %s %s` first", id, name, id)
	}
	return &l, nil
}

func nextUnwritten(st *store.Store, name string, syl *tutor.Syllabus) string {
	for _, id := range syl.LessonIDs() {
		if !st.Exists(name, lessonKey(id)) {
			return id
		}
	}
	return ""
}

// render pretty-prints Markdown on a terminal and passes it through otherwise.
func render(w io.Writer, md string, raw bool) error {
	if f, ok := w.(*os.File); raw || !ok || !isTerminal(f) {
		_, err := io.WriteString(w, md)
		return err
	}
	r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(100))
	if err != nil {
		return err
	}
	out, err := tutor.RenderTerminal(r, md)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out)
	return err
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
