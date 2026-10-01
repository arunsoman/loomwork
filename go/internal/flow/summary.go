package flow

import (
	"fmt"
	"strings"
	"time"
)

// Summary renders the board as plain text: one line per module with its
// stage, current owner and last event.
func Summary(tasks []*Task) string {
	if len(tasks) == 0 {
		return "no tasks yet\n"
	}
	var b strings.Builder
	for _, t := range tasks {
		who := t.Owner
		if who == "" {
			who = "-"
		}
		last := ""
		if n := len(t.Events); n > 0 {
			e := t.Events[n-1]
			last = fmt.Sprintf("%s %s %s", e.Time.Local().Format("15:04:05"), e.Agent, e.Action)
		}
		fmt.Fprintf(&b, "%-14s %-10s by %-8s try %d  %s\n", t.ID, t.Status, who, t.Attempts, last)
	}
	return b.String()
}

// Age is a short human duration since t.
func Age(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	return d.Round(time.Minute).String()
}
