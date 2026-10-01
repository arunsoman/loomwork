package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"loomwork.dev/loomwork/internal/flow"
)

const (
	wfNav = iota
	wfEditTest
	wfNewName
)

var kindRank = map[string]int{flow.KindPlan: 0, flow.KindBuild: 1, flow.KindVerify: 2, flow.KindShip: 3}

// workflowTab edits a workflow: which agent runs each stage, and the options
// of each stage. It edits the same JSON file the CLI runs.
type workflowTab struct {
	wf    *flow.Workflow
	sel   int
	mode  int
	in    textinput.Model
	msg   string
	dirty bool
}

func newWorkflowTab(m *Model) workflowTab {
	t := workflowTab{in: textinput.New()}
	t.load(m, m.wfName)
	return t
}

func (t *workflowTab) load(m *Model, name string) {
	w, err := flow.LoadWorkflow(m.d.Folder, name)
	if err != nil {
		t.msg = err.Error()
		return
	}
	t.wf, t.sel, t.dirty, t.msg = w, 0, false, ""
	m.wfName = name
}

func (t *workflowTab) editing() bool { return t.mode != wfNav }

func (t *workflowTab) known(m *Model) map[string]bool {
	k := map[string]bool{}
	for _, a := range m.d.Agents {
		k[a] = true
	}
	return k
}

func nextAgent(all []string, cur string) string {
	for i, a := range all {
		if a == cur {
			return all[(i+1)%len(all)]
		}
	}
	if len(all) > 0 {
		return all[0]
	}
	return cur
}

// builder returns the build stage's agent, which verifiers must not be.
func (t *workflowTab) builder() string {
	if s, ok := t.wf.Stage(flow.KindBuild); ok && len(s.Agents) > 0 {
		return s.Agents[0]
	}
	return ""
}

// pickable lists agents a stage may use: verifiers exclude the builder.
func (t *workflowTab) pickable(m *Model, kind string) []string {
	var out []string
	for _, a := range m.d.Agents {
		if kind == flow.KindVerify && a == t.builder() {
			continue
		}
		out = append(out, a)
	}
	return out
}

func (t *workflowTab) key(m *Model, k tea.KeyMsg) tea.Cmd {
	if t.mode != wfNav {
		switch k.String() {
		case "esc":
			t.mode = wfNav
			return nil
		case "enter":
			v := strings.TrimSpace(t.in.Value())
			if t.mode == wfEditTest {
				t.wf.TestCmd, t.dirty = v, true
			} else {
				t.wf.Name, t.dirty = v, true
				if err := flow.SaveWorkflow(m.d.Folder, t.wf, t.known(m)); err != nil {
					t.msg = err.Error()
				} else {
					t.dirty, t.msg = false, "saved as "+v
					m.wfName, m.wfNames = v, flow.ListWorkflows(m.d.Folder)
				}
			}
			t.mode = wfNav
			return nil
		}
		var cmd tea.Cmd
		t.in, cmd = t.in.Update(k)
		return cmd
	}
	if t.wf == nil {
		return nil
	}
	st := func() *flow.Stage {
		if t.sel >= 0 && t.sel < len(t.wf.Stages) {
			return &t.wf.Stages[t.sel]
		}
		return nil
	}
	mark := func() { t.dirty = true; t.msg = "" }
	switch k.String() {
	case "up", "k":
		if t.sel > 0 {
			t.sel--
		}
	case "down", "j":
		if t.sel < len(t.wf.Stages)-1 {
			t.sel++
		}
	case "a":
		if s := st(); s != nil && len(s.Agents) > 0 {
			if opts := t.pickable(m, s.Kind); len(opts) > 0 {
				s.Agents[len(s.Agents)-1] = nextAgent(opts, s.Agents[len(s.Agents)-1])
			}
			mark()
		}
	case "+":
		if s := st(); s != nil && s.Kind == flow.KindVerify {
			for _, a := range t.pickable(m, flow.KindVerify) {
				if !contains(s.Agents, a) {
					s.Agents = append(s.Agents, a)
					mark()
					break
				}
			}
		}
	case "-":
		if s := st(); s != nil && s.Kind == flow.KindVerify && len(s.Agents) > 1 {
			s.Agents = s.Agents[:len(s.Agents)-1]
			mark()
		}
	case "p":
		if s := st(); s != nil && s.Kind == flow.KindVerify {
			if s.Policy == "any" {
				s.Policy = "all"
			} else {
				s.Policy = "any"
			}
			mark()
		}
	case "r":
		if s := st(); s != nil && s.Kind == flow.KindVerify {
			s.Retry = (s.Retry + 1) % 6
			mark()
		}
	case "P":
		if s := st(); s != nil && s.Kind == flow.KindShip {
			s.Push = !s.Push
			mark()
		}
	case "n":
		t.addStage(m)
	case "d":
		if len(t.wf.Stages) > 0 {
			t.wf.Stages = append(t.wf.Stages[:t.sel], t.wf.Stages[t.sel+1:]...)
			t.sel = max(0, min(t.sel, len(t.wf.Stages)-1))
			mark()
		}
	case "K":
		if t.sel > 0 {
			t.wf.Stages[t.sel], t.wf.Stages[t.sel-1] = t.wf.Stages[t.sel-1], t.wf.Stages[t.sel]
			t.sel--
			mark()
		}
	case "J":
		if t.sel < len(t.wf.Stages)-1 {
			t.wf.Stages[t.sel], t.wf.Stages[t.sel+1] = t.wf.Stages[t.sel+1], t.wf.Stages[t.sel]
			t.sel++
			mark()
		}
	case "x":
		// none → each agent in turn → none
		opts := append([]string{""}, m.d.Agents...)
		t.wf.ContextAgent = nextAgent(opts, t.wf.ContextAgent)
		mark()
	case "m":
		t.parallel()
		mark()
	case "t":
		t.mode = wfEditTest
		t.in.SetValue(t.wf.TestCmd)
		t.in.Placeholder = "test command run in each worktree, e.g. go test ./..."
		t.in.Focus()
		return textinput.Blink
	case "w":
		names := flow.ListWorkflows(m.d.Folder)
		sort.Strings(names)
		t.load(m, nextAgent(names, m.wfName))
	case "c":
		t.mode = wfNewName
		t.in.SetValue("")
		t.in.Placeholder = "name for the new workflow"
		t.in.Focus()
		return textinput.Blink
	case "s":
		if err := flow.SaveWorkflow(m.d.Folder, t.wf, t.known(m)); err != nil {
			t.msg = err.Error()
		} else {
			t.dirty, t.msg = false, "saved"
			m.wfNames = flow.ListWorkflows(m.d.Folder)
		}
	}
	return nil
}

func (t *workflowTab) parallel() { t.wf.MaxParallel = (t.wf.MaxParallel)%4 + 1 }

// addStage inserts the first missing stage kind at its proper position.
func (t *workflowTab) addStage(m *Model) {
	have := map[string]bool{}
	for _, s := range t.wf.Stages {
		have[s.Kind] = true
	}
	for _, kind := range []string{flow.KindPlan, flow.KindBuild, flow.KindVerify, flow.KindShip} {
		if have[kind] {
			continue
		}
		agent := ""
		if len(m.d.Agents) > 0 {
			agent = m.d.Agents[0]
		}
		ns := flow.Stage{Kind: kind, Agents: []string{agent}}
		if kind == flow.KindVerify {
			ns.Policy = "all"
		}
		i := 0
		for i < len(t.wf.Stages) && kindRank[t.wf.Stages[i].Kind] < kindRank[kind] {
			i++
		}
		t.wf.Stages = append(t.wf.Stages[:i], append([]flow.Stage{ns}, t.wf.Stages[i:]...)...)
		t.sel, t.dirty = i, true
		return
	}
	t.msg = "all four stages are already present"
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (t *workflowTab) view(m *Model) string {
	if t.wf == nil {
		return errSt.Render(t.msg)
	}
	var b strings.Builder
	dirty := ""
	if t.dirty {
		dirty = " (unsaved)"
	}
	fmt.Fprintf(&b, "%s%s   parallel: %d   tests: %s\n", titleSt.Render("workflow "+t.wf.Name), dirty, max(1, t.wf.MaxParallel), orStr(t.wf.TestCmd, "none"))
	fmt.Fprintf(&b, "context manager agent: %s\n\n", orStr(t.wf.ContextAgent, "none (Loom's own selection only)"))
	for i, s := range t.wf.Stages {
		opt := ""
		switch s.Kind {
		case flow.KindVerify:
			pol := s.Policy
			if pol == "" {
				pol = "all"
			}
			opt = fmt.Sprintf("policy %s · retries %d", pol, s.Retry)
		case flow.KindShip:
			opt = fmt.Sprintf("push %v (asks to confirm)", s.Push)
		}
		row := fmt.Sprintf("%d. %-7s → %-24s %s", i+1, s.Kind, strings.Join(s.Agents, " + "), opt)
		if i == t.sel {
			row = selSt.Render(row)
		}
		b.WriteString(row + "\n")
	}
	b.WriteString("\n")
	if err := t.wf.Validate(t.known(m)); err != nil {
		b.WriteString(errSt.Render("✗ "+err.Error()) + "\n")
	} else {
		b.WriteString(okSt.Render("✓ valid") + "\n")
	}
	if t.msg != "" {
		b.WriteString(t.msg + "\n")
	}
	if t.mode != wfNav {
		b.WriteString("\n" + t.in.View() + "\n" + dimSt.Render("Enter confirms · Esc cancels"))
		return b.String()
	}
	b.WriteString(dimSt.Render("\n↑/↓ stage · a agent · +/- verifiers · p policy · r retries · P push · m parallel · t tests\n" +
		"n add stage · d delete · J/K move · s save · c save as new · w next workflow"))
	return b.String()
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
