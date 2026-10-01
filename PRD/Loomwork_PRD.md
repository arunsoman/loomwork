# Loomwork — Product Requirements Document

**Version:** 0.1.2 (draft) · **Status:** Open for community contribution · **Spec license:** CC-BY 4.0 · **Code license:** Apache 2.0

This document describes Loomwork as implemented in the reference Go runtime (`go/`, version 0.1.2). Requirement statements say what the runtime does today; the status column in §12 marks what is implemented, partial, or declared but not yet enforced. §6.7 is a design for the next version: everything in it is marked "Not implemented" in §12.

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

### 2.1 The user who already has a folder

Many people already carry an agent between tools as plain files: an `AGENT.md` or system prompt, markdown skills, a `MEMORY.md`, sometimes a handoff note, kept in Git or a synced directory. **That works, and they can keep doing it.** Loomwork does not ask them to change format, and it does not need any harness to learn anything new.

The need Loomwork addresses is narrower: *keep exactly that workflow, and get the guarantees a folder does not give you.*

**The upgrade is free in the ways that matter:**

- **No rewrite.** Your `AGENT.md` and `skills/` stay as they are (§6.6). Loomwork reads them and generates the rest around them; it never edits your files.
- **No lock-in.** Every file remains ordinary markdown. To leave, stop running `loomwork`; the folder still works in every harness that worked before.
- **No new dependency for other tools.** A harness that has never heard of Loomwork keeps reading the folder (§6.7, Tier 0).
- **One binary, no service, no account.** Nothing is hosted, and nothing leaves the machine with a local model (§9.5).

**What the folder does not give you, and what you get by moving:**

| You want | Plain folder | With Loomwork | Status |
|---|---|---|---|
| To hand the agent to someone and let them prove it is yours and unaltered | A zip or a repo link; the recipient cannot tell if it was changed | Digests and an Ed25519 signature the recipient checks against keys they trust (`package`, `verify`, `trust`) | Available |
| To know which exact files a run used | Nothing recorded | A signed attestation bound to the archive bytes, shown as a receipt when you run it | Available |
| To limit what an agent may touch | Whatever the harness allows | Declared file, network and time limits, enforced on the runtime's own operations (§7.2.1); not OS isolation | Available (in-process) |
| A cap on token spend | None | Per-turn, per-session and lifetime budgets | Available |
| To catch a broken skill graph before it runs | Discovered at run time, or never | Cycles and missing skills rejected at packaging and at load | Available |
| Long-term memory you can inspect, expire and delete for real | `MEMORY.md`: a line has no author, expiry or consent, and "deleting" it leaves it in history and backups | Typed records with provenance, consent and retention; revocation deletes content and everything derived from it; encrypted at rest (§8) | Available for typed records. By design not for the lines of a `MEMORY.md` file, which is the portable layer (§6.7); `memory import` brings them into the typed store as pending (§8.4) |
| Agent-written memory that you approve before it counts | The agent edits the file, or you never notice | Proposals stay `pending`, expire, are capped per writer and shown in the status line (§8.2) | Available for typed records; markdown proposals planned |
| To know your state files are intact after a crash or a sync mishap | Hope | Atomic writes and `check` against recorded digests | Planned (Tier 1, §6.7) |
| A tamper-evident history of who changed the agent's state | Git history, if you committed, and only as trustworthy as the repo | A hash-chained journal of writes, handoffs and receipts | Planned (Tier 1, §6.7) |
| Handoff you can audit | A note that may or may not have been read | A receipt recording which handoff state the next agent started from | Planned (Tier 1, §6.7) |

**Where the folder is enough.** If you never share the agent, never lose sleep over what an agent wrote into memory, and one machine or one Git repo covers you, stay with the folder. Loomwork earns its place when you hand an agent to someone else, when an agent writes to memory you rely on, or when you need to show afterwards what a run used and started from.

**What "free" does not cover.** The Tier 1 rows are a plan, not a shipped feature. `MEMORY.md` holds the same kind of content the typed records exist for (preferences, facts, decisions, failures), so the memory protections are what a folder user most wants, and they apply to typed records today, not yet to the lines of a `MEMORY.md` file. Signing proves who packaged an agent, not that the agent is safe. The sandbox is in-process and not an OS boundary.

**How the upgrade is meant to happen (planned, §6.7.1).** Running `loomwork` in your folder is enough: Loomwork keeps its own records about the folder under `~/.loomwork/` and writes nothing into it, so the folder stays exactly as you keep it and leaving is deleting those records. Anything that has to live inside the folder, for other tools to use, is added only after you agree once for that folder, and is listed so it can be removed. Signing and packaging for other people are never automatic.

## 3. Goals and non-goals

### Goals
- G1. Define an agent package (ACI) that is self-describing, integrity-checked and signed.
- G2. Run any valid ACI on a laptop or phone from one static binary, with a local LLM.
- G3. Keep memory local, encrypted, typed, and revocable, following the user rather than the application.
- G4. Provide a minimal, transport-agnostic envelope for agent-to-agent task hand-off.
- G5. Make first use short: check environment, scaffold, ask, package, run.
- G6. Adoption is free for someone who already keeps an agent as plain files: no format change, no lock-in, no requirement on other tools (§2.1, §6.7).

### Non-goals (v0.1)
- Hosting, a registry, or a marketplace.
- Payments or settlement between agents.
- Multi-agent orchestration over the AMP wire protocol beyond a single delegate/report exchange. (Local orchestration of agent CLIs is specified in §6.8.)
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

`skills/graph.json` holds `skills[]` with `name`, `description`, `inputs`, `outputs`, `requires[]`, and an optional `impl` (`type`: wasm|python|js|md, `entry`: path). Packaging fails if a skill requires an unknown skill or if the graph has a cycle. Only `md` skills have any run-time effect: the runtime attaches each one's name and markdown to the system prompt, in graph order (bounded, §6.6). There is no execution path for any skill type; `wasm`, `python` and `js` implementations are declared, digested and signed, not run.

### 6.5 Tool bindings and sandbox spec

`tools/bindings.json` declares bindings: `name`, `mcpServer`, `allowedTools[]`, `scope.paths[]`.

`sandbox.json` declares `fs.read`, `fs.write`, `net.egress`, `net.listen`, `resources.cpu`, `resources.memory`, `time.maxWall`. A policy evaluator (`CheckRead`, `CheckWrite`, `CheckEgress`) exists in the runtime; see §9.4 for enforcement status.

### 6.6 Plain-folder input

The markdown workflow is the front end of the pipeline, not a separate format. A directory with `AGENT.md` and no `manifest.json` is a valid input to `loomwork init` and `loomwork package`:

```
my-agent/
├── AGENT.md         persona / system prompt (used as persona.systemPrompt)
├── skills/*.md      one skill per file; optional front-matter: name, description, requires
└── MEMORY.md        the agent's long-term memory (preferences, facts, decisions, failures); NOT packaged (see below)
```

The runtime derives `manifest.json`, `skills/graph.json` (each skill file is the skill's `impl`, type `md`), `tools/bindings.json`, `sandbox.json` and `memory-schema.json` with defaults. The user's markdown files are digested, signed and packed as written and are never modified. At run time an `md` skill is instruction text: the runtime attaches its content (bounded, 8 KiB per skill and 32 KiB in total) to the system prompt, with the same trust as the persona because it is inside the signed archive. Skills with other `impl` types are declared only and are not executed. `MEMORY.md` is deliberately excluded from the archive, the digests and the signature: memory content never travels inside an ACI (§8.3), and `init`/`package` print a notice saying so. It creates no records.

**Two modes.**

- **Coexistence mode (the default).** `loomwork package` in a plain folder builds the manifest and the generated files in a temporary directory outside the folder and writes only the `.aci` and its sidecars (`<name>.slsa.json`, `<name>.aci.attestation.json`) into it. No JSON file is created in the folder, so there is no manifest to go stale: every run re-reads `AGENT.md` and `skills/*.md`, and a skill added since the last package is packed. The folder is the agent's identity; memory, keys and trust live in `~/.loomwork`.
- **Native mode (opt-in).** `loomwork init` in a plain folder materializes `manifest.json`, `skills/graph.json`, `tools/bindings.json`, `sandbox.json` and `memory-schema.json` in the folder (and says so). Those files are then yours to edit, and `package` uses them. In native mode `package` refreshes `skills/graph.json` from `skills/*.md` for a folder-built agent, and warns, naming the files (first 10, then "and N more"), about any regular file the manifest does not cover and will therefore not pack. Hidden entries, archives and sidecars, and `MEMORY.md` are not reported. The exit code is unchanged. `loomwork init --force` regenerates the scaffold.

The exit path is `loomwork leave` (§8.4): the folder is byte-identical to before Loomwork was used, memory is exported, and the conventional workflow still works.

### 6.7 Portable agent folder: Tier 0 and Tier 1 (planned)

*Status: design only; see R-29 to R-38 and R-42 to R-47 in §12.*

**Two problems, one folder.** Two different needs are easy to confuse:

- **A. "My agent should follow me across tools."** Instructions, skills, memory, current work and handoff state move between harnesses and machines. Nothing here needs a new runtime, container format or protocol; a shared folder is enough.
- **B. "Run an agent someone else made, safely."** This needs a signed package, an enforced sandbox and per-record consent. That is the ACI (§6.1 to §6.5) and the runtime.

Loomwork treats the folder as the source of truth for both. The same folder can be used at three tiers, and moving up a tier never restructures it:

| Tier | Contents | Needs Loomwork? | Gives you |
|---|---|---|---|
| **0: Folder** | The layout below | No | Portability: any harness that reads markdown can use it |
| **1: Sealed** | Tier 0 plus `manifest.json` and `state/journal.jsonl` | Only to seal and check | Integrity and history: tampering and half-finished writes are detected |
| **2: Packaged** | Tier 1 plus signature, sandbox spec, consent (the ACI) | Yes | Distribution to other people (§6.1 to §6.5) |

This section specifies Tiers 0 and 1. Tier 2 is the existing ACI, used when an agent is handed to someone else.

#### Tier 0: the folder

Layout (extends the plain-folder input of §6.6):

```
.agent/
├── AGENT.md                 persona / system prompt
├── skills/
│   ├── research/SKILL.md    one skill per directory (skills/*.md files remain accepted)
│   ├── coding/SKILL.md
│   └── review/SKILL.md
└── state/
    ├── MEMORY.md            long-term memory: preferences, facts, decisions, known-bad approaches
    └── HANDOFF.md           where things stand, for the next agent
```

**What `MEMORY.md` is.** It is the agent's long-term memory: the durable things it should know at the start of every session (preferences, facts about the user and their work, decisions, known-bad approaches). It is not the conversation log (the raw layer of §8), not current work (that is `HANDOFF.md`), and not a template. In the two-layer model of §8 it is the *trusted layer*, and it is read into every session, so an unreviewed agent edit to it is exactly a write to trusted memory without approval. It holds the same kinds of content as the typed records of §8.2 (preference, belief, failure, episode summaries), in prose, without their provenance, consent, retention or revocation. Whether and how the two are joined is open question 10.

Rules:

1. **Plain files only.** Every file is UTF-8 markdown. No Loomwork binary is needed to read or write a Tier 0 folder.
2. **Bootstrap.** Each harness gets a short instruction in the file it already reads (for example `CLAUDE.md` or `AGENTS.md`): read `.agent/AGENT.md`, load the skills that apply, read `state/MEMORY.md` and `state/HANDOFF.md`, and follow the write rules below. `loomwork bootstrap <harness>` generates these files; they are text a person can also write by hand.
3. **Write rules for state.** An agent updates `HANDOFF.md` when it stops or hands off, and never overwrites `MEMORY.md` directly: it adds a proposal (below).
4. **`HANDOFF.md` schema.** Front-matter (`writer`, `time`, `previous`) followed by fixed sections: *Done*, *Changed*, *To do*, *Don't repeat*, *Artifacts*, *Decisions*, *For the next agent*. A missing section is allowed; unknown sections are ignored.
5. **Memory proposals.** A harness proposes a change to memory by writing `state/pending/<id>.md` (front-matter with `writer`, `time`, `target`; the body is the proposed addition or edit). A proposal is not memory. The user reviews it (`loomwork memory review`, or by hand: move the text into `MEMORY.md` and delete the file). This is the pending model of §8.2 applied to markdown: pending files expire after the pending TTL and are limited per writer by the pending cap (R-24).
6. **Trust of context.** Content in `state/` is data written by several parties, some of them agents, and may carry prompt injection. The bootstrap tells the agent to treat agent-written lines as claims to check, not as instructions. Lines the user wrote have no such caveat.
7. **Memory does not travel in an ACI.** Consistent with §8.3, `state/` is excluded when a Tier 2 package is built. Moving your own agent between your own machines is not distribution: the whole folder, `state/` included, goes with you.

#### Tier 1: sealed

Tier 1 adds integrity and history without keys or a runtime service.

1. **`manifest.json`** lists every file in the folder with its SHA-256 digest, in the format of §6.2, using the same canonical form (§6.3). Files under `state/` are listed but expected to change; see the journal.
2. **`state/journal.jsonl`** is an append-only log. Each line records `time`, `writer` (a name the writer chooses; not authenticated), `source` (`user`, `agent` or `sync`), `file`, `digest` (of the file after the change) and `previous` (the digest of the previous journal line). A line's own digest is what the next line names as `previous`, which chains the log.
3. **Atomic writes.** `loomwork state write <file>` writes to a temporary file in the same directory, fsyncs it, checks the digest of what was written, renames it over the old file, fsyncs the directory, then appends the journal line. A crash at any point leaves either the old file or the new one, never a partial file; a journal line missing after a crash is reported by `check` and can be repaired with `loomwork state reconcile`.
4. **Commands.**
   - `loomwork seal`: recompute the manifest and append journal lines for changes made outside Loomwork (recorded with `source: user` or `sync`, as chosen).
   - `loomwork check`: exit non-zero if a non-state file differs from the manifest, a state file differs from the latest journal digest, the chain is broken, or a skill directory is not listed.
   - `loomwork state at <time>`: reconstruct the state at that time (requires each journal line to carry a reference to a stored copy; see the open question in §13).
   - `loomwork doctor --folder`: `check`, plus warnings for secret-looking content in `MEMORY.md`, a `HANDOFF.md` older than a chosen age, oversized state files, and unlisted skills.
5. **Handoff receipts.** An agent that reads `HANDOFF.md` records the digest it read in the journal (`source: agent`). This lets the user show which state each run started from.
6. **Sync.** The folder is ordinary files, so Git, Syncthing or any file sync works. A merge conflict in `state/` is resolved by hand, then `loomwork seal` records the result as `source: sync`. Two journals that share a prefix and diverge are reported by `check`; the tool does not merge them.

**What Tier 1 does and does not prove.**

- The hash chain is *tamper-evident*: an edit to a past state file or journal line is detected by `check`.
- It is not *tamper-proof*: anyone who can write the folder can rewrite the whole chain from an earlier point. Stronger provenance comes from signed Git commits, or from Tier 2 signatures.
- `writer` is a label, not an identity (compare §9.6).
- A harness that ignores the bootstrap is not stopped by Tier 1; `check` shows afterwards what changed.
- Tier 1 does not sandbox anything. Enforcement of files, network and consent remains a Tier 2 and runtime matter (§7.2.1, §9.4).

#### 6.7.1 Two lanes: what is automatic and what needs consent

The tiers describe *what* a folder gets. This subsection says *where* the extra files live and *who decides*. The aim is that a user at Tier 0 gets the benefits without doing anything, and can leave without a trace, while the contract of §8.4 (Loomwork never edits the user's own files) still holds.

**Lane A: automatic, nothing written into the folder.**

- **Trigger.** The user runs a `loomwork` command in a folder that holds an `AGENT.md`. There is no daemon and no file watcher; Loomwork does nothing between commands.
- **Shadow store.** Loomwork keeps the Tier 1 and Tier 2 data for that folder under `~/.loomwork/folders/<pathhash>/`: a manifest of digests, a hash-chained journal, handoff receipts and a local key. Typed memory stays in `memory.db` (§8.1). The folder itself is only read.
- **What the user gets without acting.** Enforcement of the sandbox and budgets on `run`, consent-filtered memory views, `check` against the recorded digests, and receipts of which state a run started from.
- **Notice.** The first time, one non-blocking line says that the folder is being tracked under `~/.loomwork`, that no file in it is touched, and how to remove the tracking (`loomwork leave`). It is not a prompt and it is not hidden.
- **Recording is not blessing.** Changes made outside Loomwork are journaled automatically with `source: detected`. `check` compares against the last state the user acknowledged with `seal`, so automatic recording never hides a change.
- **Never automatic, even here.** Signing with a trusted key, `package`, and granting any capability beyond the defaults. A receipt is labelled verified only for a key the user created and trusts. A folder the user did not create, for example a fresh clone, gets the default deny sandbox and no verified label.
- **Guards.** Loomwork refuses to track `$HOME`, `/`, and any folder without a regular-file `AGENT.md`, and follows no symlinked `AGENT.md`.

**Lane B: one consent per folder, for files that must live in the folder.** Other harnesses can only use what is in the folder: the bootstrap line in `CLAUDE.md` or `AGENTS.md`, `state/HANDOFF.md`, `state/pending/`, and an in-folder journal for syncing between machines.

- `loomwork adopt` lists the exact files it would create or change and writes them only after the user agrees for that folder. Files that do not exist are created; an existing user file is changed only by inserting a block between marker lines, never by rewriting it.
- It records an **adoption ledger** in `~/.loomwork/adopted/<pathhash>.json`: each created path with its digest, and each inserted block with the digest of the file before insertion.
- `bootstrap` without `--apply` only prints the snippet.

**Tier 2 stays explicit.** `package` and signing are commands the user runs, with a key the user generated, because they hand content to other people.

**Leave.** See §8.4. Lane A leave deletes the shadow store. Lane B leave reverses the ledger and removes only what still matches it.

**Trade-offs.**

- A shadow journal lives on one machine and does not sync. Users who move between machines use the Lane B in-folder journal.
- The promise is "removes everything Loomwork added; your own files are untouched", not "as if nothing happened": modification times, Git history and editor backup files cannot be rewound.
- Lane A depends on the user running a command; a change made and left unread by any tool is only noticed at the next run.

### 6.8 Multi-agent flow and context manager (implemented in `loomwork flow` and `loomwork tui`)

Loom can coordinate agent CLIs (Claude Code, Codex, pi, Hermes) on one goal without a daemon or any API key of its own.

**Agent CLIs as drivers.** Each CLI runs under the user's own login; Loom spawns it, passes a prompt, and reads stdout. It never reads or forwards credentials (`LOOMWORK_MEMORY_PASSPHRASE` is also removed from the child environment). When Ollama is unavailable, `ask`/`run` may fall back to the first healthy CLI in `~/.loomwork/agents.json`. These drivers are cloud: an automatic fallback needs `--allow-cloud` (naming `--driver` is explicit consent), and typed memory is not sent to them.

**Workflow.** A named JSON file in `.agent/workflows/` orders four stages: *plan* (one agent splits the goal into modules, as validated JSON: unique safe ids, no cycles, disjoint paths), *build* (one agent per module in its own git worktree and branch, parallel up to `max_parallel`, dependents branch from already-merged work), *verify* (one or more different agents, policy `all` or `any`, plus an optional objective `test_cmd`; a rejected module is rebuilt with the issues, up to `retries`), and *ship* (an agent commits the integration branch; push happens only if the stage allows it and the user confirms). A verifier may not be the builder. Unparseable verifier output is a failure. Modules are merged into `loom/<run>` only after verification; the user's checkout is never modified.

**Task board.** Tasks are JSON files under `.agent/state/tasks/`, written atomically, claimed with an atomic lock file and lease. Every stage transition records who did what.

**Context manager.** For every agent invocation Loom runs a context manager that decides what to send: it selects what is relevant to the role and job (goal and repository overview for the planner; goal, dependency specs, files written by dependencies, last test output and verifier feedback for the builder; the patch and test output for the verifier), ranks it, fits it to a per-agent byte budget (truncating or, if configured, summarising; dropping the lowest priority first), and records what was sent and why in `.agent/state/context/`. Per-role and per-agent limits (`.agent/context.json`) set which kinds of context an agent may receive, which paths are hidden (default: `.env`, keys, Loom state), and the budget. Hidden paths are removed from the agent's worktree by sparse checkout.

**Limits.** Visibility control is not a sandbox: an agent that runs git commands or reads outside its directory is not stopped, and agent processes run with the user's own permissions. Agent-written output is treated as data. Identity of the agent is the name Loom launched, not a cryptographic identity.

**Interfaces.** `loomwork tui` (prompt in Ask or Flow mode, workflow editor, context manager view, live status) and `loomwork flow run|status|context|workflows`.

## 7. Runtime

### 7.1 Commands

| Command | Behaviour |
|---|---|
| `loomwork doctor` | Checks Ollama reachability and installed models, signing key, memory store, workspace, spec version |
| `loomwork init [dir]` | Scaffolds a valid agent with computed digests. In a plain folder (§6.6) it materializes the manifest and generated files around the user's own files (native mode) |
| `loomwork ask "q" [--folder d]` | Indexes a folder through the agent's tool bindings and sandbox (listing plus small non-secret text samples) and answers with the local model |
| `loomwork keygen [--out f]` | Generates an Ed25519 key (PKCS#8 PEM, mode 0600) and public key, and trusts it |
| `loomwork trust list\|add\|remove` | Manages the keys whose signatures are accepted |
| `loomwork package [--out f] [--signing-key f]` | Recomputes digests, checks the skills graph, signs, packs only the files the manifest lists, writes a signed provenance statement and a signed attestation bound to the archive. In a plain folder it writes nothing else into the folder (§6.6) |
| `loomwork verify f.aci` | Checks digests, manifest schema, skills graph, signature, that the signer is trusted, and the provenance statement; exits non-zero on failure |
| `loomwork run [flags] f.aci` | Loads and runs an ACI: single task (`--input`) or interactive prompt |
| `loomwork receipt f.aci [--verify]` | Prints or verifies the attestation (signature and archive digest) |
| `loomwork amp token\|serve\|delegate` | Issues capability tokens, serves `amp/delegate` on stdio, delegates a task to a peer |
| `loomwork memory …` | Typed-memory management (§8.2), including `import` and `export` (§8.4) |
| `loomwork check [dir]` | Compares a tracked folder with its last sealed state and verifies the journal chain; exits non-zero on any difference (§6.7.1) |
| `loomwork seal [dir]` | Acknowledges the folder's current state; the only way a change becomes the new reference (§6.7.1) |
| `loomwork leave [--export f] [--delete-store] [--yes]` | Exports memory, stops tracking the current folder, lists what Loomwork kept, optionally deletes the memory store (§8.4) |
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

### 8.4 Import, export and leaving

The user's markdown file is an interface, one direction at a time: import reads a file into pending records; export writes active records out. There is no sync, no round-trip writing, and `MEMORY.md` is never rewritten.

- **`loomwork memory import <file.md> [--kind K] [--sensitivity S] [--allow-agent a,b] [--public]`** reads a file and creates records through the reviewer path. Every imported record is `pending`; nothing is ever activated by import. Two shapes are accepted: (1) the export format below, which restores kind, sensitivity, provenance and consent from its front-matter (new IDs are generated); (2) plain markdown, split on `## ` headings outside code fences, one record per section, kind `belief` (or `--kind`), sensitivity `low`, provenance source `imported`, consent closed to the user alone unless `--allow-agent` or `--public` is given (these flags apply to plain files only). Text before the first `##` is not imported; a file with no `##` sections is an error. The source file is only read: never written, renamed or deleted; symlinks and stdin are refused (a file path is required). **There is no deduplication:** importing the same file twice creates the records twice.
- **`loomwork memory export [--out FILE] [--force]`** emits active records only (not pending, revoked, superseded or expired), to stdout or to FILE (suggested name `memory-export.md`). `--out` refuses to overwrite an existing file without `--force`, and refuses `MEMORY.md` as a target always, even with `--force` or through a symlink.
- **Export format.** One block per record: YAML-style front-matter (`kind`, `id`, `created`, `sensitivity`, `provenance.writer`, `provenance.source`, optional `provenance.aci` and `provenance.sourceUri`, `consent` as `public`, `restricted` or `private` with `consent.allowedAgents` and `consent.allowedScopes` lists, and the typed `payload` as one line of JSON), then the content as markdown. Consent is rendered as fields, not flattened into prose. The format round-trips through import: kind, content, sensitivity, provenance and consent survive export → import → approve → export unchanged, apart from `id` and `created`. It does not preserve IDs, retention, status, derivative links, or the evidence, parent and input references that name other records' old IDs (they are dropped on import); a `created` value becomes the imported record's provenance time. Lossless by convention, not by guarantee: the front-matter is the contract.
- **`loomwork leave [--export FILE] [--delete-store] [--yes]`** is the one-step exit. With `--export` it performs the export above (same refusals) and reports the record count. It prints an inventory: the memory store path and key path (resolved from `$HOME`, not hardcoded), any `*.aci` in the current directory, and that Loomwork never modified the folder's own files. `--delete-store` deletes the memory database, its salt and its key file, and nothing else; the signing key and trust list are kept. Without `--export`, `--delete-store` prints a warning and does nothing unless `--yes` is also given. The store also holds conversation history, which is deleted with it and not exported.
- **`leave` with folder tracking (planned, §6.7.1).** `leave` also removes the shadow store for the current folder, after the export. For a folder that was adopted (Lane B) it reads the adoption ledger and removes each created file only if its digest still matches the ledger, and removes each inserted block only if the text between the markers is unchanged. Anything the user edited since is kept, and `leave` prints it with the reason. It prints what it removed and what it kept. Nothing outside the ledger is ever deleted.

**Contract.** (1) No Loomwork command edits, renames or deletes a file the user wrote (`AGENT.md`, `skills/*.md`, `MEMORY.md`). (2) The folder holds identity; `~/.loomwork` holds state. (3) After `leave`, the folder is byte-identical to before, memory is exported, and the conventional workflow still works. Once Lane B is implemented, this reads: everything Loomwork added and the user has not edited is removed, anything the user edited is kept and reported, and the user's own files are unchanged. (4) The file is the interface, one direction at a time. R-39 is the CI test of rules 1 to 3.

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
**Revocation does not unpublish.** Revoking a record deletes it and what was derived from it in the typed store. It cannot delete lines already exported with `memory export`, or copied out of a file by anyone or anything that read it: export is a copy like any copy, and once content is a line in a file the typed protections (consent, sensitivity, revocation) no longer reach it. Exporting is the user's explicit act of publication. Today `export` does not warn when it writes high-sensitivity records.

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

Serving (`loomwork amp serve`) requires, for each delegate request: AMP version 0.1; provenance signed by a trusted key; the requested skill to be offered by the agent; and a capability token for exactly that skill, unexpired, signed by a trusted key. Otherwise it answers 401 (unauthenticated), 403 (capability denied), 300 (skill not found) or 501 (version). After accepting, the server runs the task and sends an `amp/report` request. Report delivery is fire-and-forget in v0.1: the server does not wait for the caller's acknowledgement, and an acknowledgement that arrives is ignored. Error codes: 500 protocol violation, 501 version mismatch, 300 task not found, 302 budget exceeded, 401, 403.

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
| R-24 | Pending records expire (default 7 days, clamped so a writer's own longer TTL cannot outlive it) and each writer has a pending cap (default 50) | Implemented. Reachable through `loomwork memory propose --as-agent <name>` and the runtime's agent views (`run`/`ask`); the CLI's default reviewer path is exempt |
| R-25 | Pending count shown in the post-answer status line | Implemented |
| R-26 | Opt-in write-gate: proposals staged outside the record store until approved | Implemented (env `LOOMWORK_WRITE_GATE`; off by default). Applies to agent-view proposals: `memory propose --as-agent <name>` and the runtime's agent views; the default reviewer path is never gated |
| R-27 | Plain folder (`AGENT.md`, `skills/*.md`) accepted by `init` and `package`; `MEMORY.md` is never packed; `package` writes only the archive and its sidecars, `init` is the opt-in native mode (§6.6) | Implemented |
| R-28 | Conversation entries capped or covert-channel-resistant | Not implemented (documented limit, §9.6) |
| R-29 | Tier 0: `.agent/` layout (`AGENT.md`, `skills/*/SKILL.md`, `state/MEMORY.md`, `state/HANDOFF.md`) accepted by `init` and `package` | Not implemented (flat `AGENT.md` and `skills/*.md` only, R-27) |
| R-30 | `loomwork bootstrap <harness>` writes the harness's instruction file | Not implemented |
| R-31 | `HANDOFF.md` schema validated and stamped by `loomwork handoff` | Not implemented |
| R-32 | Memory proposals as `state/pending/*.md` with TTL and cap, reviewed by `loomwork memory review` | Not implemented |
| R-33 | Tier 2 packaging excludes `state/` | Partial (`MEMORY.md` is excluded today, R-27; `HANDOFF.md` and `state/` are not yet defined) |
| R-34 | Tier 1: `seal` writes `manifest.json` for the whole folder | Not implemented |
| R-35 | Tier 1: atomic `state write` (temp file, fsync, verify, rename) | Not implemented |
| R-36 | Tier 1: hash-chained `state/journal.jsonl`, checked by `loomwork check` | Not implemented |
| R-37 | Handoff receipts recorded in the journal | Not implemented |
| R-38 | `loomwork doctor --folder` | Not implemented |
| R-39 | Byte-identical lifecycle: package, import, propose (agent, one write-gated), approve, export, `leave --export --delete-store` leave the user's files unchanged and add only the archive, its sidecars and the export files | Implemented (Go test `TestByteIdenticalLifecycle`, runs the built binary with a temporary `HOME`; part of `go test ./...`) |
| R-40 | `loomwork leave [--export FILE] [--delete-store]` exports memory, prints an inventory and deletes only the memory store, never without an export or `--yes` | Implemented (`TestByteIdenticalLifecycle`, `TestLeaveDeleteStoreNeedsExportOrYes`) |
| R-41 | `loomwork memory import` (pending-only, no dedup) and `export` (active-only, never `MEMORY.md`) with a round-tripping front-matter format (§8.4) | Implemented (memory-package round-trip tests; CLI tests) |
| R-42 | Lane A: running `loomwork` in a folder with `AGENT.md` keeps a shadow store under `~/.loomwork/folders/` and writes nothing into the folder | Implemented for `package` and `run <dir>` (`internal/shadow`); `LOOMWORK_NO_TRACK=1` turns it off; refuses `$HOME`, `/`, folders without a regular `AGENT.md` and trees over 2000 files |
| R-43 | Lane A notice printed once per folder, non-blocking, naming the store path and `leave` | Implemented |
| R-44 | Lane A journals outside changes as `source: detected`; `check` compares against the last `seal` | Implemented (`loomwork check`, `loomwork seal`); the journal is hash-chained and `check` also reports a truncated or edited journal. Handoff receipts are not part of Lane A yet |
| R-45 | Signing, `package` and capability grants beyond defaults are never automatic; verified label only for a user-created trusted key; untracked or cloned folders get default deny | Partial: nothing in Lane A signs, packages or grants anything; the verified label already needs a trusted key. Default deny for folders the user did not create is not implemented |
| R-46 | `loomwork adopt` lists exact files, writes only after consent, records the adoption ledger; existing user files changed only by marker-delimited insertion | Not implemented |
| R-47 | `leave` removes the shadow store and reverses the ledger only where digests still match, keeping and reporting anything edited; extends R-39 | Partial: `leave` removes the shadow store of the current folder (Lane A); the ledger reversal for adopted files (Lane B) is not implemented |

## 13. Open design questions

1. **Key distribution.** The trusted-key set is local and manual. Should there be a shared registry, keyless signing through a transparency log, or neither?
2. **Revocation scope.** Typed records cascade; how should conversation entries, summaries and caches built by a runtime be tied to the records they came from?
3. **Agent identity.** Memory consent currently keys on the ACI name. Should identity be the signing key, a DID, or name plus signer?
4. **Enforcement layer.** In-process checks today; when are OS mechanisms (namespaces, seccomp, WASM) required for sandbox and tool scope?
5. **Minimum AMP surface.** Whether delegate and report are enough for useful interoperability, or whether a handshake and cancel are needed.
6. **Write-gate default.** Should the write-gate become the default once agents write typed records themselves, given that the writer then cannot read back its own proposal?
7. **Journal storage.** For `state at <time>`, does the journal store a copy of each state file, store a diff, or rely on Git for history? Storing copies grows the folder; relying on Git makes Tier 1 depend on it.
8. **Harness adapters.** Which harnesses does the project test against, and who maintains those adapters when a harness changes how it reads instructions? An optional MCP server (`read_handoff`, `write_handoff`, `propose_memory`) would give tool-capable harnesses atomic, journaled writes; it is not specified here.
9. **Trust labels.** Should the runtime mark each line of loaded state by provenance (user-written, agent-written, pending) in the prompt, and how should a harness without Loomwork approximate that?
10. **Two forms of long-term memory.** `MEMORY.md` and the typed records of §8.2 hold the same layer. Which is the source of truth: `MEMORY.md` rendered from active typed records (so lines gain provenance and revocation, and a hand edit becomes a proposal), typed records imported from approved `MEMORY.md` lines, or a sidecar that attaches an envelope (writer, time, retention) to each line? Revocation of a line cannot cascade to summaries derived from it unless those are tracked.
11. **Day-one path.** How a first run gets a model and a starter agent with no manual steps, without adding accounts or configuration.
12. **Adoption consent.** Is one agreement per folder the right unit, or should consent be per file kind (bootstrap line, `state/`, journal), and should it expire?
13. **Shadow journal across machines.** Lane A records live on one machine. Is that acceptable, or should Lane A offer an export/import of the journal, or is Lane B the only answer for multi-machine users?

## 14. Roadmap

- **Now:** publish the v0.1 spec and reference runtime; gather reports of real first-run times and failures.
- **Next (portable folder):** Tier 0 (layout, `bootstrap`, handoff schema, memory proposals), then Tier 1 (atomic writes, `seal`, journal, `check`, `doctor --folder`), then harness adapters and a cold-start test per supported harness. Tier 0 is built first: it removes the need for any harness to support Loomwork, and Tier 1 is only worth its habit cost once people use Tier 0.
- **Next:** built-in starter agents and a model-pull step for a true one-command first run; MCP tool execution behind the existing bindings and sandbox; OS-level isolation.
- **Later:** key distribution or keyless signing, additional AMP transports, link conversation memory to the derivatives graph, independent build attestation, optional settlement.

## 15. Contributors

This design has been shaped by public discussion. Contributions credited so far:

| Contributor | Contribution |
|---|---|
| u/Otherwise_Wave9374 | Typed memory records with provenance, consent scope, retention and per-tool access; raw memory in a user-controlled store with filtered agent views; revocation across embeddings, summaries and caches as the acceptance test; NeuraKeep as a reference for persistent agent memory |
| u/Appropriate-Fix4695 | Local-first memory that follows the user rather than the app; keep configuration minimal |
| u/Slight-Living-8098 | Tier 0/1 folder-first design (§6.7) |
| u/Other-Breakfast-3192 | Reference material on ephemeral root filesystems (LogOS discussion #77) |

## Appendix A — Example ACI

`go/examples/` contains three sample agents (`folder-qna-agent`, `git-summarizer-agent`, `meeting-notes-agent`) and a sample folder.

## Appendix B — Files

`go/internal/aci` (format, signing, skills, provenance) · `go/internal/runtime` (runner, memory, sandbox and tool gate) · `go/internal/llm` (Ollama client, model selection) · `go/internal/memory` (typed store, views, revocation) · `go/internal/amp` (envelope, capability tokens, transport, server, credit log) · `go/internal/verify` (SBOM, structural checks, attestation) · `go/internal/secret` (at-rest encryption).
