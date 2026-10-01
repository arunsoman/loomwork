// Package tui is the interactive terminal UI: enter a prompt, define a
// workflow, control what each agent can see, and watch who is doing what.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"loomwork.dev/loomwork/internal/flow"
)

// Deps is everything the TUI needs from the host program.
type Deps struct {
	Folder string
	Agents []string // agents that exist (for pickers)
	// Backend describes the chat backend, e.g. "ollama (local)".
	Backend string
	// Ask answers a one-shot prompt. Nil means ask mode is unavailable;
	// AskErr then says why.
	Ask    func(ctx context.Context, prompt string) (string, error)
	AskErr string
	// NewEngine builds an engine for a workflow, with context policy applied.
	NewEngine func(w *flow.Workflow) (*flow.Engine, error)
}

var (
	titleSt  = lipgloss.NewStyle().Bold(true)
	activeSt = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
	tabSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
	dimSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okSt     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	selSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238"))
)

const (
	tabPrompt = iota
	tabWorkflow
	tabContext
	tabStatus
)

var tabNames = []string{"Prompt", "Workflow", "Context", "Status"}

type (
	tickMsg    time.Time
	askDoneMsg struct {
		text string
		err  error
	}
	flowLogMsg  string
	flowDoneMsg struct {
		res *flow.Result
		err error
	}
	planAskMsg struct {
		mods  []flow.Module
		reply chan bool
	}
	pushAskMsg struct {
		branch string
		reply  chan bool
	}
)

// Model is the root bubbletea model.
type Model struct {
	d    Deps
	tab  int
	w, h int
	send func(tea.Msg)

	// prompt tab
	input    textinput.Model
	flowMode bool
	busy     bool
	cancel   context.CancelFunc
	lines    []string // transcript
	pendPlan *planAskMsg
	pendPush *pushAskMsg
	wfName   string
	wfNames  []string

	wf  workflowTab
	ctx contextTab
	st  statusTab
}

// Run starts the TUI and blocks until it exits.
func Run(d Deps) error {
	m := newModel(d)
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.send = p.Send
	_, err := p.Run()
	return err
}

func newModel(d Deps) *Model {
	in := textinput.New()
	in.Placeholder = "type a prompt, then Enter"
	in.Focus()
	in.CharLimit = 4000
	m := &Model{d: d, input: in, wfName: "default", wfNames: flow.ListWorkflows(d.Folder)}
	m.wf = newWorkflowTab(m)
	m.ctx = newContextTab(m)
	m.st = newStatusTab(m)
	m.say(dimSt.Render("Ctrl+T switches Ask/Flow. Tab changes view. Ctrl+C quits."))
	return m
}

func (m *Model) say(s string) {
	m.lines = append(m.lines, s)
	if len(m.lines) > 500 {
		m.lines = m.lines[len(m.lines)-500:]
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.Width = max(10, msg.Width-6)
		return m, nil
	case tickMsg:
		m.st.refresh(m)
		return m, tick()
	case askDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.say(errSt.Render("error: " + msg.err.Error()))
		} else {
			m.say(msg.text)
		}
		return m, nil
	case flowLogMsg:
		m.say(dimSt.Render(string(msg)))
		m.st.log = append(m.st.log, string(msg))
		return m, nil
	case flowDoneMsg:
		m.busy, m.cancel = false, nil
		if msg.res != nil {
			m.say(fmt.Sprintf("branch %s · merged %s · failed %s · pushed %v", msg.res.Branch,
				orDash(msg.res.Merged), orDash(msg.res.Failed), msg.res.Pushed))
			if msg.res.Note != "" {
				m.say(msg.res.Note)
			}
		}
		if msg.err != nil {
			m.say(errSt.Render("flow: " + msg.err.Error()))
		} else {
			m.say(okSt.Render("flow finished"))
		}
		return m, nil
	case planAskMsg:
		m.pendPlan = &msg
		m.say(titleSt.Render("Proposed plan:"))
		for _, mod := range msg.mods {
			m.say(fmt.Sprintf("  • %s — %s (after: %s) owns %s", mod.ID, mod.Title, orDash(mod.DependsOn), orDash(mod.Paths)))
		}
		m.say("Build this plan? [y/n]")
		return m, nil
	case pushAskMsg:
		m.pendPush = &msg
		m.say(fmt.Sprintf("Push branch %s to origin? [y/n]", msg.branch))
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func orDash(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ",")
}

func (m *Model) editing() bool {
	return (m.tab == tabWorkflow && m.wf.editing()) || (m.tab == tabContext && m.ctx.editing())
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if m.pendPlan != nil || m.pendPush != nil {
		ans := s == "y" || s == "Y"
		if s == "y" || s == "Y" || s == "n" || s == "N" {
			if m.pendPlan != nil {
				m.pendPlan.reply <- ans
				m.pendPlan = nil
			} else {
				m.pendPush.reply <- ans
				m.pendPush = nil
			}
			m.say(map[bool]string{true: "approved", false: "declined"}[ans])
		}
		return m, nil
	}
	if !m.editing() {
		switch s {
		case "tab":
			m.tab = (m.tab + 1) % len(tabNames)
			return m, nil
		case "shift+tab":
			m.tab = (m.tab + len(tabNames) - 1) % len(tabNames)
			return m, nil
		}
	}
	switch m.tab {
	case tabPrompt:
		return m.promptKey(k)
	case tabWorkflow:
		return m, m.wf.key(m, k)
	case tabContext:
		return m, m.ctx.key(m, k)
	default:
		m.st.key(m, k)
	}
	return m, nil
}

func (m *Model) promptKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+t":
		m.flowMode = !m.flowMode
		return m, nil
	case "ctrl+w":
		for i, n := range m.wfNames {
			if n == m.wfName {
				m.wfName = m.wfNames[(i+1)%len(m.wfNames)]
				break
			}
		}
		return m, nil
	case "esc":
		if m.busy && m.cancel != nil {
			m.cancel()
			m.say(dimSt.Render("cancelling…"))
		}
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" || m.busy {
			return m, nil
		}
		m.input.SetValue("")
		if m.flowMode {
			return m, m.startFlow(text)
		}
		return m, m.startAsk(text)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m *Model) startAsk(text string) tea.Cmd {
	m.say(titleSt.Render("› " + text))
	if m.d.Ask == nil {
		m.say(errSt.Render("ask mode unavailable: " + m.d.AskErr))
		return nil
	}
	m.busy = true
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ask := m.d.Ask
	return func() tea.Msg {
		defer cancel()
		out, err := ask(ctx, text)
		return askDoneMsg{out, err}
	}
}

func (m *Model) startFlow(goal string) tea.Cmd {
	m.say(titleSt.Render("› flow[" + m.wfName + "]: " + goal))
	w, err := flow.LoadWorkflow(m.d.Folder, m.wfName)
	if err != nil {
		m.say(errSt.Render(err.Error()))
		return nil
	}
	e, err := m.d.NewEngine(w)
	if err != nil {
		m.say(errSt.Render(err.Error()))
		return nil
	}
	send := m.send
	e.Logf = func(f string, a ...any) { send(flowLogMsg(fmt.Sprintf(f, a...))) }
	e.ApprovePlan = func(mods []flow.Module) bool {
		reply := make(chan bool, 1)
		send(planAskMsg{mods, reply})
		return <-reply
	}
	e.ConfirmPush = func(b string) bool {
		reply := make(chan bool, 1)
		send(pushAskMsg{b, reply})
		return <-reply
	}
	m.busy = true
	m.st.log = nil
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.tab = tabPrompt
	return func() tea.Msg {
		defer cancel()
		res, err := e.Run(ctx, goal)
		return flowDoneMsg{res, err}
	}
}

func (m *Model) View() string {
	var b strings.Builder
	for i, n := range tabNames {
		if i == m.tab {
			b.WriteString(activeSt.Render(n))
		} else {
			b.WriteString(tabSt.Render(n))
		}
	}
	b.WriteString("  " + dimSt.Render(m.d.Backend) + "\n\n")
	switch m.tab {
	case tabPrompt:
		b.WriteString(m.promptView())
	case tabWorkflow:
		b.WriteString(m.wf.view(m))
	case tabContext:
		b.WriteString(m.ctx.view(m))
	default:
		b.WriteString(m.st.view(m))
	}
	return b.String()
}

func (m *Model) promptView() string {
	mode := "ASK"
	if m.flowMode {
		mode = "FLOW[" + m.wfName + "]"
	}
	room := max(3, m.h-8)
	lines := m.lines
	if len(lines) > room {
		lines = lines[len(lines)-room:]
	}
	var b strings.Builder
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n\n")
	state := ""
	if m.busy {
		state = dimSt.Render(" working… (Esc cancels)")
	}
	fmt.Fprintf(&b, "%s%s\n%s\n", titleSt.Render(mode), state, m.input.View())
	b.WriteString(dimSt.Render("Ctrl+T ask/flow · Ctrl+W workflow · Tab view"))
	return b.String()
}
