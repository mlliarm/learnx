package tutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/mlliarm/learnx/internal/docs"
	"github.com/mlliarm/learnx/internal/llm"
	"github.com/mlliarm/learnx/internal/pack"
	"github.com/mlliarm/learnx/internal/sandbox"
)

type Tutor struct {
	Pack  *pack.Pack
	LLM   *llm.Client
	Tools []anthropic.BetaTool
	// Effort applies to lesson writing; planning always runs at high effort.
	Effort anthropic.BetaOutputConfigEffort
	// Sandbox, when set, runs every query exercise as soon as it is written;
	// exercises that fail are sent back to the model to fix.
	Sandbox sandbox.Sandbox
}

func (t *Tutor) system() string {
	return fmt.Sprintf(`You are an expert %[1]s instructor building a hands-on course for one learner.

Ground everything in the official %[1]s documentation, which you can reach through the docs tools. Search before you write, and prefer what the docs say over your own memory: the product changes between releases, and the learner will check your claims against the docs. Only cite URLs that appeared in tool results.

Teach for understanding: explain why a feature exists and when to use it, not only the syntax. Build on earlier lessons and avoid repeating them.%[2]s`, t.Pack.Title, t.baseURLNote())
}

func (t *Tutor) baseURL() string {
	if t.Pack.Docs.MCP == nil {
		return ""
	}
	return t.Pack.Docs.MCP.BaseURL
}

func (t *Tutor) baseURLNote() string {
	if t.baseURL() == "" {
		return ""
	}
	return fmt.Sprintf("\n\nLinks in tool results that start with \"/\" are relative to %s; always cite absolute URLs.", t.baseURL())
}

// Plan researches the docs and returns a fresh syllabus for the profile.
func (t *Tutor) Plan(ctx context.Context, p Profile) (*Syllabus, error) {
	prompt := fmt.Sprintf(`Design a course syllabus that takes this learner from where they are to their goal with %s.

Learner level: %s
Learner goal: %s
Learner background: %s

Survey the documentation first so the syllabus reflects the current product and its recommended practices. Organize it into modules of a few lessons each, ordered so every lesson only depends on earlier ones. Size it to the goal: enough to reach it, no filler. For each lesson, list the documentation URLs a lesson writer should start from.`,
		t.Pack.Title, p.Level, orNone(p.Goal), orNone(p.Background))

	var out syllabusOut
	if err := t.run(ctx, prompt, t.Tools, anthropic.BetaOutputConfigEffortHigh, 30, &out); err != nil {
		return nil, err
	}
	for mi := range out.Modules {
		for li := range out.Modules[mi].Lessons {
			urls := out.Modules[mi].Lessons[li].DocURLs
			for i := range urls {
				urls[i] = docs.Absolute(t.baseURL(), urls[i])
			}
		}
	}
	return &Syllabus{
		Pack:    t.Pack.Name,
		Model:   t.LLM.Model,
		Profile: p,
		Title:   out.Title,
		Summary: out.Summary,
		Modules: assignIDs(&out),
	}, nil
}

// WriteLesson researches and writes one lesson of the syllabus.
func (t *Tutor) WriteLesson(ctx context.Context, s *Syllabus, id string) (*Lesson, error) {
	mod, plan, ok := s.Lesson(id)
	if !ok {
		return nil, fmt.Errorf("no lesson %q in the syllabus", id)
	}
	prompt := fmt.Sprintf(`Write lesson %s of the course below.

<course>
%s
</course>

Learner level: %s
Learner goal: %s
Learner background: %s

Lesson %s "%s" (module %s "%s")
Objectives:
%s
Starting documentation:
%s

Research the objectives in the docs, then write the lesson. The body starts directly with the content (the title is shown separately, so don't repeat it as a heading) and should teach each objective with short explanations and worked examples the learner can run, and end with a brief recap.

Then write 3 to 5 exercises that check the objectives, mixing kinds. Give each exercise one to three hints that a stuck learner can reveal one at a time: start with a gentle nudge (which concept or feature applies), end with a strong pointer (the function or clause to use), and never give away the answer. Exercise environment: %s
Query exercises must be fully self-contained: setup_sql creates everything the exercise needs, and solution_sql must produce the exact, non-empty result the learner should reach. Both are executed to check them before the learner sees the lesson.`,
		id, outline(s), s.Profile.Level, orNone(s.Profile.Goal), orNone(s.Profile.Background),
		plan.ID, plan.Title, mod.ID, mod.Title, bullets(plan.Objectives), bullets(plan.DocURLs),
		orNone(t.Pack.Tutor.ExerciseEnv))

	var lesson Lesson
	if err := t.run(ctx, prompt, t.Tools, t.Effort, 25, &lesson); err != nil {
		return nil, err
	}
	if err := t.checkExercises(ctx, &lesson); err != nil {
		return nil, err
	}
	lesson.ID = id
	lesson.Model = t.LLM.Model
	lesson.Title = leadingNumber.ReplaceAllString(lesson.Title, "")
	lesson.Body = dropTitleHeading(lesson.Body)
	for i := range lesson.Sources {
		lesson.Sources[i].URL = docs.Absolute(t.baseURL(), lesson.Sources[i].URL)
	}
	return &lesson, nil
}

func (t *Tutor) run(ctx context.Context, prompt string, tools []anthropic.BetaTool, effort anthropic.BetaOutputConfigEffort, maxTurns int, dest any) error {
	text, err := t.LLM.Run(ctx, llm.Request{
		System:   t.system(),
		Prompt:   prompt,
		Tools:    tools,
		Effort:   effort,
		Schema:   dest,
		MaxTurns: maxTurns,
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), dest); err != nil {
		return fmt.Errorf("model returned malformed JSON: %w", err)
	}
	return nil
}

// outline renders the syllabus compactly for prompts and the terminal.
func outline(s *Syllabus) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n", s.Title)
	for _, m := range s.Modules {
		fmt.Fprintf(&sb, "%s. %s\n", m.ID, m.Title)
		for _, l := range m.Lessons {
			fmt.Fprintf(&sb, "   %s %s\n", l.ID, l.Title)
		}
	}
	return sb.String()
}

// dropTitleHeading removes a leading "# ..." line, since lessons are rendered
// under their own title.
func dropTitleHeading(body string) string {
	trimmed := strings.TrimLeft(body, " \t\r\n")
	if !strings.HasPrefix(trimmed, "# ") {
		return body
	}
	_, rest, _ := strings.Cut(trimmed, "\n")
	return strings.TrimLeft(rest, "\r\n")
}

func bullets(items []string) string {
	if len(items) == 0 {
		return "- (none)"
	}
	return "- " + strings.Join(items, "\n- ")
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(not specified)"
	}
	return s
}

// Open connects to the pack's docs and returns a ready Tutor; close releases
// the docs connection. Progress and warnings go to status.
func Open(ctx context.Context, p *pack.Pack, model, effort string, status io.Writer) (t *Tutor, close func() error, err error) {
	src, err := docs.Open(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	tools, err := llm.DocsTools(ctx, src, status)
	if err != nil {
		src.Close()
		return nil, nil, err
	}
	t = &Tutor{
		Pack:   p,
		LLM:    llm.New(model, status),
		Tools:  tools,
		Effort: anthropic.BetaOutputConfigEffort(effort),
	}
	if sb, err := sandbox.Open(p, status); err == nil {
		t.Sandbox = sb
	} else if !errors.Is(err, sandbox.ErrNoSandbox) {
		fmt.Fprintf(status, "· exercises won't be checked: %v\n", err)
	}
	return t, src.Close, nil
}
