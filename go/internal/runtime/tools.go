package runtime

import (
	"encoding/json"
	"fmt"
)

// ToolBinding is one entry of tools/bindings.json.
type ToolBinding struct {
	Name         string   `json:"name"`
	MCPServer    string   `json:"mcpServer"`
	AllowedTools []string `json:"allowedTools"`
	Scope        struct {
		Paths []string `json:"paths"`
	} `json:"scope"`
}

// ToolGate enforces the ACI's declared tool bindings: a tool may be used only
// if its binding declares it, and path-scoped tools only within the binding's
// scope. Denials are returned as errors so callers can log them.
type ToolGate struct {
	bindings map[string]*ToolBinding
	policy   *SandboxPolicy
}

// NewToolGate parses tools/bindings.json.
func NewToolGate(bindingsJSON []byte, policy *SandboxPolicy) (*ToolGate, error) {
	var doc struct {
		Bindings []*ToolBinding `json:"bindings"`
	}
	if err := json.Unmarshal(bindingsJSON, &doc); err != nil {
		return nil, fmt.Errorf("parse tools/bindings.json: %w", err)
	}
	g := &ToolGate{bindings: map[string]*ToolBinding{}, policy: policy}
	for _, b := range doc.Bindings {
		g.bindings[b.Name] = b
		for i, p := range b.Scope.Paths {
			b.Scope.Paths[i] = expandHome(p)
		}
	}
	return g, nil
}

// Check returns nil if binding may call tool on path (path may be empty for
// tools with no path argument). Filesystem tools are additionally checked
// against the sandbox policy: reads against fs.read, writes against fs.write.
func (g *ToolGate) Check(binding, tool, path string) error {
	b, ok := g.bindings[binding]
	if !ok {
		return fmt.Errorf("tool denied: no binding %q declared by this agent", binding)
	}
	allowed := false
	for _, t := range b.AllowedTools {
		if t == tool {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("tool denied: %q is not in allowedTools of binding %q", tool, binding)
	}
	if path == "" {
		return nil
	}
	abs, err := canonical(path)
	if err != nil {
		return err
	}
	if len(b.Scope.Paths) > 0 && !matchAny(abs, g.canonGlobs(b.Scope.Paths)) {
		return fmt.Errorf("tool denied: %s is outside the scope of binding %q", abs, binding)
	}
	if g.policy != nil {
		switch tool {
		case "read_file", "list_dir":
			return g.policy.CheckRead(abs)
		case "write_file":
			return g.policy.CheckWrite(abs)
		}
	}
	return nil
}

func (g *ToolGate) canonGlobs(globs []string) []string {
	return (&SandboxPolicy{}).canonGlobs(globs)
}
