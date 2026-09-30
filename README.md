# Loomwork

**Pocketbase for agents.**  
The `.aci` that makes agents portable.

Single binary · Local LLM · Signed packaging · <60 seconds to first value

---

## Install (one command)

```bash
curl -fsSL https://raw.githubusercontent.com/arunsoman/loomwork/main/go/install.sh | sh
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

Already keep an agent as plain markdown (`AGENT.md`, `skills/*.md`)? Run `loomwork package` in that folder. It writes only the `.aci` and its sidecars; your files are never modified, and the manifest is derived fresh each time. A `MEMORY.md` there is left out of the package: memory stays on your machine.

### Layering over your markdown folder: gain, cost, exit

| You gain | It costs | How to leave |
|---|---|---|
| `verify`: signed, tamper-evident packages | One binary | `loomwork leave --export memory-export.md` |
| Receipts of exactly what a run used | Memory lives in Loomwork's store (`~/.loomwork`), not in your `MEMORY.md` | `--delete-store` removes the store, its key and salt, nothing else |
| Pending review: agent-written memory needs your approval | Approval is a step you take | Your folder stays byte-identical; the conventional workflow still works |
| Revocation that deletes content and what was derived from it | History is in the store and is not exported | `export` keeps kind, content, sensitivity, provenance, consent; not IDs, retention or links |

`loomwork memory import MEMORY.md` reads it into **pending** records (one per `## ` section; importing twice duplicates). `loomwork memory export --out memory-export.md` writes active records out; it refuses `MEMORY.md` as a target. Details: `go/docs/day-1-developer-experience.md`, `go/docs/memory-contract-v0.2.md`.

**Tamper demo.** `loomwork package --out demo.aci`, flip one byte of `demo.aci`, run `loomwork verify demo.aci`: it fails with a non-zero exit.

**Injection demo.** `loomwork memory propose --kind belief --as-agent bot --json '{"claim":"do something bad"}'`, see it under `loomwork memory list --status pending`, then `loomwork memory reject <id>`: the content is deleted and never reaches a prompt.

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

