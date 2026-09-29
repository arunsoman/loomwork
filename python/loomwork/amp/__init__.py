"""AMP — Agent Mesh Protocol.

Spec: §5 of Loomwork PRD v0.1.

AMP is a peer-to-peer JSON-RPC 2.0 wire protocol for agent-to-agent
communication. It is layered strictly on top of MCP at the conceptual level
(an agent that speaks AMP also speaks MCP), but uses independent transports
and message namespaces.
"""

from loomwork.amp.envelope import (
    AmpEnvelope,
    AmpMetadata,
    Provenance,
    parse_message,
    make_request,
    make_response,
    make_error,
)
from loomwork.amp.errors import AmpError, ErrorCode, ERROR_REGISTRY
from loomwork.amp.capability import CapabilityToken, issue_capability, verify_capability
from loomwork.amp.credit import CreditReceipt, sign_receipt, countersign_receipt
from loomwork.amp.task_graph import TaskGraph, TaskNode, TaskStatus
from loomwork.amp.handshake import (
    HelloParams,
    CapabilitiesResult,
    AcceptParams,
    run_handshake_initiator,
    run_handshake_responder,
)
from loomwork.amp.transports import (
    Transport,
    StdioTransport,
    WebSocketTransport,
    Libp2pTransport,
)
from loomwork.amp.rpcs import (
    DelegateParams,
    NegotiateParams,
    ReportParams,
    CreditParams,
    SubscribeParams,
    CancelParams,
    AmpClient,
    AmpServer,
)

__all__ = [
    "AmpEnvelope", "AmpMetadata", "Provenance",
    "parse_message", "make_request", "make_response", "make_error",
    "AmpError", "ErrorCode", "ERROR_REGISTRY",
    "CapabilityToken", "issue_capability", "verify_capability",
    "CreditReceipt", "sign_receipt", "countersign_receipt",
    "TaskGraph", "TaskNode", "TaskStatus",
    "HelloParams", "CapabilitiesResult", "AcceptParams",
    "run_handshake_initiator", "run_handshake_responder",
    "Transport", "StdioTransport", "WebSocketTransport", "Libp2pTransport",
    "DelegateParams", "NegotiateParams", "ReportParams",
    "CreditParams", "SubscribeParams", "CancelParams",
    "AmpClient", "AmpServer",
]
