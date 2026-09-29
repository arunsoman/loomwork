"""AMP Message Envelope — JSON-RPC 2.0 + AMP extensions.

Spec: §5.3 of Loomwork PRD v0.1.

AMP messages are JSON-RPC 2.0 objects with three AMP-specific extensions:
  - amp: protocol metadata (version, traceId, sessionId, ttl)
  - capabilities: signed capability tokens the sender is presenting
  - provenance: traces the request back to its originating ACI

Runtimes MUST reject any message missing these fields when the message is an
AMP-namespaced method (i.e. method starts with "amp/").
"""
from __future__ import annotations

import json
import time
from dataclasses import dataclass, field, asdict
from typing import Any, Optional
from loomwork._ids import new_ulid, new_id

from loomwork.amp.errors import AmpError, ErrorCode

AMP_VERSION = "0.1"
AMP_METHOD_PREFIX = "amp/"


@dataclass
class AmpMetadata:
    """The `amp` field of the envelope."""

    version: str = AMP_VERSION
    traceId: str = field(default_factory=lambda: new_id("trace"))
    sessionId: Optional[str] = None
    ttl: int = 30  # seconds

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class Provenance:
    """The `provenance` field — traces back to the originating ACI."""

    aci: str  # e.g. "loomwork.dev/manager@v1.0.0"
    aciDigest: str  # sha256:...
    runtime: str = "loomwork-py/0.1.0"

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class AmpEnvelope:
    """Full AMP message envelope."""

    jsonrpc: str = "2.0"
    id: str = field(default_factory=lambda: new_id("req"))
    method: Optional[str] = None
    params: Optional[dict[str, Any]] = None
    result: Optional[Any] = None
    error: Optional[dict[str, Any]] = None
    amp: AmpMetadata = field(default_factory=AmpMetadata)
    capabilities: list[dict[str, Any]] = field(default_factory=list)
    provenance: Optional[Provenance] = None

    def is_request(self) -> bool:
        return self.method is not None

    def is_response(self) -> bool:
        return self.method is None and (self.result is not None or self.error is not None)

    def is_amp_method(self) -> bool:
        return bool(self.method and self.method.startswith(AMP_METHOD_PREFIX))

    def to_dict(self) -> dict:
        d: dict[str, Any] = {"jsonrpc": self.jsonrpc, "id": self.id}
        if self.method is not None:
            d["method"] = self.method
        if self.params is not None:
            d["params"] = self.params
        if self.result is not None:
            d["result"] = self.result
        if self.error is not None:
            d["error"] = self.error
        d["amp"] = self.amp.to_dict()
        d["capabilities"] = self.capabilities
        if self.provenance is not None:
            d["provenance"] = self.provenance.to_dict()
        return d

    def to_json(self) -> bytes:
        return json.dumps(self.to_dict(), separators=(",", ":")).encode("utf-8")

    def to_jsonl(self) -> bytes:
        """Newline-delimited (for stdio transport)."""
        return self.to_json() + b"\n"

    @classmethod
    def from_dict(cls, d: dict) -> "AmpEnvelope":
        # Validate required fields for AMP-method requests
        method = d.get("method")
        is_amp = method and method.startswith(AMP_METHOD_PREFIX)

        amp_d = d.get("amp", {})
        caps = d.get("capabilities", [])
        prov_d = d.get("provenance")

        # Strict validation for AMP-method messages
        if is_amp:
            if not amp_d:
                raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                               "AMP-method request missing 'amp' field")
            if "version" not in amp_d:
                raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                               "'amp.version' missing")
            if "traceId" not in amp_d:
                raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                               "'amp.traceId' missing")
            if "capabilities" not in d:
                raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                               "AMP-method request missing 'capabilities' field")
            if prov_d is None:
                raise AmpError(ErrorCode.PROTOCOL_VIOLATION,
                               "AMP-method request missing 'provenance' field")

        amp = AmpMetadata(
            version=amp_d.get("version", AMP_VERSION),
            traceId=amp_d.get("traceId", new_id("trace")),
            sessionId=amp_d.get("sessionId"),
            ttl=amp_d.get("ttl", 30),
        )
        prov = Provenance(**prov_d) if prov_d else None

        return cls(
            jsonrpc=d.get("jsonrpc", "2.0"),
            id=d.get("id", new_id("req")),
            method=method,
            params=d.get("params"),
            result=d.get("result"),
            error=d.get("error"),
            amp=amp,
            capabilities=caps,
            provenance=prov,
        )

    @classmethod
    def from_json(cls, data: bytes | str) -> "AmpEnvelope":
        if isinstance(data, bytes):
            data = data.decode("utf-8")
        return cls.from_dict(json.loads(data))


def parse_message(data: bytes | str) -> AmpEnvelope:
    """Parse a wire message. Raises AmpError on protocol violation."""
    return AmpEnvelope.from_json(data)


def make_request(
    method: str,
    params: dict[str, Any],
    *,
    capabilities: list[dict] | None = None,
    provenance: Provenance | None = None,
    session_id: str | None = None,
    ttl: int = 30,
    request_id: str | None = None,
) -> AmpEnvelope:
    """Build an AMP request envelope."""
    return AmpEnvelope(
        id=request_id or new_id("req"),
        method=method,
        params=params,
        amp=AmpMetadata(sessionId=session_id, ttl=ttl),
        capabilities=capabilities or [],
        provenance=provenance,
    )


def make_response(
    request_id: str,
    result: Any,
    *,
    session_id: str | None = None,
    trace_id: str | None = None,
) -> AmpEnvelope:
    """Build an AMP response envelope (success)."""
    return AmpEnvelope(
        id=request_id,
        result=result,
        amp=AmpMetadata(sessionId=session_id, traceId=trace_id or new_id("trace")),
        # Responses don't carry capabilities/provenance per spec, but the fields
        # exist in the envelope. We leave them empty.
    )


def make_error(
    request_id: str,
    code: ErrorCode,
    message: str,
    *,
    data: dict | None = None,
    session_id: str | None = None,
) -> AmpEnvelope:
    """Build an AMP error response envelope."""
    err = {"code": code.value, "message": message}
    if data:
        err["data"] = data
    return AmpEnvelope(
        id=request_id,
        error=err,
        amp=AmpMetadata(sessionId=session_id),
    )
