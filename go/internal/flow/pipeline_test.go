package flow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"loomwork.dev/loomwork/internal/agents"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, c := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", c...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", c, out)
		}
	}
	return dir
}

type fakes struct {
	mu        sync.Mutex
	calls     []string
	verifyN   map[string]int
	failFirst string // module whose first verification fails
}

func (f *fakes) exec(ctx context.Context, agent string, job agents.Job) (agents.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := job.Prompt
	switch {
	case strings.Contains(p, "You are the planner"):
		f.calls = append(f.calls, "plan:"+agent)
		return agents.Result{Stdout: `{"modules":[{"id":"core","title":"Core","spec":"write core.txt","paths":["core"]},{"id":"app","title":"App","spec":"write app.txt","depends_on":["core"],"paths":["app"]}]}`}, nil
	case strings.Contains(p, "You are the builder"):
		id := "core"
		if strings.Contains(p, "Module: App") {
			id = "app"
		}
		f.calls = append(f.calls, "build:"+id+":"+agent)
		if id == "app" {
			// Dependent module must see merged core work.
			if _, err := os.Stat(filepath.Join(job.Dir, "core", "core.txt")); err != nil {
				return agents.Result{ExitCode: 1, Stderr: "core missing"}, nil
			}
		}
		os.MkdirAll(filepath.Join(job.Dir, id), 0o755)
		os.WriteFile(filepath.Join(job.Dir, id, id+".txt"), []byte(id+strings.Repeat("!", strings.Count(p, "- "))), 0o644)
		return agents.Result{}, nil
	case strings.Contains(p, "You are a verifier"):
		id := "core"
		if strings.Contains(p, "Module: App") {
			id = "app"
		}
		f.calls = append(f.calls, "verify:"+id+":"+agent)
		f.verifyN[id]++
		if id == f.failFirst && f.verifyN[id] == 1 {
			return agents.Result{Stdout: `{"pass":false,"issues":["missing edge case"]}`}, nil
		}
		return agents.Result{Stdout: `{"pass":true,"issues":[]}`}, nil
	case strings.Contains(p, "You are the shipper"):
		f.calls = append(f.calls, "ship:"+agent)
		return agents.Result{}, nil
	}
	return agents.Result{ExitCode: 9, Stderr: "unexpected prompt"}, nil
}

func TestPipelineEndToEnd(t *testing.T) {
	dir := initRepo(t)
	f := &fakes{verifyN: map[string]int{}, failFirst: "core"}
	w := Default()
	w.Stages[2].Agents = []string{"pi", "hermes"}
	w.TestCmd = "test -f " + "$(git ls-files | head -1)"
	e, err := NewEngine(dir, w, &agents.Config{})
	if err != nil {
		t.Fatal(err)
	}
	e.Exec = f.exec
	res, err := e.Run(context.Background(), "build a thing")
	if err != nil {
		t.Fatalf("run: %v (%+v)", err, res)
	}
	if len(res.Merged) != 2 || len(res.Failed) != 0 {
		t.Fatalf("%+v", res)
	}
	// core was rejected once, rebuilt with the verifier's issue, then merged.
	core, _ := e.Board.Get("core")
	if core.Attempts != 2 || core.Status != StatMerged {
		t.Fatalf("core: attempts=%d status=%s", core.Attempts, core.Status)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".agent/worktrees/"+e.run+"-_integration/core/core.txt"))
	if !strings.HasPrefix(string(got), "core!") {
		t.Fatalf("retry prompt did not carry issues: %q", got)
	}
	joined := strings.Join(f.calls, ",")
	for _, want := range []string{"plan:codex", "build:core:claude", "verify:core:pi", "verify:core:hermes", "build:app:claude", "ship:claude"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing call %s in %s", want, joined)
		}
	}
	if strings.Index(joined, "build:app") < strings.Index(joined, "verify:core:hermes") {
		t.Error("app built before core was verified")
	}
	// The main checkout is untouched.
	if out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); strings.TrimSpace(string(out)) != "" && !strings.Contains(string(out), ".agent") {
		t.Errorf("main checkout dirty: %s", out)
	}
}

func TestPipelineFailureBlocksShip(t *testing.T) {
	dir := initRepo(t)
	f := &fakes{verifyN: map[string]int{}}
	w := Default()
	w.Stages[2].Retry = 0
	f.failFirst = "core"
	e, _ := NewEngine(dir, w, &agents.Config{})
	e.Exec = f.exec
	res, err := e.Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Merged) != 0 || len(res.Failed) != 2 {
		t.Fatalf("%+v", res)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "ship:") || strings.HasPrefix(c, "build:app") {
			t.Fatalf("ran %s after a failed dependency", c)
		}
	}
}

func TestOutOfScopeAndPlanApproval(t *testing.T) {
	if got := outOfScope([]string{"core/a.go", "other/b.go"}, []string{"core"}); len(got) != 1 || got[0] != "other/b.go" {
		t.Fatalf("%v", got)
	}
	dir := initRepo(t)
	f := &fakes{verifyN: map[string]int{}}
	e, _ := NewEngine(dir, Default(), &agents.Config{})
	e.Exec = f.exec
	e.ApprovePlan = func([]Module) bool { return false }
	if _, err := e.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("%v", err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "build") {
			t.Fatal("built without approval")
		}
	}
}
