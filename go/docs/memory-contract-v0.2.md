# Loomwork Memory Contract v0.2

> Typed records, provenance, consent, retention, and revocation cascade.

## Two layers

- **Conversational entries** are the raw log: auto-persisted by the runtime, never approved, replayed only as recent chat turns to the same agent.
- **Typed records** are the trusted layer: only `active` records reach an agent's prompt.
- **Pending** is persisted but untrusted: visible only to its writer and the reviewer, never in another agent's view or prompt.

## Design principles

1. **Raw memories live in a user-controlled store.** Agents never read it directly; they read through a filtered View adapter.
2. **Agents propose; users (or policy) decide.** Every write starts as `pending`. A reviewer must approve before it becomes `active` (durable).
3. **Every record carries provenance.** Who wrote it, when, from what source, parent ID if derived. You can always answer "where did this belief come from?"
4. **Consent is per-record, not per-store.** A record declares which agents/scopes may see it. High-sensitivity records are visible only to the reviewer (`user`) or to agents holding the `sensitive` scope.
5. **Retention is explicit.** `until_revoked` (default) or `ttl` with a deadline. Expired records are deleted (revoked) the next time the store is opened, and are invisible to Views until then.
6. **Revocation deletes and cascades.** Revoking a record deletes its content and embedding and everything derived from it, including beliefs that cite it as evidence. It is implemented with a derivatives graph, content-free tombstones and SQLite `secure_delete`.

## Five typed record kinds

| Kind | Purpose | Example |
|---|---|---|
| `preference` | User-stated or agent-inferred preference | `code_style.indent = tabs` |
| `episode` | Timestamped interaction event | "User asked about X, agent answered Y" |
| `artifact` | Durable output (doc, code, decision) | `notes.md` (sha256:abc) |
| `belief` | Inferred claim about the world (cited) | "User works at Acme" (evidence: [rec1, rec2]) |
| `failure` | Known-bad-path warning (NeuraKeep first-class) | "Using lib X for task Y fails because Z; do W instead" |

## Common envelope (every record has these)

```json
{
  "id": "mem_01H8...",
  "kind": "belief",
  "status": "pending",
  "sensitivity": "low",
  "provenance": {
    "writerAgentId": "did:key:...",
    "writerAci": "loomwork.dev/agent@v0.1",
    "writtenAt": "2026-09-29T...",
    "source": "agent_inferred",
    "parentId": "mem_01H8...",
    "sourceUri": "file:///..."
  },
  "consent": {
    "allowedAgents": ["did:key:..."],
    "allowedScopes": ["research"],
    "public": false
  },
  "retention": {
    "mode": "until_revoked",
    "ttlSeconds": 86400,
    "expiresAt": "2026-09-30T..."
  },
  "derivatives": ["mem_01H8..."],
  "createdAt": "2026-09-29T...",
  "updatedAt": "2026-09-29T...",
  "revokedAt": null
}
```

## Status lifecycle

```
                   propose
        ┌──────────────────────┐
        │                      ▼
   (nothing)              pending
                             │
              ┌──────────────┼──────────────┐
              │ approve      │ reject       │
              ▼              ▼              │
           active         revoked           │
              │                            │
              │ supersede                  │
              ▼                            │
          superseded                       │
              │                            │
              │ revoke                     │
              └────────────────────────────┘
                          │
                          ▼
                       revoked
                       (terminal)
```

- **pending**: Proposed by an agent, awaiting review. Visible only to the writer and the reviewer. An agent's pending record expires after the pending TTL (default 7 days) unless approved; approval clears the TTL and the record becomes `until_revoked`.
- **active**: Approved. Visible per consent scope.
- **superseded**: Replaced by a newer record. Still readable for audit; filtered out of "current beliefs" queries.
- **revoked**: Content deleted. Cascades to all derivatives. Invisible to all Views. Only a content-free tombstone (ID, kind, timestamps, derivative links) remains. Cannot be undone; propose the fact again if needed.
- **rejected**: A pending proposal the reviewer declined. Content deleted.

## Revocation

Revoking a record must remove its content, embeddings and everything derived from it.

### What is deleted

1. **The record itself** is replaced by a tombstone: ID, kind, status, timestamps and derivative links only. The encrypted payload (provenance, consent, retention, content) is overwritten.
2. **Its embedding** is deleted from the separate `embeddings` table.
3. **All derivative records** are revoked the same way, transitively. An edge exists from a record to every record that names it as `provenance.parentId`, as belief `evidence`, or as an episode `input`.
4. **Freed database pages are overwritten** (`PRAGMA secure_delete = ON`), so deleted content does not linger in the file.
5. **All Views** filter out revoked records.

`DeletedEmbeddings` in the result counts embeddings that actually existed and were removed.

### Not covered

The conversation store used by `loomwork run` / `ask` (`entries`) is separate from typed records and is not linked to them. Summaries or caches a runtime builds outside the typed store are not tracked by the derivatives graph.

### Supersede

`Supersede(old, new)` marks the old record `superseded`. It does not link the records, so revoking the old one does not revoke its replacement.

### Test coverage

`internal/memory/memory_test.go` covers the cascade, content deletion, evidence cascade, encryption at rest, TTL purge and the un-revoke refusal.

## The View adapter (filtered read)

Each agent gets a `View` constructed from the raw store. The View applies:

1. **Consent filter** — only records where the agent is in `allowedAgents`, has a matching scope in `allowedScopes`, or the record is `public`.
2. **Retention filter** — TTL-expired records are invisible.
3. **Sensitivity filter** — high-sensitivity records require the `sensitive` scope (or the reviewer identity).
4. **Status filter** — `pending` visible only to writer; `revoked` and `rejected` invisible to all; `superseded` visible for audit.

Views are read-only. Writes go through `View.Propose()`, which creates a `pending` record. A reviewer View (`NewReviewerView`) can `Approve`, `Reject`, or `Revoke`.

## Pending hygiene

- **TTL:** `LOOMWORK_PENDING_TTL` seconds (default 604800; 0 = no expiry). Applied to every agent proposal: the effective expiry while pending is `min(writer's TTL, pending TTL)`, and a writer-supplied `expiresAt` is discarded. The writer's own retention is remembered and takes effect on approval (a TTL restarts at approval), so an agent cannot opt out of the pending expiry by asking for a long TTL.
- **Cap:** `LOOMWORK_PENDING_CAP` per writer (default 50; 0 = unlimited). Counts pending records and staged proposals. A proposal over the cap is refused with an error.
- **Visibility:** `run`/`ask` print `N pending` in the status line when the agent has proposals awaiting review; `loomwork memory list --status pending` shows them.
- The reviewer view is exempt from TTL and cap.
- Expiry deletes: an expired pending record gets the same tombstone as `reject`/`revoke` (content and embedding removed). Deletion happens when the store is next opened; until then the record is hidden from all views.
- To exercise the agent path from the CLI: `loomwork memory propose ... --as-agent <name>`.

## Write-gate (opt-in)

`LOOMWORK_WRITE_GATE=1` makes agent views stage proposals in a `proposals` table instead of `records`. Staged proposals live in the `proposals` table of `~/.loomwork/memory.db`, with no derivative edges, embeddings or search entries. `loomwork memory list --status pending` shows them (marked `staged`) and `loomwork memory stats` counts them. Expired staged proposals are deleted, not hidden. Only the reviewer can list, approve (which moves the proposal into `records` as `active`) or reject (which deletes it). Tradeoff: the writer cannot read back its own proposal, so it cannot build on it before approval. Off by default.

## Limits: covert channel

A writer can see its own pending records and its own replayed conversational entries, so a compromised agent can use them to hold data or signal across sessions (contents, timing, volume; between ACIs that share a name). TTL, cap, the visible pending count and the write-gate narrow this and make it visible; they do not remove it. Conversational entries are not capped.

## Keeping setup minimal

1. **Zero required config.** `loomwork init` produces a working agent.
2. **Auto-discover everything.** Ollama URL → env var → `localhost:11434`. Signing key → auto-generate on first `package`. Memory DB and its encryption key → auto-create on first use.
3. **No schema migrations.** The typed-record schema is part of the spec. Users pick `kind`; they don't pick fields.
4. **One command to debug.** `loomwork doctor`.
5. **Safe defaults.** Sensitivity `low`, consent "writer only", retention `until_revoked`, status `pending`.

## Using typed memory from an agent

`loomwork run` and `loomwork ask` build a View for the agent (its `metadata.name`) and add its visible, active preferences, beliefs and failure notes to the system prompt. Records made with the CLI are private to the user unless you grant access:

```
loomwork memory propose --kind preference --json '{"key":"indent","value":"tabs","source":"user_stated","confidence":1}' --allow-agent my-agent
loomwork memory approve <id>
```

Agent identity here is the ACI's name, not a cryptographic identity.

## Storage and encryption

Record payloads are encrypted with AES-256-GCM (key from `LOOMWORK_MEMORY_PASSPHRASE` or `~/.loomwork/memory.key`). Only `kind`, `status`, `sensitivity` and timestamps are stored in plaintext columns so queries work.
