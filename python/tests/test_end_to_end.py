"""End-to-end integration test: build ACI → load → delegate via AMP.

Tests the full PRD §3 architecture in-process:
    1. Build an example ACI
    2. Sign it
    3. Start orchestrator, load ACI
    4. Verify lifecycle transitions fire
    5. Run AMP handshake + delegate via in-process transport
"""
import asyncio
import json
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).parent.parent


def _setup_signed_aci(tmp_path: Path) -> Path:
    """Build a signed, SLSA-attested ACI from the research-agent example."""
    src = REPO_ROOT / "examples" / "research-agent"
    dst = tmp_path / "research-agent"
    shutil.copytree(src, dst)

    # Compute digests
    subprocess.run(
        [sys.executable, str(REPO_ROOT / "scripts" / "compute_digests.py"), str(dst)],
        check=True, capture_output=True,
    )

    # Sign
    from loomwork.aci.signing import SigningKey, sign_aci
    from loomwork.aci.slsa import build_slsa_attestation
    from loomwork.aci.manifest import Manifest
    from loomwork.aci.archive import AciArchive

    sk = SigningKey.generate()
    sign_aci(dst, sk)

    # Build SLSA attestation
    manifest = Manifest.from_json((dst / "manifest.json").read_bytes())
    archive = AciArchive.from_directory(dst)
    archive_bytes = archive.to_tar_gz()
    attestation = build_slsa_attestation(manifest, archive_bytes, source_repo=dst)
    slsa_path = dst / "signatures" / "slsa.intoto.json"
    slsa_path.write_bytes(attestation.to_json())

    # Pack
    from loomwork.aci.archive import pack_aci
    return pack_aci(dst, tmp_path / "research.aci")


def test_end_to_end_load_and_lifecycle(tmp_path):
    """End-to-end: build ACI, load into orchestrator, verify lifecycle."""
    aci_path = _setup_signed_aci(tmp_path)

    from loomwork.runtime.orchestrator import Orchestrator
    from loomwork.runtime.memory import MemoryLayer
    from loomwork.aci.lifecycle import LifecycleState

    memory = MemoryLayer(tmp_path / "mem.db", passphrase="test")
    orch = Orchestrator(memory=memory)

    # Subscribe to lifecycle events
    events = []
    orch.on_lifecycle_event(events.append)

    # Load ACI — should transition cold → warm
    instance = orch.load_aci(aci_path)
    assert instance.state == LifecycleState.WARM
    assert len(events) == 1
    assert events[0].to_state == LifecycleState.WARM

    # Activate — warm → live
    orch.activate(instance.agent_id)
    assert instance.state == LifecycleState.LIVE
    assert len(events) == 2

    # Suspend
    orch.suspend(instance.agent_id)
    assert instance.state == LifecycleState.SUSPENDED
    assert len(events) == 3

    # Resume
    orch.resume(instance.agent_id)
    assert instance.state == LifecycleState.WARM
    assert len(events) == 4

    # Terminate — should also write a memory entry
    orch.terminate(instance.agent_id)
    assert len(events) == 5
    assert events[-1].to_state == LifecycleState.TERMINATED

    # Verify memory has the termination log (the episodic event is "kind:entry_id")
    timeline = memory.episodic_timeline(limit=10)
    # The terminate event wrote an episodic memory entry; verify something was logged
    assert len(timeline) > 0
    # Cross-check: query the entries table directly for "terminated" content
    from loomwork.runtime.memory import MemoryEntry
    all_entries = []
    for e in timeline:
        if e["entry_id"]:
            entry = memory.get(e["entry_id"])
            if entry:
                all_entries.append(entry.content)
    assert any("terminated" in c.lower() for c in all_entries), \
        f"No 'terminated' content in memory entries: {all_entries}"


def test_end_to_end_orchestrator_rejects_unsigned(tmp_path):
    """Orchestrator MUST reject unsigned ACI by default (conformance C1)."""
    # Build WITHOUT signing
    src = REPO_ROOT / "examples" / "research-agent"
    dst = tmp_path / "research-agent"
    shutil.copytree(src, dst)
    subprocess.run(
        [sys.executable, str(REPO_ROOT / "scripts" / "compute_digests.py"), str(dst)],
        check=True, capture_output=True,
    )
    from loomwork.aci.archive import pack_aci
    aci_path = pack_aci(dst, tmp_path / "research.aci")

    from loomwork.runtime.orchestrator import Orchestrator
    orch = Orchestrator(trust_unsigned=False)

    from loomwork.amp.errors import AmpError, ErrorCode
    with pytest.raises(AmpError) as exc:
        orch.load_aci(aci_path)
    assert exc.value.code == ErrorCode.PROTOCOL_VIOLATION


def test_end_to_end_amp_handshake_in_process(tmp_path):
    """Run an AMP HELLO/CAPABILITIES/ACCEPT handshake via in-memory transport."""
    from loomwork.amp.envelope import AmpEnvelope, Provenance
    from loomwork.amp.handshake import (
        run_handshake_initiator, run_handshake_responder,
        HelloParams, CapabilitiesResult, AcceptParams,
    )

    # Set up two in-process async queues as a transport
    initiator_to_responder = asyncio.Queue()
    responder_to_initiator = asyncio.Queue()

    async def initiator_send(env): await initiator_to_responder.put(env)
    async def initiator_recv(): return await responder_to_initiator.get()
    async def responder_send(env): await responder_to_initiator.put(env)
    async def responder_recv(): return await initiator_to_responder.get()

    async def run():
        # Run initiator and responder concurrently
        init_task = asyncio.create_task(run_handshake_initiator(
            initiator_send, initiator_recv,
            my_agent_id="did:key:ed25519:initiator",
            my_offered_skills=["delegate", "report"],
            provenance=Provenance(aci="loomwork.dev/init@v0.1",
                                  aciDigest="sha256:" + "a" * 64),
        ))
        resp_task = asyncio.create_task(run_handshake_responder(
            responder_send, responder_recv,
            my_agent_id="did:key:ed25519:responder",
            my_offered_skills=["research", "summarize"],
            my_cost_model={"currency": "loompoint", "per1kTokens": 10},
            provenance=Provenance(aci="loomwork.dev/resp@v0.1",
                                  aciDigest="sha256:" + "b" * 64),
        ))

        init_result, resp_result = await asyncio.gather(init_task, resp_task)

        # Both should report the same session id
        init_session_id, init_caps, init_accept = init_result
        resp_session_id, resp_caps, resp_accept = resp_result

        assert init_session_id == resp_session_id
        # Initiator's view of responder's offered skills should match
        assert "research" in init_caps.offeredSkills
        # Responder declines skills it doesn't offer (delegate is not in responder's offered list)
        assert "delegate" in resp_caps.declinedSkills

    asyncio.run(run())


def test_end_to_end_amp_delegate_in_process(tmp_path):
    """End-to-end: handshake → delegate → accept."""
    from loomwork.amp.envelope import AmpEnvelope, Provenance, make_request, make_response
    from loomwork.amp.handshake import run_handshake_initiator, run_handshake_responder
    from loomwork.amp.rpcs import AmpClient, AmpServer, DelegateParams, default_delegate_handler
    from loomwork.amp.task_graph import TaskGraph
    from loomwork.amp.errors import AmpError, ErrorCode

    # Two queues for bidirectional transport
    i2r = asyncio.Queue()
    r2i = asyncio.Queue()

    async def i_send(env): await i2r.put(env)
    async def i_recv(): return await r2i.get()
    async def r_send(env): await r2i.put(env)
    async def r_recv(): return await i2r.get()

    task_graph = TaskGraph()

    async def run():
        # Run handshake on both sides in parallel
        init_task = asyncio.create_task(run_handshake_initiator(
            i_send, i_recv,
            my_agent_id="did:key:ed25519:i",
            my_offered_skills=["delegate"],
            provenance=Provenance(aci="i", aciDigest="sha256:a"),
        ))
        resp_task = asyncio.create_task(run_handshake_responder(
            r_send, r_recv,
            my_agent_id="did:key:ed25519:r",
            my_offered_skills=["research"],
            provenance=Provenance(aci="r", aciDigest="sha256:b"),
        ))
        session_id, _, _ = await init_task
        await resp_task

        # Now do amp/delegate. Server side: register handler and serve.
        server = AmpServer(task_graph=task_graph)
        server.register("amp/delegate",
                        lambda p, e: default_delegate_handler(p, e, task_graph))

        # Run server in background
        server_task = asyncio.create_task(server.serve(r_send, r_recv))

        # Client: send delegate
        client = AmpClient(i_send, i_recv, session_id=session_id,
                           provenance=Provenance(aci="i", aciDigest="sha256:a"))
        params = DelegateParams(
            spec={"intent": "research", "inputs": {"topic": "ACI"}},
            budget={"maxTokens": 8000},
        )
        result = await client.delegate(params)

        assert result["accepted"] is True
        # Verify task is in graph
        assert len(task_graph.all_tasks()) == 1
        assert task_graph.all_tasks()[0].intent == "research"

        server_task.cancel()

    asyncio.run(run())
