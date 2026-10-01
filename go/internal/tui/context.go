package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"loomwork.dev/loomwork/internal/flow"
)

var ctxRoles = []string{flow.KindPlan, flow.KindBuild, flow.KindVerify, flow.KindShip}

const (
	ctxNav = iota
	ctxDeny
	ctxAllow
)

// contextTab edits .agent/context.json: what each role's agent can see.
// Per-agent overrides in the file are shown but edited by hand.
type contextTab struct {
	pol   *flow.ContextPolicy
	sel   int
	mode  int
	in    textinput.Model
	msg   string
	dirty bool
}

func newContextTab(m *Model) contextTab {
	t := contextTab{in: textinput.New()}
	p, err := flow.LoadContext(m.d.Folder)
	if err != nil {
		t.msg = err.Error()
		p = flow.DefaultContext()
	}
	t.pol = p
	return t
}

func (t *contextTab) editing() bool { return t.mode != ctxNav }

func (t *contextTab) roleRule() flow.ContextRule {
	if t.pol.Roles == nil {
		t.pol.Roles = map[string]flow.ContextRule{}
	}
	return t.pol.Roles[ctxRoles[t.sel]]
}

func (t *contextTab) setRole(r flow.ContextRule) {
	t.pol.Roles[ctxRoles[t.sel]] = r
	t.dirty, t.msg = true, ""
}

func (t *contextTab) toggle(item string) {
	r := t.roleRule()
	inc := []string{}
	found := false
	for _, i := range r.Include {
		if i == item {
			found = true
			continue
		}
		inc = append(inc, i)
	}
	if !found {
		inc = append(inc, item)
	}
	r.Include = inc
	t.setRole(r)
}

func (t *contextTab) key(m *Model, k tea.KeyMsg) tea.Cmd {
	if t.mode != ctxNav {
		switch k.String() {
		case "esc":
			t.mode = ctxNav
			return nil
		case "enter":
			v := strings.TrimSpace(t.in.Value())
			r := t.roleRule()
			if t.mode == ctxDeny && v != "" {
				r.Deny = append(r.Deny, v)
			}
			if t.mode == ctxAllow {
				r.Allow = nil
				for _, p := range strings.Split(v, ",") {
					if p = strings.TrimSpace(p); p != "" {
						r.Allow = append(r.Allow, p)
					}
				}
			}
			t.setRole(r)
			t.mode = ctxNav
			return nil
		}
		var cmd tea.Cmd
		t.in, cmd = t.in.Update(k)
		return cmd
	}
	switch k.String() {
	case "up", "k":
		if t.sel > 0 {
			t.sel--
		}
	case "down", "j":
		if t.sel < len(ctxRoles)-1 {
			t.sel++
		}
	case "g":
		t.toggle(flow.ItemGoal)
	case "p":
		t.toggle(flow.ItemPlan)
	case "d":
		t.toggle(flow.ItemDependencies)
	case "f":
		t.toggle(flow.ItemFeedback)
	case "o":
		t.toggle(flow.ItemOverview)
	case "v":
		t.toggle(flow.ItemDiff)
	case "e":
		t.toggle(flow.ItemFiles)
	case "t":
		t.toggle(flow.ItemTests)
	case "x":
		t.mode = ctxDeny
		t.in.SetValue("")
		t.in.Placeholder = "path pattern to hide, e.g. secrets/ or *.pem"
		t.in.Focus()
		return textinput.Blink
	case "X":
		r := t.roleRule()
		if n := len(r.Deny); n > 0 {
			r.Deny = r.Deny[:n-1]
			t.setRole(r)
		}
	case "a":
		t.mode = ctxAllow
		t.in.SetValue(strings.Join(t.roleRule().Allow, ","))
		t.in.Placeholder = "comma-separated paths the agent may see (empty = everything not denied)"
		t.in.Focus()
		return textinput.Blink
	case "m":
		r := t.roleRule()
		steps := []int{2000, 8000, 32000, 128000}
		r.MaxBytes = steps[0]
		for i, s := range steps {
			if t.pol.Rule(ctxRoles[t.sel], "").MaxBytes == s {
				r.MaxBytes = steps[(i+1)%len(steps)]
			}
		}
		t.setRole(r)
	case "R":
		t.pol, t.dirty, t.msg = flow.DefaultContext(), true, "reset to defaults (not saved yet)"
	case "s":
		if err := flow.SaveContext(m.d.Folder, t.pol); err != nil {
			t.msg = err.Error()
		} else {
			t.dirty, t.msg = false, "saved to "+flow.ContextPath(m.d.Folder)
		}
	}
	return nil
}

func (t *contextTab) view(m *Model) string {
	var b strings.Builder
	dirty := ""
	if t.dirty {
		dirty = " (unsaved)"
	}
	fmt.Fprintf(&b, "%s%s\n%s\n\n", titleSt.Render("context manager"),
		dirty, dimSt.Render("Loom decides what each agent is sent for every job (relevance, ranking, size budget). Below: what it may send, then what it did send."))
	b.WriteString(titleSt.Render("limits per role") + "\n")
	for i, role := range ctxRoles {
		r := t.pol.Rule(role, "")
		row := fmt.Sprintf("%-7s may receive %-52s cap %dB", role, orNothing(r.Include), r.MaxBytes)
		if i == t.sel {
			row = selSt.Render(row)
		}
		b.WriteString(row + "\n")
	}
	r := t.pol.Rule(ctxRoles[t.sel], "")
	fmt.Fprintf(&b, "  %s allow: %s · hidden: %s\n", ctxRoles[t.sel], orAll(r.Allow), strings.Join(r.Deny, " "))
	if n := len(t.pol.Agents); n > 0 {
		var names []string
		for a := range t.pol.Agents {
			names = append(names, a)
		}
		fmt.Fprintf(&b, "  per-agent overrides (edit .agent/context.json): %s\n", strings.Join(names, ", "))
	}

	b.WriteString("\n" + titleSt.Render("last sent") + "\n")
	bundles := flow.RecentBundles(m.d.Folder, max(3, m.h/3))
	if len(bundles) == 0 {
		b.WriteString(dimSt.Render("  nothing yet — run a flow\n"))
	}
	for _, bn := range bundles {
		var parts []string
		for _, it := range bn.Items {
			p := fmt.Sprintf("%s(%dB)", it.Kind, it.Bytes)
			if it.Truncated {
				p += "✂"
			}
			parts = append(parts, p)
		}
		drop := ""
		if len(bn.Dropped) > 0 {
			drop = fmt.Sprintf(" · dropped %d", len(bn.Dropped))
		}
		fmt.Fprintf(&b, "  %s %-7s %-7s %-10s %d/%dB  %s%s\n", bn.Time.Local().Format("15:04:05"), bn.Role, bn.Agent, bn.Task, bn.Used, bn.Budget, strings.Join(parts, " "), drop)
	}
	if t.msg != "" {
		b.WriteString("\n" + t.msg + "\n")
	}
	if t.mode != ctxNav {
		b.WriteString("\n" + t.in.View() + "\n" + dimSt.Render("Enter confirms · Esc cancels"))
		return b.String()
	}
	b.WriteString(dimSt.Render("\n↑/↓ role · permit: g goal p plan d dependencies f feedback o overview v diff e files t tests\n" +
		"x hide path · X unhide last · a allow-list · m size cap · s save · R reset"))
	return b.String()
}

func orAll(s []string) string {
	if len(s) == 0 {
		return "everything not hidden"
	}
	return strings.Join(s, ", ")
}

func orNothing(s []string) string {
	if len(s) == 0 {
		return "(nothing extra)"
	}
	return strings.Join(s, ", ")
}
