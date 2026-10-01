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

func TestRulePrecedence(t *testing.T) {
	p := DefaultContext()
	p.Agents = map[string]ContextRule{"pi": {Include: []string{ItemGoal}, Deny: []string{"secret/"}}}
	r := p.Rule(KindVerify, "pi")
	if !r.Has(ItemGoal) {
		t.Fatal("agent rule should override the role's empty include")
	}
	if r2 := p.Rule(KindVerify, "hermes"); r2.Has(ItemGoal) {
		t.Fatal("verifier default must not see the goal")
	}
	joined := strings.Join(r.Deny, ",")
	if !strings.Contains(joined, ".env") || !strings.Contains(joined, "secret/") {
		t.Fatalf("deny should accumulate: %v", r.Deny)
	}
	if err := (&ContextPolicy{Default: ContextRule{Include: []string{"everything"}}}).Validate(); err == nil {
		t.Fatal("unknown item accepted")
	}
	if err := (&ContextPolicy{Default: ContextRule{Allow: []string{"../x"}}}).Validate(); err == nil {
		t.Fatal("escaping allow path accepted")
	}
}

func TestVisibilityHidesFilesOnDisk(t *testing.T) {
	dir := initRepo(t)
	write := func(rel, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
		os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644)
	}
	write("src/a.go", "a")
	write("docs/x.md", "x")
	write(".env", "TOKEN=1")
	write("keys/server.pem", "pem")
	for _, c := range [][]string{{"add", "-A"}, {"commit", "-qm", "files"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, c...)...).CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	exists := func(rel string) bool { _, err := os.Stat(filepath.Join(dir, rel)); return err == nil }

	hidden, err := ApplyVisibility(dir, DefaultContext().Rule(KindBuild, "claude"))
	if err != nil {
		t.Fatal(err)
	}
	if exists(".env") || exists("keys/server.pem") || !exists("src/a.go") || !exists("docs/x.md") {
		t.Fatalf("default deny wrong; hidden=%v", hidden)
	}
	if _, err := ApplyVisibility(dir, ContextRule{Allow: []string{"src"}}); err != nil {
		t.Fatal(err)
	}
	if exists("docs/x.md") || !exists("src/a.go") {
		t.Fatal("allow-list should show only src")
	}
	if _, err := ApplyVisibility(dir, ContextRule{}); err != nil {
		t.Fatal(err)
	}
	if !exists("docs/x.md") || !exists(".env") {
		t.Fatal("empty rule should restore everything")
	}
}

type promptLog struct {
	mu      sync.Mutex
	prompts map[string]string
}

func TestPipelineHonoursContextRules(t *testing.T) {
	dir := initRepo(t)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=1"), 0o644)
	exec.Command("git", "-C", dir, "add", "-A").Run()
	exec.Command("git", "-C", dir, "commit", "-qm", "env").Run()

	f := &fakes{verifyN: map[string]int{}}
	seen := &promptLog{prompts: map[string]string{}}
	var envVisible sync.Map
	e, _ := NewEngine(dir, Default(), &agents.Config{})
	e.Exec = func(ctx context.Context, agent string, job agents.Job) (agents.Result, error) {
		role := map[bool]string{true: "plan"}[strings.Contains(job.Prompt, "You are the planner")]
		for _, k := range []string{"builder", "verifier", "shipper"} {
			if strings.Contains(job.Prompt, "You are the "+k) || strings.Contains(job.Prompt, "You are a "+k) {
				role = k
			}
		}
		seen.mu.Lock()
		if !strings.Contains(job.Prompt, "Module: App") || role == "plan" {
			seen.prompts[role] = job.Prompt
		} else {
			seen.prompts[role+"-app"] = job.Prompt
		}
		seen.mu.Unlock()
		_, err := os.Stat(filepath.Join(job.Dir, ".env"))
		envVisible.Store(role, err == nil)
		return f.exec(ctx, agent, job)
	}
	if _, err := e.Run(context.Background(), "SECRET-GOAL-TEXT"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen.prompts["plan"], "SECRET-GOAL-TEXT") {
		t.Error("planner should see the goal")
	}
	if !strings.Contains(seen.prompts["builder-app"], "write core.txt") {
		t.Error("builder should see its dependency's spec")
	}
	// The goal text appears once, quoted, only where included; verifiers get none.
	if strings.Contains(seen.prompts["verifier"], "SECRET-GOAL-TEXT") {
		t.Error("verifier must not see the goal")
	}
	envVisible.Range(func(k, v any) bool {
		if v.(bool) {
			t.Errorf("%v could see .env", k)
		}
		return true
	})
	core, _ := e.Board.Get("core")
	var logged bool
	for _, ev := range core.Events {
		if ev.Action == "context" && strings.Contains(ev.Note, "hidden files") {
			logged = true
		}
	}
	if !logged {
		t.Error("context shown/hidden was not recorded on the task")
	}
}
