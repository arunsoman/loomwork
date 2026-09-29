package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"loomwork.dev/loomwork/internal/llm"
	"loomwork.dev/loomwork/internal/runtime"
)

// cmdAsk loads the agent in the current directory, optionally indexes a
// folder, and answers a question via the local LLM (Ollama by default).
//
// Usage:
//
//	loomwork ask "what is in this folder?" [--folder path] [--agent-dir .]
//
// Folder access goes through the agent's tool bindings and sandbox policy.
func cmdAsk(args []string) {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	folder := fs.String("folder", "", "folder to index before answering")
	agentDir := fs.String("agent-dir", ".", "directory containing the agent manifest")
	verbose := fs.Bool("verbose", false, "print index + memory debug info")
	allowCloudSamples := fs.Bool("allow-cloud-samples", false, "let cloud models see file contents (default: listing only)")
	question := strings.Join(parseArgs(fs, args), " ")
	if question == "" {
		fail(fmt.Errorf("usage: loomwork ask \"question\" [--folder path]"))
	}

	mem, err := runtime.OpenDefaultMemory(homeDir())
	if err != nil {
		fail(fmt.Errorf("open memory: %w", err))
	}
	defer mem.Close()

	archive, err := loadArchiveFromDir(*agentDir)
	if err != nil {
		fail(fmt.Errorf("load agent from %s: %w (did you run `loomwork init` first? if you edited files, run `loomwork package` to refresh digests)", *agentDir, err))
	}

	ollama := llm.NewOllama("")
	if !ollama.IsAvailable() {
		fail(fmt.Errorf("Ollama not reachable at %s\nInstall: curl -fsSL https://ollama.com/install.sh | sh\nThen: ollama pull llama3.2",
			ollama.BaseURL))
	}

	runner := &runtime.Runner{Archive: archive, Memory: mem, Ollama: ollama, AllowCloudSamples: *allowCloudSamples}
	if store, view := openTypedView(archive.Manifest.Metadata.Name); store != nil {
		defer store.Close()
		runner.Typed = view
	}
	if err := runner.Prepare(); err != nil {
		fail(err)
	}
	if n := runner.ModelNotice(); n != "" {
		fmt.Fprintf(os.Stderr, "ℹ %s\n", n)
	}

	var extra string
	if *folder != "" {
		if *verbose {
			fmt.Fprintf(os.Stderr, "Indexing %s...\n", *folder)
		}
		listing, err := runner.IndexFolder(*folder)
		if err != nil {
			fail(fmt.Errorf("index folder: %w", err))
		}
		if *verbose {
			fmt.Fprintln(os.Stderr, listing)
		}
		extra = fmt.Sprintf("[Folder index for %s:\n%s]", *folder, truncate(listing, 4000))
	}

	answer, err := runner.AskWithContext(question, extra)
	if err != nil {
		fail(err)
	}
	fmt.Println(answer)

	if *verbose {
		fmt.Fprintln(os.Stderr, "\n--- Recent memory ---")
		recent, _ := mem.QueryConversation(runner.AgentID(), 5)
		for _, e := range recent {
			b, _ := json.MarshalIndent(e, "  ", "  ")
			fmt.Fprintf(os.Stderr, "  %s\n", b)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (truncated)"
}
