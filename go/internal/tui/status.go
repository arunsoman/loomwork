package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"loomwork.dev/loomwork/internal/flow"
)

// statusTab shows who did what on each module, refreshed from the task
// board once a second. It only reads the board.
type statusTab struct {
	tasks []*flow.Task
	sel   int
	log   []string
	err   string
}

func newStatusTab(m *Model) statusTab { return statusTab{} }

func (s *statusTab) refresh(m *Model) {
	b, err := flow.OpenBoard(m.d.Folder)
	if err != nil {
		s.err = err.Error()
		return
	}
	tasks, err := b.List()
	if err != nil {
		s.err = err.Error()
		return
	}
	s.err, s.tasks = "", tasks
	if s.sel >= len(tasks) {
		s.sel = max(0, len(tasks)-1)
	}
}

func (s *statusTab) key(m *Model, k tea.KeyMsg) {
	switch k.String() {
	case "up", "k":
		if s.sel > 0 {
			s.sel--
		}
	case "down", "j":
		if s.sel < len(s.tasks)-1 {
			s.sel++
		}
	}
}

func (s *statusTab) view(m *Model) string {
	var b strings.Builder
	if s.err != "" {
		b.WriteString(errSt.Render(s.err) + "\n")
	}
	if len(s.tasks) == 0 {
		b.WriteString(dimSt.Render("No tasks yet. Start a flow from the Prompt tab (Ctrl+T)."))
		return b.String()
	}
	fmt.Fprintf(&b, "%s\n", titleSt.Render(fmt.Sprintf("%-14s %-10s %-9s %-4s %s", "MODULE", "STATUS", "AGENT", "TRY", "LAST EVENT")))
	for i, t := range s.tasks {
		who := t.Owner
		if who == "" {
			who = "-"
		}
		last := ""
		if n := len(t.Events); n > 0 {
			e := t.Events[n-1]
			last = fmt.Sprintf("%s %s %s", e.Time.Local().Format("15:04:05"), e.Agent, e.Action)
		}
		row := fmt.Sprintf("%-14s %-10s %-9s %-4d %s", t.ID, statusLabel(t.Status), who, t.Attempts, last)
		if i == s.sel {
			row = selSt.Render(row)
		}
		b.WriteString(row + "\n")
	}
	t := s.tasks[s.sel]
	fmt.Fprintf(&b, "\n%s  %s\n", titleSt.Render(t.ID), dimSt.Render(t.Title))
	for _, v := range t.Verdicts {
		mark := okSt.Render("pass")
		if !v.Pass {
			mark = errSt.Render("fail")
		}
		fmt.Fprintf(&b, "  verdict %s: %s %s\n", v.Agent, mark, strings.Join(v.Issues, "; "))
	}
	evs := t.Events
	room := max(3, m.h-len(s.tasks)-12)
	if len(evs) > room {
		evs = evs[len(evs)-room:]
	}
	for _, e := range evs {
		fmt.Fprintf(&b, "  %s %-8s %-12s %s\n", e.Time.Local().Format("15:04:05"), e.Agent, e.Action, e.Note)
	}
	b.WriteString(dimSt.Render("\n↑/↓ select module · Tab view"))
	return b.String()
}

func statusLabel(s string) string {
	switch s {
	case flow.StatMerged:
		return okSt.Render(fmt.Sprintf("%-10s", s))
	case flow.StatFailed:
		return errSt.Render(fmt.Sprintf("%-10s", s))
	}
	return fmt.Sprintf("%-10s", s)
}
