// Package flow coordinates several agent CLIs on one task: a planner splits a
// goal into modules, a builder implements each in its own git worktree,
// verifiers check it, and a shipper commits (and optionally pushes).
package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Stage kinds, in the only order a workflow may use them.
const (
	KindPlan   = "plan"
	KindBuild  = "build"
	KindVerify = "verify"
	KindShip   = "ship"
)

// Stage is one step of a workflow.
type Stage struct {
	Kind   string   `json:"kind"`
	Agents []string `json:"agents"`            // one for plan/build/ship; one or more for verify
	Policy string   `json:"policy,omitempty"`  // verify: "all" (default) or "any"
	Retry  int      `json:"retries,omitempty"` // verify: builder retries after a failed verdict
	Push   bool     `json:"push,omitempty"`    // ship: also push (still needs confirmation at run time)
}

// Workflow is a named pipeline definition, stored as JSON.
type Workflow struct {
	Name    string  `json:"name"`
	Stages  []Stage `json:"stages"`
	TestCmd string  `json:"test_cmd,omitempty"` // objective gate run in each worktree, e.g. "go test ./..."
	// ContextAgent is the agent that curates what each job is sent. Empty
	// means the deterministic context manager alone. The agent only ever
	// picks from candidates Loom offers; see curator.go.
	ContextAgent string `json:"context_agent,omitempty"`
	MaxParallel  int    `json:"max_parallel,omitempty"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)

// Default returns the codex → claude → pi+hermes → claude pipeline.
func Default() *Workflow {
	return &Workflow{
		Name: "default",
		Stages: []Stage{
			{Kind: KindPlan, Agents: []string{"codex"}},
			{Kind: KindBuild, Agents: []string{"claude"}},
			{Kind: KindVerify, Agents: []string{"pi"}, Policy: "all", Retry: 2},
			{Kind: KindShip, Agents: []string{"claude"}},
		},
		MaxParallel: 2,
	}
}

// Stage returns the first stage of the given kind.
func (w *Workflow) Stage(kind string) (Stage, bool) {
	for _, s := range w.Stages {
		if s.Kind == kind {
			return s, true
		}
	}
	return Stage{}, false
}

// Validate checks structure only. known, if non-nil, is the set of agent
// names that exist; unknown names are rejected.
func (w *Workflow) Validate(known map[string]bool) error {
	if !nameRe.MatchString(w.Name) {
		return fmt.Errorf("workflow name %q must be 1-40 letters, digits, - or _", w.Name)
	}
	order := map[string]int{KindPlan: 0, KindBuild: 1, KindVerify: 2, KindShip: 3}
	last := -1
	seen := map[string]bool{}
	for i, s := range w.Stages {
		rank, ok := order[s.Kind]
		if !ok {
			return fmt.Errorf("stage %d: unknown kind %q", i+1, s.Kind)
		}
		if seen[s.Kind] {
			return fmt.Errorf("stage %d: duplicate %s stage", i+1, s.Kind)
		}
		seen[s.Kind] = true
		if rank < last {
			return fmt.Errorf("stage %d: %s must come before the previous stage (order is plan, build, verify, ship)", i+1, s.Kind)
		}
		last = rank
		if len(s.Agents) == 0 {
			return fmt.Errorf("stage %d (%s): choose an agent", i+1, s.Kind)
		}
		if s.Kind != KindVerify && len(s.Agents) > 1 {
			return fmt.Errorf("stage %d (%s): exactly one agent", i+1, s.Kind)
		}
		for _, a := range s.Agents {
			if known != nil && !known[a] {
				return fmt.Errorf("stage %d (%s): unknown agent %q", i+1, s.Kind, a)
			}
		}
		if s.Kind == KindVerify && s.Policy != "" && s.Policy != "all" && s.Policy != "any" {
			return fmt.Errorf("stage %d: verify policy must be all or any", i+1)
		}
		if s.Retry < 0 || s.Retry > 5 {
			return fmt.Errorf("stage %d: retries must be 0-5", i+1)
		}
	}
	if !seen[KindPlan] || !seen[KindBuild] {
		return errors.New("a workflow needs at least a plan stage and a build stage")
	}
	if seen[KindShip] && !seen[KindVerify] {
		return errors.New("a ship stage needs a verify stage before it: nothing unverified gets committed")
	}
	if v, ok := w.Stage(KindVerify); ok {
		b, _ := w.Stage(KindBuild)
		for _, a := range v.Agents {
			if a == b.Agents[0] {
				return fmt.Errorf("verifier %q is also the builder: a module must be verified by a different agent", a)
			}
		}
	}
	if p, ok := w.Stage(KindShip); ok && p.Push && !seen[KindShip] {
		return errors.New("push requires a ship stage")
	}
	if w.ContextAgent != "" && known != nil && !known[w.ContextAgent] {
		return fmt.Errorf("unknown context agent %q", w.ContextAgent)
	}
	if w.MaxParallel < 0 || w.MaxParallel > 8 {
		return errors.New("max_parallel must be 0-8")
	}
	if strings.ContainsAny(w.TestCmd, "\x00") {
		return errors.New("test_cmd contains a NUL byte")
	}
	return nil
}

// Dir returns the workflows directory inside a project folder.
func WorkflowDir(folder string) string { return filepath.Join(folder, ".agent", "workflows") }

// LoadWorkflow reads <folder>/.agent/workflows/<name>.json; the name
// "default" falls back to the built-in when the file does not exist.
func LoadWorkflow(folder, name string) (*Workflow, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid workflow name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(WorkflowDir(folder), name+".json"))
	if errors.Is(err, os.ErrNotExist) && name == "default" {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	var w Workflow
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("workflow %s: %w", name, err)
	}
	if w.Name != name {
		return nil, fmt.Errorf("workflow file %s.json declares name %q", name, w.Name)
	}
	return &w, nil
}

// SaveWorkflow validates and writes a workflow atomically.
func SaveWorkflow(folder string, w *Workflow, known map[string]bool) error {
	if err := w.Validate(known); err != nil {
		return err
	}
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(WorkflowDir(folder), w.Name+".json"), append(data, '\n'))
}

// ListWorkflows returns the saved workflow names (plus "default").
func ListWorkflows(folder string) []string {
	names := []string{"default"}
	ents, _ := os.ReadDir(WorkflowDir(folder))
	for _, e := range ents {
		n := strings.TrimSuffix(e.Name(), ".json")
		if n != e.Name() && n != "default" && nameRe.MatchString(n) {
			names = append(names, n)
		}
	}
	return names
}

// writeAtomic writes via a temp file and rename so readers never see a
// partial file.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
