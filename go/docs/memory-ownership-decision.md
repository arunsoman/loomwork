# Memory Layer Ownership Decision

> Answers PRD open question Q4: "Should the Memory Layer be part of the ACI
> spec, a separate spec, or left to runtime implementations entirely?"
>
> **Decision:** Minimal schema + provenance requirements are part of ACI
> v0.1 (the portable unit is self-describing). The full hybrid store
> (vector + graph + episodic, sync, encryption) is a runtime concern,
> deferred to the v0.2 typed-memory spec.

## What's in ACI v0.1 (the portable unit)

Every ACI MUST include `memory-schema.json`. This file declares:

1. **What memory kinds the agent expects** (subset of: preference, episode, artifact, belief, failure)
2. **Provenance requirements** (does this agent require cited sources? does it write derived records?)
3. **Sensitivity levels the agent may produce** (low, medium, high)
4. **Retention defaults** (until_revoked, or TTL in seconds)

This makes the portable unit self-describing: a runtime loading an ACI
knows what shape of memory the agent will read and write, without having
to run the agent first.

### Minimal `memory-schema.json` (required)

```json
{
  "$schema": "https://loomwork.dev/schemas/memory/v0.1.json",
  "kinds": ["preference", "episode", "artifact"],
  "provenance": {
    "required": true,
    "citedSources": true
  },
  "sensitivity": ["low", "medium"],
  "retention": {
    "default": "until_revoked",
    "maxTtlSeconds": 7776000
  }
}
```

This is enough for a runtime to:
- Allocate the right storage (e.g. a SQL table per kind, or a single typed table)
- Enforce provenance (reject memory writes that lack source citations if `citedSources: true`)
- Enforce sensitivity (refuse to persist `high` sensitivity if the agent only declared `low, medium`)
- Enforce retention (apply TTL expiry)

### What's NOT in ACI v0.1 (deferred to runtime)

- The storage engine (SQLite, Postgres, vector DB, etc.)
- Encryption (AES-256-GCM, etc.)
- Sync (libp2p, CRDT, etc.)
- The derivatives graph for revocation cascade
- The agents-propose-humans-decide lifecycle

These are all runtime concerns. The reference Go runtime implements them
(see `internal/memory/`), but they're not required for ACI v0.1
conformance. A minimal runtime could implement memory as a JSON file on
disk and still be ACI-compatible.

## Why this split

1. **The portable unit must be self-describing.** If memory shape isn't in
   the ACI, a runtime can't prepare storage before loading the agent. This
   creates a chicken-and-egg: the agent needs memory to run, but the
   runtime needs to know what memory shape to allocate.
2. **The storage engine is an implementation detail.** SQLite vs Postgres
   vs a vector DB is a runtime choice, not a spec choice. Forcing one
   would make the spec less portable, not more.
3. **Sync and encryption are deployment concerns.** A single-user laptop
   runtime doesn't need sync. A multi-user enterprise runtime needs both.
   The spec shouldn't force either.
4. **The derivatives graph is the runtime's job.** The spec defines what
   revocation means (the record + all derivatives become invisible); the
   runtime implements the cascade. Different runtimes may use different
   graph structures.

## What this means for implementers

If you're building a Loomwork-compatible runtime:

1. **You MUST** parse `memory-schema.json` from every ACI you load.
2. **You MUST** allocate storage for the declared kinds.
3. **You MUST** enforce provenance requirements (reject writes that don't match).
4. **You MUST** enforce sensitivity (reject writes above the declared levels).
5. **You MUST** enforce retention defaults (apply TTL if declared).
6. **You MAY** implement the full v0.2 typed-memory contract (5 kinds, derivatives graph, revocation cascade, agents-propose-humans-decide).
7. **You MAY** implement sync, encryption, vector search — none are required.

If you're building a Loomwork-compatible ACI (packaging an agent):

1. **You MUST** include `memory-schema.json` in the archive.
2. **You MUST** declare at least one kind.
3. **You SHOULD** declare provenance requirements (most agents should cite sources).
4. **You SHOULD** declare sensitivity (defaults to `low` if omitted).
5. **You MAY** declare a TTL (defaults to `until_revoked` if omitted).

## Relationship to the v0.2 typed-memory contract

The v0.2 typed-memory contract (see `docs/memory-contract-v0.2.md`) is the
reference runtime's implementation of the v0.1 schema requirements. It
adds:

- 5 specific record kinds (the schema's `kinds` field is a subset selector)
- Provenance as a structured field (not just a boolean)
- Consent scope per record
- Retention as a structured field
- Derivatives graph for revocation cascade
- Agents-propose-humans-decide lifecycle

A runtime that implements the v0.2 contract is ACI v0.1 + Memory v0.2
compatible. A runtime that implements only the v0.1 schema is ACI v0.1
compatible (but may not support all the features a v0.2-runtime-produced
agent expects).

## What this means for the spec versions

- **ACI v0.1** (this release): `memory-schema.json` is required; minimal
  fields. Portable unit is self-describing.
- **Memory v0.2** (this release, reference impl only): full typed contract.
  Not required for ACI v0.1 conformance, but the reference impl ships it.
- **ACI v1.0** (future): may fold Memory v0.2 into the ACI spec if the
  reference impl's contract proves stable in real usage.

This keeps v0.1 shippable while the v0.2 contract matures.
