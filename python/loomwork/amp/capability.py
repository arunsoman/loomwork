"""Capability Tokens — PRD §5.8.

A capability token is signed by the issuer (the agent granting the capability),
scoped to a specific skill, time-boxed, and revocable via amp/revoke.
"""
from __future__ import annotations

import base64
import json
import time
from dataclasses import dataclass, field, asdict
from typing import Any, Optional
from loomwork._ids import new_ulid, new_id

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import (
    Ed25519PrivateKey,
    Ed25519PublicKey,
)
from cryptography.exceptions import InvalidSignature


@dataclass
class CapabilityToken:
    """A signed capability token (PRD §5.3 capabilities field)."""

    id: str = field(default_factory=lambda: new_id("cap"))
    skill: str = ""  # e.g. "search_web"
    scope: dict[str, Any] = field(default_factory=dict)
    issuedBy: str = ""  # did:key:...
    issuedAt: str = ""
    expiresAt: str = ""
    signature: Optional[str] = None  # base64 Ed25519

    def to_unsigned_dict(self) -> dict:
        d = asdict(self)
        d.pop("signature", None)
        return d

    def to_dict(self) -> dict:
        return asdict(self)

    def to_canonical_json(self) -> bytes:
        import canonicaljson
        return canonicaljson.encode_canonical_json(self.to_unsigned_dict())


def issue_capability(
    *,
    issuer_key: Ed25519PrivateKey,
    issuer_did: str,
    skill: str,
    scope: dict[str, Any] | None = None,
    ttl_seconds: int = 3600,
) -> CapabilityToken:
    """Issue a signed capability token.

    Args:
        issuer_key: Ed25519 private key of the issuing agent
        issuer_did: did:key:... identifier of the issuer
        skill: name of the skill being granted
        scope: skill-specific scope (e.g. {"paths": ["$HOME/docs/**"]})
        ttl_seconds: how long the token is valid
    """
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    exp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + ttl_seconds))

    # Derive issuer did:key from the public key
    pk_bytes = issuer_key.public_key().public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )
    # did:key format: did:key:z<base58 multibase>
    # For simplicity, use did:key:ed25519:<hex>
    actual_issuer_did = issuer_did or f"did:key:ed25519:{pk_bytes.hex()}"

    token = CapabilityToken(
        skill=skill,
        scope=scope or {},
        issuedBy=actual_issuer_did,
        issuedAt=now,
        expiresAt=exp,
    )

    # Sign
    payload = token.to_canonical_json()
    sig = issuer_key.sign(payload)
    token.signature = base64.b64encode(sig).decode("ascii")
    return token


def verify_capability(
    token: CapabilityToken,
    issuer_public_key: Ed25519PublicKey,
    *,
    now: float | None = None,
    revoked: set[str] | None = None,
) -> bool:
    """Verify a capability token.

    Checks:
      - Signature is valid
      - Token has not expired
      - Token has not been revoked

    Args:
        token: The capability token to verify
        issuer_public_key: Ed25519 public key of the issuer
        now: Override current time (for testing)
        revoked: Set of revoked token IDs
    """
    if token.signature is None:
        return False

    if revoked and token.id in revoked:
        return False

    if now is None:
        now = time.time()

    # Check expiry
    exp_str = token.expiresAt
    if exp_str:
        try:
            exp_time = time.strptime(exp_str, "%Y-%m-%dT%H:%M:%SZ")
            if time.mktime(exp_time) < now:
                return False
        except (ValueError, OverflowError):
            return False

    # Verify signature
    try:
        sig = base64.b64decode(token.signature)
        issuer_public_key.verify(sig, token.to_canonical_json())
        return True
    except (InvalidSignature, Exception):
        return False


def did_to_public_key(did: str) -> Ed25519PublicKey:
    """Convert a did:key:ed25519:<hex> DID to an Ed25519PublicKey."""
    if not did.startswith("did:key:ed25519:"):
        raise ValueError(f"Unsupported DID format: {did!r}")
    hex_part = did.removeprefix("did:key:ed25519:")
    pk_bytes = bytes.fromhex(hex_part)
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
    return Ed25519PublicKey.from_public_bytes(pk_bytes)
