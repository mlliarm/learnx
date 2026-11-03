package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mlliarm/learnx/internal/practice"
	"github.com/mlliarm/learnx/internal/store"
	"github.com/mlliarm/learnx/internal/tutor"
)

const editorHeight = 8

// practiceState steps through exercises; for a lesson or for review.
type practiceState struct {
	title string
	items []practice.Item
	i     int

	view   viewport.Model // exercise, feedback and solution
	editor textarea.Model // query and short answers
	choice int            // cursor for multiple choice

	feedback   string // Markdown shown under the exercise
	revealed   bool   // the solution is shown; enter moves on
	checking   bool   // waiting for a verdict or for hints
	hintsShown int
	spin       spinner.Model

	correct, answered int
	finished          bool
}

type hintsMsg struct {
	item  practice.Item
	added bool
	err   error
}

type checkedMsg struct {
	item   practice.Item
	answer string
	out    practice.Outcome
	err    error
}

func (a *app) startLessonPractice(l *tutor.Lesson) tea.Cmd {
	if len(l.Exercises) == 0 {
		a.flash = "This lesson has no exercises."
		return nil
	}
	items, err := practice.LessonItems(a.opt.Store, a.opt.Pack.Name, l, false)
	if err == nil && len(items) == 0 {
		items, err = practice.LessonItems(a.opt.Store, a.opt.Pack.Name, l, true) // all done: redo
	}
	if err != nil {
		a.flash = err.Error()
		return nil
	}
	return a.startPractice(fmt.Sprintf("Practice %s %s", l.ID, l.Title), items)
}

func (a *app) startPractice(title string, items []practice.Item) tea.Cmd {
	p := &practiceState{title: title, items: items, spin: spinner.New()}
	p.spin.Spinner = spinner.Dot
	p.editor = textarea.New()
	p.editor.ShowLineNumbers = false
	p.editor.CharLimit = 0
	a.prac = p
	a.screen = scrPractice
	p.resize(a)
	p.load(a)
	return textarea.Blink
}

func (p *practiceState) item() practice.Item { return p.items[p.i] }

func (p *practiceState) usesEditor() bool {
	return !p.finished && p.item().Exercise().Kind != "multiple_choice"
}

func (p *practiceState) resize(a *app) {
	h := a.height - 3 // title, gap, help
	if p.usesEditor() {
		h -= editorHeight + 2
	}
	p.view.Width, p.view.Height = a.width, max(h, 3)
	p.editor.SetWidth(max(a.width-2, 10))
	p.editor.SetHeight(editorHeight)
	p.render(a)
}

// queryPlaceholder hints at the shape of an answer with the first word of the
// reference solution, e.g. "FROM …" for ES|QL, without giving it away.
func queryPlaceholder(solution string) string {
	for _, line := range strings.Split(solution, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		return strings.Fields(t)[0] + " …"
	}
	return "Write your query…"
}

// load prepares the current exercise.
func (p *practiceState) load(a *app) {
	p.feedback, p.revealed, p.checking, p.choice, p.hintsShown = "", false, false, 0, 0
	p.editor.Reset()
	ex := p.item().Exercise()
	switch ex.Kind {
	case "query":
		p.editor.Placeholder = queryPlaceholder(ex.SolutionSQL)
		if err := a.checker.CanRun(); err != nil {
			p.feedback = fmt.Sprintf("Can't run query exercises: %v", err)
			p.reveal()
		}
	case "short_answer":
		p.editor.Placeholder = "Your answer…"
	}
	if p.usesEditor() && !p.revealed {
		p.editor.Focus()
	} else {
		p.editor.Blur()
	}
	p.resize(a)
	p.view.GotoTop()
}

func (p *practiceState) reveal() {
	p.revealed = true
	p.editor.Blur()
}

// render rebuilds the scrollable part: exercise, choices, feedback, solution.
func (p *practiceState) render(a *app) {
	if p.finished {
		return
	}
	it := p.item()
	ex := it.Exercise()
	var md strings.Builder
	if p.title == "Review" {
		fmt.Fprintf(&md, "*Lesson %s %s*\n\n", it.Lesson.ID, it.Lesson.Title)
	}
	shown := *ex
	shown.Choices = nil // drawn below with a cursor
	md.WriteString(shown.Markdown(it.Index+1, false))
	out := a.markdown(md.String(), a.width)

	if ex.Kind == "multiple_choice" {
		for j, c := range ex.Choices {
			cursor := "  "
			if j == p.choice && !p.revealed {
				cursor = "› "
			}
			out += fmt.Sprintf("  %s%c) %s\n", cursor, 'a'+j, c)
		}
		out += "\n"
	}
	if p.hintsShown > 0 {
		var hints strings.Builder
		for i, h := range ex.Hints[:p.hintsShown] {
			fmt.Fprintf(&hints, "> 💡 **Hint %d/%d:** %s\n>\n", i+1, len(ex.Hints), h)
		}
		out += a.markdown(hints.String(), a.width)
	}
	if p.feedback != "" {
		out += "  " + strings.ReplaceAll(p.feedback, "\n", "\n  ") + "\n"
	}
	if p.revealed {
		out += a.markdown(ex.SolutionMarkdown(), a.width)
	}
	p.view.SetContent(out)
}

func (a *app) updatePractice(msg tea.Msg) tea.Cmd {
	p := a.prac
	switch msg := msg.(type) {
	case checkedMsg:
		return p.checked(a, msg)
	case hintsMsg:
		return p.hintsLoaded(a, msg)
	case spinner.TickMsg:
		if !p.checking {
			return nil
		}
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(msg)
		return cmd
	case tea.KeyMsg:
		return p.key(a, msg)
	}
	var cmds []tea.Cmd
	var cmd tea.Cmd
	p.view, cmd = p.view.Update(msg)
	cmds = append(cmds, cmd)
	if p.usesEditor() {
		p.editor, cmd = p.editor.Update(msg)
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (p *practiceState) key(a *app, k tea.KeyMsg) tea.Cmd {
	if p.finished {
		if k.String() == "enter" || k.String() == "esc" || k.String() == "q" {
			a.backToSyllabus()
		}
		return nil
	}
	if p.checking {
		return nil
	}
	switch k.String() {
	case "ctrl+b":
		a.backToSyllabus()
		return nil
	case "pgup", "pgdown":
		var cmd tea.Cmd
		p.view, cmd = p.view.Update(k)
		return cmd
	}
	if p.revealed {
		switch k.String() {
		case "enter", "n", "right":
			p.next(a)
		case "up", "down", "k", "j":
			var cmd tea.Cmd
			p.view, cmd = p.view.Update(k)
			return cmd
		}
		return nil
	}
	if k.String() == "ctrl+g" {
		return p.hint(a)
	}
	if k.String() == "esc" { // skip: show the solution, record nothing
		p.feedback = "Skipped."
		p.reveal()
		p.render(a)
		return nil
	}

	ex := p.item().Exercise()
	if ex.Kind == "multiple_choice" {
		switch s := k.String(); {
		case s == "up" || s == "k":
			p.choice = (p.choice + len(ex.Choices) - 1) % len(ex.Choices)
		case s == "down" || s == "j":
			p.choice = (p.choice + 1) % len(ex.Choices)
		case len(s) == 1 && s[0] >= 'a' && int(s[0]-'a') < len(ex.Choices):
			p.choice = int(s[0] - 'a')
			return p.submit(a, string('a'+rune(p.choice)))
		case s == "enter":
			return p.submit(a, string('a'+rune(p.choice)))
		}
		p.render(a)
		return nil
	}
	if k.String() == "ctrl+s" {
		return p.submit(a, strings.TrimSpace(p.editor.Value()))
	}
	var cmd tea.Cmd
	p.editor, cmd = p.editor.Update(k)
	return cmd
}

// hint reveals the next hint; an exercise without hints gets some written
// by the model first.
func (p *practiceState) hint(a *app) tea.Cmd {
	ex := p.item().Exercise()
	switch {
	case len(ex.Hints) == 0:
		p.checking = true
		p.render(a)
		it := p.item()
		load := func() tea.Msg {
			_, added, err := a.checker.Hints(a.ctx, it.Exercise())
			return hintsMsg{item: it, added: added, err: err}
		}
		return tea.Batch(load, p.spin.Tick)
	case p.hintsShown < len(ex.Hints):
		p.hintsShown++
		p.feedback = ""
	default:
		p.feedback = "No more hints; esc shows the solution."
	}
	p.render(a)
	p.view.GotoBottom()
	return nil
}

func (p *practiceState) hintsLoaded(a *app, msg hintsMsg) tea.Cmd {
	p.checking = false
	if msg.item != p.item() {
		return nil
	}
	if msg.err != nil {
		p.feedback = badStyle.Render("Error: ") + msg.err.Error()
		p.render(a)
		return nil
	}
	cmd := p.hint(a)
	if msg.added {
		if err := a.opt.Store.Save(a.opt.Pack.Name, practice.LessonKey(msg.item.Lesson.ID), msg.item.Lesson); err != nil {
			p.feedback = badStyle.Render("Couldn't save the hints: ") + err.Error()
			p.render(a)
		}
	}
	return cmd
}

func (p *practiceState) submit(a *app, answer string) tea.Cmd {
	if answer == "" {
		return nil
	}
	p.checking = true
	p.feedback = ""
	p.render(a)
	it := p.item()
	check := func() tea.Msg {
		out, err := a.checker.Check(a.ctx, it.Exercise(), answer)
		return checkedMsg{item: it, answer: answer, out: out, err: err}
	}
	return tea.Batch(check, p.spin.Tick)
}

func (p *practiceState) checked(a *app, msg checkedMsg) tea.Cmd {
	p.checking = false
	if msg.item != p.item() {
		return nil
	}
	switch {
	case msg.err != nil:
		p.feedback = badStyle.Render("Error: ") + msg.err.Error()
	case msg.out.Invalid:
		p.feedback = msg.out.Feedback
	default:
		err := a.opt.Store.RecordAttempt(store.Attempt{Pack: a.opt.Pack.Name, Lesson: msg.item.Lesson.ID,
			Exercise: msg.item.Index, Correct: msg.out.Correct, Answer: msg.answer})
		mark := badStyle.Render("✗ ")
		if msg.out.Correct {
			mark = okStyle.Render("✓ ")
		}
		p.feedback = mark + msg.out.Feedback
		if err != nil {
			p.feedback += "\n" + badStyle.Render("Couldn't save the attempt: ") + err.Error()
		}
		if msg.out.Final {
			p.answered++
			if msg.out.Correct {
				p.correct++
			}
			p.reveal()
		}
	}
	p.render(a)
	p.view.GotoBottom()
	return nil
}

func (p *practiceState) next(a *app) {
	if p.i+1 >= len(p.items) {
		p.finished = true
		p.editor.Blur()
		p.resize(a)
		return
	}
	p.i++
	p.load(a)
}

func (a *app) viewPractice() string {
	p := a.prac
	if p.finished {
		body := fmt.Sprintf("\n  Done: %d of %d answered correctly.\n", p.correct, len(p.items))
		if skipped := len(p.items) - p.answered; skipped > 0 {
			body += fmt.Sprintf("  %d skipped.\n", skipped)
		}
		body += "\n  Missed exercises come back in review (r on the syllabus).\n"
		return titleStyle.Render(p.title) + "\n" + body + "\n" + helpStyle.Render("enter back to the syllabus")
	}

	head := titleStyle.Render(fmt.Sprintf("%s  ·  %d/%d", p.title, p.i+1, len(p.items)))
	out := head + "\n" + p.view.View() + "\n"
	if p.usesEditor() {
		if p.checking {
			out += " " + p.spin.View() + " checking…\n"
		} else {
			out += "\n"
		}
		out += p.editor.View() + "\n"
	}
	var help string
	switch {
	case p.checking:
		help = "checking…"
	case p.revealed:
		help = "enter next · ↑/↓ scroll · ctrl+b back"
	case p.item().Exercise().Kind == "multiple_choice":
		help = "↑/↓ choose · enter or a–z answer · ctrl+g hint · esc skip · ctrl+b back"
	default:
		help = "ctrl+s submit · ctrl+g hint · esc skip · pgup/pgdn scroll · ctrl+b back"
	}
	return out + helpStyle.Render(help)
}
