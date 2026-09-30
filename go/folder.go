package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"loomwork.dev/loomwork/internal/aci"
	"loomwork.dev/loomwork/internal/runtime"
)

// A plain folder is the low-ceremony way to write an agent:
//
//	AGENT.md       the persona / system prompt
//	skills/*.md    one skill per file (optional front-matter: name, description, requires)
//	MEMORY.md      your notes; deliberately NOT packaged (memory stays on your machine)
//
// `loomwork init` and `loomwork package` accept such a folder and generate the
// manifest, skills graph, bindings, sandbox and memory schema around it. The
// markdown files are referenced, digested and signed as they are; they are
// never rewritten.

// isPlainFolder reports whether dir holds an AGENT.md and needs its manifest
// generated: there is no manifest yet, or the caller asked to regenerate it.
func isPlainFolder(dir string, force bool) bool {
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil && !force {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "AGENT.md"))
	return err == nil && info.Mode().IsRegular()
}

// scaffoldFromFolder generates the ACI files for a plain folder.
func scaffoldFromFolder(dir string) error {
	skills, err := aci.SkillsFromMarkdownDir(dir)
	if err != nil {
		return err
	}
	opts := runtime.ScaffoldOptions{PersonaPath: "AGENT.md", Skills: skills}
	if skills == nil {
		opts.Skills = []aci.SkillDef{}
	}
	abs, _ := filepath.Abs(dir)
	if err := runtime.SaveManifest(dir, filepath.Base(abs), opts); err != nil {
		return err
	}
	fmt.Printf("✓ Plain folder detected: AGENT.md + %d skill file(s)\n", len(skills))
	if _, err := os.Stat(filepath.Join(dir, "MEMORY.md")); err == nil {
		fmt.Printf("ℹ MEMORY.md is not packaged: memory never travels inside an ACI (PRD §8.3)\n")
	}
	return nil
}

// isFolderAgent reports whether dir's manifest was generated from a plain
// folder (its persona is the user's own AGENT.md).
func isFolderAgent(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return false
	}
	var m aci.Manifest
	return json.Unmarshal(data, &m) == nil && m.Persona.SystemPrompt == "AGENT.md"
}

// refreshFolderSkills re-reads skills/*.md and rewrites skills/graph.json so a
// skill added after `init` is packaged, not silently dropped. Only the skills
// graph is regenerated; bindings, sandbox and the memory schema are left alone.
func refreshFolderSkills(dir string) error {
	skills, err := aci.SkillsFromMarkdownDir(dir)
	if err != nil {
		return err
	}
	if skills == nil {
		skills = []aci.SkillDef{}
	}
	b, err := json.MarshalIndent(aci.SkillsGraph{Skills: skills}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "graph.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("✓ Skills refreshed from skills/*.md (%d)\n", len(skills))
	return nil
}

// warnUnlistedSkills tells the user about skills/*.md files the manifest does
// not list, which package would otherwise leave out without a word.
func warnUnlistedSkills(dir string, digests map[string]string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "skills", "*.md"))
	for _, m := range matches {
		rel := "skills/" + filepath.Base(m)
		if _, ok := digests[rel]; !ok {
			fmt.Fprintf(os.Stderr, "⚠ %s is not in the manifest and will NOT be packaged (list it as a skill implementation in skills/graph.json)\n", rel)
		}
	}
}
