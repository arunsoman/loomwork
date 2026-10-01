package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Context items Loom can add to an agent's prompt. The module's own spec is
// always included: an agent cannot work without it.
const (
	ItemGoal         = "goal"         // the user's overall goal
	ItemPlan         = "plan"         // titles of every module in the plan
	ItemDependencies = "dependencies" // specs of the modules this one depends on
	ItemFeedback     = "feedback"     // a verifier's issues from a rejected attempt
)

var validItems = map[string]bool{ItemGoal: true, ItemPlan: true, ItemDependencies: true, ItemFeedback: true, ItemDiff: true, ItemFiles: true, ItemTests: true, ItemOverview: true}

// ContextRule limits what the context manager may send one agent. The manager
// (ctxmgr.go) decides what is relevant; the rule says which kinds of context
// the agent is permitted to receive, which paths never reach it, and how many
// bytes of extra context it may be given.
//
// Files are controlled with a git sparse-checkout of the agent's worktree:
// excluded paths are not on disk there. This is visibility, not a sandbox: an
// agent that runs `git show` or reads outside its directory is not stopped.
type ContextRule struct {
	Allow    []string `json:"allow,omitempty"`     // if set, only these paths are present
	Deny     []string `json:"deny,omitempty"`      // never present (gitignore-style patterns)
	Include  []string `json:"include"`             // prompt items, see Item* constants
	MaxBytes int      `json:"max_bytes,omitempty"` // cap on the extra context Loom adds
}

// ContextPolicy holds the manager's limits. It resolves a rule for (role, agent): built-in default, then
// "default", then the role's rule, then the agent's rule. A non-empty field in
// a later rule replaces the earlier one; Deny lists accumulate.
type ContextPolicy struct {
	Default ContextRule            `json:"default"`
	Roles   map[string]ContextRule `json:"roles,omitempty"`
	Agents  map[string]ContextRule `json:"agents,omitempty"`
}

// DefaultDeny keeps common secrets out of every agent's checkout.
var DefaultDeny = []string{".env", ".env.*", "*.pem", "*.key", "id_rsa*", "id_ed25519*", ".agent/state/", ".agent/worktrees/"}

// DefaultContext is used when .agent/context.json is absent. The planner sees
// the goal; the builder sees its module and its dependencies; verifiers see
// only the module under review.
func DefaultContext() *ContextPolicy {
	return &ContextPolicy{
		Default: ContextRule{Deny: DefaultDeny, MaxBytes: 8000},
		Roles: map[string]ContextRule{
			KindPlan:   {Include: []string{ItemGoal, ItemOverview}},
			KindBuild:  {Include: []string{ItemGoal, ItemDependencies, ItemFeedback, ItemFiles, ItemTests}},
			KindVerify: {Include: []string{ItemDiff, ItemTests}},
			KindShip:   {Include: []string{ItemPlan}},
		},
	}
}

func ContextPath(folder string) string { return filepath.Join(folder, ".agent", "context.json") }

// LoadContext reads .agent/context.json, or returns the default.
func LoadContext(folder string) (*ContextPolicy, error) {
	data, err := os.ReadFile(ContextPath(folder))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultContext(), nil
	}
	if err != nil {
		return nil, err
	}
	var p ContextPolicy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", ContextPath(folder), err)
	}
	return &p, p.Validate()
}

// SaveContext validates and writes the policy atomically.
func SaveContext(folder string, p *ContextPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(ContextPath(folder), append(data, '\n'))
}

// Validate rejects unknown items, unsafe paths and absurd limits.
func (p *ContextPolicy) Validate() error {
	check := func(where string, r ContextRule) error {
		for _, it := range r.Include {
			if !validItems[it] {
				return fmt.Errorf("%s: unknown context item %q", where, it)
			}
		}
		for _, a := range r.Allow {
			if err := checkRelPath(a); err != nil {
				return fmt.Errorf("%s allow: %w", where, err)
			}
		}
		for _, d := range r.Deny {
			if d == "" || strings.ContainsAny(d, "\x00\n") || strings.Contains(d, "..") {
				return fmt.Errorf("%s: bad deny pattern %q", where, d)
			}
		}
		if r.MaxBytes < 0 || r.MaxBytes > 1<<20 {
			return fmt.Errorf("%s: max_bytes out of range", where)
		}
		return nil
	}
	if err := check("default", p.Default); err != nil {
		return err
	}
	for k, r := range p.Roles {
		if k != KindPlan && k != KindBuild && k != KindVerify && k != KindShip {
			return fmt.Errorf("unknown role %q", k)
		}
		if err := check("role "+k, r); err != nil {
			return err
		}
	}
	for k, r := range p.Agents {
		if err := check("agent "+k, r); err != nil {
			return err
		}
	}
	return nil
}

// Rule resolves the effective rule. Secrets in DefaultDeny stay denied unless
// a rule explicitly lists the same pattern under allow.
func (p *ContextPolicy) Rule(role, agent string) ContextRule {
	out := ContextRule{MaxBytes: 8000}
	deny := map[string]bool{}
	layers := []ContextRule{p.Default, p.Roles[role], p.Agents[agent]}
	for _, l := range layers {
		if l.Allow != nil {
			out.Allow = l.Allow
		}
		if l.Include != nil {
			out.Include = l.Include
		}
		if l.MaxBytes > 0 {
			out.MaxBytes = l.MaxBytes
		}
		for _, d := range l.Deny {
			deny[d] = true
		}
	}
	for d := range deny {
		out.Deny = append(out.Deny, d)
	}
	sort.Strings(out.Deny)
	return out
}

// Has reports whether the rule includes a prompt item.
func (r ContextRule) Has(item string) bool {
	for _, i := range r.Include {
		if i == item {
			return true
		}
	}
	return false
}

// SparsePatterns renders the rule as git sparse-checkout (non-cone) patterns,
// or nil when nothing is restricted.
func (r ContextRule) SparsePatterns() []string {
	if len(r.Allow) == 0 && len(r.Deny) == 0 {
		return nil
	}
	var pats []string
	if len(r.Allow) == 0 {
		pats = append(pats, "/*")
	}
	for _, a := range r.Allow {
		a = "/" + strings.TrimPrefix(norm(a), "/")
		pats = append(pats, a, a+"/")
	}
	for _, d := range r.Deny {
		pats = append(pats, "!"+d)
	}
	return pats
}

// ApplyVisibility restricts which files exist in dir (a worktree) to what the
// rule allows, and returns the files hidden from the agent.
func ApplyVisibility(dir string, r ContextRule) (hidden []string, err error) {
	pats := r.SparsePatterns()
	if pats == nil {
		_, err = git(dir, "sparse-checkout", "disable")
		return nil, err
	}
	args := append([]string{"sparse-checkout", "set", "--no-cone", "--"}, pats...)
	if _, err := git(dir, args...); err != nil {
		return nil, err
	}
	out, err := git(dir, "ls-files", "-t")
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "S ") { // skip-worktree: not checked out
			hidden = append(hidden, strings.TrimPrefix(l, "S "))
		}
	}
	return hidden, nil
}
