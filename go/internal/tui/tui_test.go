package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loomwork.dev/loomwork/internal/flow"
)

func press(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m.Update(msg)
	}
}

func newTestModel(t *testing.T) *Model {
	m := newModel(Deps{Folder: t.TempDir(), Agents: []string{"claude", "codex", "pi", "hermes"}, Backend: "test"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

func TestTabsRender(t *testing.T) {
	m := newTestModel(t)
	for i, want := range []string{"type a prompt", "workflow default", "context manager", "No tasks yet"} {
		if v := m.View(); !strings.Contains(v, want) {
			t.Fatalf("tab %d missing %q:\n%s", i, want, v)
		}
		press(m, "tab")
	}
}

func TestAskUnavailableMessage(t *testing.T) {
	m := newTestModel(t)
	m.d.AskErr = "no local model"
	m.input.SetValue("hello")
	press(m, "enter")
	if !strings.Contains(m.View(), "ask mode unavailable: no local model") {
		t.Fatal("expected explanation when Ask is nil")
	}
}

func TestWorkflowEditorValidatesAndSaves(t *testing.T) {
	m := newTestModel(t)
	press(m, "tab") // workflow tab
	// Make the verifier the same agent as the builder: invalid.
	press(m, "down", "down") // select verify
	m.wf.wf.Stages[2].Agents = []string{"claude"}
	if v := m.View(); !strings.Contains(v, "different agent") {
		t.Fatalf("invalid workflow not flagged:\n%s", v)
	}
	press(m, "s")
	if !strings.Contains(m.wf.msg, "different agent") || len(flow.ListWorkflows(m.d.Folder)) != 1 {
		t.Fatalf("invalid workflow was saved: %q", m.wf.msg)
	}
	press(m, "a") // cycle verifier agent: claude → codex
	press(m, "a") // → pi
	if got := m.wf.wf.Stages[2].Agents[0]; got != "pi" {
		t.Fatalf("agent cycle: %s", got)
	}
	press(m, "+", "p", "r", "s")
	w, err := flow.LoadWorkflow(m.d.Folder, "default")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := w.Stage(flow.KindVerify)
	if len(v.Agents) != 2 || v.Policy != "any" || v.Retry != 3 {
		t.Fatalf("saved stage: %+v", v)
	}
}

func TestContextTabTogglesAndSaves(t *testing.T) {
	m := newTestModel(t)
	press(m, "tab", "tab")   // context tab
	press(m, "down", "down") // verify
	press(m, "g")            // permit goal for verifier
	press(m, "s")
	p, err := flow.LoadContext(m.d.Folder)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Rule(flow.KindVerify, "pi").Has(flow.ItemGoal) {
		t.Fatal("toggle not saved")
	}
	press(m, "g", "s")
	p, _ = flow.LoadContext(m.d.Folder)
	if p.Rule(flow.KindVerify, "pi").Has(flow.ItemGoal) {
		t.Fatal("untoggle not saved")
	}
}

func TestPlanApprovalFlow(t *testing.T) {
	m := newTestModel(t)
	reply := make(chan bool, 1)
	m.Update(planAskMsg{[]flow.Module{{ID: "a", Title: "A"}}, reply})
	press(m, "x") // ignored while waiting
	if m.pendPlan == nil {
		t.Fatal("unrelated key cleared the pending approval")
	}
	press(m, "y")
	if !<-reply || m.pendPlan != nil {
		t.Fatal("approval not delivered")
	}
}
