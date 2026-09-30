package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loomwork.dev/loomwork/internal/memory"
	"loomwork.dev/loomwork/internal/secret"
)

// cmdMemoryImport: loomwork memory import <file.md>
//
// Reads a markdown file into pending records. The file is only read.
func cmdMemoryImport(view *memory.View, args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: loomwork memory import <file.md> [--kind K] [--sensitivity S] [--allow-agent a,b] [--public]

Reads a markdown file into PENDING records; nothing is activated until you
approve it. The file is never modified, renamed or deleted.

Two input shapes:
  Loomwork export format  (--- front-matter blocks): kind, provenance, consent and
                          sensitivity come from the file; new IDs are generated.
  Plain markdown          split on "## " headings, one record per section
                          (kind belief unless --kind, sensitivity low, source imported,
                          consent closed to you alone unless --allow-agent/--public).

No deduplication: importing the same file twice creates the records twice.
A file path is required (no stdin); symlinks are refused.
`)
	}
	kind := fs.String("kind", "", "record kind for plain markdown (default belief)")
	sens := fs.String("sensitivity", "low", "low|medium|high (plain markdown)")
	allow := fs.String("allow-agent", "", "comma-separated agents allowed to read the imported records (plain markdown)")
	public := fs.Bool("public", false, "any agent may read the imported records (plain markdown)")
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		fs.Usage()
		os.Exit(1)
	}
	path := pos[0]
	info, err := os.Lstat(path)
	if err != nil {
		fail(err)
	}
	if !info.Mode().IsRegular() {
		fail(fmt.Errorf("%s is not a regular file (symlinks and directories are refused)", path))
	}
	if info.Size() > memory.MaxImportBytes {
		fail(fmt.Errorf("%s is larger than %d bytes", path, memory.MaxImportBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	abs, _ := filepath.Abs(path)
	opts := memory.PlainImportOptions{
		Kind: memory.Kind(*kind), Sensitivity: memory.Sensitivity(*sens), Public: *public,
		SourceURI: "file://" + filepath.ToSlash(abs),
	}
	if *allow != "" {
		opts.AllowedAgents = strings.Split(*allow, ",")
	}
	ids, err := memory.ImportMarkdown(view, string(data), opts)
	if err != nil {
		if len(ids) > 0 {
			fmt.Fprintf(os.Stderr, "imported %d record(s) before the error\n", len(ids))
		}
		fail(err)
	}
	fmt.Printf("✓ Imported %d record(s) from %s as pending (nothing is active until approved)\n", len(ids), path)
	for _, id := range ids {
		fmt.Printf("  %s\n", id)
	}
	fmt.Printf("  Review with: loomwork memory list --status pending\n")
}

// cmdMemoryExport: loomwork memory export [--out FILE] [--force]
func cmdMemoryExport(store *memory.Store, args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: loomwork memory export [--out memory-export.md] [--force]

Writes ACTIVE records (never pending, revoked or superseded) as markdown: one
front-matter block per record, then its content. Prints to stdout unless --out
is given. --out refuses to overwrite an existing file without --force, and
refuses MEMORY.md as a target always. The format round-trips through
`+"`loomwork memory import`"+`.
`)
	}
	out := fs.String("out", "", "write to FILE instead of stdout (suggested name: memory-export.md)")
	force := fs.Bool("force", false, "overwrite an existing --out file (never MEMORY.md)")
	parseArgs(fs, args)
	if *out == "" {
		text, _, err := store.ExportMarkdown()
		if err != nil {
			fail(err)
		}
		fmt.Print(text)
		return
	}
	n, err := exportToFile(store, *out, *force)
	if err != nil {
		fail(err)
	}
	fmt.Printf("✓ Exported %d active record(s) to %s\n", n, *out)
}

// exportToFile writes the export to path, enforcing the write rules: never
// MEMORY.md (checked on the name and on where a symlink resolves), never an
// existing file unless force.
func exportToFile(store *memory.Store, path string, force bool) (int, error) {
	if isMemoryMD(path) {
		return 0, fmt.Errorf("refusing to write %s: MEMORY.md is your file and Loomwork never writes it (export to another name, e.g. memory-export.md)", path)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && isMemoryMD(resolved) {
		return 0, fmt.Errorf("refusing to write %s: it resolves to MEMORY.md, which Loomwork never writes", path)
	}
	if _, err := os.Lstat(path); err == nil && !force {
		return 0, fmt.Errorf("%s already exists; use --force to overwrite it", path)
	}
	text, n, err := store.ExportMarkdown()
	if err != nil {
		return 0, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return 0, err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return 0, err
	}
	return n, f.Close()
}

func isMemoryMD(path string) bool {
	return strings.EqualFold(filepath.Base(path), "MEMORY.md")
}

// cmdLeave: loomwork leave [--export FILE] [--delete-store] [--yes]
//
// The one-step exit. It never touches the user's own files.
func cmdLeave(args []string) {
	fs := flag.NewFlagSet("leave", flag.ExitOnError)
	exportPath := fs.String("export", "", "export active memory to FILE first (same rules as `memory export`)")
	del := fs.Bool("delete-store", false, "delete the memory database and its key file (nothing else)")
	yes := fs.Bool("yes", false, "confirm --delete-store without an export")
	force := fs.Bool("force", false, "with --export: overwrite an existing FILE (never MEMORY.md)")
	parseArgs(fs, args)

	home := homeDir()
	dbPath := secret.DefaultDBPath(home)
	keyPath := secret.DefaultKeyPath(home)
	_, dbErr := os.Stat(dbPath)
	haveStore := dbErr == nil

	if *exportPath != "" {
		if !haveStore {
			fmt.Printf("• No memory store at %s: nothing to export.\n", dbPath)
		} else {
			store, err := memory.OpenDefaultStore(home)
			if err != nil {
				fail(err)
			}
			n, err := exportToFile(store, *exportPath, *force)
			store.Close()
			if err != nil {
				fail(err)
			}
			fmt.Printf("✓ Exported %d active record(s) to %s\n", n, *exportPath)
		}
	}

	fmt.Println("\nWhat Loomwork keeps on this machine:")
	fmt.Printf("  memory store: %s (typed memory and conversation history)\n", dbPath)
	fmt.Printf("  memory key:   %s\n", keyPath)
	fmt.Printf("  signing key and trust list: %s (not touched by --delete-store)\n", filepath.Join(home, ".loomwork"))
	if acis, _ := filepath.Glob("*.aci"); len(acis) > 0 {
		fmt.Println("  .aci files in this folder (yours to keep or delete):")
		for _, a := range acis {
			fmt.Printf("    %s\n", a)
		}
	} else {
		fmt.Println("  .aci files in this folder: none")
	}
	fmt.Println("\nLoomwork never modified your files (AGENT.md, skills/*.md, MEMORY.md); your folder works as it did before.")

	if !*del {
		fmt.Println("\nNothing was deleted. To remove the store: loomwork leave --export memory-export.md --delete-store")
		return
	}
	if *exportPath == "" && !*yes {
		fmt.Println("\n⚠ --delete-store without --export would lose your approved memory. Nothing was deleted.")
		fmt.Println("  Export first:  loomwork leave --export memory-export.md --delete-store")
		fmt.Println("  Or confirm the loss with --yes.")
		return
	}
	if *exportPath == "" {
		fmt.Println("\n⚠ No export was made: approved memory will be lost.")
	}
	removed := 0
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal", dbPath + ".salt", keyPath} {
		err := os.Remove(p)
		if err == nil {
			removed++
			fmt.Printf("  deleted %s\n", p)
		} else if !os.IsNotExist(err) {
			fail(err)
		}
	}
	fmt.Printf("✓ Store deleted (%d file(s)). Signing key, trust list and your files were left alone.\n", removed)
}
