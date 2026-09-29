package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"loomwork.dev/loomwork/internal/runtime"
)

// cmdInit scaffolds a minimal agent in the current (or specified) directory.
//
// Usage: loomwork init [dir] [--force]
//
// Creates: manifest.json, persona/system_prompt.md, skills/graph.json,
// tools/bindings.json, memory-schema.json, sandbox.json, all with computed
// digests, ready for `loomwork package`. It refuses to overwrite an existing
// agent unless --force is given.
func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite an existing agent in the directory")
	pos := parseArgs(fs, args)
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil && !*force {
		fail(fmt.Errorf("%s already contains an agent (manifest.json); use --force to overwrite it", dir))
	}
	abs, _ := filepath.Abs(dir)
	name := filepath.Base(abs)
	fmt.Printf("Initializing agent in %s/ (name=%s)\n", dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
	}
	if err := runtime.SaveManifestForInit(dir, name); err != nil {
		fail(err)
	}
	fmt.Printf("✓ Created manifest.json with computed digests\n")
	fmt.Printf("✓ Created persona/system_prompt.md\n")
	fmt.Printf("✓ Created skills/graph.json (2 skills: answer_question, propose_action)\n")
	fmt.Printf("✓ Created tools/bindings.json (1 binding: filesystem, scoped to your home directory)\n")
	fmt.Printf("✓ Created memory-schema.json\n")
	fmt.Printf("✓ Created sandbox.json\n")
	fmt.Printf("\nNext: point it at a folder and ask a question:\n")
	fmt.Printf("  cd %s && loomwork ask \"what does this folder contain?\" --folder .\n", dir)
	fmt.Printf("Then package it:\n")
	fmt.Printf("  loomwork package --out %s.aci\n", name)
}
