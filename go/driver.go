package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"loomwork.dev/loomwork/internal/agents"
	"loomwork.dev/loomwork/internal/llm"
	"loomwork.dev/loomwork/internal/runtime"
)

// configureBackend picks the chat backend for r. Ollama is used when it is
// reachable. Otherwise (or when driver names an agent CLI) the first healthy
// agent CLI in the chain is used under that tool's own login; Loom handles no
// API keys. Because those CLIs send prompts to a cloud provider, an automatic
// fallback needs --allow-cloud; naming --driver is explicit consent. Typed
// memory is not attached to a cloud driver.
func configureBackend(r *runtime.Runner, ollama *llm.Ollama, driver string, allowCloud bool) error {
	cfg, err := agents.LoadConfig(loomHome())
	if err != nil {
		return err
	}
	d, name, err := agents.Pick(context.Background(), cfg, ollama.IsAvailable(), driver)
	if err != nil {
		return fmt.Errorf("%w\nStart Ollama (ollama serve) or install/log in to an agent CLI", err)
	}
	if d == nil {
		return nil
	}
	if driver == "" && !allowCloud {
		return fmt.Errorf("Ollama is not reachable at %s, but the %s CLI is available as a fallback.\n"+
			"It sends prompts to its provider under your own login. Re-run with --allow-cloud to use it, or --driver %s", ollama.BaseURL, name, name)
	}
	r.Driver = d
	if r.Typed != nil {
		r.Typed = nil
		fmt.Fprintln(os.Stderr, "ℹ typed memory is not sent to a cloud driver")
	}
	return nil
}

// loomHome is Loom's data directory, ~/.loomwork.
func loomHome() string { return filepath.Join(homeDir(), ".loomwork") }
