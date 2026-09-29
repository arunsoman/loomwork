# AMP Minimal Subset for v0.1 Conformance

> Answers PRD open question Q7: "What is the minimum viable subset of AMP
> that a runtime must implement to call itself 'AMP-compatible'?"
>
> **Decision:** The four RPCs below are MUST. The rest are SHOULD or MAY.
> This dramatically lowers the bar for independent implementations.

## Conformance levels

| Level | RPCs required | What you can call yourself |
|---|---|---|
| **AMP-Minimal** | `hello`, `accept`, `delegate`, `report` | "AMP-compatible" |
| **AMP-Standard** | Above + `negotiate`, `cancel` | "AMP-complete" |
| **AMP-Full** | Above + `subscribe`, `credit`, `revoke` | "AMP-mesh-ready" |

## The minimal subset (MUST for v0.1)

### 1. `amp/hello` (MUST)

Initiate a session. Declares the initiator's identity, offered skills, and runtime info. The responder returns the session ID, accepted/declined skills, cost model, and sandbox policy.

**Why it's MUST:** Without hello, there's no session. No session means no capability negotiation, no delegation, no credit. Hello is the entry point.

**Minimal implementation:** Accept the hello, return a session ID, accept all offered skills (or decline all). No cost model, no sandbox policy — empty dicts are valid.

### 2. `amp/accept` (MUST)

Finalize the session. The initiator confirms which of the responder's offered skills it wants to use, declares a budget, and presents a capability token.

**Why it's MUST:** Without accept, the session is half-open. Delegate won't work because there's no agreed capability scope.

**Minimal implementation:** Accept the session, return `{"accepted": true}`. No budget enforcement, no capability token verification — those are SHOULD.

### 3. `amp/delegate` (MUST)

Delegate a subtask to a peer. The peer receives a task spec, deadline, and budget; returns either an acknowledgment or an immediate refusal.

**Why it's MUST:** This is the core value of AMP. Without delegate, AMP is just a handshake protocol with nothing to do.

**Minimal implementation:** Accept the task, add it to a local task graph (or just a list), return `{"accepted": true}`. No deadline enforcement, no budget tracking — those are SHOULD.

### 4. `amp/report` (MUST)

Report progress, partial results, or final results on a delegated task. The callee emits one or more report messages; the final report sets `status = "complete"` or `"failed"`.

**Why it's MUST:** Without report, the delegator never learns the outcome. Delegate without report is fire-and-forget, which isn't a protocol — it's a message queue.

**Minimal implementation:** Accept the report, return `{"received": true}`. No artifact verification, no usage tracking — those are SHOULD.

## The standard subset (SHOULD for v0.1)

### 5. `amp/negotiate` (SHOULD)

Negotiate the parameters of a previously-delegated task. Used when the callee realizes the original spec is ambiguous or under-budgeted.

**Why it's SHOULD:** Useful for real-world delegation, but not required for the minimal loop. A minimal impl can refuse all negotiations and the protocol still works (the delegator just gets a refusal).

### 6. `amp/cancel` (SHOULD)

Cancel an outstanding task. Returns the resources consumed so far.

**Why it's SHOULD:** Important for long-running tasks, but a minimal impl can ignore cancellation requests (the task completes or times out).

## The full subset (MAY / deferred for v0.1)

### 7. `amp/subscribe` (MAY)

Subscribe to a stream of events from a peer. Returns a subscription ID that future report messages reference.

**Why it's MAY:** Only needed for mesh-wide observability. A minimal impl doesn't need to subscribe to anything.

### 8. `amp/credit` (MAY — experimental in v0.1)

Settle credit for work performed. The payer issues a signed receipt; the payee countersigns.

**Why it's MAY:** Credit settlement is the most complex part of AMP. It requires a settlement mechanism (Lightning, ledger, manual) and dual-signature verification. For v0.1, credit is **experimental and optional**. The protocol defines the receipt format so future implementations are wire-compatible, but conformant runtimes MAY omit credit entirely.

**Pure logging mode:** A v0.1 runtime MAY implement credit as a pure log writer — every delegation emits a signed receipt to a local log file, no settlement attempted. This preserves the audit trail without the Lightning dependency. See `docs/credit-experimental.md` (TODO).

### 9. `amp/revoke` (MAY)

Revoke a capability token before its expiry.

**Why it's MAY:** Important for security-critical deployments, but a minimal impl can rely on token expiry alone.

## Conformance checklist (for implementers)

To call your runtime "AMP-compatible" (AMP-Minimal), you MUST:

- [ ] Implement `amp/hello` (initiator + responder sides)
- [ ] Implement `amp/accept` (initiator + responder sides)
- [ ] Implement `amp/delegate` (initiator + responder sides)
- [ ] Implement `amp/report` (initiator + responder sides)
- [ ] Support at least one transport (stdio, WebSocket, or libp2p)
- [ ] Validate the AMP envelope (reject messages missing `amp`, `capabilities`, or `provenance` for AMP-method requests)
- [ ] Return proper error codes for the 4 MUST RPCs (at minimum: `PROTOCOL_VIOLATION`, `VERSION_MISMATCH`, `TASK_NOT_FOUND`)

You SHOULD:

- [ ] Implement `amp/negotiate`
- [ ] Implement `amp/cancel`
- [ ] Enforce budgets (`BUDGET_EXCEEDED` error)
- [ ] Enforce deadlines (`DEADLINE_EXCEEDED` error)
- [ ] Detect cycles in the task graph (`CYCLE_DETECTED` error)

You MAY:

- [ ] Implement `amp/subscribe`
- [ ] Implement `amp/credit` (experimental)
- [ ] Implement `amp/revoke`
- [ ] Support multiple transports concurrently
- [ ] Implement Lightning settlement

## Test matrix

The Loomwork reference impl (this repo) passes AMP-Minimal. The test suite includes:

- `test_handshake.py` / `test_handshake.go` — hello + accept round-trip
- `test_end_to_end.py` / `test_end_to_end.go` — full delegate + report loop
- `test_amp.py` — envelope validation, error codes, capability tokens

A third-party impl claiming AMP-Minimal compatibility should be able to run these tests against the reference impl and pass.

## Versioning

The minimal subset is stable for all v0.x releases. Breaking changes require v1.0. Within v0.x, new RPCs MAY be added as MAY-level (never MUST), so existing impls remain compatible.
