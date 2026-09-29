# Credit: Experimental and Optional in v0.1

> Answers PRD feedback item 4: "Credit / Lightning is ambitious for v0.1.
> For the first version it may be better to treat credit as optional (or
> even deferred) so the core delegation + capability negotiation loop can
> ship faster."

## Decision

**Credit is experimental and optional in v0.1.** The protocol defines the
receipt format (so future implementations are wire-compatible), but
conformant runtimes MAY omit credit entirely. Lightning settlement is
deferred to v0.2+.

## Three modes (implementer's choice)

| Mode | What happens | Audit trail | Settlement | Complexity |
|---|---|---|---|---|
| **Off** (default) | No credit RPCs; delegation is free | None | None | Zero |
| **Logging** | Every delegation emits a signed receipt to a local log | Full | None (manual reconciliation) | Low |
| **Settled** (future) | Receipts are redeemed via Lightning / ledger / manual | Full | Automated | High (v0.2+) |

### Off (default for v0.1)

A v0.1 runtime MAY omit `amp/credit` entirely. Delegations are free. This is
the simplest mode and the one the reference impl ships with by default.

Use this mode when:
- You're running a single-user personal agent mesh (no economic relationship between agents)
- You're testing the delegation loop and don't care about accounting
- You're building a minimal AMP-compatible runtime

### Logging mode (opt-in for v0.1)

A v0.1 runtime MAY implement `amp/credit` as a pure log writer. Every
delegation emits a dual-signed receipt (Ed25519 payer signature + payee
countersignature) to a local log file at `~/.loomwork/credit.log.jsonl`.

The receipt format is identical to the settled mode — so a future upgrade to
Lightning settlement requires no protocol changes, only swapping the log
writer for a Lightning payer.

Use this mode when:
- You want an audit trail of work performed across agents
- You're running a trusted mesh (e.g. your laptop agent + your server agent) and want to see the accounting
- You're testing the credit receipt format before committing to Lightning

### Settled mode (v0.2+, not in v0.1)

A future runtime will redeem logged receipts via:
- **Lightning micropayments** for internet-scale peer-to-peer settlement
- **Batched ledger** for organizational deployments where a single operator settles periodically
- **Manual settlement** for trusted-mesh deployments where receipts are logged but not redeemed

The protocol is settlement-agnostic: the receipt format is identical across
all three.

## Why this is the right call for v0.1

1. **Lightning is a multi-thousand-line dependency.** Adding it to the single
   binary breaks the "Pocketbase for agents" positioning. Deferring keeps the
   binary lean.
2. **Credit isn't required for the viral demo.** The 25-second demo shows
   delegation + report + receipt. The receipt is the audit-trail beat; it
   doesn't need to be settled to be demo-worthy.
3. **The wire format is what matters for v0.1.** If the receipt format is
   right, settlement can be added later without breaking compatibility. If
   we ship the wrong format, we're stuck with it. Logging mode lets us
   validate the format in real usage before committing to Lightning.
4. **Most early adopters don't need credit.** Personal agents on a single
   user's devices don't have an economic relationship. Credit matters for
   commercial agent marketplaces, which are a v0.2+ concern.

## The receipt format (stable across all modes)

```json
{
  "receiptId": "rct_01H8...",
  "taskId": "task_01H8...",
  "amount": { "currency": "loompoint", "value": 73 },
  "breakdown": { "tokens": 50, "toolCalls": 15, "wallSeconds": 8 },
  "payer": "did:key:ed25519:...",
  "payee": "did:key:ed25519:...",
  "issuedAt": "2026-09-29T...",
  "payerSig": "Base64Ed25519Signature",
  "payeeSig": "Base64Ed25519Signature"
}
```

The receipt is dual-signed: payer signs first, payee countersigns. Both
signatures are over the canonical-JSON serialization of the receipt with
the respective signature field omitted.

This format is identical in logging mode and settled mode. A log full of
these receipts can be redeemed by a future Lightning settler without
re-signing.

## Conformance

- **AMP-Minimal:** credit is MAY. A runtime MAY omit `amp/credit` entirely.
- **AMP-Standard:** credit is SHOULD. A runtime SHOULD implement at least logging mode.
- **AMP-Full:** credit is MUST (settled mode). v0.2+.

See `docs/amp-minimal-subset.md` for the full conformance ladder.
