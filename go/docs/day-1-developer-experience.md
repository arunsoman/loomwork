# Day-1 Developer Experience

> Target: useful agent answering a question in **under 60 seconds** from `curl | sh`.
> This is the contract. If we miss it, we land in the "technically excellent but
> high-friction" cluster. Every PR that adds friction to this path requires
> explicit justification.

## The contract (60 seconds, single machine, demo-worthy)

```
T+0s    curl -fsSL https://raw.githubusercontent.com/arunsoman/loomwork/main/go/install.sh | sh
T+3s    ollama pull llama3.2  (assumed pre-installed; doctor checks)
T+5s    loomwork doctor
T+10s   loomwork init my-agent
T+15s   cd my-agent
T+20s   loomwork ask "what does this folder contain?" --folder .
T+45s   [agent indexes folder, answers via Ollama, writes episode to memory]
T+50s   loomwork package --out my-agent.aci
T+58s   loomwork run my-agent.aci --input 'what did we just discuss?'
T+60s   [agent answers from memory — visible proof of portability]
```

## The exact sequence a new user runs

### Step 1: Install (≤5 seconds)

```bash
curl -fsSL https://raw.githubusercontent.com/arunsoman/loomwork/main/go/install.sh | sh
```

What happens:
- Detects OS + arch (linux/amd64, linux/arm64, darwin/arm64)
- Downloads ~15 MB statically-linked binary
- Installs to `/usr/local/bin/loomwork` (or `~/.local/bin/loomwork` if no sudo)
- No config files written. No environment variables set. No daemons started.

Failure modes (all addressed by `loomwork doctor`):
- No sudo → falls back to `~/.local/bin` (must be on PATH; installer prints reminder)
- Corporate proxy → respects `HTTPS_PROXY`
- Broken curl → installer verifies SHA-256 of downloaded binary

### Step 2: Verify setup (≤5 seconds)

```bash
loomwork doctor
```

One command, no flags. Checks:
- ✓ Ollama reachable at `localhost:11434` (or `OLLAMA_URL`)
- ✓ At least one model installed (warns if zero, suggests `ollama pull llama3.2`)
- ✓ Signing key present (auto-generates on first `package` if missing)
- ✓ Memory DB opens (auto-creates `~/.loomwork/memory.db`)
- ✓ Workspace dir writable (`~/.loomwork/workspace/`)
- ✓ Spec version matches binary

Output is human-readable by default, `--json` for tooling. Every failure prints a `fix:` line with the exact command to run.

### Step 3: Scaffold an agent (≤5 seconds)

```bash
loomwork init my-agent
cd my-agent
```

Creates 6 files, all with computed digests, ready to package:
- `manifest.json` — ACI v0.1 manifest with 5 typed digest entries
- `persona/system_prompt.md` — minimal but useful system prompt
- `skills/graph.json` — 2 skills: `answer_question`, `propose_action`
- `tools/bindings.json` — 1 binding: filesystem (read-only on `$HOME/**`)
- `memory-schema.json` — minimal schema (required even if Memory Layer deferred; see `docs/memory-contract-v0.2.md`)
- `sandbox.json` — conservative defaults (read `**`, write `$HOME/.loomwork/workspace/**`, egress localhost only)

No questions asked. No choices to make. The defaults work.

### Step 4: First value (≤30 seconds)

```bash
loomwork ask "what does this folder contain?" --folder .
```

What happens:
1. Loads `manifest.json` from current dir
2. Opens memory DB at `~/.loomwork/memory.db`
3. Indexes the folder (walks files, reads small text samples, writes `folder_index` memory entry)
4. Builds a chat request: system prompt + folder index + recent memory + user question
5. Dispatches to Ollama (`llama3.2` by default, or `OLLAMA_MODEL`)
6. Prints the answer to stdout
7. Persists user message + assistant response as `episode` memory entries

Output is demo-worthy: the answer cites specific file paths from the folder. The `--verbose` flag shows the index + recent memory for debugging.

### Step 5: Package (≤5 seconds)

```bash
loomwork package --out my-agent.aci
```

What happens:
1. Re-computes all digests (in case files changed since `init`)
2. Signs the manifest with Ed25519 (auto-generates key if missing)
3. Packs to a gzipped tarball (~2 KB for a minimal agent)
4. Writes SLSA attestation sidecar (`.slsa.json`)
5. Writes verification attestation sidecar (`.attestation.json`) — SBOM + property tests, signed
6. Prints: `Ready to share: my-agent.aci + my-agent.slsa.json`

The `.aci` is now portable. Copy it to another machine, `loomwork run my-agent.aci` just works.

### Step 6: Prove portability (≤10 seconds)

```bash
loomwork run my-agent.aci --input 'what did we just discuss?'
```

What happens:
1. Loads the .aci
2. Verifies signature (warns if invalid, fails closed if `--strict`)
3. Prints the verification receipt line: `receipt: sha256:...  tests=PASSED  signer=...  signed=...`
4. Opens memory DB
5. Dispatches the question to Ollama
6. The agent answers **from memory** — proving the conversation persisted

This is the demo-worthy moment: the agent remembers what was discussed in step 4, even though step 6 loads from a packaged .aci.

## What makes this work (the design constraints)

1. **Zero required config.** No YAML, no JSON config files, no env files. Sensible defaults everywhere. `loomwork doctor` is the only debugging command.
2. **Auto-discover everything.** Ollama URL → env var → `localhost:11434`. Signing key → auto-generate. Memory DB → auto-create. Workspace → auto-mkdir.
3. **No schema migrations.** The ACI manifest schema is part of the spec, not user-configurable. The typed-memory schema is part of the spec. Users pick `kind`; they don't pick fields.
4. **Single binary, no deps.** Go static link. `modernc.org/sqlite` (pure Go). No CGO. No Python. No Node. No Docker.
5. **Local-first by default.** Ollama on localhost. Memory in SQLite. No cloud calls unless explicitly opted in per command.

## The shareable demo asset

The 25-second demo (`scripts/demo-full.sh`) extends this path with the cross-device + AMP handoff beats. But the **day-1 developer experience is the single-machine path above** — it's what a new user actually runs. The 25-second demo is the recording you post to HN; the day-1 path is what converts viewers into users.

## Failure modes we explicitly accept

To keep the 60-second contract, we accept these trade-offs in v0.1:

- **No multi-device sync.** Copy the memory DB manually for the phone demo. Sync server is v0.2.
- **No GUI.** CLI only. A web UI would add a daemon, a port, a browser tab — friction.
- **No model selection prompt.** Defaults to `llama3.2`; if it isn't installed, another installed model is used and the runtime says which. Set `OLLAMA_MODEL` to choose one.
- **No project wizard.** `init` produces one agent shape. Users who want different skills edit `skills/graph.json` by hand.
- **No telemetry.** We don't collect data on whether users hit the 60-second target; timing has to be reported by users.

Each of these is a deliberate friction-reduction choice. Adding any of them back requires proving the friction they add is outweighed by the value they provide.
