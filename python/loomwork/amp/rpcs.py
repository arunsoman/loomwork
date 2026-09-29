"""AMP Core RPCs — PRD §5.5.

Six RPCs in v0.1:
  - amp/delegate (§5.5.1)
  - amp/negotiate (§5.5.2)
  - amp/report (§5.5.3)
  - amp/credit (§5.5.4)
  - amp/subscribe (§5.5.5)
  - amp/cancel (§5.5.5)
"""
from __future__ import annotations

import asyncio
from dataclasses import dataclass, field, asdict
from typing import Any, Callable, Awaitable, Optional
from loomwork._ids import new_ulid, new_id

from loomwork.amp.envelope import (
    AmpEnvelope, Provenance, make_request, make_response, make_error,
)
from loomwork.amp.errors import AmpError, ErrorCode
from loomwork.amp.task_graph import TaskGraph, TaskNode, TaskStatus


# ── Request params ───────────────────────────────────────────────────────────
@dataclass
class DelegateParams:
    """amp/delegate params (§5.5.1)."""

    taskId: str = field(default_factory=lambda: new_id("task"))
    spec: dict[str, Any] = field(default_factory=dict)
    deadline: Optional[str] = None
    budget: dict[str, int] = field(default_factory=dict)

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class NegotiateParams:
    """amp/negotiate params (§5.5.2)."""

    taskId: str = ""
    version: int = 1
    proposedChanges: dict[str, Any] = field(default_factory=dict)
    reason: str = ""

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class ReportParams:
    """amp/report params (§5.5.3)."""

    taskId: str = ""
    status: str = "complete"  # pending, partial, complete, failed
    artifacts: list[dict] = field(default_factory=list)
    usage: dict[str, int] = field(default_factory=dict)
    error: Optional[str] = None

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class CreditParams:
    """amp/credit params (§5.5.4)."""

    receiptId: str = field(default_factory=lambda: new_id("rct"))
    taskId: str = ""
    amount: dict[str, Any] = field(default_factory=dict)
    breakdown: dict[str, Any] = field(default_factory=dict)
    payer: str = ""
    payee: str = ""
    issuedAt: str = ""
    payerSig: Optional[str] = None

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class SubscribeParams:
    """amp/subscribe params (§5.5.5)."""

    eventType: str = "task_complete"  # task_complete, task_failed, budget_alert
    filter: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class CancelParams:
    """amp/cancel params (§5.5.5)."""

    taskId: str = ""
    reason: str = ""

    def to_dict(self) -> dict:
        return asdict(self)


# ── Client (initiator side) ──────────────────────────────────────────────────
class AmpClient:
    """High-level AMP client for sending RPCs to a peer.

    Wraps a Transport + send/recv pair. Each method blocks (async) for the
    response. Use the lower-level envelope + transport if you need streaming.
    """

    def __init__(self, send: Callable[[AmpEnvelope], Awaitable[None]],
                 recv: Callable[[], Awaitable[AmpEnvelope]],
                 *,
                 session_id: Optional[str] = None,
                 provenance: Optional[Provenance] = None):
        self._send = send
        self._recv = recv
        self.session_id = session_id
        self.provenance = provenance

    async def _rpc(self, method: str, params: dict, *, capabilities: list[dict] | None = None) -> AmpEnvelope:
        req = make_request(
            method, params,
            capabilities=capabilities or [],
            provenance=self.provenance,
            session_id=self.session_id,
        )
        await self._send(req)
        resp = await self._recv()
        if resp.id != req.id:
            raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                           f"Response id {resp.id!r} != request id {req.id!r}")
        if resp.error:
            raise AmpError(
                ErrorCode(resp.error["code"]),
                resp.error.get("message", "RPC failed"),
                data=resp.error.get("data"),
            )
        return resp

    async def delegate(self, params: DelegateParams) -> dict:
        """amp/delegate — delegate a subtask to the peer."""
        resp = await self._rpc("amp/delegate", params.to_dict())
        return resp.result

    async def negotiate(self, params: NegotiateParams) -> dict:
        """amp/negotiate — negotiate task parameters."""
        resp = await self._rpc("amp/negotiate", params.to_dict())
        return resp.result

    async def report(self, params: ReportParams) -> dict:
        """amp/report — send progress / partial / final results."""
        resp = await self._rpc("amp/report", params.to_dict())
        return resp.result

    async def credit(self, params: CreditParams) -> dict:
        """amp/credit — settle credit for work performed."""
        resp = await self._rpc("amp/credit", params.to_dict())
        return resp.result

    async def subscribe(self, params: SubscribeParams) -> dict:
        """amp/subscribe — subscribe to peer events."""
        resp = await self._rpc("amp/subscribe", params.to_dict())
        return resp.result

    async def cancel(self, params: CancelParams) -> dict:
        """amp/cancel — cancel an outstanding task."""
        resp = await self._rpc("amp/cancel", params.to_dict())
        return resp.result


# ── Server (responder side) ──────────────────────────────────────────────────
# Each handler is an async callable taking (params_dict, envelope) and returning
# a result dict (or raising AmpError).
HandlerFn = Callable[[dict, AmpEnvelope], Awaitable[dict]]


class AmpServer:
    """Dispatches incoming AMP RPCs to registered handlers.

    Usage:
        server = AmpServer(task_graph=...)
        server.register("amp/delegate", handle_delegate)
        server.register("amp/report", handle_report)
        # ...
        await server.serve(send, recv)
    """

    def __init__(self, *, task_graph: TaskGraph | None = None):
        self._handlers: dict[str, HandlerFn] = {}
        self.task_graph = task_graph or TaskGraph()

    def register(self, method: str, handler: HandlerFn) -> None:
        if not method.startswith("amp/"):
            raise ValueError(f"Method must start with 'amp/', got {method!r}")
        self._handlers[method] = handler

    async def serve(self, send: Callable[[AmpEnvelope], Awaitable[None]],
                    recv: Callable[[], Awaitable[AmpEnvelope]]) -> None:
        """Main loop: recv a request, dispatch to handler, send response."""
        while True:
            try:
                env = await recv()
            except Exception:
                break

            if not env.is_request():
                continue  # ignore non-requests (responses are handled by clients)

            handler = self._handlers.get(env.method or "")
            if handler is None:
                err = make_error(env.id, ErrorCode.PROTOCOL_VIOLATION,
                                 f"Unknown method: {env.method!r}",
                                 session_id=env.amp.sessionId)
                await send(err)
                continue

            try:
                result = await handler(env.params or {}, env)
                resp = make_response(env.id, result,
                                     session_id=env.amp.sessionId,
                                     trace_id=env.amp.traceId)
                await send(resp)
            except AmpError as e:
                err = make_error(env.id, e.code, str(e),
                                 data=e.data, session_id=env.amp.sessionId)
                await send(err)
            except Exception as e:
                err = make_error(env.id, ErrorCode.PROTOCOL_VIOLATION,
                                 f"Handler raised: {e}",
                                 session_id=env.amp.sessionId)
                await send(err)


# ── Default delegate handler ─────────────────────────────────────────────────
async def default_delegate_handler(params: dict, env: AmpEnvelope,
                                    task_graph: TaskGraph) -> dict:
    """Default amp/delegate handler: accept the task, add to graph, return ack."""
    p = DelegateParams(**params)

    # Add to task graph
    task = TaskNode(
        task_id=p.taskId,
        intent=p.spec.get("intent", ""),
        inputs=p.spec.get("inputs", {}),
        expected_outputs=p.spec.get("expectedOutputs", []),
        deadline=p.deadline,
        budget=p.budget,
        status=TaskStatus.RUNNING,
    )
    task_graph.add_task(task)

    return {
        "accepted": True,
        "estimatedCompletion": None,
    }
