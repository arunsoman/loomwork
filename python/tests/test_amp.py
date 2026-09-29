"""Tests for AMP envelope, errors, capability tokens, credit, task graph."""
import json
import time
import pytest

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey

from loomwork.amp.envelope import (
    AmpEnvelope, AmpMetadata, Provenance, make_request, make_response, make_error,
    parse_message,
)
from loomwork.amp.errors import AmpError, ErrorCode, ERROR_REGISTRY
from loomwork.amp.capability import (
    CapabilityToken, issue_capability, verify_capability, did_to_public_key,
)
from loomwork.amp.credit import (
    CreditReceipt, sign_receipt, countersign_receipt, verify_receipt,
)
from loomwork.amp.task_graph import TaskGraph, TaskNode, TaskStatus


# ── Envelope ─────────────────────────────────────────────────────────────────
def test_envelope_request_round_trip():
    env = make_request(
        "amp/delegate",
        {"taskId": "task_1", "spec": {"intent": "research"}},
        provenance=Provenance(aci="loomwork.dev/test@v0.1", aciDigest="sha256:abc"),
    )
    json_bytes = env.to_json()
    parsed = parse_message(json_bytes)
    assert parsed.method == "amp/delegate"
    assert parsed.params["taskId"] == "task_1"
    assert parsed.provenance.aci == "loomwork.dev/test@v0.1"


def test_envelope_rejects_amp_method_without_amp_field():
    """AMP-method request missing 'amp' field MUST be rejected (PROTOCOL_VIOLATION)."""
    bad = {
        "jsonrpc": "2.0",
        "id": "req_1",
        "method": "amp/delegate",
        "params": {},
        # no "amp" field
        "capabilities": [],
        "provenance": {"aci": "x", "aciDigest": "y"},
    }
    with pytest.raises(AmpError) as exc:
        AmpEnvelope.from_dict(bad)
    assert exc.value.code == ErrorCode.PROTOCOL_VIOLATION


def test_envelope_rejects_amp_method_without_capabilities():
    bad = {
        "jsonrpc": "2.0",
        "id": "req_1",
        "method": "amp/delegate",
        "params": {},
        "amp": {"version": "0.1", "traceId": "t1", "ttl": 30},
        # no "capabilities" field
        "provenance": {"aci": "x", "aciDigest": "y"},
    }
    with pytest.raises(AmpError) as exc:
        AmpEnvelope.from_dict(bad)
    assert exc.value.code == ErrorCode.PROTOCOL_VIOLATION


def test_envelope_response_does_not_require_amp_fields():
    """Plain JSON-RPC responses don't need capabilities/provenance."""
    env = make_response("req_1", {"ok": True})
    json_bytes = env.to_json()
    parsed = parse_message(json_bytes)
    assert parsed.result == {"ok": True}


def test_envelope_error_round_trip():
    env = make_error("req_1", ErrorCode.TASK_NOT_FOUND, "no such task",
                     data={"taskId": "task_99"})
    parsed = parse_message(env.to_json())
    assert parsed.error["code"] == 300
    assert parsed.error["data"]["taskId"] == "task_99"


# ── Errors ───────────────────────────────────────────────────────────────────
def test_error_registry_complete():
    """All 15 error codes from PRD §5.9 must be in the registry."""
    assert len(ERROR_REGISTRY) == 15
    # 1xx transport
    assert ErrorCode.TRANSPORT_ERROR == 100
    assert ErrorCode.TTL_EXPIRED == 101
    # 2xx capability
    assert ErrorCode.CAPABILITY_MISSING == 200
    # 3xx task
    assert ErrorCode.TASK_NOT_FOUND == 300
    # 4xx credit
    assert ErrorCode.CURRENCY_MISMATCH == 400
    # 5xx protocol
    assert ErrorCode.PROTOCOL_VIOLATION == 500
    assert ErrorCode.CYCLE_DETECTED == 502


def test_error_retry_policy():
    """Some errors are retryable, some aren't."""
    assert ERROR_REGISTRY[ErrorCode.TRANSPORT_ERROR].retry is True
    assert ERROR_REGISTRY[ErrorCode.CAPABILITY_MISSING].retry is False
    assert ERROR_REGISTRY[ErrorCode.BUDGET_EXCEEDED].retry is True


# ── Capability tokens ────────────────────────────────────────────────────────
def test_capability_token_issue_verify():
    sk = Ed25519PrivateKey.generate()
    token = issue_capability(
        issuer_key=sk, issuer_did="",
        skill="search_web",
        scope={"paths": ["/tmp/**"]},
        ttl_seconds=3600,
    )
    assert token.signature is not None

    # Verify
    pk = sk.public_key()
    assert verify_capability(token, pk)


def test_capability_token_expires():
    sk = Ed25519PrivateKey.generate()
    token = issue_capability(
        issuer_key=sk, issuer_did="",
        skill="search_web",
        ttl_seconds=3600,
    )
    pk = sk.public_key()
    # Valid now
    assert verify_capability(token, pk, now=time.time())
    # Expired 2 hours ago
    assert not verify_capability(token, pk, now=time.time() + 7200)


def test_capability_token_revoked():
    sk = Ed25519PrivateKey.generate()
    token = issue_capability(
        issuer_key=sk, issuer_did="",
        skill="search_web",
        ttl_seconds=3600,
    )
    pk = sk.public_key()
    assert not verify_capability(token, pk, revoked={token.id})


# ── Credit receipts ──────────────────────────────────────────────────────────
def test_credit_receipt_dual_sign():
    payer_sk = Ed25519PrivateKey.generate()
    payee_sk = Ed25519PrivateKey.generate()

    receipt = sign_receipt(
        payer_key=payer_sk,
        payer_did="did:key:ed25519:payer",
        payee_did="did:key:ed25519:payee",
        task_id="task_1",
        amount={"currency": "loompoint", "value": 73},
        breakdown={"tokens": 50, "toolCalls": 15, "wallSeconds": 8},
    )
    assert receipt.payerSig is not None
    assert receipt.payeeSig is None

    # Countersign
    countersign_receipt(receipt, payee_sk)
    assert receipt.payeeSig is not None

    # Verify
    assert verify_receipt(receipt, payer_sk.public_key(), payee_sk.public_key())


def test_credit_receipt_rejects_tampered():
    payer_sk = Ed25519PrivateKey.generate()
    payee_sk = Ed25519PrivateKey.generate()

    receipt = sign_receipt(
        payer_key=payer_sk,
        payer_did="did:key:ed25519:payer",
        payee_did="did:key:ed25519:payee",
        task_id="task_1",
        amount={"currency": "loompoint", "value": 73},
        breakdown={"tokens": 50},
    )
    countersign_receipt(receipt, payee_sk)

    # Tamper
    receipt.amount["value"] = 9999

    assert not verify_receipt(receipt, payer_sk.public_key(), payee_sk.public_key())


# ── Task graph ───────────────────────────────────────────────────────────────
def test_task_graph_add_and_get():
    g = TaskGraph()
    t = g.add_task(TaskNode(intent="research"))
    assert g.get(t.task_id).intent == "research"


def test_task_graph_rejects_cycle():
    """PRD §5.6: cycles MUST be detected and rejected (CYCLE_DETECTED 502)."""
    g = TaskGraph()
    t1 = g.add_task(TaskNode(intent="t1"))
    t2 = g.add_task(TaskNode(parent_id=t1.task_id, intent="t2"))
    t3 = g.add_task(TaskNode(parent_id=t2.task_id, intent="t3"))

    # Try to make t3 the parent of t1 — would create a cycle
    with pytest.raises(AmpError) as exc:
        g.add_task(TaskNode(task_id=t1.task_id, parent_id=t3.task_id, intent="cycle"))
    assert exc.value.code == ErrorCode.CYCLE_DETECTED


def test_task_graph_budget_exceeded():
    """PRD §5.5.1: budget.maxTokens MUST be enforced (BUDGET_EXCEEDED 302)."""
    g = TaskGraph()
    t = g.add_task(TaskNode(intent="research", budget={"maxTokens": 1000}))
    g.consume_budget(t.task_id, tokens=500)
    g.consume_budget(t.task_id, tokens=400)
    assert g.get(t.task_id).budget_consumed["tokens"] == 900

    with pytest.raises(AmpError) as exc:
        g.consume_budget(t.task_id, tokens=200)  # total would be 1100 > 1000
    assert exc.value.code == ErrorCode.BUDGET_EXCEEDED


def test_task_graph_terminal_state_rejects_status_change():
    """PRD §5.6: terminal tasks cannot be mutated (TASK_ALREADY_COMPLETE 301)."""
    g = TaskGraph()
    t = g.add_task(TaskNode(intent="research"))
    g.update_status(t.task_id, TaskStatus.COMPLETE)

    with pytest.raises(AmpError) as exc:
        g.update_status(t.task_id, TaskStatus.RUNNING)
    assert exc.value.code == ErrorCode.TASK_ALREADY_COMPLETE
