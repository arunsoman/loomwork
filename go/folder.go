package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loomwork.dev/loomwork/internal/aci"
	"loomwork.dev/loomwork/internal/runtime"
)

// A plain folder is the low-ceremony way to write an agent:
//
//	AGENT.md       the persona / system prompt
//	skills/*.md    one skill per file (optional front-matter: name, description, requires)
//	MEMORY.md      the agent's long-term memory (preferences, facts, decisions);
//	               deliberately NOT packaged: memory stays on your machine
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

// buildFolderManifest is the one place a plain folder is turned into an agent:
// it parses skills/*.md and generates the manifest, skills graph, bindings,
// sandbox and memory schema into target. The user's markdown is only read.
//
// With target == dir (`loomwork init`, native mode) the generated files land in
// the folder. With any other target (`loomwork package`, coexistence mode) the
// files the manifest references (AGENT.md, skills/*.md) are copied there first,
// so the folder itself is never written to.
func buildFolderManifest(dir, target string) ([]aci.SkillDef, error) {
	skills, err := aci.SkillsFromMarkdownDir(dir)
	if err != nil {
		return nil, err
	}
	if skills == nil {
		skills = []aci.SkillDef{}
	}
	if target != dir {
		files := []string{"AGENT.md"}
		for _, sk := range skills {
			files = append(files, sk.Impl.Entry)
		}
		for _, rel := range files {
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil {
				return nil, err
			}
			dst := filepath.Join(target, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return nil, err
			}
		}
	}
	abs, _ := filepath.Abs(dir)
	opts := runtime.ScaffoldOptions{PersonaPath: "AGENT.md", Skills: skills}
	if err := runtime.SaveManifest(target, filepath.Base(abs), opts); err != nil {
		return nil, err
	}
	return skills, nil
}

// printFolderNotice reports what a plain folder contained.
func printFolderNotice(dir string, skills []aci.SkillDef) {
	fmt.Printf("✓ Plain folder detected: AGENT.md + %d skill file(s)\n", len(skills))
	if _, err := os.Stat(filepath.Join(dir, "MEMORY.md")); err == nil {
		fmt.Printf("ℹ MEMORY.md is not packaged: memory never travels inside an ACI (PRD §8.3)\n")
	}
}

// scaffoldFromFolder materializes the ACI files in the folder (native mode).
func scaffoldFromFolder(dir string) error {
	skills, err := buildFolderManifest(dir, dir)
	if err != nil {
		return err
	}
	printFolderNotice(dir, skills)
	return nil
}

// stageFolder builds the generated agent files for a plain folder in a
// temporary directory outside it (coexistence mode). The caller removes the
// returned directory.
func stageFolder(dir string) (string, error) {
	stage, err := os.MkdirTemp("", "loomwork-stage-")
	if err != nil {
		return "", err
	}
	skills, err := buildFolderManifest(dir, stage)
	if err != nil {
		os.RemoveAll(stage)
		return "", err
	}
	printFolderNotice(dir, skills)
	return stage, nil
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

// warnUnlistedFiles tells the user about regular files in a native-mode folder
// that the manifest does not cover and package therefore leaves out. Hidden
// entries, archives and their sidecars, the manifest and its generated
// signature, and MEMORY.md (deliberately never packed) are not reported.
func warnUnlistedFiles(dir string, digests map[string]string, outPath string) {
	skip := map[string]bool{"MEMORY.md": true, "manifest.json": true}
	if outPath != "" {
		skip[filepath.ToSlash(filepath.Clean(outPath))] = true
	}
	var stray []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() || strings.HasPrefix(rel, "signatures/") || skip[rel] {
			return nil
		}
		if strings.HasSuffix(rel, ".aci") || strings.HasSuffix(rel, ".slsa.json") || strings.HasSuffix(rel, ".attestation.json") {
			return nil
		}
		if _, ok := digests[rel]; !ok {
			stray = append(stray, rel)
		}
		return nil
	})
	if len(stray) == 0 {
		return
	}
	sort.Strings(stray)
	shown := stray
	if len(shown) > 10 {
		shown = shown[:10]
	}
	fmt.Fprintf(os.Stderr, "⚠ %d file(s) in this folder are not in the manifest and will NOT be packed:\n", len(stray))
	for _, f := range shown {
		fmt.Fprintf(os.Stderr, "    %s\n", f)
	}
	if len(stray) > len(shown) {
		fmt.Fprintf(os.Stderr, "    and %d more\n", len(stray)-len(shown))
	}
	fmt.Fprintf(os.Stderr, "  (list skill files in skills/graph.json to package them, or remove the manifest to let loomwork derive everything from AGENT.md and skills/*.md)\n")
}
