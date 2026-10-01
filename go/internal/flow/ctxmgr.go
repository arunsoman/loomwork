package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Additional item kinds the manager can choose to send. ContextRule.Include
// lists the kinds an agent is permitted to receive; the manager decides which
// of those are relevant for the job and how much of each fits.
const (
	ItemDiff     = "diff"     // the patch under review (verifier)
	ItemFiles    = "files"    // file excerpts the module builds on
	ItemTests    = "tests"    // output of the project's test command
	ItemOverview = "overview" // repository tree and README head (planner)
)

// BundleItem records one piece of context the manager sent, and why.
type BundleItem struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Why       string `json:"why"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Bundle is what the manager decided to send one agent for one job.
type Bundle struct {
	Time    time.Time    `json:"time"`
	Role    string       `json:"role"`
	Agent   string       `json:"agent"`
	Task    string       `json:"task,omitempty"`
	Budget  int          `json:"budget"`
	Used    int          `json:"used"`
	Items   []BundleItem `json:"items"`
	Dropped []string     `json:"dropped,omitempty"` // candidates that did not fit or were not permitted
	Hidden  int          `json:"hidden_files"`
	Text    string       `json:"-"`
}

// Request describes the job the context is for.
type Request struct {
	Role, Agent string
	Task        *Task
	Goal        string
	All         []*Task
	Dir         string // the agent's working directory, after visibility is applied
	Feedback    []string
	Hidden      int
	Rule        ContextRule
}

// ContextManager decides what Loom sends each agent. It is run for every
// agent invocation; nothing is attached to a prompt except through it.
type ContextManager struct {
	Folder string
	// Summarize, if set, compacts an over-budget item to at most max bytes
	// (for example with a local model). Without it, items are truncated.
	Summarize func(ctx context.Context, kind, text string, max int) (string, error)
}

type candidate struct {
	kind, name, why, body string
	prio                  int
}

// Build selects, ranks and fits context for one agent job, and records the
// bundle for the status and context views.
func (cm *ContextManager) Build(ctx context.Context, r Request) (*Bundle, error) {
	var cands []candidate
	add := func(prio int, kind, name, why, body string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		cands = append(cands, candidate{kind, name, why, body, prio})
	}
	t := r.Task

	// What is relevant depends on the role and the job's state.
	if len(r.Feedback) > 0 {
		add(0, ItemFeedback, "verifier feedback", "previous attempt was rejected", "- "+strings.Join(r.Feedback, "\n- "))
	}
	switch r.Role {
	case KindPlan:
		add(1, ItemGoal, "goal", "the planner splits this into modules", r.Goal)
		add(2, ItemOverview, "repository overview", "existing layout the plan must fit", cm.overview(r.Dir))
	case KindBuild:
		add(1, ItemGoal, "goal", "why this module exists", r.Goal)
		byID := map[string]*Task{}
		for _, x := range r.All {
			byID[x.ID] = x
		}
		fileN := 0
		for _, id := range t.DependsOn {
			d := byID[id]
			if d == nil {
				continue
			}
			add(2, ItemDependencies, "module "+d.ID, "this module depends on it", fmt.Sprintf("%s (%s): %s\nfiles: %s", d.ID, d.Title, d.Spec, strings.Join(d.Files, ", ")))
			for _, f := range d.Files {
				if fileN >= 4 {
					break
				}
				if body := readExcerpt(r.Dir, f, 80); body != "" {
					fileN++
					add(4+fileN, ItemFiles, f, "written by dependency "+d.ID, body)
				}
			}
		}
		if t.TestLog != "" {
			add(3, ItemTests, "last test run", "result of the previous attempt", t.TestLog)
		}
	case KindVerify:
		add(1, ItemDiff, "patch under review", "what the builder changed", gitOut(r.Dir, "diff", "HEAD~1", "HEAD"))
		if t != nil && t.TestLog != "" {
			add(2, ItemTests, "test run", "objective result for this patch", t.TestLog)
		}
	case KindShip:
		var sb strings.Builder
		for _, x := range r.All {
			fmt.Fprintf(&sb, "- %s: %s\n", x.ID, x.Title)
		}
		add(1, ItemPlan, "merged modules", "for the commit message", sb.String())
	}

	rule := r.Rule
	b := &Bundle{Time: time.Now().UTC(), Role: r.Role, Agent: r.Agent, Budget: rule.MaxBytes, Hidden: r.Hidden}
	if t != nil {
		b.Task = t.ID
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].prio < cands[j].prio })

	var out strings.Builder
	for _, c := range cands {
		if !rule.Has(c.kind) {
			b.Dropped = append(b.Dropped, c.kind+":"+c.name+" (not permitted for this agent)")
			continue
		}
		head := fmt.Sprintf("\n--- context: %s — %s ---\n", c.kind, c.name)
		room := rule.MaxBytes - out.Len() - len(head) - 1
		body, trunc := c.body, false
		if rule.MaxBytes > 0 && len(body) > room {
			if room < 200 {
				b.Dropped = append(b.Dropped, c.kind+":"+c.name+" (over budget)")
				continue
			}
			trunc = true
			if cm.Summarize != nil {
				if s, err := cm.Summarize(ctx, c.kind, body, room); err == nil && s != "" && len(s) <= room {
					body = s
				} else {
					body = truncateUTF8(body, room-20) + "\n[truncated]"
				}
			} else {
				body = truncateUTF8(body, room-20) + "\n[truncated]"
			}
		}
		out.WriteString(head + body + "\n")
		b.Items = append(b.Items, BundleItem{Kind: c.kind, Name: c.name, Why: c.why, Bytes: len(body), Truncated: trunc})
	}
	b.Text, b.Used = out.String(), out.Len()
	cm.record(b)
	return b, nil
}

func (cm *ContextManager) record(b *Bundle) {
	name := strings.Join([]string{b.Task, b.Role, b.Agent}, ".")
	if b.Task == "" {
		name = b.Role + "." + b.Agent
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return
	}
	writeAtomic(filepath.Join(cm.Folder, ".agent", "state", "context", name+".json"), append(data, '\n'))
}

// RecentBundles returns the recorded bundles, newest first.
func RecentBundles(folder string, n int) []*Bundle {
	dir := filepath.Join(folder, ".agent", "state", "context")
	ents, _ := os.ReadDir(dir)
	var out []*Bundle
	for _, e := range ents {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var b Bundle
		if json.Unmarshal(data, &b) == nil {
			out = append(out, &b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// overview is a bounded listing of the repository plus the README's head.
func (cm *ContextManager) overview(dir string) string {
	var sb strings.Builder
	files := strings.Split(strings.TrimSpace(gitOut(dir, "ls-files")), "\n")
	for i, f := range files {
		if f == "" {
			continue
		}
		if i >= 200 {
			fmt.Fprintf(&sb, "… %d more files\n", len(files)-i)
			break
		}
		sb.WriteString(f + "\n")
	}
	for _, name := range []string{"README.md", "README"} {
		if body := readExcerpt(dir, name, 40); body != "" {
			sb.WriteString("\n" + name + ":\n" + body)
			break
		}
	}
	return sb.String()
}

// readExcerpt returns the first lines of a regular file under dir. It refuses
// symlinks, paths that escape dir, and binary files.
func readExcerpt(dir, rel string, lines int) string {
	if checkRelPath(rel) != nil {
		return ""
	}
	p := filepath.Join(dir, rel)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return ""
	}
	parts := strings.SplitAfterN(string(data), "\n", lines+1)
	if len(parts) > lines {
		parts = append(parts[:lines], "[…]\n")
	}
	return strings.Join(parts, "")
}

func gitOut(dir string, args ...string) string {
	o, _ := git(dir, args...)
	return o
}

func truncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 { // do not cut a multi-byte rune
		n--
	}
	return s[:n]
}
