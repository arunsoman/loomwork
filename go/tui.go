package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattn/go-isatty"

	"loomwork.dev/loomwork/internal/agents"
	"loomwork.dev/loomwork/internal/flow"
	"loomwork.dev/loomwork/internal/llm"
	"loomwork.dev/loomwork/internal/runtime"
	"loomwork.dev/loomwork/internal/tui"
)

// cmdTUI opens the interactive UI: a prompt, a workflow editor, a context
// manager and a live status board.
//
//	loomwork tui [--folder dir] [--allow-cloud]
func cmdTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	folder := fs.String("folder", ".", "project folder (a git repository for flows)")
	allowCloud := fs.Bool("allow-cloud", false, "let Ask mode fall back to an agent CLI when Ollama is unavailable")
	parseArgs(fs, args)
	if !isatty.IsTerminal(os.Stdout.Fd()) || !isatty.IsTerminal(os.Stdin.Fd()) {
		fail(fmt.Errorf("the TUI needs a terminal; use `loomwork flow run|status|context` instead"))
	}
	abs, err := filepath.Abs(*folder)
	if err != nil {
		fail(err)
	}
	cfg, err := agents.LoadConfig(loomHome())
	if err != nil {
		fail(err)
	}
	var names []string
	for _, a := range cfg.Agents {
		names = append(names, a.Name)
	}

	d := tui.Deps{Folder: abs, Agents: names}
	d.NewEngine = func(w *flow.Workflow) (*flow.Engine, error) {
		e, err := flow.NewEngine(abs, w, cfg)
		if err != nil {
			return nil, err
		}
		if err := w.Validate(knownAgents(cfg)); err != nil {
			return nil, err
		}
		e.Context, err = flow.LoadContext(abs)
		return e, err
	}

	closeAsk := func() {}
	d.Ask, d.Backend, d.AskErr, closeAsk = newAsker(abs, *allowCloud)
	defer closeAsk()

	if err := tui.Run(d); err != nil {
		fail(err)
	}
}

// newAsker prepares one-shot Ask for the TUI without calling fail(), which
// would exit mid-session. On failure the returned func is nil and errMsg says
// why; the other tabs still work.
func newAsker(dir string, allowCloud bool) (ask func(context.Context, string) (string, error), backend, errMsg string, closeFn func()) {
	closeFn = func() {}
	mem, err := runtime.OpenDefaultMemory(homeDir())
	if err != nil {
		return nil, "no backend", "open memory: " + err.Error(), closeFn
	}
	closeFn = func() { mem.Close() }
	archive, err := loadArchiveFromDir(dir)
	if err != nil {
		return nil, "no agent here", "no agent in this folder (run `loomwork init`): " + err.Error(), closeFn
	}
	ollama := llm.NewOllama("")
	runner := &runtime.Runner{Archive: archive, Memory: mem, Ollama: ollama}
	if store, view := openTypedView(archive.Manifest.Metadata.Name); store != nil {
		prev := closeFn
		closeFn = func() { store.Close(); prev() }
		runner.Typed = view
	}
	if err := configureBackend(runner, ollama, "", allowCloud); err != nil {
		return nil, "no backend", err.Error(), closeFn
	}
	if err := runner.Prepare(); err != nil {
		return nil, "no backend", err.Error(), closeFn
	}
	backend = runner.Model() + " (local)"
	if runner.Driver != nil {
		backend = runner.Driver.Name() + " CLI (CLOUD)"
	}
	return func(ctx context.Context, q string) (string, error) {
		type res struct {
			s   string
			err error
		}
		ch := make(chan res, 1)
		go func() {
			s, err := runner.Ask(q)
			ch <- res{s, err}
		}()
		select {
		case r := <-ch:
			return r.s, r.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}, backend, "", closeFn
}
