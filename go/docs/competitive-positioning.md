# Competitive Positioning

> "We differ because X" is stronger than "we should discuss whether to merge."
> This document analyzes Loomwork against existing and emerging agent-to-agent
> standards. It's the answer to PRD open question Q8.

## The landscape (as of September 2026)

| Project | What it standardizes | Transport | Memory | Credit | Status |
|---|---|---|---|---|---|
| **MCP** (Anthropic) | Agent → tool calls | stdio, WS | — | — | Widely adopted; the lingua franca |
| **OCI Image Format** | Container packaging | — | — | — | Battle-tested at planet scale |
| **A2A Protocol** (Google) | Agent → agent task delegation | HTTP/JSON | Per-agent | — | Emerging; spec published 2025 |
| **ACP** (IBM/BeeAI) | Agent → agent messaging | HTTP, gRPC | — | — | Emerging; framework-coupled |
| **AutoGen** (Microsoft) | Multi-agent orchestration | In-process | Per-conversation | — | Framework; no wire protocol |
| **CrewAI** | Role-based agent teams | In-process | Per-crew | — | Framework; no wire protocol |
| **Mem0 / Zep / NeuraKeep** | Agent memory | SDK / MCP | Vendor-hosted | — | Product, not spec |
| **Loomwork (this repo)** | **Agent packaging (ACI) + agent-to-agent (AMP)** | stdio, WS, (libp2p deferred) | Typed, local-first (spec'd) | Optional, deferred | v0.1 draft, community review |

## What Loomwork borrows (explicitly)

| Borrowed from | What we take | Why |
|---|---|---|
| **OCI Image Format** | Manifest + digests + signature envelope pattern | Battle-tested at planet scale; every dev already has the mental model |
| **MCP** | JSON-RPC 2.0 framing, capability concept, transport-agnostic design | The lingua franca; not competing with it, layering on top |
| **Sigstore / SLSA** | Cosign-compatible envelope shape, in-toto statement format | Industry standard for software supply-chain attestation |
| **NeuraKeep** | Cited memory, agents-propose-humans-decide, failure as first-class | Best-in-class design for agent memory loops |
| **Pocketbase** | Single-binary, instant-value, zero-config positioning | The category-defining exemplar for friction-free dev tools |

## What Loomwork adds (the gap-fillers)

| Gap | What's new in Loomwork | Why no existing standard fills it |
|---|---|---|
| **Portable agent packaging** | ACI: a signed, self-describing archive that bundles persona + skills + tools + sandbox + memory schema | OCI packages software; no one packages *agents* (with their identity, capabilities, and policy) as a portable unit |
| **Agent-to-agent wire protocol** | AMP: peer-to-peer JSON-RPC with capability tokens, credit receipts, task graph | A2A and ACP are HTTP-only and don't address portability or local-first; AutoGen/CrewAI are in-process frameworks with no wire protocol |
| **Typed memory contract with revocation cascade** | 5 record kinds, provenance, consent, retention, derivatives graph | Mem0/Zep/NeuraKeep are products (vendor-hosted or SDK-locked); no open spec for a local-first typed memory store with cascading revocation |
| **Verification gate as packaging primitive** | SBOM + property tests + signed attestation, automatic on every `package` | Bolt/Totalum generate apps but don't verify them; we make verification a non-optional part of the packaging step |

## Head-to-head: Loomwork vs A2A Protocol

| Dimension | A2A Protocol | Loomwork AMP |
|---|---|---|
| **Primary transport** | HTTP/JSON | stdio + WebSocket (libp2p deferred) |
| **Agent identity** | URL (`https://agent.example.com`) | `did:key:ed25519:...` (self-sovereign) |
| **Packaging** | None (agents are HTTP endpoints) | ACI (signed, portable archive) |
| **Memory** | Out of scope | Typed memory contract (v0.2) |
| **Credit** | Out of scope | Optional, deferred (logging mode in v0.1) |
| **Local-first** | No (requires HTTP server) | Yes (stdio transport, Ollama on localhost) |
| **Conformance bar** | Full HTTP server | Minimal subset: hello + accept + delegate + report (see `docs/amp-minimal-subset.md`) |

**Positioning:** A2A is for internet-scale agent services (cloud-hosted, HTTP-reachable). Loomwork is for personal agents (local-first, portable, cross-device). They're complementary; a future Loomwork transport could bridge to A2A for cloud agents.

## Head-to-head: Loomwork vs ACP

| Dimension | ACP (IBM/BeeAI) | Loomwork AMP |
|---|---|---|
| **Scope** | Agent-to-agent messaging | Agent packaging + agent-to-agent |
| **Transport** | HTTP, gRPC | stdio, WebSocket |
| **Framework coupling** | Coupled to BeeAI runtime | Runtime-agnostic (reference impl is Go, but spec is language-neutral) |
| **Memory** | Out of scope | Typed memory contract |
| **Verification** | Out of scope | SBOM + property tests + signed attestation |

**Positioning:** ACP is a messaging protocol for a specific framework ecosystem. Loomwork is a packaging + protocol spec that any runtime can implement. If ACP adopts a portable packaging format, ACI is a candidate.

## Head-to-head: Loomwork vs AutoGen / CrewAI

| Dimension | AutoGen / CrewAI | Loomwork |
|---|---|---|
| **Layer** | Framework (in-process) | Spec + reference runtime |
| **Portability** | None (agents are Python objects) | ACI (signed, cross-runtime) |
| **Wire protocol** | None (in-process function calls) | AMP (JSON-RPC 2.0) |
| **Memory** | Per-conversation, in-memory | Typed, persistent, local-first |
| **Cross-language** | No (Python only) | Yes (spec is language-neutral; Go reference, Python compat) |

**Positioning:** AutoGen and CrewAI are excellent for building multi-agent systems in Python. Loomwork doesn't compete with them — it provides the packaging and wire-format layer that lets an AutoGen agent talk to a non-Python agent. A future `loomwork-autogen` adapter could wrap AutoGen agents as ACIs.

## Head-to-head: Loomwork memory vs Mem0 / Zep / NeuraKeep

| Dimension | Mem0 / Zep | NeuraKeep | Loomwork memory |
|---|---|---|---|
| **Form** | SDK + hosted service | SDK + hosted service | Spec + local-first reference impl |
| **Hosting** | Vendor-hosted by default | Vendor-hosted by default | Local-first (SQLite), hosted optional |
| **Types** | Loose (facts, events) | Loose (facts, events, decisions, failures) | Typed (5 kinds: preference, episode, artifact, belief, failure) |
| **Provenance** | Optional | Cited (source, timestamp, trust) | Required (writer, ACI, source, parent ID, URI) |
| **Consent** | Workspace-level | Workspace-level | Per-record (allowedAgents, allowedScopes, public) |
| **Revocation** | Delete record | Delete record | **Cascade** (revokes all derivatives + deletes embeddings) |
| **Wire format** | SDK-specific | MCP tools | Spec'd (part of ACI memory-schema.json) |

**Positioning:** Mem0/Zep are hosted products. NeuraKeep is a closely related product. Loomwork's contribution is making the memory contract part of the open spec, with local-first as the default and a revocation cascade that actually works (the hardest test from Reddit community feedback).

## The one-sentence pitch (against each competitor)

- **vs MCP**: "MCP made tools portable. Loomwork makes agents portable."
- **vs A2A**: "A2A is for cloud agents. Loomwork is for personal agents that follow you across devices."
- **vs ACP**: "ACP is a messaging protocol for BeeAI. Loomwork is a packaging + protocol spec any runtime can implement."
- **vs AutoGen/CrewAI**: "AutoGen is a Python framework. Loomwork is the spec that lets an AutoGen agent talk to a non-Python agent."
- **vs Mem0/Zep/NeuraKeep**: "Mem0 is a hosted product. Loomwork is an open spec with a local-first reference impl — your memory follows you, not the vendor."

## What we explicitly don't compete with

- **LLM runtimes** (Ollama, vLLM, llama.cpp) — we use them, don't replace them.
- **Sandbox runtimes** (Docker, Firecracker, gVisor) — we declare policy, they enforce it.
- **IDE integrations** (Cursor, Claude Code, Copilot) — we don't replace the editor; we make the agent inside it portable.
- **Model marketplaces** (Hugging Face, Ollama Hub) — we don't host models; we host agents (via ACI).

This is a wedge, not a platform. The platform grows once the unit of packaging (ACI) is adopted.
