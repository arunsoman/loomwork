package flow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loomwork.dev/loomwork/internal/agents"
)

func TestManagerSelectsByRoleAndBudget(t *testing.T) {
	dir := initRepo(t)
	os.WriteFile(filepath.Join(dir, "dep.txt"), []byte(strings.Repeat("line\n", 100)), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello project\n"), 0o644)
	exec.Command("git", "-C", dir, "add", "-A").Run()
	exec.Command("git", "-C", dir, "commit", "-qm", "files").Run()

	cm := &ContextManager{Folder: dir}
	dep := &Task{ID: "dep", Title: "Dep", Spec: "make dep", Files: []string{"dep.txt"}}
	task := &Task{ID: "app", Spec: "s", DependsOn: []string{"dep"}, TestLog: "FAIL: x"}
	rule := DefaultContext().Rule(KindBuild, "claude")

	b, err := cm.Build(context.Background(), Request{Role: KindBuild, Agent: "claude", Task: task, Goal: "the goal", All: []*Task{dep, task}, Dir: dir, Feedback: []string{"fix y"}, Rule: rule})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, it := range b.Items {
		kinds = append(kinds, it.Kind)
	}
	got := strings.Join(kinds, ",")
	// Feedback outranks everything; then goal, dependencies, tests, files.
	if !strings.HasPrefix(got, "feedback,goal,dependencies,tests,files") {
		t.Fatalf("order/selection: %s", got)
	}
	if !strings.Contains(b.Text, "fix y") || !strings.Contains(b.Text, "line") {
		t.Fatal("expected feedback and dependency file excerpt in text")
	}

	// A tight budget drops the lowest-priority items first and says so.
	rule.MaxBytes = 400
	b, _ = cm.Build(context.Background(), Request{Role: KindBuild, Agent: "claude", Task: task, Goal: "the goal", All: []*Task{dep, task}, Dir: dir, Feedback: []string{"fix y"}, Rule: rule})
	if b.Used > 400 || !strings.Contains(b.Text, "fix y") || len(b.Dropped)+len(b.Items) < 5 {
		t.Fatalf("budget not respected: used=%d items=%d dropped=%v", b.Used, len(b.Items), b.Dropped)
	}
	if len(b.Dropped) == 0 && !b.Items[len(b.Items)-1].Truncated {
		t.Fatal("something should have been dropped or truncated")
	}

	// Kinds an agent is not permitted to receive are never sent.
	rule = DefaultContext().Rule(KindBuild, "claude")
	rule.Include = []string{ItemFeedback}
	b, _ = cm.Build(context.Background(), Request{Role: KindBuild, Agent: "claude", Task: task, Goal: "the goal", All: []*Task{dep, task}, Dir: dir, Feedback: []string{"fix y"}, Rule: rule})
	if strings.Contains(b.Text, "the goal") || len(b.Items) != 1 {
		t.Fatalf("unpermitted context leaked: %v", b.Items)
	}

	// Planner gets an overview including the README; verifier gets a diff and no goal.
	b, _ = cm.Build(context.Background(), Request{Role: KindPlan, Agent: "codex", Goal: "g", Dir: dir, Rule: DefaultContext().Rule(KindPlan, "codex")})
	if !strings.Contains(b.Text, "Hello project") || !strings.Contains(b.Text, "dep.txt") {
		t.Fatal("overview missing README/tree")
	}
	b, _ = cm.Build(context.Background(), Request{Role: KindVerify, Agent: "pi", Task: task, Goal: "SECRET", Dir: dir, Rule: DefaultContext().Rule(KindVerify, "pi")})
	if strings.Contains(b.Text, "SECRET") || !strings.Contains(b.Text, "diff") {
		t.Fatalf("verifier bundle wrong: %q", b.Text)
	}
	if rec := RecentBundles(dir, 10); len(rec) < 3 {
		t.Fatalf("bundles not recorded: %d", len(rec))
	}
}

func TestManagerSummarizerAndRuneSafety(t *testing.T) {
	dir := initRepo(t)
	cm := &ContextManager{Folder: dir, Summarize: func(ctx context.Context, kind, text string, max int) (string, error) {
		return "SHORT", nil
	}}
	rule := ContextRule{Include: []string{ItemGoal}, MaxBytes: 500}
	b, _ := cm.Build(context.Background(), Request{Role: KindPlan, Agent: "x", Goal: strings.Repeat("é", 1000), Dir: dir, Rule: rule})
	if !strings.Contains(b.Text, "SHORT") || !b.Items[0].Truncated {
		t.Fatal("summarizer not used for over-budget item")
	}
	cm.Summarize = nil
	b, _ = cm.Build(context.Background(), Request{Role: KindPlan, Agent: "x", Goal: strings.Repeat("é", 1000), Dir: dir, Rule: rule})
	if !strings.HasSuffix(strings.TrimSpace(b.Text), "[truncated]") || strings.ContainsRune(b.Text, '�') {
		t.Fatal("truncation produced invalid UTF-8")
	}
}

func TestReadExcerptRefusesSymlinksAndEscapes(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("secret"), 0o644)
	os.Symlink(outside, filepath.Join(dir, "link"))
	if readExcerpt(dir, "link", 10) != "" || readExcerpt(dir, "../x", 10) != "" {
		t.Fatal("symlink or escape read")
	}
	_ = agents.Config{}
}
