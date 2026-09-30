package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"loomwork.dev/loomwork/internal/aci"
	"loomwork.dev/loomwork/internal/llm"
	"loomwork.dev/loomwork/internal/runtime"
)

// cmdRun loads an ACI (or an agent source directory) and either dispatches a
// single task (--input) or starts an interactive prompt.
//
// Usage:
//
//	loomwork run [--input '...'] [--allow-unsigned] [--allow-untrusted-signer] <agent.aci | dir>
//
// Flags may come before or after the path.
func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	input := fs.String("input", "", "single task to run (omit for an interactive prompt)")
	allowUnsigned := fs.Bool("allow-unsigned", false, "run an ACI whose signature is missing or invalid (unsafe)")
	allowUntrusted := fs.Bool("allow-untrusted-signer", false, "run an ACI signed by a key you have not trusted")
	allowCloudSamples := fs.Bool("allow-cloud-samples", false, "let cloud models see file contents")
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		fail(fmt.Errorf("usage: loomwork run [--input '...'] <agent.aci | dir>"))
	}
	path := pos[0]

	mem, err := runtime.OpenDefaultMemory(homeDir())
	if err != nil {
		fail(err)
	}
	defer mem.Close()

	var archive *aci.Archive
	var raw []byte
	fromDir := !strings.HasSuffix(path, ".aci")
	if fromDir {
		archive, err = loadArchiveFromDir(path)
		if err != nil {
			fail(err)
		}
		fmt.Fprintf(os.Stderr, "ℹ running from source directory %s (not a signed package)\n", path)
	} else {
		raw, err = os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		archive, err = aci.ArchiveFromTarGz(raw)
		if err != nil {
			fail(fmt.Errorf("load %s: %w", path, err))
		}
		// An ACI without a valid signature from a trusted key is refused.
		status, vk, err := signaturePolicy(archive, *allowUnsigned, *allowUntrusted)
		if err != nil {
			fail(err)
		}
		switch status {
		case aci.SigTrusted:
			fmt.Fprintf(os.Stderr, "✓ Signature verified (trusted key %s)\n", vk.KeyID)
		default:
			fmt.Fprintf(os.Stderr, "⚠ %s; running anyway because of an override flag\n", status)
		}
	}

	ollama := llm.NewOllama("")
	if !ollama.IsAvailable() {
		fail(fmt.Errorf("Ollama not reachable at %s", ollama.BaseURL))
	}

	runner := &runtime.Runner{Archive: archive, Memory: mem, Ollama: ollama, AllowCloudSamples: *allowCloudSamples}
	if store, view := openTypedView(archive.Manifest.Metadata.Name); store != nil {
		defer store.Close()
		runner.Typed = view
	}
	if err := runner.Prepare(); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "Loaded agent: %s@%s (model: %s)\n",
		archive.Manifest.Metadata.Name, archive.Manifest.Metadata.Version, runner.Model())
	if n := runner.ModelNotice(); n != "" {
		fmt.Fprintf(os.Stderr, "ℹ %s\n", n)
	}
	if !fromDir {
		printVerificationReceipt(path, raw, archive)
	}

	if *input != "" {
		answer, err := runner.Ask(*input)
		if !askOK(err) {
			fail(err)
		}
		fmt.Println(answer)
		fmt.Fprintln(os.Stderr, runner.StatusLine())
		return
	}

	fmt.Println("Interactive mode. Type your questions (Ctrl-D to exit):")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		answer, err := runner.Ask(line)
		if !askOK(err) {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		fmt.Println(answer)
		fmt.Fprintln(os.Stderr, runner.StatusLine())
	}
}

// loadArchiveFromDir builds an archive from a source directory.
func loadArchiveFromDir(dir string) (*aci.Archive, error) {
	return aci.ArchiveFromDirectory(dir)
}
