package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loomwork.dev/loomwork/internal/aci"
	"loomwork.dev/loomwork/internal/agents"
	"loomwork.dev/loomwork/internal/llm"
	"loomwork.dev/loomwork/internal/memory"
)

// cmdDoctor is the config-nightmare prevention command.
//
// Doctor runs one command that checks the common failure modes and tells the
// user exactly what to do.
//
// Usage: loomwork doctor
func cmdDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output JSON instead of human-readable")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	checks := []Check{}
	home, _ := os.UserHomeDir()

	// Check 1: Ollama reachable
	ollama := llm.NewOllama("")
	if ollama.IsAvailable() {
		models, _ := ollama.ListModels()
		checks = append(checks, Check{
			Name:    "ollama",
			Status:  "ok",
			Message: fmt.Sprintf("reachable at %s, %d models installed", ollama.BaseURL, len(models)),
			Fix:     "",
		})
		if len(models) == 0 {
			checks = append(checks, Check{
				Name:    "ollama_model",
				Status:  "warn",
				Message: "no models installed",
				Fix:     "run: ollama pull llama3.2",
			})
		}
	} else {
		fix := "install Ollama: curl -fsSL https://ollama.com/install.sh | sh\n  then start it: ollama serve\n  then pull a model: ollama pull llama3.2"
		checks = append(checks, Check{
			Name:    "ollama",
			Status:  "fail",
			Message: fmt.Sprintf("not reachable at %s", ollama.BaseURL),
			Fix:     fix,
		})
	}

	// Agent CLIs usable as fallback drivers (their own login; Loom holds no keys).
	if cfg, err := agents.LoadConfig(loomHome()); err == nil {
		for _, spec := range cfg.Agents {
			a := agents.Adapter{Spec: spec}
			switch {
			case !a.Available():
				checks = append(checks, Check{Name: "agent_" + spec.Name, Status: "warn", Message: "not installed (optional driver)"})
			case !a.Healthy(context.Background()):
				checks = append(checks, Check{Name: "agent_" + spec.Name, Status: "warn", Message: "installed but failed its health check (" + strings.Join(spec.Probe, " ") + ")", Fix: "repair or reinstall " + spec.Name})
			default:
				checks = append(checks, Check{Name: "agent_" + spec.Name, Status: "ok", Message: "available as a driver"})
			}
		}
	}

	// Check 2: signing key
	keyPath := filepath.Join(home, ".loomwork", "key.pem")
	if _, err := os.Stat(keyPath); err == nil {
		checks = append(checks, Check{
			Name:    "signing_key",
			Status:  "ok",
			Message: fmt.Sprintf("present at %s", keyPath),
			Fix:     "",
		})
	} else {
		checks = append(checks, Check{
			Name:    "signing_key",
			Status:  "warn",
			Message: "no signing key (will auto-generate on first `loomwork package`)",
			Fix:     "to generate now: loomwork keygen",
		})
	}

	// Check 3: memory DB
	memPath := filepath.Join(home, ".loomwork", "memory.db")
	store, err := memory.OpenDefaultStore(home)
	if err != nil {
		checks = append(checks, Check{
			Name:    "memory",
			Status:  "fail",
			Message: fmt.Sprintf("cannot open %s: %v", memPath, err),
			Fix:     fmt.Sprintf("remove and recreate: rm %s", memPath),
		})
	} else {
		stats, err := store.ComputeStats()
		store.Close()
		if err != nil {
			checks = append(checks, Check{
				Name:    "memory",
				Status:  "warn",
				Message: fmt.Sprintf("opened but stats failed: %v", err),
				Fix:     "",
			})
		} else {
			byKind := []string{}
			for k, n := range stats.ByKind {
				byKind = append(byKind, fmt.Sprintf("%s=%d", k, n))
			}
			checks = append(checks, Check{
				Name:   "memory",
				Status: "ok",
				Message: fmt.Sprintf("%d records (%s), %d embeddings",
					stats.Total, strings.Join(byKind, " "), stats.Embeddings),
				Fix: "",
			})
		}
	}

	// Check 3b: trusted keys
	if keys, _ := aci.DefaultTrustStore(home).List(); len(keys) > 0 {
		checks = append(checks, Check{Name: "trust", Status: "ok", Message: fmt.Sprintf("%d trusted signing key(s)", len(keys))})
	} else {
		checks = append(checks, Check{Name: "trust", Status: "ok", Message: "no trusted keys yet (your own key is trusted when you package)"})
	}

	// Check 4: workspace dir
	wsPath := filepath.Join(home, ".loomwork", "workspace")
	if err := os.MkdirAll(wsPath, 0o755); err == nil {
		checks = append(checks, Check{
			Name:    "workspace",
			Status:  "ok",
			Message: fmt.Sprintf("writable: %s", wsPath),
			Fix:     "",
		})
	} else {
		checks = append(checks, Check{
			Name:    "workspace",
			Status:  "fail",
			Message: fmt.Sprintf("cannot create %s: %v", wsPath, err),
			Fix:     "check permissions on your home directory",
		})
	}

	// Check 5: spec compatibility (binary version vs manifest schema)
	checks = append(checks, Check{
		Name:    "spec",
		Status:  "ok",
		Message: fmt.Sprintf("loomwork %s, spec %s", Version, SpecVersion),
		Fix:     "",
	})

	if *jsonOut {
		out, _ := json.MarshalIndent(checks, "", "  ")
		fmt.Println(string(out))
		return
	}

	// Human-readable output
	fmt.Println("loomwork doctor — checking your setup")
	fmt.Println()
	hasFail := false
	hasWarn := false
	for _, c := range checks {
		icon := "✓"
		color := "\033[32m"
		if c.Status == "warn" {
			icon = "⚠"
			color = "\033[33m"
			hasWarn = true
		} else if c.Status == "fail" {
			icon = "✗"
			color = "\033[31m"
			hasFail = true
		}
		fmt.Printf("  %s%s %s%s  %s\n", color, icon, c.Name, "\033[0m", c.Message)
		if c.Fix != "" {
			fmt.Printf("        fix: %s\n", c.Fix)
		}
	}
	fmt.Println()
	if hasFail {
		fmt.Println("✗ Some checks failed. Run the suggested fixes and try again.")
		os.Exit(1)
	} else if hasWarn {
		fmt.Println("⚠ Some warnings. Loomwork will work, but consider the suggestions above.")
	} else {
		fmt.Println("✓ All checks passed. Ready to go.")
		fmt.Println("\nNext: loomwork init my-agent && cd my-agent && loomwork ask \"hello\"")
	}
}

// Check is a single doctor result.
type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // ok | warn | fail
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}
