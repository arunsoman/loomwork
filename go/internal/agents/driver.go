package agents

import (
	"context"
	"fmt"
	"strings"

	"loomwork.dev/loomwork/internal/llm"
)

// CLIDriver adapts an agent CLI to llm.Driver so the runner can use it when
// Ollama is unavailable.
type CLIDriver struct {
	Adapter Adapter
	Dir     string
}

func (d *CLIDriver) Name() string { return d.Adapter.Spec.Name }

// Cloud is always true: the CLIs send prompts to their provider.
func (d *CLIDriver) Cloud() bool { return true }

// ChatContext flattens the conversation into one prompt and runs the agent.
func (d *CLIDriver) ChatContext(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	res, err := d.Adapter.Run(ctx, Job{Prompt: flatten(req.Messages), Dir: d.Dir})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return nil, fmt.Errorf("agent %s exited %d: %s", d.Name(), res.ExitCode, msg)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, fmt.Errorf("agent %s returned no output", d.Name())
	}
	return &llm.ChatResponse{
		Model:   d.Name(),
		Message: llm.ChatMessage{Role: "assistant", Content: out},
		Done:    true,
	}, nil
}

// flatten renders messages as a transcript. CLIs take one prompt, so system
// and prior turns are labelled and the final user turn comes last.
func flatten(msgs []llm.ChatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "system":
			b.WriteString("[Instructions]\n")
		case "assistant":
			b.WriteString("[Assistant]\n")
		default:
			b.WriteString("[User]\n")
		}
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("Reply as the assistant to the last [User] message only. Answer in text; do not use tools or edit files.")
	return b.String()
}

// Pick returns the first available driver in the chain. ollamaUp says whether
// Ollama is reachable; if "ollama" comes first and is up, it returns nil, ""
// meaning use Ollama. A non-nil driver means fall back to that CLI.
func Pick(ctx context.Context, cfg *Config, ollamaUp bool, force string) (llm.Driver, string, error) {
	chain := cfg.Chain
	if force != "" {
		chain = []string{force}
	}
	for _, name := range chain {
		if name == "ollama" {
			if ollamaUp {
				return nil, "ollama", nil
			}
			continue
		}
		spec, ok := cfg.Spec(name)
		if !ok {
			if force != "" {
				return nil, "", fmt.Errorf("unknown agent %q (not in agents.json or defaults)", name)
			}
			continue
		}
		a := Adapter{Spec: spec}
		if a.Healthy(ctx) {
			return &CLIDriver{Adapter: a}, name, nil
		}
		if force != "" {
			return nil, "", fmt.Errorf("agent %q is not installed or failed its health check", name)
		}
	}
	return nil, "", fmt.Errorf("no driver available: Ollama is not running and no agent CLI (%s) passed its health check", strings.Join(chain, ", "))
}
