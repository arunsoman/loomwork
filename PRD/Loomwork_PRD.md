# Loomwork — Product Requirements Document

**Version:** 0.1.1 (draft) · **Status:** Open for community contribution · **Spec license:** CC-BY 4.0 · **Code license:** Apache 2.0

This document describes Loomwork as implemented in the reference Go runtime (`go/`, version 0.1.0). Requirement statements say what the runtime does today; the status column in §12 marks what is implemented, partial, or declared but not yet enforced.

---

## 1. Summary

Loomwork is a single-binary runtime and packaging format for personal AI agents. An agent is packaged as an **ACI** (Agent Container Image): a signed archive holding its persona, skills, tool bindings, memory schema and sandbox policy. The runtime loads an ACI, verifies it, runs it against a local LLM served by Ollama, and keeps the agent's memory in a local encrypted store owned by the user.

The project is a community effort to find a workable answer to a concrete problem, described next.

## 2. Problem

Today an agent is not a thing you can own or move. What makes it useful — its instructions, its accumulated memory of you, the tools it may call, the permissions it holds — lives inside whichever product hosts it.

1. **State does not travel.** Moving between assistants, editors, or machines means starting again. Preferences, past decisions and known-bad approaches are re-taught each time.
2. **Tools are portable; agents are not.** Standard protocols now let one tool be used by many hosts. Nothing equivalent describes the agent that uses the tools.
3. **Trust is implicit.** When you receive someone's agent, config or prompt bundle, you cannot check who produced it, whether it was altered, or what it is allowed to touch.
4. **Memory has no contract.** A shared vector store says nothing about what a memory is, who wrote it, who may read it, how long it lives, or what happens when you delete it.
5. **Setup cost is high.** Local-first tools often fail at first contact because of configuration, not capability.

Loomwork's position is that a portable agent needs four things together: a **packaging format**, **verifiable provenance**, a **memory contract** the user controls, and a **runtime small enough to start in one command**.

## 3. Goals and non-goals

### Goals
- G1. Define an agent package (ACI) that is self-describing, integrity-checked and signed.
- G2. Run any valid ACI on a laptop or phone from one static binary, with a local LLM.
- G3. Keep memory local, encrypted, typed, and revocable, following the user rather than the application.
- G4. Provide a minimal, transport-agnostic envelope for agent-to-agent task hand-off.
- G5. Make first use short: check environment, scaffold, ask, package, run.

### Non-goals (v0.1)
- Hosting, a registry, or a marketplace.
- Payments or settlement between agents.
- Multi-agent orchestration beyond a single delegate/report exchange.
- Formal verification of agent behaviour.
- Hardened OS-level sandboxing (see §9.4).

## 4. Users

| User | Need |
|---|---|
| Individual running local models | An agent that remembers, on their machine, without cloud lock-in |
| Agent author | A format to package, sign and share an agent |
| Recipient of an agent | A way to verify what they received before running it |
| Runtime implementer | A small, unambiguous spec to build a compatible runtime |

## 5. Core concepts

| Term | Meaning |
|---|---|
| **ACI** | Gzipped tar archive with a fixed layout (§6) |
| **Manifest** | `manifest.json`; root of trust; lists SHA-256 digests of every referenced file |
| **Persona** | System prompt plus model preferences and token budgets |
| **Skills graph** | DAG of skills declared by the agent (`skills/graph.json`) |
| **Tool bindings** | MCP servers and allowed tools the agent declares (`tools/bindings.json`) |
| **Sandbox spec** | Declared filesystem, network and resource limits (`sandbox.json`) |
| **Memory** | Local SQLite store; conversational entries and typed records (§8) |
| **AMP** | Agent Mesh Protocol; JSON-RPC 2.0 envelope with provenance and capability fields (§10) |
| **Attestation** | Signed SBOM plus structural check results emitted at packaging time |

## 6. ACI format

### 6.1 Layout

```
agent.aci  (tar.gz)
├── manifest.json
├── persona/system_prompt.md
├── skills/graph.json
├── tools/bindings.json
├── memory-schema.json
├── sandbox.json
└── signatures/
    ├── manifest.sig     (JSON envelope with base64 Ed25519 signature)
    └── manifest.cert    (PEM public key)
```

Sidecar files, written next to the archive and not inside it: `<agent>.slsa.json` (provenance statement) and `<agent>.aci.attestation.json` (SBOM and check results).

### 6.2 Manifest

```jsonc
{
  "apiVersion": "aci.loomwork.dev/v0.1",   // must match exactly
  "kind": "Agent",
  "metadata": { "name": "...", "version": "v0.1.0",
                "architecture": "amd64|arm64|wasm",
                "os": "linux|darwin|windows|any",
                "description": "", "license": "", "homepage": "" },
  "persona": { "systemPrompt": "persona/system_prompt.md",
               "fewShot": "",
               "modelPrefs": { "preferred": ["llama3.2", "qwen2.5"],
                               "minContextWindow": 32768, "temperature": 0.3,
                               "tokenBudget": { "perTurn": 4096, "perSession": 131072, "hardLimit": 1048576 } },
               "capabilities": { "supportsStreaming": true, "supportsTools": true,
                                 "supportsVision": false, "supportsCodeExecution": false } },
  "skills":  { "graph": "skills/graph.json" },
  "tools":   { "bindings": "tools/bindings.json" },
  "memory":  { "schema": "memory-schema.json" },
  "sandbox": { "spec": "sandbox.json" },
  "digests": { "<path>": "sha256:<64 hex>" }
}
```

Validation rules enforced by the runtime:
- `apiVersion` and `kind` match exactly; `metadata.name` matches `^[a-z0-9][a-z0-9-]*$`; `metadata.version` matches `^v?\d+\.\d+\.\d+`.
- `architecture` and `os` are drawn from the lists above.
- Every digest is `sha256:` plus 64 lowercase hex characters.
- Each of the five referenced files has an entry in `digests`.
- On load, every file named in `digests` must be present and match its digest.

### 6.3 Canonical form and signature

The signature covers the manifest's canonical JSON: keys sorted, no insignificant whitespace, nulls omitted. Because the manifest carries the digest of every referenced file, one signature transitively covers the referenced content.

The signature envelope is cosign-shaped JSON: `critical.identity`, `critical.type`, `optional.issuer`, `optional.subject` (key ID), `base64Signature`, `payloadHash`. Signing is Ed25519. The key ID is the first 16 hex characters of the public key.

### 6.4 Skills graph

`skills/graph.json` holds `skills[]` with `name`, `description`, `inputs`, `outputs`, `requires[]`, and an optional `impl` (`type`: wasm|python|js|md, `entry`: path). Packaging fails if a skill requires an unknown skill or if the graph has a cycle.

### 6.5 Tool bindings and sandbox spec

`tools/bindings.json` declares bindings: `name`, `mcpServer`, `allowedTools[]`, `scope.paths[]`.

`sandbox.json` declares `fs.read`, `fs.write`, `net.egress`, `net.listen`, `resources.cpu`, `resources.memory`, `time.maxWall`. A policy evaluator (`CheckRead`, `CheckWrite`, `CheckEgress`) exists in the runtime; see §9.4 for enforcement status.

### 6.6 Plain-folder input

The markdown workflow is the front end of the pipeline, not a separate format. A directory with `AGENT.md` and no `manifest.json` is a valid input to `loomwork init` and `loomwork package`:

```
my-agent/
├── AGENT.md         persona / system prompt (used as persona.systemPrompt)
├── skills/*.md      one skill per file; optional front-matter: name, description, requires
└── MEMORY.md        your notes; NOT packaged (see below)
```

The runtime generates `manifest.json`, `skills/graph.json` (each skill file is the skill's `impl`, type `md`), `tools/bindings.json`, `sandbox.json` and `memory-schema.json` with defaults. The user's markdown files are digested, signed and packed as written and are never modified. At run time an `md` skill is instruction text: the runtime attaches its content (bounded, 8 KiB per skill and 32 KiB in total) to the system prompt, with the same trust as the persona because it is inside the signed archive. Skills with other `impl` types are declared only and are not executed. Re-running `loomwork package` in a folder-built agent re-reads `skills/*.md`, so a skill added after `init` is packaged; `loomwork init --force` regenerates the whole scaffold. `package` warns about any `skills/*.md` the manifest does not list. `MEMORY.md` is deliberately excluded from the archive, the digests and the signature: memory content never travels inside an ACI (§8.3), and `init`/`package` print a notice saying so. It creates no records.

## 7. Runtime

### 7.1 Commands

| Command | Behaviour |
|---|---|
| `loomwork doctor` | Checks Ollama reachability and installed models, signing key, memory store, workspace, spec version |
| `loomwork init [dir]` | Scaffolds a valid agent with computed digests. In a plain folder (§6.6) it generates the manifest around the user's own files |
| `loomwork ask "q" [--folder d]` | Indexes a folder through the agent's tool bindings and sandbox (listing plus small non-secret text samples) and answers with the local model |
| `loomwork keygen [--out f]` | Generates an Ed25519 key (PKCS#8 PEM, mode 0600) and public key, and trusts it |
| `loomwork trust list\|add\|remove` | Manages the keys whose signatures are accepted |
| `loomwork package [--out f] [--signing-key f]` | Recomputes digests, checks the skills graph, signs, packs only the files the manifest lists, writes a signed provenance statement and a signed attestation bound to the archive |
| `loomwork verify f.aci` | Checks digests, manifest schema, skills graph, signature, that the signer is trusted, and the provenance statement; exits non-zero on failure |
| `loomwork run [flags] f.aci` | Loads and runs an ACI: single task (`--input`) or interactive prompt |
| `loomwork receipt f.aci [--verify]` | Prints or verifies the attestation (signature and archive digest) |
| `loomwork amp token\|serve\|delegate` | Issues capability tokens, serves `amp/delegate` on stdio, delegates a task to a peer |
| `loomwork memory …` | Typed-memory management (§8.2) |
| `loomwork version` | Prints version |

Flags may appear before or after positional arguments. `loomwork init` refuses to overwrite an existing agent without `--force`.

### 7.2 Loading and trust

1. Parse the archive and manifest; validate schema; reject unsafe entry names, duplicate entries, oversized files (16 MiB each, 64 MiB total, 1024 files), unsupported entry types and any file the manifest does not list; verify all listed digests.
2. Verify the signature and look the signer up in `~/.loomwork/trusted/`. An ACI with no signature, an invalid one, or a valid one from an untrusted key is refused. Overrides: `--allow-unsigned`, `--allow-untrusted-signer`. Directories (a user's own working copy) are not subject to this check.
3. Validate the skills graph, parse `sandbox.json` and `tools/bindings.json`, and confirm the model server is an allowed egress host.
4. Print a receipt line only if the attestation verifies against the ACI's signer and exact bytes, and a status line (model, local or cloud, tokens this session, sandbox, and the number of this agent's proposals still pending, when non-zero) after each answer.

### 7.2.1 Enforcement while running

| Declared | Applied |
|---|---|
| `sandbox.json` `fs.read` / `fs.write` | Checked (after resolving symlinks) for folder indexing and every tool call |
| `sandbox.json` `net.egress` | Checked against the model server host |
| `sandbox.json` `time.maxWall` | Deadline on each model request |
| `tools/bindings.json` | A tool is usable only if its binding lists it in `allowedTools` and any path is inside `scope.paths` |
| `persona.modelPrefs.tokenBudget` | `perTurn` caps generated tokens; `perSession` stops the session; `hardLimit` is counted across sessions in memory |
| `resources.cpu`, `resources.memory` | Declared only |

### 7.3 Model selection

The runtime uses Ollama. Selection order:
1. `OLLAMA_MODEL`, if set (used as given).
2. The first installed model in the ACI's `preferred` list; a bare name matches any tag (`llama3.2` matches `llama3.2:3b`).
3. Any other installed local model.
4. Any installed cloud model (names ending `:cloud` or `-cloud`); the user is told prompts leave the machine.

If a chat call returns 404 or 410 (model missing or retired), the runtime tries the next candidate and reports the switch. If nothing is installed, it reports which model to pull. Any fallback prints one explanatory line.

### 7.4 Conversation memory

Each `Ask` sends the agent's ten most recent user/assistant turns as real chat turns (oldest first) after the system prompt, together with the approved typed memory the agent may see (§8.2). It stores only the question and reply, plus a token-usage entry. A folder index is stored once per agent and folder and replaces itself on re-index. When a cloud model is in use, folder indexing sends the file listing only unless `--allow-cloud-samples` is given.

## 8. Memory

**Two layers.** Memory has two layers with different trust:

- **Conversational entries** are the raw log. The runtime persists them automatically (question, reply, token usage). They are never approved, and only the most recent turns are replayed to the same agent as chat history (§7.4).
- **Typed records** are the trusted layer. Only `active` records, which a reviewer approved, are added to an agent's system prompt.
- **Pending** records sit between them: persisted, but untrusted. A pending record is visible only to its writer and to the reviewer, and never appears in another agent's view or prompt. Pending is bounded (§8.2, R-24) and its size is shown after each answer (R-25).

### 8.1 Storage

One SQLite file, `~/.loomwork/memory.db`.

- **Conversation store** (used by `run` and `ask`): `entries` (id, agent, aci, kind, encrypted content, timestamp), `episodic` (timeline), `graph` (subject–predicate–object triples). Content is encrypted with AES-256-GCM. The key is derived with PBKDF2-SHA256 (200,000 iterations, per-database salt) from `LOOMWORK_MEMORY_PASSPHRASE`, or from a random key generated once and kept at `~/.loomwork/memory.key` (mode 0600).
- **Typed store** (used by `loomwork memory`, and read by `run`/`ask` through a per-agent view): `records`, `embeddings`, `derivatives` tables. Each record's content, provenance, consent and retention live only inside one encrypted payload with the same key; `kind`, `status`, `sensitivity` and timestamps are plaintext columns. SQLite `secure_delete` is on.

### 8.2 Typed memory contract

Five record kinds share one envelope:

| Kind | Payload |
|---|---|
| `preference` | key, value, source (user_stated / inferred), confidence |
| `episode` | timestamp, summary, input and output record IDs, outcome |
| `artifact` | name, mime, digest, content reference, creator |
| `belief` | claim, evidence record IDs, confidence, last verified, superseded-by |
| `failure` | pattern, what failed, why, avoidance, occurrences |

Envelope fields: `id`, `kind`, `status` (pending → active → superseded | revoked), `sensitivity` (low|medium|high), `provenance` (writer agent, writer ACI, time, source, parent ID, source URI), `consent` (allowed agents, allowed scopes, public), `retention` (until_revoked | ttl), `derivatives[]`, timestamps.

Lifecycle (statuses: pending, active, superseded, revoked, rejected):
- **Propose:** agents create records as `pending`; a pending record is visible only to its writer (and the reviewer).
- **Pending hygiene:** an agent's unapproved proposal expires after at most 7 days, whatever retention the writer asked for: the effective expiry while pending is min(writer's TTL, pending TTL), and a writer-supplied expiry timestamp is discarded. The writer's own retention takes effect only on approval, where a TTL restarts. The default pending TTL is 7 days (`LOOMWORK_PENDING_TTL`, seconds; 0 disables) and is deleted like any expired record (same tombstone as `reject`/`revoke`: content and embedding removed, only a content-free row remains); approval clears it. Expired records are deleted the next time the store is opened, and are invisible to every view until then. Each writer may have at most 50 records awaiting review (`LOOMWORK_PENDING_CAP`; 0 disables); further proposals are refused with a message. The reviewer (the user's CLI) is exempt from both.
- **Write-gate (opt-in):** with `LOOMWORK_WRITE_GATE=1`, an agent's proposals are staged in the `proposals` table of `~/.loomwork/memory.db`, outside the `records` table, with no derivatives, embeddings or search entries, until the reviewer approves or rejects them. Staged proposals are shown by `loomwork memory list --status pending` (marked `staged`) and counted by `loomwork memory stats`. The cost: the writer cannot read back its own proposal. Off by default.
- **Approve / reject:** only a reviewer view (the user's CLI) may promote or reject.
- **Views:** an agent reads through a filtered view: revoked, rejected and expired records are hidden; high-sensitivity records need the `sensitive` scope; otherwise visibility follows consent lists, scopes, or the public flag. Agent identity is the ACI name.
- **Revoke:** deletes the record's content and embedding, leaving a content-free tombstone, and does the same to everything derived from it: children by parent link, beliefs citing it as evidence, episodes that used it as input. It cannot be undone. `reject` does the same for a pending proposal.
- **Expiry:** records past their TTL are revoked when the store is opened.
- **Supersede:** marks a record replaced by a newer one; it does not link them, so revoking the old record leaves its replacement.
- **Search:** keyword search over visible records.

### 8.3 Ownership decision for v0.1

The user's machine owns the store. An ACI carries only a memory *schema*; memory content never travels inside an ACI.

## 9. Security model

### 9.1 What is verified
- Content integrity: every file in an archive is covered by a manifest digest, and nothing else is accepted.
- Manifest authenticity: an Ed25519 signature over the canonical (RFC 8785) manifest.
- Signer identity: the signing key must be in the user's trusted set (§9.2).
- Schema and version: apiVersion mismatch is refused.
- Receipts and provenance: signed by the ACI's signer and bound to the exact archive bytes.

### 9.2 Trust
A signature proves the manifest was signed by the holder of the key shipped in `signatures/manifest.cert`. Whether that key belongs to someone the user trusts is decided by `~/.loomwork/trusted/`. Keys the user generates are added automatically; other keys are added with `loomwork trust add`, which shows the fingerprint and asks. There is no key distribution service in v0.1: users compare fingerprints out of band.

### 9.3 Provenance and attestation
The provenance statement and the attestation are generated on the packaging machine and signed with the ACI's key. They record build context and structural checks (files match, skills graph acyclic, bindings well-formed, sandbox and memory schema valid, persona present). They do not claim an isolated or reproducible build.

### 9.4 Sandbox and budgets
See §7.2.1. Enforcement is in-process, on the operations the runtime performs. The agent has no shell or network tool of its own; the built-in tool surface is the file listing and sampling used by `ask --folder`. Running third-party MCP servers, and OS-level isolation (namespaces, seccomp, WASM), are not part of v0.1.

### 9.5 Data leaving the machine
With a local model, nothing leaves the machine. If the runtime falls back to an Ollama cloud model, prompts and conversation history go to that service; file contents are withheld unless `--allow-cloud-samples` is set. The runtime announces the cloud fallback at selection time and marks CLOUD in the status line.

### 9.6 Limits
**Covert channel.** An agent can see its own pending records, and the runtime replays its recent conversational entries to it, so a compromised agent can use them to stash data or signal across sessions: through record contents, the timing or volume of proposals, or, if two ACIs share a name, to each other. The pending TTL, the per-writer cap, the visible pending count and the write-gate reduce and expose this channel; they do not close it. Conversational entries are not capped.

Agent identity for memory consent is the ACI name, which any ACI can claim. The per-database encryption key is stored on the same machine as the database; it protects against reading a copied database, not against a process running as the user.

## 10. Agent Mesh Protocol (AMP) — v0.1 subset

A JSON-RPC 2.0 envelope with AMP extensions:

```
jsonrpc, id, method?, params?, result?, error?,
amp: { version, traceId, sessionId?, ttl },
capabilities: [ { id, skill, scope?, issuedBy, issuedAt, expiresAt, signature } ],
provenance:  { aci, aciDigest, runtime, signer, signature }
```

Requests whose method starts with `amp/` must carry `amp.version`, `amp.traceId`, `capabilities` and `provenance`. Messages are limited to 1 MiB; malformed input yields a protocol-violation error, never a crash.

Methods: `amp/delegate` (task ID, skill, intent, inputs, budget → accepted/reason) and `amp/report` (task ID, status pending|partial|complete|failed, artifacts with digest and optional inline text, usage). Transport: newline-delimited JSON over stdio, including a spawned peer process (`--exec`).

Serving (`loomwork amp serve`) requires, for each delegate request: AMP version 0.1; provenance signed by a trusted key; the requested skill to be offered by the agent; and a capability token for exactly that skill, unexpired, signed by a trusted key. Otherwise it answers 401 (unauthenticated), 403 (capability denied), 300 (skill not found) or 501 (version). After accepting, the server runs the task and sends an `amp/report` request, which the caller acknowledges. Error codes: 500 protocol violation, 501 version mismatch, 300 task not found, 302 budget exceeded, 401, 403.

Credit: an append-only JSONL receipt log (`CreditReceipt` with payer, payee, breakdown) supports logging mode, with Ed25519 signing and verification for payer and payee; there is no settlement.

## 11. Non-functional requirements

| ID | Requirement |
|---|---|
| N-1 | Single statically linked binary; cross-built for linux/amd64, linux/arm64, darwin/arm64 |
| N-2 | No configuration required when Ollama is already running |
| N-3 | `doctor → init → ask → package → run` completes without editing any file |
| N-4 | Secrets and keys stored with mode 0600; key directory 0700 |
| N-5 | Manifest canonicalization follows RFC 8785 (UTF-16 key order, shortest numbers) |
| N-6 | The default path needs no extra trust step for the user's own agents |
| N-7 | Time to first answer is under 60 seconds when a local model is already installed (under 90 when one must be pulled); anything needing extra setup (remote peers, credit, custom sandbox policy) is opt-in and never blocks this path |

## 12. Requirements and status

| ID | Requirement | Status |
|---|---|---|
| R-01 | Reject archives whose files do not match manifest digests | Implemented |
| R-02 | Reject unsigned or invalidly signed ACIs at load | Implemented (override flag) |
| R-03 | Verify signatures against a user-trusted key set | Implemented (`trust`); no key distribution |
| R-04 | Reject files in an archive that are not covered by the manifest | Implemented |
| R-05 | Reject archive entries with unsafe paths | Implemented (load and unpack) |
| R-06 | Bound archive and entry sizes when reading | Implemented |
| R-07 | Package only files the manifest names | Implemented |
| R-08 | Reject skills-graph cycles at packaging and at load | Implemented |
| R-09 | Enforce sandbox policy on file access, egress and wall time | Implemented in-process; CPU/memory declared only |
| R-10 | Enforce tool bindings and token budgets | Implemented |
| R-11 | Encrypt conversation memory at rest | Implemented |
| R-12 | Encrypt typed records at rest, honouring sensitivity | Implemented |
| R-13 | Revocation removes content, embeddings and derived records | Implemented for typed records; conversation store not linked |
| R-14 | Agents read typed memory through consent-filtered views | Implemented (`run`, `ask`, `amp serve`) |
| R-15 | Model fallback with notice; skip retired models | Implemented |
| R-16 | Verify attestation before displaying a receipt | Implemented |
| R-17 | AMP: verify capability tokens and provenance | Implemented |
| R-18 | AMP: hand-off available from the CLI | Implemented (stdio) |
| R-19 | AMP: bounded message size, no panics on malformed input | Implemented |
| R-20 | Single-command install and model pull | Not implemented (install script exists; no model pull) |
| R-21 | Run a named built-in agent (`loomwork run research`) | Not implemented |
| R-22 | Tool execution through MCP servers | Not implemented |
| R-23 | Keyless signing / transparency log | Not implemented |
| R-24 | Pending records expire (default 7 days) and each writer has a pending cap (default 50) | Implemented (agent views; reviewer exempt) |
| R-25 | Pending count shown in the post-answer status line | Implemented |
| R-26 | Opt-in write-gate: proposals staged outside the record store until approved | Implemented (env `LOOMWORK_WRITE_GATE`; off by default) |
| R-27 | Plain folder (`AGENT.md`, `skills/*.md`) accepted by `init` and `package`; `MEMORY.md` is never packed | Implemented |
| R-28 | Conversation entries capped or covert-channel-resistant | Not implemented (documented limit, §9.6) |

## 13. Open design questions

1. **Key distribution.** The trusted-key set is local and manual. Should there be a shared registry, keyless signing through a transparency log, or neither?
2. **Revocation scope.** Typed records cascade; how should conversation entries, summaries and caches built by a runtime be tied to the records they came from?
3. **Agent identity.** Memory consent currently keys on the ACI name. Should identity be the signing key, a DID, or name plus signer?
4. **Enforcement layer.** In-process checks today; when are OS mechanisms (namespaces, seccomp, WASM) required for sandbox and tool scope?
5. **Minimum AMP surface.** Whether delegate and report are enough for useful interoperability, or whether a handshake and cancel are needed.
6. **Write-gate default.** Should the write-gate become the default once agents write typed records themselves, given that the writer then cannot read back its own proposal?
7. **Day-one path.** How a first run gets a model and a starter agent with no manual steps, without adding accounts or configuration.

## 14. Roadmap

- **Now:** publish the v0.1 spec and reference runtime; gather reports of real first-run times and failures.
- **Next:** built-in starter agents and a model-pull step for a true one-command first run; MCP tool execution behind the existing bindings and sandbox; OS-level isolation.
- **Later:** key distribution or keyless signing, additional AMP transports, link conversation memory to the derivatives graph, independent build attestation, optional settlement.

## 15. Contributors

This design has been shaped by public discussion. Contributions credited so far:

| Contributor | Contribution |
|---|---|
| u/Otherwise_Wave9374 | Typed memory records with provenance, consent scope, retention and per-tool access; raw memory in a user-controlled store with filtered agent views; revocation across embeddings, summaries and caches as the acceptance test; NeuraKeep as a reference for persistent agent memory |
| u/Appropriate-Fix4695 | Local-first memory that follows the user rather than the app; keep configuration minimal |
| u/Other-Breakfast-3192 | Reference material on ephemeral root filesystems (LogOS discussion #77) |

## Appendix A — Example ACI

`go/examples/` contains three sample agents (`folder-qna-agent`, `git-summarizer-agent`, `meeting-notes-agent`) and a sample folder.

## Appendix B — Files

`go/internal/aci` (format, signing, skills, provenance) · `go/internal/runtime` (runner, memory, sandbox and tool gate) · `go/internal/llm` (Ollama client, model selection) · `go/internal/memory` (typed store, views, revocation) · `go/internal/amp` (envelope, capability tokens, transport, server, credit log) · `go/internal/verify` (SBOM, structural checks, attestation) · `go/internal/secret` (at-rest encryption).
