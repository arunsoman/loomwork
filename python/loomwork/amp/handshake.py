"""AMP Handshake — PRD §5.4.

Before any AMP method can be invoked, two agents MUST complete the capability
negotiation handshake. Three messages: HELLO, CAPABILITIES, ACCEPT.
"""
from __future__ import annotations

import time
from dataclasses import dataclass, field, asdict
from typing import Any, Optional
from loomwork._ids import new_ulid, new_id

from loomwork.amp.envelope import AmpEnvelope, Provenance, make_request, make_response
from loomwork.amp.errors import AmpError, ErrorCode


@dataclass
class HelloParams:
    """amp/hello params."""

    ampVersion: str = "0.1"
    agentId: str = ""  # did:key:...
    offeredSkills: list[str] = field(default_factory=list)
    runtimeInfo: dict[str, str] = field(default_factory=dict)

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class CapabilitiesResult:
    """amp/hello response result."""

    sessionId: str = field(default_factory=lambda: new_id("sess"))
    acceptedSkills: list[str] = field(default_factory=list)
    declinedSkills: list[str] = field(default_factory=list)
    offeredSkills: list[str] = field(default_factory=list)
    costModel: dict[str, Any] = field(default_factory=dict)
    sandboxPolicy: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class AcceptParams:
    """amp/accept params."""

    sessionId: str = ""
    acceptedSkills: list[str] = field(default_factory=list)
    budget: dict[str, int] = field(default_factory=dict)
    capabilityToken: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict:
        return asdict(self)


# ── Initiator side ───────────────────────────────────────────────────────────
async def run_handshake_initiator(
    send,
    recv,
    *,
    my_agent_id: str,
    my_offered_skills: list[str],
    runtime_name: str = "loomwork-py/0.1.0",
    provenance: Provenance | None = None,
) -> tuple[str, CapabilitiesResult, AcceptParams]:
    """Run HELLO → CAPABILITIES → ACCEPT as the initiator.

    Args:
        send: async callable that takes an AmpEnvelope and sends it
        recv: async callable that returns the next AmpEnvelope
        my_agent_id: did:key:... for the initiator
        my_offered_skills: skills the initiator offers to the responder
        provenance: ACI provenance for the initiator

    Returns:
        (session_id, capabilities_result, accept_params)
    """
    # Step 1: HELLO
    hello = make_request(
        "amp/hello",
        HelloParams(
            ampVersion="0.1",
            agentId=my_agent_id,
            offeredSkills=my_offered_skills,
            runtimeInfo={"name": runtime_name.split("/")[0], "version": runtime_name.split("/")[-1]},
        ).to_dict(),
        provenance=provenance,
    )
    await send(hello)

    # Step 2: receive CAPABILITIES (response to HELLO)
    resp = await recv()
    if resp.error:
        raise AmpError(
            ErrorCode(resp.error["code"]),
            resp.error.get("message", "Handshake failed"),
            data=resp.error.get("data"),
        )
    if resp.id != hello.id:
        raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                       f"Response id {resp.id!r} != request id {hello.id!r}")

    caps = CapabilitiesResult(
        sessionId=resp.result.get("sessionId", ""),
        acceptedSkills=resp.result.get("acceptedSkills", []),
        declinedSkills=resp.result.get("declinedSkills", []),
        offeredSkills=resp.result.get("offeredSkills", []),
        costModel=resp.result.get("costModel", {}),
        sandboxPolicy=resp.result.get("sandboxPolicy", {}),
    )

    # Check version compatibility
    if resp.amp.version != "0.1":
        raise AmpError(ErrorCode.VERSION_MISMATCH,
                       f"Responder wants amp version {resp.amp.version!r}, we support 0.1")

    # Step 3: ACCEPT
    accept = AcceptParams(
        sessionId=caps.sessionId,
        acceptedSkills=caps.offeredSkills,  # accept what they offered
        budget={"maxTokens": 50000, "maxWallSeconds": 300},
        capabilityToken={"note": "populated by caller with real CapabilityToken"},
    )
    accept_msg = make_request(
        "amp/accept",
        accept.to_dict(),
        provenance=provenance,
        session_id=caps.sessionId,
    )
    await send(accept_msg)

    # Receive ACCEPT ack
    ack = await recv()
    if ack.error:
        raise AmpError(
            ErrorCode(ack.error["code"]),
            ack.error.get("message", "ACCEPT rejected"),
            data=ack.error.get("data"),
        )

    return caps.sessionId, caps, accept


# ── Responder side ───────────────────────────────────────────────────────────
async def run_handshake_responder(
    send,
    recv,
    *,
    my_agent_id: str,
    my_offered_skills: list[str],
    my_cost_model: dict[str, Any] | None = None,
    my_sandbox_policy: dict[str, Any] | None = None,
    provenance: Provenance | None = None,
) -> tuple[str, CapabilitiesResult, AcceptParams]:
    """Run HELLO ← CAPABILITIES ← ACCEPT as the responder.

    Returns:
        (session_id, capabilities_result_we_sent, accept_params_we_received)
    """
    # Receive HELLO
    hello = await recv()
    if hello.method != "amp/hello":
        raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                       f"Expected amp/hello, got {hello.method!r}")

    if hello.params.get("ampVersion") != "0.1":
        # Send version mismatch error
        err = AmpEnvelope(
            id=hello.id,
            error={"code": 501, "message": "VERSION_MISMATCH: only 0.1 supported"},
            amp=hello.amp,
        )
        await send(err)
        raise AmpError(ErrorCode.VERSION_MISMATCH,
                       f"Initiator wants amp {hello.params.get('ampVersion')!r}")

    # Build CAPABILITIES response
    caps = CapabilitiesResult(
        acceptedSkills=[s for s in hello.params.get("offeredSkills", []) if s in my_offered_skills],
        declinedSkills=[s for s in hello.params.get("offeredSkills", []) if s not in my_offered_skills],
        offeredSkills=my_offered_skills,
        costModel=my_cost_model or {"currency": "loompoint", "per1kTokens": 10},
        sandboxPolicy=my_sandbox_policy or {},
    )
    resp = make_response(hello.id, caps.to_dict(),
                          session_id=caps.sessionId, trace_id=hello.amp.traceId)
    await send(resp)

    # Receive ACCEPT
    accept_msg = await recv()
    if accept_msg.method != "amp/accept":
        raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                       f"Expected amp/accept, got {accept_msg.method!r}")
    accept = AcceptParams(**accept_msg.params)

    # Send ACCEPT ack
    ack = make_response(accept_msg.id, {"accepted": True},
                         session_id=caps.sessionId, trace_id=accept_msg.amp.traceId)
    await send(ack)

    return caps.sessionId, caps, accept
