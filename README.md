**Here’s a rewritten README optimized for zero-friction install and making the credits visible in the first scroll.**

---

```markdown
# Loomwork

**Pocketbase for agents.**  
The `.aci` that makes agents portable.

Single binary · Local LLM · Signed packaging · <60 seconds to first value

---

## Install (one command)

```bash
curl -fsSL https://loomwork.dev/install.sh | sh
```

Or download a prebuilt binary:

| Platform              | Binary                     |
|-----------------------|----------------------------|
| Linux (amd64)         | `loomwork-linux-amd64`     |
| Linux (arm64 / Termux)| `loomwork-linux-arm64`     |
| macOS (Apple Silicon) | `loomwork-darwin-arm64`    |

```bash
chmod +x loomwork-* && sudo mv loomwork-* /usr/local/bin/loomwork
```

That’s it. No config file required.

---

## First value (< 60 seconds)

```bash
# Make sure a local model is available
ollama pull llama3.2

# Check everything is ready
loomwork doctor

# Scaffold → ask → package → run
loomwork init my-agent && cd my-agent
loomwork ask "what does this folder contain?" --folder .
loomwork package --out my-agent.aci
loomwork run my-agent.aci --input "what did we just discuss?"
```

The agent remembers. The package is signed and portable.

---

## Community

Loomwork was shaped by public feedback. Early contributors:

| Reddit user              | Contribution |
|--------------------------|--------------|
| **u/Otherwise_Wave9374** | Defined the memory contract: typed records (preferences, episodes, artifacts, beliefs) with provenance, consent, retention and revocation that actually cascades. Pointed to NeuraKeep as prior art. |
| **u/Appropriate-Fix4695**| Confirmed the core pain — switching tools feels like starting over. Pushed for local-first memory that follows the *user*, not the app, without becoming another config nightmare. |
| **u/Other-Breakfast-3192** | Shared LogOS discussion #77 (ephemeral root filesystems) as related prior art for portable agent environments. |

Thank you.

---

## What Loomwork is

An agent today is locked inside whichever product hosts it. Its instructions, memory, tools and permissions do not travel.

Loomwork makes the agent itself a portable unit:

- **ACI** — a signed archive (persona + skills + tool bindings + sandbox policy + memory schema)
- **Runtime** — one static binary that loads any valid ACI against a local LLM (Ollama)
- **Memory** — stays on *your* machine, encrypted, typed, and under your control
- **AMP** — minimal agent-to-agent hand-off (delegate + report)

Go is the primary implementation (single ~15 MB binary). Python is a wire-compatible reference.

---

## Security (what is actually enforced)

- Signatures are **required**. The signer must be in `~/.loomwork/trusted/`. Your own keys are trusted automatically. For someone else’s ACI: `loomwork trust add their.aci`
- Only files listed in the manifest are packed or accepted. Path traversal, duplicates and oversized entries are rejected.
- Declared sandbox policy, tool bindings and token budgets are enforced at runtime.
- Memory is encrypted at rest (AES-256-GCM). Revocation deletes content + embeddings + derivatives.
- Cloud models are never used silently. You are told when prompts leave the machine.

Overrides exist (`--allow-unsigned`, `--allow-untrusted-signer`) and are clearly unsafe.

---

## 25-second demo

```bash
cd go/
./scripts/demo-full.sh
```

Packages on laptop → runs on phone → remembers → hands off via AMP → produces a verification receipt.  
Record with asciinema, trim to 25 s, post.

---

## Model selection

1. `OLLAMA_MODEL` (if set)
2. First *installed* model from the ACI’s preferred list
3. Any other local model
4. Cloud model (with explicit warning)

If a model returns 404/410, Loomwork tries the next candidate and tells you.

---

## Two implementations, one spec

| Implementation | Role |
|----------------|------|
| **Go** (`go/`) | Primary MVP. Single static binary. This is what `curl \| sh` installs. |
| **Python** (`python/`) | Reference implementation. 64 tests. Wire-compatible with Go. |

A Go-built `.aci` verifies and runs in the Python runtime, and vice versa.

---

## Docs

| Document | Content |
|----------|---------|
| `PRD/Loomwork_PRD.md` | Current product requirements |
| `go/docs/day-1-developer-experience.md` | The <60-second contract |
| `go/docs/memory-contract-v0.2.md` | Typed memory + revocation |
| `go/docs/amp-minimal-subset.md` | Minimal viable AMP |
| `go/docs/competitive-positioning.md` | vs A2A, ACP, AutoGen, Mem0, MCP, OCI |

