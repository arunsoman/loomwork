package llm

import "context"

// Driver is a chat backend other than a local Ollama server, such as an agent
// CLI (claude, codex, pi, hermes) running under the user's own login. Loom
// never handles credentials for a Driver: it spawns the tool and reads stdout.
type Driver interface {
	// Name is the driver's short name, e.g. "claude".
	Name() string
	// Cloud reports whether prompts leave this machine. Cloud drivers are
	// subject to the same consent and memory gating as cloud Ollama models.
	Cloud() bool
	// ChatContext sends req and returns the assistant's reply.
	ChatContext(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
}
