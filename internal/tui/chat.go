package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mlliarm/learnx/internal/llm"
	"github.com/mlliarm/learnx/internal/tutor"
)

const chatInputHeight = 3

// chatState is a conversation with the docs; the tutor (and its docs
// connection) is opened with the first question and closed on leaving.
type chatState struct {
	ctx    context.Context
	cancel context.CancelFunc
	status chan string

	tutor     *tutor.Tutor
	closeDocs func() error
	history   []llm.Turn
	md        strings.Builder // the transcript as Markdown

	view  viewport.Model
	input textarea.Model
	spin  spinner.Model
	busy  bool
	last  string // latest status line while busy
}

type answerMsg struct {
	chat      *chatState
	tutor     *tutor.Tutor
	closeDocs func() error
	question  string
	answer    string
	err       error
}

func (a *app) startChat() tea.Cmd {
	ctx, cancel := context.WithCancel(a.ctx)
	c := &chatState{ctx: ctx, cancel: cancel, status: make(chan string, 64), spin: spinner.New()}
	c.spin.Spinner = spinner.Dot
	c.input = textarea.New()
	c.input.ShowLineNumbers = false
	c.input.Placeholder = "Ask about " + a.opt.Pack.Title + "…"
	c.input.Focus()
	c.md.WriteString("*Answers are researched in the official docs and cite the pages used.*\n\n")
	a.chat = c
	a.screen = scrChat
	c.resize(a)
	return textarea.Blink
}

func (c *chatState) resize(a *app) {
	c.view.Width, c.view.Height = a.width, max(a.height-chatInputHeight-4, 3)
	c.input.SetWidth(max(a.width-2, 10))
	c.input.SetHeight(chatInputHeight)
	c.render(a)
}

func (c *chatState) render(a *app) {
	c.view.SetContent(a.markdown(c.md.String(), a.width))
	c.view.GotoBottom()
}

func (a *app) leaveChat() {
	if c := a.chat; c != nil {
		c.cancel()
		if c.closeDocs != nil {
			c.closeDocs()
		}
	}
	a.chat = nil
	a.backToSyllabus()
}

func (a *app) updateChat(msg tea.Msg) tea.Cmd {
	c := a.chat
	switch msg := msg.(type) {
	case answerMsg:
		if msg.chat != c {
			return nil // from a chat already left
		}
		c.busy, c.last = false, ""
		if msg.tutor != nil {
			c.tutor, c.closeDocs = msg.tutor, msg.closeDocs
		}
		if msg.err != nil {
			c.md.WriteString("> **Error:** " + msg.err.Error() + "\n\n")
		} else {
			c.md.WriteString(msg.answer + "\n\n---\n\n")
			c.history = append(c.history, llm.Turn{Role: "user", Text: msg.question}, llm.Turn{Role: "assistant", Text: msg.answer})
		}
		c.input.Focus()
		c.render(a)
		return nil
	case spinner.TickMsg:
		if !c.busy {
			return nil
		}
		var cmd tea.Cmd
		c.spin, cmd = c.spin.Update(msg)
		return cmd
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			a.leaveChat()
			return nil
		case "pgup", "pgdown":
			var cmd tea.Cmd
			c.view, cmd = c.view.Update(msg)
			return cmd
		case "ctrl+s":
			q := strings.TrimSpace(c.input.Value())
			if q == "" || c.busy {
				return nil
			}
			return c.ask(a, q)
		}
		if c.busy {
			return nil
		}
		var cmd tea.Cmd
		c.input, cmd = c.input.Update(msg)
		return cmd
	}
	var cmd tea.Cmd
	c.view, cmd = c.view.Update(msg)
	return cmd
}

func (c *chatState) ask(a *app, q string) tea.Cmd {
	c.busy = true
	c.input.Reset()
	c.input.Blur()
	c.md.WriteString("**You:** " + q + "\n\n")
	c.render(a)

	opt, t, history := a.opt, c.tutor, append([]llm.Turn(nil), c.history...)
	done := make(chan struct{}) // stops the status listener with the answer
	run := func() tea.Msg {
		defer close(done)
		msg := answerMsg{chat: c, question: q}
		if t == nil {
			var err error
			t, msg.closeDocs, err = tutor.Open(c.ctx, opt.Pack, opt.Model, "", &lineWriter{ch: c.status})
			if err != nil {
				msg.err = err
				return msg
			}
			msg.tutor = t
		}
		msg.answer, msg.err = t.Ask(c.ctx, history, q)
		return msg
	}
	return tea.Batch(run, c.spin.Tick, waitStatusUntil(c.status, done))
}

// waitStatusUntil is waitStatus for a channel that stays open: it gives up
// once done is closed.
func waitStatusUntil(ch chan string, done chan struct{}) tea.Cmd {
	return func() tea.Msg {
		select {
		case line := <-ch:
			return statusMsg{line: line, ch: ch, done: done}
		case <-done:
			return nil
		}
	}
}

func (a *app) viewChat() string {
	c := a.chat
	out := titleStyle.Render("Ask the "+a.opt.Pack.Title+" docs") + "\n" + c.view.View() + "\n"
	if c.busy {
		out += " " + c.spin.View() + " researching… " + truncate(strings.TrimSpace(c.last), a.width-20) + "\n"
	} else {
		out += "\n"
	}
	out += c.input.View() + "\n"
	return out + helpStyle.Render("ctrl+s ask · pgup/pgdn scroll · esc back")
}
