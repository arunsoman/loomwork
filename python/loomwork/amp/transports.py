"""AMP Transports — PRD §5.2.

AMP defines three transport bindings in v0.1:
  - WebSocket (mandatory-ish)
  - libp2p (stub — NotImplementedError, real libp2p is a multi-thousand-line dep)
  - stdio (mandatory)

The choice of transport does not affect message semantics, only the framing.
"""
from __future__ import annotations

import asyncio
import json
import sys
from abc import ABC, abstractmethod
from typing import AsyncIterator, Callable, Optional

from loomwork.amp.envelope import AmpEnvelope


class Transport(ABC):
    """Abstract AMP transport. All transports send/receive AmpEnvelope objects."""

    @abstractmethod
    async def send(self, env: AmpEnvelope) -> None:
        """Send a single envelope."""
        ...

    @abstractmethod
    async def recv(self) -> AsyncIterator[AmpEnvelope]:
        """Async iterator of incoming envelopes."""
        ...

    @abstractmethod
    async def close(self) -> None:
        """Close the transport."""
        ...


class StdioTransport(Transport):
    """Newline-delimited JSON-RPC 2.0 over stdin/stdout.

    Used for parent-process ↔ child-agent (e.g. CLI tool spawning an ACI).
    """

    def __init__(self, *, stdin=None, stdout=None):
        self._stdin = stdin or sys.stdin.buffer
        self._stdout = stdout or sys.stdout.buffer
        self._closed = False
        self._recv_queue: asyncio.Queue[AmpEnvelope] = asyncio.Queue()
        self._reader_task: Optional[asyncio.Task] = None

    async def __aenter__(self):
        self._reader_task = asyncio.create_task(self._read_loop())
        return self

    async def __aexit__(self, *exc):
        await self.close()

    async def _read_loop(self):
        loop = asyncio.get_event_loop()
        while not self._closed:
            try:
                line = await loop.run_in_executor(None, self._stdin.readline)
                if not line:
                    break
                line = line.strip()
                if not line:
                    continue
                env = AmpEnvelope.from_json(line)
                await self._recv_queue.put(env)
            except Exception as e:
                # Push parse errors as transport errors? For now, log to stderr
                print(f"stdio transport: parse error: {e}", file=sys.stderr)
                break

    async def send(self, env: AmpEnvelope) -> None:
        if self._closed:
            raise RuntimeError("Transport closed")
        data = env.to_jsonl()
        # Use executor since stdout.buffer.write may block
        loop = asyncio.get_event_loop()
        await loop.run_in_executor(None, self._stdout.write, data)
        await loop.run_in_executor(None, self._stdout.flush)

    async def recv(self) -> AsyncIterator[AmpEnvelope]:
        while not self._closed:
            try:
                env = await asyncio.wait_for(self._recv_queue.get(), timeout=1.0)
                yield env
            except asyncio.TimeoutError:
                continue

    async def close(self) -> None:
        self._closed = True
        if self._reader_task:
            self._reader_task.cancel()
            try:
                await self._reader_task
            except asyncio.CancelledError:
                pass


class WebSocketTransport(Transport):
    """JSON-RPC 2.0 over WebSocket frames.

    For local network, same-machine agents, IDE ↔ runtime.
    Well-known port 7878 + mDNS discovery (mDNS not implemented here).
    """

    def __init__(self, ws):
        """Wrap a websockets.WebSocketClientProtocol or .WebSocketServerProtocol."""
        self._ws = ws
        self._closed = False

    @classmethod
    async def connect(cls, url: str) -> "WebSocketTransport":
        import websockets
        ws = await websockets.connect(url)
        return cls(ws)

    @classmethod
    async def serve(cls, host: str, port: int, handler: Callable) -> "asyncio.Server":
        """Start a WebSocket server. handler is called per-connection with a transport."""
        import websockets

        async def _ws_handler(ws):
            transport = cls(ws)
            await handler(transport)

        return await websockets.serve(_ws_handler, host, port)

    async def send(self, env: AmpEnvelope) -> None:
        if self._closed:
            raise RuntimeError("Transport closed")
        await self._ws.send(env.to_json().decode("utf-8"))

    async def recv(self) -> AsyncIterator[AmpEnvelope]:
        while not self._closed:
            try:
                msg = await self._ws.recv()
                if isinstance(msg, bytes):
                    msg = msg.decode("utf-8")
                yield AmpEnvelope.from_json(msg)
            except Exception:
                self._closed = True
                break

    async def close(self) -> None:
        self._closed = True
        try:
            await self._ws.close()
        except Exception:
            pass


class Libp2pTransport(Transport):
    """libp2p transport — STUB.

    Real libp2p is a multi-thousand-line dependency (libp2p, go-libp2p, rust-libp2p,
    py-libp2p). For the v0.1 reference impl, we stub this out with a clear
    extension point. Production deployments should swap in py-libp2p.

    Spec: §5.2 — 'JSON-RPC 2.0 inside libp2p streams, DHT + PeerId advertise'.
    """

    def __init__(self, *args, **kwargs):
        raise NotImplementedError(
            "libp2p transport is not implemented in v0.1 reference. "
            "Use WebSocketTransport or StdioTransport. Production deployments "
            "should swap in py-libp2p."
        )

    async def send(self, env: AmpEnvelope) -> None:
        raise NotImplementedError

    async def recv(self) -> AsyncIterator[AmpEnvelope]:
        raise NotImplementedError
        # Make this an async generator for type-checkers
        yield  # type: ignore[unreachable]

    async def close(self) -> None:
        raise NotImplementedError
