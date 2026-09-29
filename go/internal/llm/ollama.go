// Package llm wraps Ollama as the default local LLM provider.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Ollama is an HTTP client to a local (or remote) Ollama server.
type Ollama struct {
	BaseURL    string
	HTTPClient *http.Client
}

// DefaultOllamaURL returns the OLLAMA_URL env var or falls back to
// localhost:11434 (Ollama's default).
func DefaultOllamaURL() string {
	if u := os.Getenv("OLLAMA_URL"); u != "" {
		return u
	}
	return "http://localhost:11434"
}

// NewOllama constructs a client.
func NewOllama(baseURL string) *Ollama {
	if baseURL == "" {
		baseURL = DefaultOllamaURL()
	}
	return &Ollama{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

// IsAvailable returns true if the Ollama server is reachable.
func (o *Ollama) IsAvailable() bool {
	resp, err := o.HTTPClient.Get(o.BaseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// ChatRequest is the Ollama /api/chat request body.
type ChatRequest struct {
	Model    string                 `json:"model"`
	Messages []ChatMessage          `json:"messages"`
	Stream   bool                   `json:"stream"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// ChatMessage is a single message in the chat history.
type ChatMessage struct {
	Role    string `json:"role"` // system, user, assistant
	Content string `json:"content"`
}

// ChatResponse is the Ollama /api/chat response body.
type ChatResponse struct {
	Model           string      `json:"model"`
	Message         ChatMessage `json:"message"`
	Done            bool        `json:"done"`
	TotalDuration   int64       `json:"total_duration"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
}

// Chat sends a chat completion request.
func (o *Ollama) Chat(req *ChatRequest) (*ChatResponse, error) {
	return o.ChatContext(context.Background(), req)
}

// ChatContext is Chat with a context for cancellation and deadlines.
func (o *Ollama) ChatContext(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	endpoint, err := url.JoinPath(o.BaseURL, "/api/chat")
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama chat: %w (is Ollama running at %s?)", err, o.BaseURL)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return nil, &ModelUnavailableError{Model: req.Model, Status: resp.StatusCode, Body: string(b)}
		}
		return nil, fmt.Errorf("ollama returned %d: %s", resp.StatusCode, string(b))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out ChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListModels returns the models installed on the Ollama server.
func (o *Ollama) ListModels() ([]string, error) {
	resp, err := o.HTTPClient.Get(o.BaseURL + "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ollama /api/tags returned %d", resp.StatusCode)
	}
	var data struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	out := make([]string, len(data.Models))
	for i, m := range data.Models {
		out[i] = m.Name
	}
	return out, nil
}

// DefaultModel returns a sensible default model, preferring smaller local models.
func (o *Ollama) DefaultModel() string {
	if m := os.Getenv("OLLAMA_MODEL"); m != "" {
		return m
	}
	models, err := o.ListModels()
	if err == nil {
		// Prefer llama3.2 if available
		for _, m := range models {
			if m == "llama3.2" || m == "llama3.2:3b" {
				return m
			}
		}
		// Fall back to any installed model
		if len(models) > 0 {
			return models[0]
		}
	}
	// Last-resort default
	return "llama3.2"
}

// IsCloudModel reports whether an Ollama model name is a cloud-hosted model
// (e.g. "glm-5.1:cloud", "gpt-oss:120b-cloud"); prompts to it leave the machine.
func IsCloudModel(name string) bool {
	return strings.HasSuffix(name, ":cloud") || strings.HasSuffix(name, "-cloud")
}

// modelMatches reports whether an installed model satisfies a wanted name.
// "llama3.2" matches "llama3.2", "llama3.2:latest" and "llama3.2:3b".
func modelMatches(installed, want string) bool {
	if installed == want {
		return true
	}
	return !strings.Contains(want, ":") && strings.HasPrefix(installed, want+":")
}

// ModelUnavailableError means Ollama lists or was asked for a model it cannot
// serve (404 not found, 410 retired). Callers can fall back to another model.
type ModelUnavailableError struct {
	Model  string
	Status int
	Body   string
}

func (e *ModelUnavailableError) Error() string {
	return fmt.Sprintf("ollama returned %d: %s", e.Status, e.Body)
}

// ModelChoice is one usable model plus a message explaining why it was chosen
// (empty when it is the ACI's first preference).
type ModelChoice struct {
	Name   string
	Notice string
}

// ResolveModels returns installed models in the order they should be tried:
// OLLAMA_MODEL alone if set; otherwise the ACI's preferred models that are
// installed, then other local models, then cloud models. Ollama can keep
// listing retired cloud models, so callers should try the next choice when a
// chat call fails with ModelUnavailableError. An error means nothing is
// installed.
func (o *Ollama) ResolveModels(preferred []string) ([]ModelChoice, error) {
	if m := os.Getenv("OLLAMA_MODEL"); m != "" {
		return []ModelChoice{{Name: m}}, nil
	}
	installed, err := o.ListModels()
	if err != nil {
		return nil, err
	}
	first := "llama3.2"
	if len(preferred) > 0 {
		first = preferred[0]
	}
	var out []ModelChoice
	seen := map[string]bool{}
	add := func(name, notice string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, ModelChoice{name, notice})
		}
	}
	for i, want := range preferred {
		for _, have := range installed {
			if modelMatches(have, want) {
				notice := ""
				if i > 0 {
					notice = fmt.Sprintf("preferred model %q is not installed; using %q instead", first, have)
				}
				add(have, notice)
			}
		}
	}
	for _, have := range installed {
		if !IsCloudModel(have) {
			add(have, fmt.Sprintf("preferred model %q is not installed; using installed local model %q", first, have))
		}
	}
	for _, have := range installed {
		if IsCloudModel(have) {
			add(have, fmt.Sprintf("preferred model %q is not installed; no local model found, but cloud model %q is available, so using it (note: prompts are sent to Ollama's cloud)", first, have))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no models installed on Ollama at %s (try: ollama pull %s)", o.BaseURL, first)
	}
	return out, nil
}

// ResolveModel returns the first choice from ResolveModels.
func (o *Ollama) ResolveModel(preferred []string) (model, notice string, err error) {
	c, err := o.ResolveModels(preferred)
	if err != nil {
		return "", "", err
	}
	return c[0].Name, c[0].Notice, nil
}
