// Package agents runs external agent CLIs (claude, codex, pi, hermes) as
// subprocesses. Loom never reads or forwards their API keys: each tool uses
// its own login, and Loom only passes a prompt in and reads stdout.
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Spec describes how to run one agent CLI non-interactively.
type Spec struct {
	Name string `json:"name"`
	// Cmd is the argv. The prompt is appended as the last argument unless
	// Stdin is true, in which case it is written to the process's stdin.
	Cmd   []string `json:"cmd"`
	Stdin bool     `json:"stdin,omitempty"`
	// Work, if set, is the argv used when the job may edit files (build and
	// ship roles). It grants the tool's own edit permission for the job's
	// working directory only; Cmd stays read-only for chat, plan and verify.
	Work []string `json:"work,omitempty"`
	// Plain, if set, is the argv for jobs that must not use tools or touch
	// files (the context curator): tools off, no session saved.
	Plain []string `json:"plain,omitempty"`
	// Probe is a cheap argv used to check the tool works, e.g. ["claude","--version"].
	Probe []string `json:"probe,omitempty"`
}

// DefaultSpecs are starting points. Flags differ between CLI versions, so
// they can be overridden in ~/.loomwork/agents.json.
func DefaultSpecs() []Spec {
	return []Spec{
		{Name: "claude", Cmd: []string{"claude", "-p"}, Work: []string{"claude", "-p", "--permission-mode", "acceptEdits"}, Plain: []string{"claude", "-p", "--tools", "", "--no-session-persistence", "--disable-slash-commands"}, Stdin: true, Probe: []string{"claude", "--version"}},
		{Name: "codex", Cmd: []string{"codex", "exec", "--sandbox", "read-only", "-"}, Work: []string{"codex", "exec", "--sandbox", "workspace-write", "-"}, Plain: []string{"codex", "exec", "--sandbox", "read-only", "--ephemeral", "-"}, Stdin: true, Probe: []string{"codex", "--version"}},
		{Name: "pi", Cmd: []string{"pi", "-p"}, Plain: []string{"pi", "-p", "--no-tools", "--no-session"}, Probe: []string{"pi", "--version"}},
		{Name: "hermes", Cmd: []string{"hermes", "chat", "-q"}, Probe: []string{"hermes", "--version"}},
	}
}

// Config is the on-disk agents.json.
type Config struct {
	// Chain is the driver preference order for `ask`/`run`. "ollama" is
	// recognised; every other name must match a Spec.
	Chain  []string `json:"chain,omitempty"`
	Agents []Spec   `json:"agents,omitempty"`
}

// ConfigPath returns the agents.json path under the Loom home directory.
func ConfigPath(home string) string { return filepath.Join(home, "agents.json") }

// LoadConfig reads agents.json, falling back to defaults. Entries in the
// file replace defaults with the same name; others are added.
func LoadConfig(home string) (*Config, error) {
	cfg := &Config{Chain: []string{"ollama", "claude", "codex", "pi", "hermes"}}
	specs := map[string]Spec{}
	var order []string
	for _, s := range DefaultSpecs() {
		specs[s.Name] = s
		order = append(order, s.Name)
	}
	data, err := os.ReadFile(ConfigPath(home))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		var user Config
		if err := json.Unmarshal(data, &user); err != nil {
			return nil, fmt.Errorf("%s: %w", ConfigPath(home), err)
		}
		if len(user.Chain) > 0 {
			cfg.Chain = user.Chain
		}
		for _, s := range user.Agents {
			if s.Name == "" || len(s.Cmd) == 0 {
				return nil, fmt.Errorf("%s: agent needs a name and cmd", ConfigPath(home))
			}
			if _, ok := specs[s.Name]; !ok {
				order = append(order, s.Name)
			}
			specs[s.Name] = s
		}
	}
	for _, n := range order {
		cfg.Agents = append(cfg.Agents, specs[n])
	}
	return cfg, nil
}

// Spec returns the named spec.
func (c *Config) Spec(name string) (Spec, bool) {
	for _, s := range c.Agents {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}

// Job is one invocation of an agent.
type Job struct {
	Prompt  string
	Dir     string        // working directory (e.g. a git worktree)
	Write   bool          // job may edit files: use Spec.Work when set
	Plain   bool          // job must not use tools: use Spec.Plain when set
	Timeout time.Duration // 0 means no limit beyond ctx
}

// Result is what an agent run produced.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Adapter runs a Spec.
type Adapter struct {
	Spec Spec
}

// Available reports whether the CLI is on PATH.
func (a Adapter) Available() bool {
	if len(a.Spec.Cmd) == 0 {
		return false
	}
	_, err := exec.LookPath(a.Spec.Cmd[0])
	return err == nil
}

// Healthy runs the probe command and reports whether it exits 0. A tool that
// is on PATH but broken (for example a missing virtualenv) fails here.
func (a Adapter) Healthy(ctx context.Context) bool {
	if !a.Available() {
		return false
	}
	probe := a.Spec.Probe
	if len(probe) == 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, probe[0], probe[1:]...).Run() == nil
}

// Env returns the environment passed to agent CLIs: the user's own, minus
// Loom's secrets so a spawned tool never sees the memory passphrase.
func Env() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LOOMWORK_MEMORY_PASSPHRASE=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Run executes the agent with no shell. A non-zero exit is reported in
// Result.ExitCode with a nil error; the error is for failures to run at all
// (not found, timeout, cancellation).
func (a Adapter) Run(ctx context.Context, job Job) (Result, error) {
	if len(a.Spec.Cmd) == 0 {
		return Result{}, fmt.Errorf("agent %q has no command", a.Spec.Name)
	}
	if job.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, job.Timeout)
		defer cancel()
	}
	argv := append([]string(nil), a.Spec.Cmd...)
	if job.Write && len(a.Spec.Work) > 0 {
		argv = append([]string(nil), a.Spec.Work...)
	}
	if job.Plain && !job.Write && len(a.Spec.Plain) > 0 {
		argv = append([]string(nil), a.Spec.Plain...)
	}
	if !a.Spec.Stdin {
		argv = append(argv, job.Prompt)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = job.Dir
	cmd.Env = Env()
	if a.Spec.Stdin {
		cmd.Stdin = strings.NewReader(job.Prompt)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ctx.Err() == nil {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		if ctx.Err() != nil {
			return res, fmt.Errorf("agent %s: %w", a.Spec.Name, ctx.Err())
		}
		return res, fmt.Errorf("agent %s: %w", a.Spec.Name, err)
	}
	return res, nil
}
