// Package tui is learnx's full-screen terminal UI: the syllabus, a lesson
// viewer, exercise practice and spaced review.
package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/mlliarm/learnx/internal/pack"
	"github.com/mlliarm/learnx/internal/practice"
	"github.com/mlliarm/learnx/internal/store"
	"github.com/mlliarm/learnx/internal/tutor"
)

type Options struct {
	Pack     *pack.Pack
	Store    *store.Store
	Syllabus *tutor.Syllabus
	Model    string // LLM for writing lessons and grading short answers
	Effort   string // lesson-writing effort
}

// Run shows the TUI until the learner quits.
func Run(ctx context.Context, opt Options) error {
	// Bubble Tea's init has already asked the terminal for its background,
	// so this is answered from lipgloss's cache.
	style := "light"
	if lipgloss.HasDarkBackground() {
		style = "dark"
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a := &app{ctx: ctx, opt: opt, style: style, checker: practice.NewChecker(opt.Pack, opt.Model, io.Discard)}
	a.initList()
	_, err := tea.NewProgram(a, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx)).Run()
	if err == tea.ErrProgramKilled && ctx.Err() == nil {
		err = nil
	}
	return err
}

type screen int

const (
	scrSyllabus screen = iota
	scrLesson
	scrWriting
	scrPractice
	scrChat
)

type app struct {
	ctx     context.Context
	opt     Options
	style   string // glamour style: "dark" or "light"
	checker *practice.Checker

	width, height int
	screen        screen
	flash         string // one-line notice under the syllabus

	list list.Model

	lesson *tutor.Lesson
	view   viewport.Model

	writing writingState
	prac    *practiceState
	chat    *chatState
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	helpStyle  = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	flashStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Padding(0, 1)
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	badStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
)

func (a *app) Init() tea.Cmd { return nil }

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.list.SetSize(msg.Width, msg.Height-1)
		a.view.Width, a.view.Height = msg.Width, msg.Height-2
		if a.screen == scrLesson {
			a.view.SetContent(a.markdown(a.lesson.Markdown(false), msg.Width))
		}
		if a.prac != nil {
			a.prac.resize(a)
		}
		if a.chat != nil {
			a.chat.resize(a)
		}
		return a, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
	case lessonWrittenMsg:
		return a, a.lessonWritten(msg)
	case statusMsg:
		return a, a.writingStatus(msg)
	}

	switch a.screen {
	case scrLesson:
		return a, a.updateLesson(msg)
	case scrWriting:
		return a, a.updateWriting(msg)
	case scrPractice:
		return a, a.updatePractice(msg)
	case scrChat:
		return a, a.updateChat(msg)
	default:
		return a, a.updateSyllabus(msg)
	}
}

func (a *app) View() string {
	if a.width == 0 {
		return ""
	}
	switch a.screen {
	case scrLesson:
		return a.viewLesson()
	case scrWriting:
		return a.viewWriting()
	case scrPractice:
		return a.viewPractice()
	case scrChat:
		return a.viewChat()
	default:
		help := "enter read · p practice · r review · a ask the docs · / filter · q quit"
		if a.flash != "" {
			return a.list.View() + "\n" + flashStyle.Render(a.flash)
		}
		return a.list.View() + "\n" + helpStyle.Render(help)
	}
}

// markdown renders Markdown for the terminal at the given width.
func (a *app) markdown(md string, width int) string {
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(a.style), glamour.WithWordWrap(max(width-4, 20)))
	if err != nil {
		return md
	}
	out, err := tutor.RenderTerminal(r, md)
	if err != nil {
		return md
	}
	return out
}

// --- syllabus ---

type lessonItem struct {
	plan    tutor.LessonPlan
	written bool
	done    int
	total   int
}

func (i lessonItem) Title() string       { return i.plan.ID + "  " + i.plan.Title }
func (i lessonItem) FilterValue() string { return i.plan.ID + " " + i.plan.Title }
func (i lessonItem) Description() string {
	switch {
	case !i.written:
		return "not written yet"
	case i.total == 0:
		return "written · no exercises"
	case i.done == i.total:
		return fmt.Sprintf("written · all %d exercises done ✓", i.total)
	default:
		return fmt.Sprintf("written · %d of %d exercises done", i.done, i.total)
	}
}

func (a *app) initList() {
	a.list = list.New(nil, list.NewDefaultDelegate(), 0, 0)
	a.list.SetShowHelp(false)
	a.list.DisableQuitKeybindings()
	a.refreshList()
}

func (a *app) refreshList() {
	st, name := a.opt.Store, a.opt.Pack.Name
	var items []list.Item
	for _, m := range a.opt.Syllabus.Modules {
		for _, l := range m.Lessons {
			it := lessonItem{plan: l}
			var lesson tutor.Lesson
			if found, err := st.Load(name, practice.LessonKey(l.ID), &lesson); err == nil && found {
				it.written = true
				it.done, it.total, _ = practice.LessonProgress(st, name, &lesson)
			}
			items = append(items, it)
		}
	}
	a.list.SetItems(items)
	title := a.opt.Syllabus.Title
	if due, err := practice.DueItems(st, name, time.Now()); err == nil && len(due) > 0 {
		title += fmt.Sprintf("  ·  %d to review", len(due))
	}
	a.list.Title = title
}

func (a *app) updateSyllabus(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok && a.list.FilterState() != list.Filtering {
		a.flash = ""
		it, _ := a.list.SelectedItem().(lessonItem)
		switch k.String() {
		case "q", "esc":
			if a.list.FilterState() == list.Unfiltered {
				return tea.Quit
			}
		case "enter":
			return a.openLesson(it.plan.ID)
		case "p":
			if !it.written {
				a.flash = "Open the lesson first (enter) so it gets written."
				return nil
			}
			l, err := a.loadLesson(it.plan.ID)
			if err != nil {
				a.flash = err.Error()
				return nil
			}
			return a.startLessonPractice(l)
		case "a":
			return a.startChat()
		case "r":
			items, err := practice.DueItems(a.opt.Store, a.opt.Pack.Name, time.Now())
			if err != nil {
				a.flash = err.Error()
				return nil
			}
			if len(items) == 0 {
				a.flash = "Nothing to review right now."
				return nil
			}
			return a.startPractice("Review", items)
		}
	}
	var cmd tea.Cmd
	a.list, cmd = a.list.Update(msg)
	return cmd
}

func (a *app) backToSyllabus() {
	a.screen = scrSyllabus
	a.prac = nil
	a.refreshList()
}

// --- lesson viewer ---

func (a *app) loadLesson(id string) (*tutor.Lesson, error) {
	var l tutor.Lesson
	found, err := a.opt.Store.Load(a.opt.Pack.Name, practice.LessonKey(id), &l)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &l, nil
}

func (a *app) openLesson(id string) tea.Cmd {
	l, err := a.loadLesson(id)
	if err != nil {
		a.flash = err.Error()
		return nil
	}
	if l == nil {
		return a.startWriting(id)
	}
	a.showLesson(l)
	return nil
}

func (a *app) showLesson(l *tutor.Lesson) {
	a.lesson = l
	a.screen = scrLesson
	a.view = viewport.New(a.width, a.height-2)
	a.view.SetContent(a.markdown(l.Markdown(false), a.width))
}

func (a *app) updateLesson(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "q":
			a.backToSyllabus()
			return nil
		case "p":
			return a.startLessonPractice(a.lesson)
		}
	}
	var cmd tea.Cmd
	a.view, cmd = a.view.Update(msg)
	return cmd
}

func (a *app) viewLesson() string {
	head := titleStyle.Render(a.opt.Syllabus.Title) // the lesson's own Markdown carries its title
	help := helpStyle.Render(fmt.Sprintf("↑/↓ scroll · p practice · esc back · %3.f%%", a.view.ScrollPercent()*100))
	return head + "\n" + a.view.View() + "\n" + help
}

// --- writing a lesson ---

type writingState struct {
	id     string
	lines  []string
	status chan string
	cancel context.CancelFunc
	spin   spinner.Model
	start  time.Time
}

type statusMsg struct {
	line string
	ch   chan string
	done chan struct{} // set when the channel outlives the run (chat)
}

type lessonWrittenMsg struct {
	id     string
	lesson *tutor.Lesson
	err    error
}

// lineWriter turns writes into lines on a channel.
type lineWriter struct {
	ch  chan string
	buf string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf += string(p)
	for {
		line, rest, ok := strings.Cut(w.buf, "\n")
		if !ok {
			return len(p), nil
		}
		w.buf = rest
		select {
		case w.ch <- line:
		default: // the UI is behind; drop the line rather than block the LLM
		}
	}
}

func waitStatus(ch chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return nil
		}
		return statusMsg{line: line, ch: ch}
	}
}

func (a *app) startWriting(id string) tea.Cmd {
	ctx, cancel := context.WithCancel(a.ctx)
	ch := make(chan string, 64)
	a.writing = writingState{id: id, status: ch, cancel: cancel, spin: spinner.New(), start: time.Now()}
	a.writing.spin.Spinner = spinner.Dot
	a.screen = scrWriting
	opt := a.opt
	write := func() tea.Msg {
		defer close(ch)
		w := &lineWriter{ch: ch}
		t, closeDocs, err := tutor.Open(ctx, opt.Pack, opt.Model, opt.Effort, w)
		if err != nil {
			return lessonWrittenMsg{id: id, err: err}
		}
		defer closeDocs()
		fmt.Fprintf(w, "Writing lesson %s with %s…\n", id, t.LLM.Model)
		l, err := t.WriteLesson(ctx, opt.Syllabus, id)
		if err == nil {
			err = opt.Store.Save(opt.Pack.Name, practice.LessonKey(id), l)
		}
		if err == nil {
			err = opt.Store.ResetLesson(opt.Pack.Name, id)
		}
		return lessonWrittenMsg{id: id, lesson: l, err: err}
	}
	return tea.Batch(write, waitStatus(ch), a.writing.spin.Tick)
}

func (a *app) writingStatus(msg statusMsg) tea.Cmd {
	if a.chat != nil && msg.ch == a.chat.status {
		if strings.TrimSpace(msg.line) != "" {
			a.chat.last = msg.line
		}
		return waitStatusUntil(msg.ch, msg.done)
	}
	if msg.ch != a.writing.status {
		return nil // from a cancelled run
	}
	a.writing.lines = append(a.writing.lines, msg.line)
	return waitStatus(msg.ch)
}

func (a *app) lessonWritten(msg lessonWrittenMsg) tea.Cmd {
	if a.screen != scrWriting || msg.id != a.writing.id {
		return nil
	}
	a.writing.cancel()
	if msg.err != nil {
		a.backToSyllabus()
		if a.ctx.Err() == nil && !strings.Contains(msg.err.Error(), "context canceled") {
			a.flash = "Couldn't write lesson " + msg.id + ": " + msg.err.Error()
		}
		return nil
	}
	a.showLesson(msg.lesson)
	return nil
}

func (a *app) updateWriting(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		a.writing.cancel()
		a.backToSyllabus()
		a.flash = "Cancelled."
		return nil
	}
	var cmd tea.Cmd
	a.writing.spin, cmd = a.writing.spin.Update(msg)
	return cmd
}

func (a *app) viewWriting() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Writing lesson "+a.writing.id) + "\n\n")
	fmt.Fprintf(&b, " %s researching the docs and writing… %s\n\n", a.writing.spin.View(),
		time.Since(a.writing.start).Round(time.Second))
	lines := a.writing.lines
	if room := a.height - 6; room > 0 && len(lines) > room {
		lines = lines[len(lines)-room:]
	}
	for _, l := range lines {
		b.WriteString(" " + truncate(l, a.width-2) + "\n")
	}
	return lipgloss.PlaceVertical(a.height-1, lipgloss.Top, b.String()) + "\n" + helpStyle.Render("esc cancel")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if n <= 1 || len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
