package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"loomwork.dev/loomwork/internal/aci"
	"loomwork.dev/loomwork/internal/llm"
	"loomwork.dev/loomwork/internal/memory"
)

// Runner is the local ACI runner. Loads an ACI, prepares its persona + tools,
// and dispatches chat requests via Ollama.
type Runner struct {
	Archive *aci.Archive
	Memory  *Memory
	Ollama  *llm.Ollama

	// Typed, if set, supplies the user's approved typed memory (preferences,
	// beliefs, failures visible to this agent) as context for each turn.
	Typed *memory.View

	// AllowCloudSamples lets `ask --folder` send file contents to a cloud
	// model. By default only the file listing is sent to cloud models.
	AllowCloudSamples bool

	Policy *SandboxPolicy
	Tools  *ToolGate

	budget        tokenBudget
	sessionTokens int
	prepared      bool

	candidates []llm.ModelChoice // resolved lazily, in the order to try
	current    int
	resolved   bool
}

type tokenBudget struct {
	perTurn, perSession, hardLimit int
}

// LoadRunner loads an ACI from a .aci file.
func LoadRunner(aciPath string, mem *Memory, ollama *llm.Ollama) (*Runner, error) {
	data, err := os.ReadFile(aciPath)
	if err != nil {
		return nil, err
	}
	archive, err := aci.ArchiveFromTarGz(data)
	if err != nil {
		return nil, err
	}
	return &Runner{Archive: archive, Memory: mem, Ollama: ollama}, nil
}

// Prepare validates the agent and applies its declared policy: the skills
// graph must be acyclic, sandbox.json and tools/bindings.json must parse, the
// model server must be an allowed egress host, and token budgets are read
// from the persona. It is called automatically by Ask and IndexFolder.
func (r *Runner) Prepare() error {
	if r.prepared {
		return nil
	}
	if err := r.Archive.ValidateSkillsGraph(); err != nil {
		return fmt.Errorf("skills graph: %w", err)
	}
	m := r.Archive.Manifest
	sandboxData, ok := r.Archive.Files[m.Sandbox.Spec]
	if !ok {
		return fmt.Errorf("sandbox spec %s not in archive", m.Sandbox.Spec)
	}
	policy, err := ParseSandboxPolicy(sandboxData)
	if err != nil {
		return err
	}
	bindingsData, ok := r.Archive.Files[m.Tools.Bindings]
	if !ok {
		return fmt.Errorf("tool bindings %s not in archive", m.Tools.Bindings)
	}
	gate, err := NewToolGate(bindingsData, policy)
	if err != nil {
		return err
	}
	if r.Ollama != nil {
		u, err := url.Parse(r.Ollama.BaseURL)
		if err != nil {
			return fmt.Errorf("model server URL: %w", err)
		}
		if err := policy.CheckEgress(u.Hostname()); err != nil {
			return fmt.Errorf("%w (the agent's sandbox.json does not allow reaching the model server)", err)
		}
	}
	r.Policy, r.Tools = policy, gate
	if tb, ok := m.Persona.ModelPrefs["tokenBudget"].(map[string]interface{}); ok {
		num := func(k string) int {
			if f, ok := tb[k].(float64); ok {
				return int(f)
			}
			return 0
		}
		r.budget = tokenBudget{perTurn: num("perTurn"), perSession: num("perSession"), hardLimit: num("hardLimit")}
	}
	r.prepared = true
	return nil
}

// SystemPrompt returns the agent's persona system prompt.
func (r *Runner) SystemPrompt() (string, error) {
	path := r.Archive.Manifest.Persona.SystemPrompt
	data, ok := r.Archive.Files[path]
	if !ok {
		return "", fmt.Errorf("system prompt not found: %s", path)
	}
	return string(data), nil
}

// Ask dispatches a user question to the agent, with conversation history
// pulled from local memory.
func (r *Runner) Ask(userInput string) (string, error) {
	return r.AskWithContext(userInput, "")
}

// AskWithContext is Ask with extra material (for example a folder index) sent
// to the model alongside the question. Only the question is stored in memory.
func (r *Runner) AskWithContext(userInput, extra string) (string, error) {
	if err := r.Prepare(); err != nil {
		return "", err
	}
	systemPrompt, err := r.SystemPrompt()
	if err != nil {
		return "", err
	}
	agentID := r.AgentID()

	// Token budgets (PRD: persona.tokenBudget).
	if r.budget.hardLimit > 0 {
		used, err := r.Memory.SumUsage(agentID)
		if err != nil {
			return "", err
		}
		if used >= r.budget.hardLimit {
			return "", fmt.Errorf("token budget exhausted: %d of hardLimit %d tokens used", used, r.budget.hardLimit)
		}
	}
	if r.budget.perSession > 0 && r.sessionTokens >= r.budget.perSession {
		return "", fmt.Errorf("session token budget exhausted: %d of %d tokens used", r.sessionTokens, r.budget.perSession)
	}

	// Markdown skills are instructions for the model, attached to the prompt.
	// They are never executed.
	if section := r.markdownSkillsSection(); section != "" {
		systemPrompt += "\n\n" + section
	}

	// The user's approved typed memory that this agent may see.
	if section := r.typedMemorySection(); section != "" {
		systemPrompt += "\n\n" + section
	}

	// Prior turns are sent as real chat turns (oldest first), not pasted into
	// the system prompt, so stored text cannot pose as instructions.
	messages := []llm.ChatMessage{{Role: "system", Content: systemPrompt}}
	recent, err := r.Memory.QueryConversation(agentID, 10)
	if err != nil {
		return "", err
	}
	for i := len(recent) - 1; i >= 0; i-- {
		role := "user"
		if recent[i].Kind == "assistant_response" {
			role = "assistant"
		}
		messages = append(messages, llm.ChatMessage{Role: role, Content: truncate(recent[i].Content, 2000)})
	}
	prompt := userInput
	if extra != "" {
		prompt += "\n\n" + extra
	}
	messages = append(messages, llm.ChatMessage{Role: "user", Content: prompt})

	req := &llm.ChatRequest{Messages: messages, Stream: false}
	if r.budget.perTurn > 0 {
		req.Options = map[string]interface{}{"num_predict": r.budget.perTurn}
	}
	ctx := context.Background()
	if d := r.Policy.MaxWall(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	resp, err := r.chat(ctx, req)
	if err != nil {
		return "", err
	}

	tokens := resp.PromptEvalCount + resp.EvalCount
	r.sessionTokens += tokens

	// Persist the turn and its token usage. A failed write is reported, not
	// swallowed: the answer is still returned (the model call is already paid
	// for), together with a *MemoryWriteError, because a lost turn or lost token
	// count also weakens hardLimit enforcement.
	aciID := r.Archive.Manifest.Metadata.Name + "@" + r.Archive.Manifest.Metadata.Version
	var writeErr error
	for _, e := range []*MemoryEntry{
		{AgentID: agentID, ACI: aciID, Kind: "user_message", Content: userInput},
		{AgentID: agentID, ACI: aciID, Kind: "assistant_response", Content: resp.Message.Content},
		{AgentID: agentID, ACI: aciID, Kind: "token_usage", Content: fmt.Sprintf("%d", tokens)},
	} {
		if err := r.Memory.Write(e); err != nil && writeErr == nil {
			writeErr = fmt.Errorf("saving %s: %w", e.Kind, err)
		}
	}
	if writeErr != nil {
		return resp.Message.Content, &MemoryWriteError{Err: writeErr}
	}
	return resp.Message.Content, nil
}

// MemoryWriteError reports that a reply was produced but the conversation (or
// its token usage) could not be saved. Ask returns it together with the reply.
type MemoryWriteError struct{ Err error }

func (e *MemoryWriteError) Error() string {
	return "conversation not saved to memory: " + e.Err.Error()
}
func (e *MemoryWriteError) Unwrap() error { return e.Err }

// AsMemoryWriteError reports whether err is a *MemoryWriteError.
func AsMemoryWriteError(err error) bool {
	var m *MemoryWriteError
	return errors.As(err, &m)
}

// Limits on skill text attached to the prompt, so a large skill folder cannot
// crowd out the conversation.
const (
	maxMemoryLineBytes  = 500 // one approved memory line in the prompt
	maxSkillBytes       = 8 << 10
	maxSkillsTotalBytes = 32 << 10
)

// markdownSkillsSection renders the agent's skills whose implementation is a
// markdown file (impl.type "md"). The files are part of the signed archive, so
// they carry the same trust as the persona. Skills with other impl types are
// declared only; the runtime does not execute them.
func (r *Runner) markdownSkillsSection() string {
	g, err := r.Archive.ParseSkillsGraph()
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, sk := range g.Skills {
		if sk.Impl == nil || sk.Impl.Type != "md" {
			continue
		}
		body, ok := r.Archive.Files[sk.Impl.Entry]
		if !ok {
			continue
		}
		text := strings.TrimSpace(truncate(string(body), maxSkillBytes))
		if b.Len()+len(text) > maxSkillsTotalBytes {
			break
		}
		b.WriteString("### Skill: " + sk.Name + "\n" + text + "\n\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Skills\n\nUse these skills when the request calls for them.\n\n" + strings.TrimRight(b.String(), "\n")
}

// StatusLine summarizes what is running: the model, tokens used this session,
// and the sandbox the agent is confined to. It is meant for stderr after each answer.
func (r *Runner) StatusLine() string {
	sandbox := "sandbox: off"
	if r.Policy != nil {
		sandbox = fmt.Sprintf("sandbox: read %s · net %s", strings.Join(r.Policy.FSRead, ","), strings.Join(r.Policy.NetEgress, ","))
	}
	where := "local"
	if llm.IsCloudModel(r.Model()) {
		where = "CLOUD"
	}
	pending := ""
	if r.Typed != nil {
		if n, err := r.Typed.PendingCount(); err != nil {
			pending = " · pending count unavailable: " + err.Error()
		} else if n > 0 {
			pending = fmt.Sprintf(" · %d pending (loomwork memory list --status pending)", n)
		}
	}
	return fmt.Sprintf("[%s (%s) · %d tokens this session · %s%s]", r.Model(), where, r.sessionTokens, sandbox, pending)
}

// typedMemorySection renders active preferences, beliefs and failure notes
// visible to this agent. Consent, retention, sensitivity and revocation are
// applied by the view.
func (r *Runner) typedMemorySection() string {
	if r.Typed == nil {
		return ""
	}
	var b strings.Builder
	add := func(kind memory.Kind, render func(*memory.Record) string) {
		recs, err := r.Typed.List(kind, 20)
		if err != nil {
			return
		}
		for _, rec := range recs {
			if rec.Status != memory.StatusActive {
				continue
			}
			if line := render(rec); line != "" {
				b.WriteString("- " + truncate(line, maxMemoryLineBytes) + "\n")
			}
		}
	}
	add(memory.KindPreference, func(x *memory.Record) string {
		if x.Preference == nil {
			return ""
		}
		return "Preference: " + x.Preference.Key + " = " + x.Preference.Value
	})
	add(memory.KindBelief, func(x *memory.Record) string {
		if x.Belief == nil {
			return ""
		}
		return "Known: " + x.Belief.Claim
	})
	add(memory.KindFailure, func(x *memory.Record) string {
		if x.Failure == nil {
			return ""
		}
		return "Avoid: " + x.Failure.Pattern + " (" + x.Failure.Avoidance + ")"
	})
	if b.Len() == 0 {
		return ""
	}
	return "## What you know about the user (approved memory)\n" + b.String()
}

// chat sends req, moving to the next candidate model when the current one is
// unavailable (retired or not found). Skips are reported on stderr.
func (r *Runner) chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	r.resolve()
	for {
		req.Model = r.Model()
		resp, err := r.Ollama.ChatContext(ctx, req)
		var unavailable *llm.ModelUnavailableError
		if err == nil || !errors.As(err, &unavailable) || r.current+1 >= len(r.candidates) {
			return resp, err
		}
		r.current++
		fmt.Fprintf(os.Stderr, "ℹ model %q is unavailable (HTTP %d); trying %q\n",
			unavailable.Model, unavailable.Status, r.candidates[r.current].Name)
	}
}

// Model returns the model currently in use: the manifest's first installed
// preference, else any installed local model, else an installed cloud model.
// Ask moves to the next candidate if the model turns out to be retired or
// missing. Call ModelNotice afterwards for a message explaining a fallback.
func (r *Runner) Model() string {
	r.resolve()
	if len(r.candidates) == 0 {
		return r.Ollama.DefaultModel()
	}
	return r.candidates[r.current].Name
}

// ModelNotice returns a non-empty message when the model in use differs from
// the ACI's first preference.
func (r *Runner) ModelNotice() string {
	r.resolve()
	if len(r.candidates) == 0 {
		return ""
	}
	return r.candidates[r.current].Notice
}

func (r *Runner) resolve() {
	if r.resolved {
		return
	}
	r.resolved = true
	var preferred []string
	if r.Archive.Manifest.Persona.ModelPrefs != nil {
		if list, ok := r.Archive.Manifest.Persona.ModelPrefs["preferred"].([]interface{}); ok {
			for _, v := range list {
				if s, ok := v.(string); ok {
					preferred = append(preferred, s)
				}
			}
		}
	}
	// On error (nothing installed) candidates stays empty and the chat call
	// reports the failure.
	r.candidates, _ = r.Ollama.ResolveModels(preferred)
}

// AgentID returns a stable identifier for this agent (used as memory key).
func (r *Runner) AgentID() string {
	return r.Archive.Manifest.Metadata.Name
}

// IndexFolder lists a folder and samples a few small text files, then stores
// the index in memory (one entry per agent+folder, replaced on each call).
// Access goes through the agent's tool bindings and sandbox policy. Hidden
// files and files that look like secrets are never read. If the model in use
// is a cloud model, only the listing is produced unless AllowCloudSamples is set.
func (r *Runner) IndexFolder(folder string) (string, error) {
	if err := r.Prepare(); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(folder)
	if err != nil {
		return "", err
	}
	if err := r.Tools.Check("filesystem", "list_dir", abs); err != nil {
		return "", fmt.Errorf("%w\n  (the agent's tools/bindings.json and sandbox.json control which folders it may read)", err)
	}

	const maxFiles = 200
	const maxEntries = 20000 // files + directories visited, so empty directories cannot stall the walk
	entries, withheld := 0, 0
	var listing strings.Builder
	listing.WriteString(fmt.Sprintf("Contents of %s:\n\n", folder))
	count := 0
	var samples []string
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if path != abs && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entries++; entries > maxEntries {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(abs, path)
		if d.IsDir() {
			if path != abs {
				listing.WriteString(fmt.Sprintf("📁 %s/\n", rel))
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		count++
		if secretLookingName(name) {
			// The listing goes to the model (a cloud model, possibly): a name like
			// passwords_backup.txt is itself sensitive, so it is counted, not shown.
			withheld++
		} else {
			listing.WriteString(fmt.Sprintf("  📄 %s (%d bytes)\n", rel, info.Size()))
		}
		if len(samples) < 5 && sampleable(name, info.Size()) &&
			r.Tools.Check("filesystem", "read_file", path) == nil {
			samples = append(samples, path)
		}
		if count >= maxFiles {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	listing.WriteString(fmt.Sprintf("\n(%d files listed)\n", count))
	if withheld > 0 {
		listing.WriteString(fmt.Sprintf("(%d files with secret-looking names not shown)\n", withheld))
	}
	if entries > maxEntries {
		listing.WriteString("(folder too large; listing stopped early)\n")
	}

	if r.AllowCloudSamples || !llm.IsCloudModel(r.Model()) {
		var out strings.Builder
		for _, p := range samples {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			rel, _ := filepath.Rel(abs, p)
			out.WriteString(fmt.Sprintf("--- %s ---\n%s\n\n", rel, data))
		}
		if out.Len() > 0 {
			listing.WriteString("\nSample content:\n\n" + out.String())
		}
	} else if len(samples) > 0 {
		listing.WriteString("\n(file contents not sent: a cloud model is in use)\n")
	}

	sum := sha256.Sum256([]byte(r.AgentID() + "\x00" + abs))
	_ = r.Memory.Write(&MemoryEntry{
		ID:      "folder_index_" + hex.EncodeToString(sum[:8]),
		AgentID: r.AgentID(),
		ACI:     r.Archive.Manifest.Metadata.Name + "@" + r.Archive.Manifest.Metadata.Version,
		Kind:    "folder_index",
		Content: listing.String(),
	})
	return listing.String(), nil
}

// sampleable reports whether a file is a small text file that does not look
// like it holds credentials.
func sampleable(name string, size int64) bool {
	if size > 10*1024 {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".txt", ".py", ".go", ".json":
	default:
		return false
	}
	return !secretLookingName(name)
}

// secretLookingName reports whether a file name suggests credentials.
func secretLookingName(name string) bool {
	lower := strings.ToLower(name)
	for _, bad := range []string{"secret", "credential", "password", "passwd", "token", "apikey", "api_key", "private", "id_rsa", "key"} {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return TruncateBytes(s, n) + "..."
}

// TruncateBytes returns at most n bytes of s, cut on a rune boundary so the
// result is always valid UTF-8 (a plain s[:n] can split a multi-byte rune).
func TruncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// SaveManifestForInit writes a minimal manifest + supporting files to dir.
// Used by `loomwork init`.
func SaveManifestForInit(dir, name string) error {
	return SaveManifest(dir, name, ScaffoldOptions{})
}

// ScaffoldOptions lets a plain-folder agent supply its own files. The zero
// value produces the default `loomwork init` agent.
type ScaffoldOptions struct {
	PersonaPath string         // existing persona file to use as the system prompt (e.g. "AGENT.md"); "" writes the default
	Skills      []aci.SkillDef // skills built from the folder; nil writes the two default skills
}

// SaveManifest scaffolds an agent in dir. Files the user already has (persona,
// skills) are referenced and digested, never rewritten.
func SaveManifest(dir, name string, opts ScaffoldOptions) error {
	manifest := &aci.Manifest{
		APIVersion: aci.APIVersion,
		Kind:       aci.Kind,
		Metadata: aci.Metadata{
			Name:         name,
			Version:      "v0.1.0",
			Architecture: "amd64",
			OS:           "any",
			Description:  "A minimal personal agent scaffolded by loomwork init.",
			License:      "Apache-2.0",
		},
		Persona: aci.PersonaRef{
			SystemPrompt: "persona/system_prompt.md",
			ModelPrefs: map[string]interface{}{
				"minContextWindow": float64(32768),
				"preferred":        []interface{}{"llama3.2", "qwen2.5"},
				"temperature":      0.3,
				"tokenBudget": map[string]interface{}{
					"perTurn":    float64(4096),
					"perSession": float64(131072),
					"hardLimit":  float64(1048576),
				},
			},
			Capabilities: map[string]bool{
				"supportsStreaming":     true,
				"supportsTools":         true,
				"supportsVision":        false,
				"supportsCodeExecution": false,
			},
		},
		Skills:  aci.SkillsRef{Graph: "skills/graph.json"},
		Tools:   aci.ToolsRef{Bindings: "tools/bindings.json"},
		Memory:  aci.MemoryRef{Schema: "memory-schema.json"},
		Sandbox: aci.SandboxRef{Spec: "sandbox.json"},
		Digests: map[string]string{},
	}

	// Write system prompt
	if err := os.MkdirAll(filepath.Join(dir, "persona"), 0o755); err != nil {
		return err
	}
	systemPrompt := `# Personal Agent

You are a helpful personal agent. Your job is to answer questions about files
and folders on this machine, propose actions, and remember what was discussed.

## Operating principles

1. **Be concrete.** Cite specific file paths and line numbers when answering
   questions about the local filesystem.
2. **Propose, don't surprise.** When suggesting an action, describe it
   precisely and let the user confirm before doing anything destructive.
3. **Remember.** Everything the user tells you goes into local memory and
   will be available next time.
4. **Stay local.** You run on the user's machine via Ollama. No cloud.
`
	if opts.PersonaPath != "" {
		manifest.Persona.SystemPrompt = opts.PersonaPath
	} else if err := os.WriteFile(filepath.Join(dir, "persona/system_prompt.md"),
		[]byte(systemPrompt), 0o644); err != nil {
		return err
	}

	// Write skills graph
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		return err
	}
	skillsJSON := `{
  "skills": [
    {
      "name": "answer_question",
      "description": "Answer a question about the local filesystem or conversation history.",
      "inputs":  { "question": "string" },
      "outputs": { "answer": "string" },
      "requires": [],
      "impl": null
    },
    {
      "name": "propose_action",
      "description": "Propose an action (file edit, command, search) for the user to confirm.",
      "inputs":  { "intent": "string", "context": "string" },
      "outputs": { "proposal": "string", "risk": "low|medium|high" },
      "requires": ["answer_question"],
      "impl": null
    }
  ]
}
`
	if opts.Skills != nil {
		b, err := json.MarshalIndent(aci.SkillsGraph{Skills: opts.Skills}, "", "  ")
		if err != nil {
			return err
		}
		skillsJSON = string(b) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "skills/graph.json"),
		[]byte(skillsJSON), 0o644); err != nil {
		return err
	}

	// Write tool bindings
	if err := os.MkdirAll(filepath.Join(dir, "tools"), 0o755); err != nil {
		return err
	}
	toolsJSON := `{
  "bindings": [
    {
      "name": "filesystem",
      "mcpServer": "stdio:///usr/local/bin/mcp-fs",
      "allowedTools": ["read_file", "write_file", "list_dir"],
      "scope": { "paths": ["$HOME/**"] }
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(dir, "tools/bindings.json"),
		[]byte(toolsJSON), 0o644); err != nil {
		return err
	}

	// Write memory schema
	memorySchema := `{
  "$schema": "https://loomwork.dev/schemas/memory/v0.1.json",
  "stores": {
    "episodic": { "type": "timeline", "retention": "90d" },
    "semantic": { "type": "graph" },
    "procedural": { "type": "vector", "dimensions": 384 }
  },
  "encryption": { "algorithm": "AES-256-GCM", "keyDerivation": "PBKDF2-SHA256-200k" },
  "provenance": { "required": true, "signatureAlgorithm": "Ed25519" }
}
`
	if err := os.WriteFile(filepath.Join(dir, "memory-schema.json"),
		[]byte(memorySchema), 0o644); err != nil {
		return err
	}

	// Write sandbox
	sandboxJSON := `{
  "fs.read": ["$HOME/**"],
  "fs.write": ["$HOME/.loomwork/workspace/**"],
  "net.egress": ["localhost", "127.0.0.1"],
  "net.listen": [],
  "resources.cpu": "1",
  "resources.memory": "1GiB",
  "time.maxWall": "10m"
}
`
	if err := os.WriteFile(filepath.Join(dir, "sandbox.json"),
		[]byte(sandboxJSON), 0o644); err != nil {
		return err
	}

	// Compute digests and write manifest
	manifest.Digests = map[string]string{}
	for _, p := range []string{
		manifest.Persona.SystemPrompt,
		manifest.Skills.Graph,
		manifest.Tools.Bindings,
		manifest.Memory.Schema,
		manifest.Sandbox.Spec,
	} {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			return err
		}
		manifest.Digests[p] = aci.Sha256Bytes(data)
	}
	for _, sk := range opts.Skills {
		if sk.Impl == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(sk.Impl.Entry)))
		if err != nil {
			return err
		}
		manifest.Digests[sk.Impl.Entry] = aci.Sha256Bytes(data)
	}
	manifestJSON, err := manifest.ToJSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestJSON, 0o644); err != nil {
		return err
	}

	return nil
}

// MarshalJSON helper for SandboxPolicy (used in conformance checks)
func (p *SandboxPolicy) MarshalJSON() ([]byte, error) {
	type alias SandboxPolicy
	return json.Marshal((*alias)(p))
}
