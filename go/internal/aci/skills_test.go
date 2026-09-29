package aci

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkillsGraph(t *testing.T, dir, json string) {
	t.Helper()
	skillsDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "graph.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsGraphAcyclic(t *testing.T) {
	g := &SkillsGraph{
		Skills: []SkillDef{
			{Name: "search", Requires: nil},
			{Name: "summarize", Requires: []string{"search"}},
			{Name: "synthesize", Requires: []string{"summarize"}},
		},
	}
	if err := g.DetectCycles(); err != nil {
		t.Fatalf("expected no cycle, got: %v", err)
	}
}

func TestSkillsGraphCyclic(t *testing.T) {
	g := &SkillsGraph{
		Skills: []SkillDef{
			{Name: "a", Requires: []string{"c"}},
			{Name: "b", Requires: []string{"a"}},
			{Name: "c", Requires: []string{"b"}},
		},
	}
	err := g.DetectCycles()
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestSkillsGraphSelfReference(t *testing.T) {
	g := &SkillsGraph{
		Skills: []SkillDef{
			{Name: "a", Requires: []string{"a"}},
		},
	}
	err := g.DetectCycles()
	if err == nil {
		t.Fatal("expected self-reference cycle error, got nil")
	}
}

func TestSkillsGraphUnknownRequires(t *testing.T) {
	g := &SkillsGraph{
		Skills: []SkillDef{
			{Name: "a", Requires: []string{"nonexistent"}},
		},
	}
	err := g.DetectCycles()
	if err == nil {
		t.Fatal("expected unknown-requires error, got nil")
	}
}

func TestDetectCyclesInDirectory(t *testing.T) {
	dir := t.TempDir()
	// Build a minimal valid ACI dir with a cyclic skills graph
	m := minimalManifest(t, dir) // writes persona, skills/graph.json (empty), etc.
	// Write the manifest to disk
	manifestJSON, err := m.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	// Overwrite skills/graph.json with a cyclic one
	writeSkillsGraph(t, dir, `{"skills":[{"name":"a","requires":["b"]},{"name":"b","requires":["a"]}]}`)

	// Re-read manifest, update the skills/graph.json digest, write back
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "skills/graph.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Digests["skills/graph.json"] = Sha256Bytes(data)
	out, err := manifest.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}

	err = DetectCyclesInDirectory(dir)
	if err == nil {
		t.Fatal("expected cycle error from directory, got nil")
	}
}
