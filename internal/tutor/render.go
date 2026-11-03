package tutor

import (
	"fmt"
	"strings"
)

// Outline is the human-readable syllabus; done marks lessons already written.
func (s *Syllabus) Outline(done func(id string) bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n%s\n\n", s.Title, s.Summary)
	for _, m := range s.Modules {
		fmt.Fprintf(&sb, "## %s. %s\n\n%s\n\n", m.ID, m.Title, m.Summary)
		for _, l := range m.Lessons {
			mark := " "
			if done != nil && done(l.ID) {
				mark = "x"
			}
			fmt.Fprintf(&sb, "- [%s] **%s** %s\n", mark, l.ID, l.Title)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// Markdown renders a lesson for reading; solutions are included only on request.
func (l *Lesson) Markdown(withSolutions bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s %s\n\n%s\n\n", l.ID, l.Title, strings.TrimSpace(l.Body))

	if len(l.Exercises) > 0 {
		sb.WriteString("## Exercises\n\n")
	}
	for i, ex := range l.Exercises {
		sb.WriteString(ex.Markdown(i+1, withSolutions))
	}

	if len(l.Sources) > 0 {
		sb.WriteString("## Sources\n\n")
		for _, s := range l.Sources {
			fmt.Fprintf(&sb, "- [%s](%s)\n", s.Title, s.URL)
		}
	}
	return sb.String()
}

// Markdown renders exercise n; the solution is included only on request.
func (ex *Exercise) Markdown(n int, withSolution bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "### Exercise %d (%s)\n\n%s\n\n", n, strings.ReplaceAll(ex.Kind, "_", " "), strings.TrimSpace(ex.Prompt))
	if ex.SetupSQL != "" {
		fmt.Fprintf(&sb, "Setup:\n\n```%s\n%s\n```\n\n", fenceLang(ex.SetupSQL), strings.TrimSpace(ex.SetupSQL))
	}
	for j, c := range ex.Choices {
		fmt.Fprintf(&sb, "%c) %s\n", 'a'+j, c)
	}
	if len(ex.Choices) > 0 {
		sb.WriteString("\n")
	}
	if withSolution {
		sb.WriteString(ex.SolutionMarkdown())
	}
	return sb.String()
}

// SolutionMarkdown renders the reference solution and its explanation.
func (ex *Exercise) SolutionMarkdown() string {
	var sb strings.Builder
	if ex.SolutionSQL != "" {
		fmt.Fprintf(&sb, "**Solution:**\n\n```%s\n%s\n```\n\n", fenceLang(ex.SolutionSQL), strings.TrimSpace(ex.SolutionSQL))
	}
	if ex.Answer != "" {
		fmt.Fprintf(&sb, "**Answer:** %s\n\n", ex.Answer)
	}
	if ex.Explanation != "" {
		fmt.Fprintf(&sb, "%s\n\n", ex.Explanation)
	}
	return sb.String()
}

// fenceLang labels a code block: Dev Tools console requests, ES|QL or SQL.
func fenceLang(code string) string {
	first := strings.ToUpper(strings.TrimSpace(code))
	for _, m := range []string{"GET ", "PUT ", "POST ", "DELETE ", "HEAD "} {
		if strings.HasPrefix(first, m) {
			return "console"
		}
	}
	for _, kw := range []string{"FROM ", "ROW ", "SHOW "} {
		if strings.HasPrefix(first, kw) && strings.Contains(first, "|") {
			return "esql"
		}
	}
	return "sql"
}
