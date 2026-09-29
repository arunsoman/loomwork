"""Orchestrator — PRD §3.

The orchestrator is the mesh runtime: it spawns ACIs, routes AMP traffic,
manages lifecycle, enforces sandbox policy, hosts the Personal Memory Layer.

This is the central component that ties everything together.
"""
from __future__ import annotations

import asyncio
import json
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Optional
from loomwork._ids import new_ulid, new_id

from loomwork.aci.archive import AciArchive
from loomwork.aci.manifest import Manifest
from loomwork.aci.lifecycle import LifecycleMachine, LifecycleState, LifecycleEvent
from loomwork.aci.signing import VerifyingKey, verify_manifest
from loomwork.aci.conformance import ConformanceChecker
from loomwork.amp.envelope import AmpEnvelope, Provenance
from loomwork.amp.task_graph import TaskGraph
from loomwork.amp.errors import AmpError, ErrorCode
from loomwork.runtime.sandbox import Sandbox, SandboxPolicy
from loomwork.runtime.memory import MemoryLayer, MemoryEntry


@dataclass
class AgentInstance:
    """A loaded ACI + its lifecycle state + sandbox."""

    agent_id: str
    archive: AciArchive
    manifest: Manifest
    lifecycle: LifecycleMachine
    sandbox: Sandbox
    loaded_at: float = field(default_factory=time.time)
    last_activity: float = field(default_factory=time.time)

    @property
    def name(self) -> str:
        return self.manifest.metadata.name

    @property
    def version(self) -> str:
        return self.manifest.metadata.version

    @property
    def state(self) -> LifecycleState:
        return self.lifecycle.state


class Orchestrator:
    """The mesh runtime. Manages a fleet of AgentInstances.

    Responsibilities (PRD §3.1, Mesh Runtime layer):
      - Spawns ACIs
      - Routes AMP traffic
      - Manages lifecycle (cold → warm → live → suspended → terminated)
      - Enforces sandbox policy
      - Hosts Personal Memory Layer
    """

    def __init__(self, *, memory: MemoryLayer | None = None,
                 trust_unsigned: bool = False):
        self._agents: dict[str, AgentInstance] = {}  # agent_id -> instance
        self._by_name: dict[str, str] = {}  # name -> agent_id (latest)
        self._task_graph = TaskGraph()
        self.memory = memory
        self.trust_unsigned = trust_unsigned
        # Event subscribers (per PRD §4.9 — every lifecycle transition emits an event)
        self._lifecycle_subscribers: list = []

    # ── Lifecycle subscription ──────────────────────────────────────────────
    def on_lifecycle_event(self, callback) -> None:
        """Register a callback for ALL agent lifecycle events."""
        self._lifecycle_subscribers.append(callback)

    def _emit_lifecycle(self, event: LifecycleEvent) -> None:
        for cb in self._lifecycle_subscribers:
            try:
                cb(event)
            except Exception:
                pass

    # ── Load ACI ─────────────────────────────────────────────────────────────
    def load_aci(self, aci_path: str | Path, *, agent_id: str | None = None) -> AgentInstance:
        """Load an ACI into the runtime.

        Per conformance C1: MUST verify manifest signature before loading.
        Per conformance C3: MUST reject ACI whose manifest fails JSON Schema validation.

        Args:
            aci_path: Path to .aci file
            agent_id: Optional explicit ID (defaults to ULID)
            trust_unsigned: Override runtime-level trust_unsigned for this load
        """
        aci_path = Path(aci_path)
        data = aci_path.read_bytes()
        archive = AciArchive.from_tar_gz(data)

        # C1: verify signature
        sig_envelope = json.loads(archive.signatures.get("signatures/manifest.sig", b"{}"))
        cert_bytes = archive.signatures.get("signatures/manifest.cert", b"")
        has_sig = bool(sig_envelope) and bool(cert_bytes)
        sig_valid = False
        if has_sig:
            sig_valid = verify_manifest(archive.manifest, sig_envelope, cert_bytes)

        if not has_sig and not self.trust_unsigned:
            raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                           "ACI has no signature and trust_unsigned=False",
                           data={"aci": str(aci_path)})
        if has_sig and not sig_valid:
            raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                           "ACI signature verification failed",
                           data={"aci": str(aci_path)})

        # Load sandbox policy
        sandbox_spec_bytes = archive.get_file(archive.manifest.sandbox.spec)
        sandbox_policy = SandboxPolicy.from_dict(json.loads(sandbox_spec_bytes))
        sandbox = Sandbox(sandbox_policy)

        # Create agent
        agent_id = agent_id or new_id("agent")
        lifecycle = LifecycleMachine(agent_id)
        lifecycle.subscribe(self._emit_lifecycle)

        instance = AgentInstance(
            agent_id=agent_id,
            archive=archive,
            manifest=archive.manifest,
            lifecycle=lifecycle,
            sandbox=sandbox,
        )

        # C1 verified → transition cold → warm
        lifecycle.load()  # cold → warm

        self._agents[agent_id] = instance
        self._by_name[instance.name] = agent_id

        return instance

    def activate(self, agent_id: str) -> LifecycleEvent:
        """Move an agent from warm → live."""
        instance = self._get(agent_id)
        return instance.lifecycle.activate()

    def suspend(self, agent_id: str, reason: str = "idle_timeout") -> LifecycleEvent:
        """Suspend an agent (live → suspended)."""
        instance = self._get(agent_id)
        return instance.lifecycle.suspend(reason=reason)

    def resume(self, agent_id: str) -> LifecycleEvent:
        """Resume a suspended agent (suspended → warm)."""
        instance = self._get(agent_id)
        return instance.lifecycle.resume()

    def terminate(self, agent_id: str, reason: str = "shutdown") -> LifecycleEvent:
        """Terminate an agent. Per C9, SHOULD persist a sealed log bundle."""
        instance = self._get(agent_id)
        event = instance.lifecycle.terminate(reason=reason)
        # C9: persist sealed log bundle
        if self.memory is not None:
            log_entry = MemoryEntry(
                agent_id=agent_id,
                aci=f"{instance.name}@{instance.version}",
                kind="episodic",
                content=f"Agent terminated: reason={reason}",
                metadata={
                    "lifecycle_events": [e.to_dict() for e in instance.lifecycle.events],
                    "final_state": instance.lifecycle.state.value,
                },
            )
            self.memory.write(log_entry)
        # Remove from active registry
        del self._agents[agent_id]
        if self._by_name.get(instance.name) == agent_id:
            del self._by_name[instance.name]
        return event

    # ── Lookup ──────────────────────────────────────────────────────────────
    def get(self, agent_id: str) -> AgentInstance:
        return self._get(agent_id)

    def get_by_name(self, name: str) -> AgentInstance | None:
        agent_id = self._by_name.get(name)
        if agent_id is None:
            return None
        return self._agents.get(agent_id)

    def list_agents(self) -> list[AgentInstance]:
        return list(self._agents.values())

    @property
    def task_graph(self) -> TaskGraph:
        return self._task_graph

    # ── Provenance helper ───────────────────────────────────────────────────
    def provenance_for(self, agent_id: str) -> Provenance:
        instance = self._get(agent_id)
        archive_bytes = instance.archive.to_tar_gz()
        import hashlib
        digest = f"sha256:{hashlib.sha256(archive_bytes).hexdigest()}"
        return Provenance(
            aci=f"loomwork.dev/{instance.name}@{instance.version}",
            aciDigest=digest,
            runtime="loomwork-py/0.1.0",
        )

    def _get(self, agent_id: str) -> AgentInstance:
        if agent_id not in self._agents:
            raise KeyError(f"Agent not loaded: {agent_id!r}")
        return self._agents[agent_id]
