"""Tests for AMP handshake (HELLO / CAPABILITIES / ACCEPT)."""
import asyncio
import pytest

from loomwork.amp.envelope import Provenance
from loomwork.amp.handshake import (
    run_handshake_initiator, run_handshake_responder,
)
from loomwork.amp.errors import AmpError, ErrorCode


def _make_queues():
    i2r = asyncio.Queue()
    r2i = asyncio.Queue()
    return i2r, r2i


def test_handshake_happy_path():
    i2r, r2i = _make_queues()

    async def i_send(env): await i2r.put(env)
    async def i_recv(): return await r2i.get()
    async def r_send(env): await r2i.put(env)
    async def r_recv(): return await i2r.get()

    async def run():
        init_task = asyncio.create_task(run_handshake_initiator(
            i_send, i_recv,
            my_agent_id="did:key:ed25519:i",
            my_offered_skills=["delegate", "report"],
            provenance=Provenance(aci="i", aciDigest="sha256:a"),
        ))
        resp_task = asyncio.create_task(run_handshake_responder(
            r_send, r_recv,
            my_agent_id="did:key:ed25519:r",
            my_offered_skills=["research", "summarize"],
            provenance=Provenance(aci="r", aciDigest="sha256:b"),
        ))
        return await asyncio.gather(init_task, resp_task)

    init_result, resp_result = asyncio.run(run())
    init_session, init_caps, _ = init_result
    resp_session, resp_caps, _ = resp_result
    assert init_session == resp_session
    assert "research" in init_caps.offeredSkills
    # Responder accepted the skills the initiator offered that the responder also supports
    # The initiator offers ["delegate", "report"]; the responder offers ["research", "summarize"]
    # The responder accepts the intersection (none, since they don't overlap)
    # and declines the rest.
    assert "delegate" in resp_caps.declinedSkills


def test_handshake_version_mismatch():
    """Responder should reject initiator with wrong ampVersion."""
    i2r, r2i = _make_queues()

    async def i_send(env): await i2r.put(env)
    async def i_recv(): return await r2i.get()
    async def r_send(env): await r2i.put(env)
    async def r_recv(): return await i2r.get()

    async def run():
        # Initiator sends HELLO with version "0.2"
        from loomwork.amp.envelope import make_request
        hello = make_request(
            "amp/hello",
            {"ampVersion": "0.2", "agentId": "i", "offeredSkills": []},
            provenance=Provenance(aci="i", aciDigest="sha256:a"),
        )
        await i_send(hello)

        with pytest.raises(AmpError) as exc:
            await run_handshake_responder(
                r_send, r_recv,
                my_agent_id="r",
                my_offered_skills=[],
                provenance=Provenance(aci="r", aciDigest="sha256:b"),
            )
        assert exc.value.code == ErrorCode.VERSION_MISMATCH

    asyncio.run(run())
