// Package tutor plans curricula and writes lessons grounded in a pack's docs.
package tutor

import (
	"fmt"
	"regexp"
)

// Profile describes the learner; it steers both planning and lessons.
type Profile struct {
	Level      string `json:"level"`
	Goal       string `json:"goal"`
	Background string `json:"background"`
}

// Syllabus is stored per pack. IDs are assigned locally after generation
// ("2.3" = module 2, lesson 3) so they stay stable and predictable.
type Syllabus struct {
	Pack    string   `json:"pack"`
	Model   string   `json:"model"`
	Profile Profile  `json:"profile"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Modules []Module `json:"modules"`
}

type Module struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Summary string       `json:"summary"`
	Lessons []LessonPlan `json:"lessons"`
}

type LessonPlan struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Objectives []string `json:"objectives"`
	DocURLs    []string `json:"doc_urls"`
}

// syllabusOut is the structured-output shape the model fills in.
type syllabusOut struct {
	Title   string `json:"title" jsonschema:"description=Course title"`
	Summary string `json:"summary" jsonschema:"description=Two or three sentences on what the learner will be able to do by the end"`
	Modules []struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Lessons []struct {
			Title      string   `json:"title"`
			Objectives []string `json:"objectives" jsonschema:"description=2-4 concrete and checkable learning objectives"`
			DocURLs    []string `json:"doc_urls" jsonschema:"description=Documentation URLs returned by the docs tools that cover this lesson"`
		} `json:"lessons"`
	} `json:"modules"`
}

func (s *Syllabus) Lesson(id string) (*Module, *LessonPlan, bool) {
	for mi := range s.Modules {
		for li := range s.Modules[mi].Lessons {
			if s.Modules[mi].Lessons[li].ID == id {
				return &s.Modules[mi], &s.Modules[mi].Lessons[li], true
			}
		}
	}
	return nil, nil, false
}

// LessonIDs lists every lesson ID in course order.
func (s *Syllabus) LessonIDs() []string {
	var ids []string
	for _, m := range s.Modules {
		for _, l := range m.Lessons {
			ids = append(ids, l.ID)
		}
	}
	return ids
}

// Lesson is a generated lesson. Exercises are kept structured so the
// sandbox grader can run them later.
type Lesson struct {
	ID        string     `json:"id" jsonschema:"-"`
	Model     string     `json:"model" jsonschema:"-"`
	Title     string     `json:"title"`
	Body      string     `json:"body" jsonschema:"description=The lesson itself in Markdown without the exercises or sources"`
	Exercises []Exercise `json:"exercises"`
	Sources   []Source   `json:"sources" jsonschema:"description=Documentation pages actually used as returned by the docs tools"`
}

type Exercise struct {
	Kind        string   `json:"kind" jsonschema:"enum=query,enum=multiple_choice,enum=short_answer"`
	Prompt      string   `json:"prompt" jsonschema:"description=What the learner must do in Markdown"`
	SetupSQL    string   `json:"setup_sql" jsonschema:"description=For query exercises: statements that create and fill the tables the exercise needs; empty otherwise"`
	SolutionSQL string   `json:"solution_sql" jsonschema:"description=For query exercises: a reference query whose result the learner's query must match; empty otherwise"`
	Choices     []string `json:"choices" jsonschema:"description=For multiple_choice: the options; empty otherwise"`
	Answer      string   `json:"answer" jsonschema:"description=For multiple_choice: the correct option verbatim. For short_answer: a model answer. For query: empty"`
	Explanation string   `json:"explanation" jsonschema:"description=Why the answer is correct; shown after the attempt"`
	Hints       []string `json:"hints" jsonschema:"description=One to three hints for a stuck learner; from a gentle nudge to a strong pointer; never the answer itself"`
}

type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// leadingNumber matches a model-written number such as "2." or "1.3 " that
// would duplicate the IDs assigned here.
var leadingNumber = regexp.MustCompile(`^\s*(Module\s+|Lesson\s+)?\d+(\.\d+)*[.):]?\s+`)

func assignIDs(out *syllabusOut) []Module {
	modules := make([]Module, len(out.Modules))
	for mi, m := range out.Modules {
		modules[mi] = Module{ID: fmt.Sprint(mi + 1), Title: leadingNumber.ReplaceAllString(m.Title, ""), Summary: m.Summary}
		for li, l := range m.Lessons {
			modules[mi].Lessons = append(modules[mi].Lessons, LessonPlan{
				ID:         fmt.Sprintf("%d.%d", mi+1, li+1),
				Title:      leadingNumber.ReplaceAllString(l.Title, ""),
				Objectives: l.Objectives,
				DocURLs:    l.DocURLs,
			})
		}
	}
	return modules
}
