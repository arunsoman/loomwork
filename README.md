# Loomwork — Complete Package

> Pocketbase for agents. The `.aci` that makes agents portable.
> Single binary. Local LLM. Signed, portable packaging. <60 seconds to first value.

This is the complete Loomwork package — everything from the PRD to the reference implementations to the launch assets.

## What's in this zip

```
loomwork-complete/
├── README.md                          ← you are here
├── PRD/
│   ├── Loomwork_PRD.md                ← product requirements (current)
│   └── Code_Review.md                 ← code review with fix status
├── go/                                ← PRIMARY: single-binary Go MVP (v0.1 + v0.2)
│   ├── loomwork                        ← prebuilt binary (linux/amd64)
│   ├── loomwork-linux-amd64            ← prebuilt (linux/amd64)
│   ├── loomwork-linux-arm64            ← prebuilt (linux/arm64 — for Termux/phone)
│   ├── loomwork-darwin-arm64           ← prebuilt (macOS/Apple Silicon)
│   ├── *.go                            ← source (one file per subcommand)
│   ├── internal/                       ← ACI, AMP, memory, runtime, llm, verify
│   ├── examples/                       ← 3 immediately-useful reference ACIs
│   ├── docs/                           ← 9 spec docs (memory, AMP subset, positioning, etc.)
│   ├── scripts/                        ← demo-full.sh, install.sh
│   ├── landing.html                    ← the launch page
│   ├── go.mod / go.sum
│   └── README.md
├── python/                             ← Python reference impl (v0.1)
│   ├── loomwork/                       ← ACI + AMP + runtime modules
│   ├── examples/                       ← 3 example ACIs (research, coding, deploy)
│   ├── tests/                          ← 64 tests, all passing
│   ├── pyproject.toml
│   └── README.md
└── launch/
    └── README.md                       ← how to use these assets for the launch
```

## Quick start (60 seconds to first value)

```bash
# 1. Install (one command — or use the prebuilt binary in go/)
curl -fsSL https://loomwork.dev/install.sh | sh

# 2. Make sure Ollama is running
ollama pull llama3.2

# 3. Check your setup
loomwork doctor

# 4. Scaffold an agent
loomwork init my-agent
cd my-agent

# 5. Ask it about a folder
loomwork ask "what does this folder contain?" --folder .

# 6. Package it as a signed, portable .aci
loomwork package --out my-agent.aci

# 7. Run it — it remembers
loomwork run my-agent.aci --input 'what did we just discuss?'
```

## Model selection and fallback

`loomwork run` / `loomwork ask` use the first model in the ACI's `persona.modelPrefs.preferred` list (`llama3.2` for a fresh `init`). If that model isn't installed, Loomwork picks another one and says so:

1. `OLLAMA_MODEL`, if set (used as-is, no fallback)
2. the first *installed* model from the ACI's preferred list (`llama3.2` also matches `llama3.2:3b`)
3. any other installed **local** model
4. any installed **cloud** model (`*:cloud`, `*-cloud`), with a warning that prompts leave your machine

```
ℹ preferred model "llama3.2" is not installed; using installed local model "lfm2.5-thinking:latest"
```

Ollama can keep listing cloud models it has retired. If a chat call returns 404/410, Loomwork moves on to the next candidate and prints `ℹ model "glm-5.1:cloud" is unavailable (HTTP 410); trying "gpt-oss:120b-cloud"`. If nothing is installed you get an error telling you what to `ollama pull`.

## Security behaviour

- **Signatures are required, and the signer must be trusted.** `loomwork verify` and `loomwork run` accept an ACI only if its signature verifies *and* was made by a key in `~/.loomwork/trusted/`. Keys you generate (`keygen`, first `package`) are trusted automatically, so your own agents just work. For someone else's ACI: `loomwork trust add their.aci` shows the key fingerprint and asks before trusting it. Overrides: `--allow-untrusted-signer`, `--allow-unsigned` (unsafe).
- **Only files the manifest lists are packed or accepted.** A stray `.env` or old build in the folder is never shipped; an archive with unlisted files, path tricks (`../`), duplicate entries or oversized files is rejected.
- **The agent's declared policy is enforced.** `sandbox.json` (`fs.read`, `net.egress`, `time.maxWall`), `tools/bindings.json` (which tool, which paths) and the persona's `tokenBudget` (`perTurn`, `perSession`, `hardLimit`) are applied when the agent runs. Folder indexing skips hidden files and files that look like secrets. CPU/memory limits are declared but not applied.
- **Receipts are checked.** The receipt line shown by `run` appears only if the attestation's signature and archive digest verify (`loomwork receipt agent.aci --verify`).
- **Memory is encrypted at rest** (AES-256-GCM) for both the conversation store and typed records. The key is `LOOMWORK_MEMORY_PASSPHRASE` if set, otherwise a random key at `~/.loomwork/memory.key` (mode 0600). Lose the key and the memory can't be read.
- **Revocation deletes.** `loomwork memory revoke <id>` removes the record's content and embedding and everything derived from it (including beliefs citing it). It cannot be undone.
- **Cloud models see less.** If a cloud model is used, `ask --folder` sends only the file listing unless you pass `--allow-cloud-samples`.
- Flags can go before or after the ACI path: `loomwork run --input '...' agent.aci` and `loomwork run agent.aci --input '...'` both work.

### Typed memory and agents

`loomwork run` / `ask` give the agent the user's approved preferences, beliefs and failure notes that it is allowed to see. Records are private until you grant access:

```bash
loomwork memory propose --kind preference --allow-agent my-agent \
  --json '{"key":"indent","value":"tabs","source":"user_stated","confidence":1}'
loomwork memory approve <id>
```

### Handing a task to another agent (AMP over stdio)

```bash
loomwork amp token --skill answer_question --out token.json      # signed capability token
loomwork amp delegate --exec "loomwork amp serve b.aci" --from a.aci --token token.json "summarize this"
```

The callee must trust the caller's key (`loomwork trust add`), verifies the caller's signed provenance and the token, and returns its result with a digest.

## The 25-second demo (the launch asset)

```bash
cd go/
./scripts/demo-full.sh
```

Scripts the complete sequence: **package on laptop → run on phone → it remembers → hand off via AMP → output has verification receipt.** Record with asciinema, trim to 25s, post to HN.

## Two implementations, one spec

The Go and Python impls are **wire-format compatible** — a Go-built `.aci` loads and verifies in the Python impl, and vice versa. The cross-compat test (`go/cross_compat_test.go`) proves this.

- **Go** (`go/`): the primary MVP. Single binary, ~15 MB, statically linked. Cross-compiled for linux/amd64, linux/arm64, darwin/arm64. This is what you `curl | sh`.
- **Python** (`python/`): the reference impl. 64 tests. Useful for understanding the spec, extending, or embedding in a Python codebase.

## Specs and docs

| Doc | What's in it |
|---|---|
| `PRD/Loomwork_PRD_v0.1.pdf` | The canonical PRD — 27 pages, ACI spec, AMP wire format, market thesis, roadmap |
| `go/docs/day-1-developer-experience.md` | The <60-second contract with exact timestamps |
| `go/docs/amp-minimal-subset.md` | Answers Q7: minimal viable AMP (4 MUST RPCs) |
| `go/docs/memory-ownership-decision.md` | Answers Q4: minimal schema in ACI v0.1, full store deferred |
| `go/docs/memory-contract-v0.2.md` | Typed memory: 5 kinds, provenance, consent, retention, revocation cascade |
| `go/docs/credit-experimental.md` | Credit as optional: Off / Logging / Settled modes |
| `go/docs/competitive-positioning.md` | vs A2A, ACP, AutoGen, CrewAI, Mem0, NeuraKeep, MCP, OCI |
| `go/docs/demo-on-phone.md` | Real Termux setup for the phone half of the demo |

## Community contributors

Thanks to everyone who gave feedback on the launch threads (r/ollama, r/SelfHostedAI):

| Reddit user | Contribution |
|---|---|
| u/Otherwise_Wave9374 | Portability needs a **memory contract**, not just a shared vector directory: typed records (preferences, episodes, artifacts, inferred beliefs) with provenance, consent scope, retention and tool-specific access rules. Keep raw memories in a user-controlled store and give each agent a filtered view via an adapter. Named **revocation** as the hardest test: deleting one memory must remove its embeddings, summaries, caches and downstream availability everywhere. Pointed to [NeuraKeep](https://www.neurakeep.com) as a reference for persistent agent memory. (→ `go/docs/memory-contract-v0.2.md`) |
| u/Appropriate-Fix4695 | Confirmed the pain: switching tools feels like starting over. Asked for **local-first memory that follows the user, not the app**, and warned it must not become "another config nightmare nobody wants to debug". (→ local-first memory; <60-second day-1 contract) |
| u/Other-Breakfast-3192 | Shared [LogOS discussion #77](https://github.com/toolate28/LogOS/discussions/77), an explanation of ephemeral root filesystems, as related prior art for portable agent environments. |

## License

- **Code**: Apache 2.0
- **Specification**: CC-BY 4.0

## Status

**v0.1 + v0.2 — Draft.** Open for community review. The 90-day review window is open.

Built by the Loomwork Working Group.
