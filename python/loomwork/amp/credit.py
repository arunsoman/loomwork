"""Credit Receipts — PRD §5.5.4, §5.7.

Off-ledger credit model: agents exchange signed receipts in real time, and
settlement happens out-of-band. Receipts are dual-signed (payer and payee).
"""
from __future__ import annotations

import base64
import time
from dataclasses import dataclass, field, asdict
from typing import Any, Optional
from loomwork._ids import new_ulid, new_id

from cryptography.hazmat.primitives.asymmetric.ed25519 import (
    Ed25519PrivateKey,
    Ed25519PublicKey,
)
from cryptography.exceptions import InvalidSignature


@dataclass
class CreditReceipt:
    """Dual-signed credit receipt for work performed (PRD §5.5.4)."""

    receiptId: str = field(default_factory=lambda: new_id("rct"))
    taskId: str = ""
    amount: dict[str, Any] = field(default_factory=dict)  # {currency, value}
    breakdown: dict[str, Any] = field(default_factory=dict)  # {tokens, toolCalls, wallSeconds}
    payer: str = ""  # did:key:...
    payee: str = ""  # did:key:...
    issuedAt: str = ""
    payerSig: Optional[str] = None  # base64 Ed25519
    payeeSig: Optional[str] = None  # base64 Ed25519

    def to_unsigned_dict(self) -> dict:
        d = asdict(self)
        d.pop("payerSig", None)
        d.pop("payeeSig", None)
        return d

    def to_payer_signed_dict(self) -> dict:
        """For payee countersigning — includes payerSig but not payeeSig."""
        d = asdict(self)
        d.pop("payeeSig", None)
        return d

    def to_canonical_json(self, *, exclude: set[str] | None = None) -> bytes:
        import canonicaljson
        d = asdict(self)
        for f in (exclude or set()):
            d.pop(f, None)
        return canonicaljson.encode_canonical_json(d)

    def to_dict(self) -> dict:
        return asdict(self)


def sign_receipt(
    *,
    payer_key: Ed25519PrivateKey,
    payer_did: str,
    payee_did: str,
    task_id: str,
    amount: dict[str, Any],
    breakdown: dict[str, Any],
) -> CreditReceipt:
    """Payer signs a credit receipt. Returns the payer-signed receipt.

    The payee must then countersign via `countersign_receipt`.
    """
    receipt = CreditReceipt(
        taskId=task_id,
        amount=amount,
        breakdown=breakdown,
        payer=payer_did,
        payee=payee_did,
        issuedAt=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    )
    payload = receipt.to_canonical_json(exclude={"payerSig", "payeeSig"})
    sig = payer_key.sign(payload)
    receipt.payerSig = base64.b64encode(sig).decode("ascii")
    return receipt


def countersign_receipt(
    receipt: CreditReceipt,
    payee_key: Ed25519PrivateKey,
) -> CreditReceipt:
    """Payee countersigns a payer-signed receipt."""
    if receipt.payerSig is None:
        raise ValueError("Receipt must be payer-signed before countersigning")
    payload = receipt.to_canonical_json(exclude={"payeeSig"})
    sig = payee_key.sign(payload)
    receipt.payeeSig = base64.b64encode(sig).decode("ascii")
    return receipt


def verify_receipt(
    receipt: CreditReceipt,
    payer_pubkey: Ed25519PublicKey,
    payee_pubkey: Ed25519PublicKey,
) -> bool:
    """Verify both signatures on a dual-signed receipt."""
    if receipt.payerSig is None or receipt.payeeSig is None:
        return False
    try:
        # Verify payer sig
        payload1 = receipt.to_canonical_json(exclude={"payerSig", "payeeSig"})
        payer_pubkey.verify(base64.b64decode(receipt.payerSig), payload1)

        # Verify payee sig
        payload2 = receipt.to_canonical_json(exclude={"payeeSig"})
        payee_pubkey.verify(base64.b64decode(receipt.payeeSig), payload2)
        return True
    except (InvalidSignature, Exception):
        return False
