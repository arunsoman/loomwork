package aci

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// SkillsGraph is the parsed skills/graph.json file.
type SkillsGraph struct {
	Skills []SkillDef `json:"skills"`
}

// SkillDef is a single skill node in the DAG.
type SkillDef struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Inputs      map[string]string `json:"inputs"`
	Outputs     map[string]string `json:"outputs"`
	Requires    []string          `json:"requires"`
	Impl        *SkillImpl        `json:"impl,omitempty"`
}

// SkillImpl is the optional implementation reference.
type SkillImpl struct {
	Type  string `json:"type"`  // "wasm", "python", "js" (declared only), "md" (attached to the prompt)
	Entry string `json:"entry"` // path to impl file
}

// ParseSkillsGraph parses skills/graph.json from the archive.
func (a *Archive) ParseSkillsGraph() (*SkillsGraph, error) {
	path := a.Manifest.Skills.Graph
	data, ok := a.Files[path]
	if !ok {
		return nil, fmt.Errorf("skills graph not found: %s", path)
	}
	var g SkillsGraph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("parse skills graph: %w", err)
	}
	return &g, nil
}

// DetectCycles returns an error if the skills graph contains a cycle.
// This is the static check (run at packaging time); the runtime also
// checks dynamically via the task graph.
//
// Per PRD feedback: "consider whether cycles should be statically rejected
// at packaging time as well as runtime." Answer: yes, they should.
func (g *SkillsGraph) DetectCycles() error {
	// Build adjacency: skill name -> list of skills it requires
	adj := make(map[string][]string)
	names := make(map[string]bool)
	for _, s := range g.Skills {
		names[s.Name] = true
		adj[s.Name] = s.Requires
	}

	// Validate that all `requires` reference existing skills
	for _, s := range g.Skills {
		for _, req := range s.Requires {
			if !names[req] {
				return fmt.Errorf("skill %q requires unknown skill %q", s.Name, req)
			}
		}
	}

	// DFS-based cycle detection
	const (
		white = 0 // unvisited
		gray  = 1 // in progress
		black = 2 // done
	)
	color := make(map[string]int)
	for name := range adj {
		color[name] = white
	}

	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		if color[name] == gray {
			// Found a cycle — build the cycle path for the error message
			cycleStart := -1
			for i, p := range path {
				if p == name {
					cycleStart = i
					break
				}
			}
			cycle := append(append([]string{}, path[cycleStart:]...), name)
			return fmt.Errorf("cycle detected in skills graph: %v", cycle)
		}
		if color[name] == black {
			return nil
		}
		color[name] = gray
		for _, dep := range adj[name] {
			next := append(append([]string{}, path...), name)
			if err := visit(dep, next); err != nil {
				return err
			}
		}
		color[name] = black
		return nil
	}

	// Sort names for deterministic traversal
	sortedNames := make([]string, 0, len(adj))
	for name := range adj {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	for _, name := range sortedNames {
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// ImplEntries returns the impl.entry paths declared by skills, so packaging
// can digest them and the loader can require them.
func (g *SkillsGraph) ImplEntries() []string {
	var out []string
	for _, s := range g.Skills {
		if s.Impl != nil && s.Impl.Entry != "" {
			out = append(out, s.Impl.Entry)
		}
	}
	return out
}

// ValidateSkillsGraph loads and validates the skills graph from an archive.
// Checks: parseable, no unknown requires, no cycles.
func (a *Archive) ValidateSkillsGraph() error {
	g, err := a.ParseSkillsGraph()
	if err != nil {
		return err
	}
	if err := g.DetectCycles(); err != nil {
		return err
	}
	for _, e := range g.ImplEntries() {
		if _, ok := a.Files[e]; !ok {
			return fmt.Errorf("skill implementation %q is not in the archive", e)
		}
	}
	return nil
}

// DetectCyclesInDirectory is a convenience for the packaging step.
// Reads skills/graph.json from dir and checks for cycles.
func DetectCyclesInDirectory(dir string) error {
	archive, err := ArchiveFromDirectory(dir)
	if err != nil {
		return err
	}
	return archive.ValidateSkillsGraph()
}

// Sentinel to silence unused import in some build configs
var _ = os.ReadFile
