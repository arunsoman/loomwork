package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loomwork.dev/loomwork/internal/agents"
)

// ExecFunc runs one agent job. The default uses agents.Adapter; tests inject
// fakes.
type ExecFunc func(ctx context.Context, agent string, job agents.Job) (agents.Result, error)

// Engine runs a workflow over a git project folder.
type Engine struct {
	Folder   string
	Workflow *Workflow
	Board    *Board
	Exec     ExecFunc
	// Context holds the context manager's limits per role and agent. Nil
	// means DefaultContext.
	Context *ContextPolicy
	// Summarize optionally compacts over-budget context (nil truncates).
	Summarize func(ctx context.Context, kind, text string, max int) (string, error)

	// Logf reports progress (may be nil).
	Logf func(format string, args ...any)
	// ApprovePlan lets the user review the plan before any building. Nil
	// approves.
	ApprovePlan func([]Module) bool
	// ConfirmPush is asked before anything is pushed. Nil refuses.
	ConfirmPush func(branch string) bool

	AgentTimeout time.Duration

	mergeMu sync.Mutex
	run     string
	goal    string
}

// NewEngine builds an engine that runs real agent CLIs from cfg.
func NewEngine(folder string, w *Workflow, cfg *agents.Config) (*Engine, error) {
	b, err := OpenBoard(folder)
	if err != nil {
		return nil, err
	}
	e := &Engine{Folder: folder, Workflow: w, Board: b, AgentTimeout: 30 * time.Minute}
	e.Exec = func(ctx context.Context, agent string, job agents.Job) (agents.Result, error) {
		spec, ok := cfg.Spec(agent)
		if !ok {
			return agents.Result{}, fmt.Errorf("unknown agent %q", agent)
		}
		return agents.Adapter{Spec: spec}.Run(ctx, job)
	}
	return e, nil
}

func (e *Engine) logf(f string, a ...any) {
	if e.Logf != nil {
		e.Logf(f, a...)
	}
}

// Result summarises a run.
type Result struct {
	Branch string // integration branch holding the merged work
	Merged []string
	Failed []string
	Pushed bool
	Note   string
}

// prepare restricts dir to what (role, agent) may see and returns the extra
// prompt context. What was shown and hidden is recorded on the task.
func (e *Engine) prepare(role, agent, dir string, t *Task, feedback []string) (string, error) {
	pol := e.Context
	if pol == nil {
		pol = DefaultContext()
	}
	rule := pol.Rule(role, agent)
	hidden, err := ApplyVisibility(dir, rule)
	if err != nil {
		return "", fmt.Errorf("context for %s: %w", agent, err)
	}
	all, _ := e.Board.List()
	cm := &ContextManager{Folder: e.Folder, Summarize: e.Summarize}
	bundle, err := cm.Build(context.Background(), Request{Role: role, Agent: agent, Task: t, Goal: e.goal, All: all, Dir: dir, Feedback: feedback, Hidden: len(hidden), Rule: rule})
	if err != nil {
		return "", err
	}
	if t != nil {
		var kinds []string
		for _, it := range bundle.Items {
			kinds = append(kinds, it.Kind)
		}
		t.Log(agent, "context", fmt.Sprintf("sent %d/%d bytes: %s; hidden files: %d; dropped: %d", bundle.Used, bundle.Budget, strings.Join(kinds, ","), len(hidden), len(bundle.Dropped)))
	}
	return bundle.Text, nil
}

func (e *Engine) agent(kind string) string {
	s, _ := e.Workflow.Stage(kind)
	return s.Agents[0]
}

// Run executes plan → build → verify (→ ship) for goal.
func (e *Engine) Run(ctx context.Context, goal string) (*Result, error) {
	if err := e.Workflow.Validate(nil); err != nil {
		return nil, err
	}
	if _, err := git(e.Folder, "rev-parse", "--git-dir"); err != nil {
		return nil, errors.New("flow needs a git repository: run `git init` and commit first")
	}
	if _, err := git(e.Folder, "rev-parse", "--verify", "HEAD"); err != nil {
		return nil, errors.New("the repository has no commits yet: make an initial commit first")
	}
	e.run = time.Now().UTC().Format("20060102-150405")
	if err := e.excludeWorktrees(); err != nil {
		return nil, err
	}

	// 1. Plan, in a throwaway worktree so the planner cannot touch the user's
	// checkout and sees only what its context rule allows.
	e.goal = goal
	planner := e.agent(KindPlan)
	planDir := e.wtPath("_plan")
	if _, err := git(e.Folder, "worktree", "add", "--detach", planDir, "HEAD"); err != nil {
		return nil, err
	}
	defer func() {
		git(e.Folder, "worktree", "remove", "--force", planDir)
	}()
	extra, err := e.prepare(KindPlan, planner, planDir, nil, nil)
	if err != nil {
		return nil, err
	}
	e.logf("plan: asking %s to split the goal into modules", planner)
	res, err := e.Exec(ctx, planner, agents.Job{Prompt: PlanPrompt(goal) + extra, Dir: planDir, Timeout: e.AgentTimeout})
	if err != nil {
		return nil, fmt.Errorf("planner: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("planner %s exited %d: %s", planner, res.ExitCode, tail(res.Stderr, 400))
	}
	mods, err := ParsePlan(res.Stdout)
	if err != nil {
		return nil, fmt.Errorf("plan rejected: %w", err)
	}
	if e.ApprovePlan != nil && !e.ApprovePlan(mods) {
		return nil, errors.New("plan not approved")
	}
	if err := e.Board.Clear(); err != nil {
		return nil, err
	}
	for _, m := range mods {
		t := &Task{ID: m.ID, Title: m.Title, Spec: m.Spec, DependsOn: m.DependsOn, Paths: m.Paths, Status: StatPlanned}
		if t.Title == "" {
			t.Title = m.ID
		}
		t.Log(planner, "planned", "")
		if err := e.Board.Put(t); err != nil {
			return nil, err
		}
	}
	e.logf("plan: %d modules", len(mods))

	// 2. Integration worktree, where verified modules are merged.
	out := &Result{Branch: "loom/" + e.run}
	intDir := e.wtPath("_integration")
	if _, err := git(e.Folder, "worktree", "add", "-b", out.Branch, intDir, "HEAD"); err != nil {
		return nil, err
	}

	// 3. Build + verify each ready module, up to MaxParallel at a time.
	par := e.Workflow.MaxParallel
	if par < 1 {
		par = 1
	}
	for ctx.Err() == nil {
		ready, err := e.Board.Ready()
		if err != nil {
			return out, err
		}
		if len(ready) == 0 {
			break
		}
		if len(ready) > par {
			ready = ready[:par]
		}
		var wg sync.WaitGroup
		for _, t := range ready {
			wg.Add(1)
			go func(t *Task) {
				defer wg.Done()
				e.process(ctx, t, out.Branch, intDir)
			}(t)
		}
		wg.Wait()
		// A task that could not be claimed stays planned; stop rather than spin.
		stuck := false
		for _, t := range ready {
			if cur, err := e.Board.Get(t.ID); err == nil && cur.Status == StatPlanned {
				stuck = true
			}
		}
		if stuck {
			break
		}
	}
	all, _ := e.Board.List()
	for _, t := range all {
		switch t.Status {
		case StatMerged:
			out.Merged = append(out.Merged, t.ID)
		default:
			out.Failed = append(out.Failed, t.ID)
		}
	}
	if len(out.Failed) > 0 {
		out.Note = "not all modules merged; nothing shipped. Failed or blocked: " + strings.Join(out.Failed, ", ")
		return out, ctx.Err()
	}

	// 4. Ship.
	if _, ok := e.Workflow.Stage(KindShip); ok {
		if err := e.ship(ctx, out, intDir); err != nil {
			out.Note = err.Error()
			return out, err
		}
	}
	return out, nil
}

func (e *Engine) wtPath(name string) string {
	return filepath.Join(e.Folder, ".agent", "worktrees", e.run+"-"+name)
}

// excludeWorktrees keeps worktrees out of `git status` without touching the
// tracked .gitignore.
func (e *Engine) excludeWorktrees() error {
	p, err := git(e.Folder, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.Folder, p)
	}
	data, _ := os.ReadFile(p)
	if strings.Contains(string(data), ".agent/worktrees/") {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n.agent/worktrees/\n")
	return err
}

func (e *Engine) fail(t *Task, agent, why string) {
	t.Status, t.Owner = StatFailed, ""
	t.Log(agent, "failed", why)
	e.Board.Put(t)
	e.logf("%s: FAILED: %s", t.ID, why)
}

// process builds, verifies and merges one module.
func (e *Engine) process(ctx context.Context, t *Task, intBranch, intDir string) {
	builder := e.agent(KindBuild)
	if err := e.Board.Claim(t.ID, "loom-"+e.run, e.AgentTimeout*4); err != nil {
		e.logf("%s: skipped: %v", t.ID, err)
		return
	}
	defer e.Board.Release(t.ID, "loom-"+e.run)

	t.Branch = "loom/" + e.run + "-" + t.ID
	t.Worktree = e.wtPath(t.ID)
	// Branch from the integration branch so dependents see merged work.
	if _, err := git(e.Folder, "worktree", "add", "-b", t.Branch, t.Worktree, intBranch); err != nil {
		e.fail(t, "loom", err.Error())
		return
	}
	verify, hasVerify := e.Workflow.Stage(KindVerify)
	maxAttempts := 1
	if hasVerify {
		maxAttempts += verify.Retry
	}
	var issues []string
	for attempt := 1; attempt <= maxAttempts && ctx.Err() == nil; attempt++ {
		t.Attempts = attempt
		t.Status, t.Owner = StatBuilding, builder
		t.Log(builder, "building", fmt.Sprintf("attempt %d", attempt))
		e.Board.Put(t)
		e.logf("%s: %s building (attempt %d)", t.ID, builder, attempt)

		extra, err := e.prepare(KindBuild, builder, t.Worktree, t, issues)
		if err != nil {
			e.fail(t, "loom", err.Error())
			return
		}
		res, err := e.Exec(ctx, builder, agents.Job{Prompt: BuildPrompt(t, nil) + extra, Dir: t.Worktree, Write: true, Timeout: e.AgentTimeout})
		if err != nil {
			e.fail(t, builder, err.Error())
			return
		}
		if res.ExitCode != 0 {
			issues = []string{"builder exited " + fmt.Sprint(res.ExitCode) + ": " + tail(res.Stderr, 300)}
			t.Log(builder, "build-error", issues[0])
			continue
		}
		changed, err := changedFiles(t.Worktree)
		if err != nil {
			e.fail(t, "loom", err.Error())
			return
		}
		if len(changed) == 0 {
			issues = []string{"the builder made no changes"}
			t.Log(builder, "no-changes", "")
			continue
		}
		if bad := outOfScope(changed, t.Paths); len(bad) > 0 {
			issues = []string{"files outside the module's paths were changed: " + strings.Join(bad, ", ") + ". Revert them."}
			t.Log(builder, "out-of-scope", strings.Join(bad, ", "))
			// Remove the stray changes so the retry starts clean.
			git(t.Worktree, "checkout", "--", ".")
			git(t.Worktree, "clean", "-fdq")
			continue
		}
		if _, err := git(t.Worktree, "add", "-A"); err != nil {
			e.fail(t, "loom", err.Error())
			return
		}
		if _, err := git(t.Worktree, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "loom: "+t.Title+" ("+t.ID+")"); err != nil {
			e.fail(t, "loom", err.Error())
			return
		}
		t.Files = changed
		t.Status, t.Owner = StatBuilt, ""
		t.Log(builder, "built", fmt.Sprintf("%d files", len(changed)))
		e.Board.Put(t)

		pass, why := e.check(ctx, t, verify, hasVerify)
		if !pass {
			issues = why
			t.Log("loom", "rejected", strings.Join(why, "; "))
			continue
		}
		t.Status = StatVerified
		t.Log("loom", "verified", "")
		e.Board.Put(t)

		e.mergeMu.Lock()
		_, merr := git(intDir, "-c", "commit.gpgsign=false", "merge", "--no-ff", "-q", "-m", "loom: merge "+t.ID, t.Branch)
		if merr != nil {
			git(intDir, "merge", "--abort")
		}
		e.mergeMu.Unlock()
		if merr != nil {
			e.fail(t, "loom", "merge conflict: "+merr.Error())
			return
		}
		t.Status, t.Owner = StatMerged, ""
		t.Log("loom", "merged", "into "+intBranch)
		e.Board.Put(t)
		// The branch keeps the work; the checkout is no longer needed. Failed
		// modules keep theirs for inspection.
		git(e.Folder, "worktree", "remove", "--force", t.Worktree)
		e.logf("%s: merged", t.ID)
		return
	}
	if t.Status != StatFailed {
		e.fail(t, builder, "out of attempts: "+strings.Join(issues, "; "))
	}
}

// check runs the objective test command and the verifier agents.
func (e *Engine) check(ctx context.Context, t *Task, v Stage, hasVerify bool) (bool, []string) {
	if e.Workflow.TestCmd != "" {
		t.Status, t.Owner = StatVerifying, "loom"
		e.Board.Put(t)
		cmd := exec.CommandContext(ctx, "sh", "-c", e.Workflow.TestCmd)
		cmd.Dir, cmd.Env = t.Worktree, agents.Env()
		out, err := cmd.CombinedOutput()
		t.TestLog = tail(string(out), 2000)
		if err != nil {
			t.Verdicts = []Verdict{{Agent: "test_cmd", Pass: false, Issues: []string{"test command failed: " + tail(string(out), 600)}}}
			return false, t.Verdicts[0].Issues
		}
		t.Log("loom", "tests-passed", e.Workflow.TestCmd)
	}
	if !hasVerify {
		return true, nil
	}
	stat, _ := git(t.Worktree, "diff", "--stat", "HEAD~1", "HEAD")
	t.Verdicts = nil
	var failures []string
	passes := 0
	for _, a := range v.Agents {
		t.Status, t.Owner = StatVerifying, a
		t.Log(a, "verifying", "")
		e.Board.Put(t)
		e.logf("%s: %s verifying", t.ID, a)
		extra, perr := e.prepare(KindVerify, a, t.Worktree, t, nil)
		var res agents.Result
		var err error
		if perr == nil {
			res, err = e.Exec(ctx, a, agents.Job{Prompt: VerifyPrompt(t, stat) + extra, Dir: t.Worktree, Timeout: e.AgentTimeout})
		}
		var verdict Verdict
		switch {
		case perr != nil:
			verdict = Verdict{Agent: a, Issues: []string{perr.Error()}}
		case err != nil:
			verdict = Verdict{Agent: a, Issues: []string{"verifier error: " + err.Error()}}
		case res.ExitCode != 0:
			verdict = Verdict{Agent: a, Issues: []string{fmt.Sprintf("verifier exited %d: %s", res.ExitCode, tail(res.Stderr, 200))}}
		default:
			verdict = ParseVerdict(a, res.Stdout)
		}
		t.Verdicts = append(t.Verdicts, verdict)
		if verdict.Pass {
			passes++
		} else {
			failures = append(failures, verdict.Issues...)
		}
		// A verifier must not have edited the module.
		if dirty, _ := changedFiles(t.Worktree); len(dirty) > 0 {
			git(t.Worktree, "checkout", "--", ".")
			git(t.Worktree, "clean", "-fdq")
			t.Log(a, "edited-files", "verifier changes discarded")
		}
	}
	need := len(v.Agents)
	if v.Policy == "any" {
		need = 1
	}
	if passes >= need {
		return true, nil
	}
	if len(failures) == 0 {
		failures = []string{"verification did not pass"}
	}
	return false, failures
}

// ship has the shipper agent commit (and, if confirmed, push) the
// integration branch. Loom verifies the outcome instead of trusting it.
func (e *Engine) ship(ctx context.Context, out *Result, dir string) error {
	s, _ := e.Workflow.Stage(KindShip)
	shipper := s.Agents[0]
	if e.Workflow.TestCmd != "" {
		cmd := exec.CommandContext(ctx, "sh", "-c", e.Workflow.TestCmd)
		cmd.Dir, cmd.Env = dir, agents.Env()
		if o, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tests fail on the merged result, not shipping: %s", tail(string(o), 600))
		}
	}
	push := s.Push && e.ConfirmPush != nil && e.ConfirmPush(out.Branch)
	prompt := "You are the shipper in a multi-agent pipeline. The current directory is a git worktree on branch " + out.Branch +
		" where verified modules have been merged. Make sure every change is committed (use `git status`); if anything is uncommitted, commit it with a clear message. Do not rewrite history."
	if push {
		prompt += " Then push the branch with `git push -u origin " + out.Branch + "`. Do not force-push."
	} else {
		prompt += " Do NOT push."
	}
	extra, err := e.prepare(KindShip, shipper, dir, nil, nil)
	if err != nil {
		return err
	}
	prompt += extra
	e.logf("ship: %s committing%s", shipper, map[bool]string{true: " and pushing", false: ""}[push])
	res, err := e.Exec(ctx, shipper, agents.Job{Prompt: prompt, Dir: dir, Write: true, Timeout: e.AgentTimeout})
	if err != nil {
		return fmt.Errorf("shipper: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("shipper %s exited %d: %s", shipper, res.ExitCode, tail(res.Stderr, 300))
	}
	if st, _ := git(dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		return errors.New("shipper left uncommitted changes in " + dir)
	}
	if push {
		head, _ := git(dir, "rev-parse", "HEAD")
		up, err := git(dir, "rev-parse", "@{u}")
		if err != nil || strings.TrimSpace(up) != strings.TrimSpace(head) {
			return errors.New("shipper did not push " + out.Branch + " (upstream missing or behind HEAD)")
		}
		out.Pushed = true
	}
	return nil
}

func changedFiles(dir string) ([]string, error) {
	o, err := git(dir, "status", "--porcelain", "-uall")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(o, "\n") {
		if len(l) > 3 {
			f := strings.TrimSpace(l[3:])
			if i := strings.Index(f, " -> "); i >= 0 {
				f = f[i+4:]
			}
			files = append(files, strings.Trim(f, `"`))
		}
	}
	return files, nil
}

// outOfScope returns changed files not under any allowed prefix. No paths
// means no restriction.
func outOfScope(files, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	var bad []string
	for _, f := range files {
		ok := false
		for _, p := range paths {
			if pathsOverlap(f, p) && (norm(f) == norm(p) || strings.HasPrefix(norm(f), norm(p)+"/")) {
				ok = true
			}
		}
		if !ok {
			bad = append(bad, f)
		}
	}
	return bad
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, agents.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, tail(string(out), 300))
	}
	return string(out), nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
