"""ACI Signing — Ed25519 signatures in Sigstore-cosign-compatible shape.

Spec: §4.8 of Loomwork PRD v0.1 says "Sigstore cosign signatures over the
canonical-JSON serialization of manifest.json, with Fulcio-issued certificates."

This reference implementation uses Ed25519 keys directly, with the same JSON
envelope shape as cosign. Real Sigstore needs network access to Fulcio (for
ephemeral certs) and Rekor (for transparency log entries). A reference impl
should work offline, so we substitute local keys.

The wire format is identical:
    {
      "critical": { "identity": {"docker-reference": "..."}, "type": "cosign container image signature" },
      "optional": { "issuer": "loomwork-local", "subject": "<key-id>" }
    }

The signature itself is base64-encoded Ed25519 over the canonical-JSON bytes.
"""
from __future__ import annotations

import base64
import json
from dataclasses import dataclass
from pathlib import Path

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import (
    Ed25519PrivateKey,
    Ed25519PublicKey,
)
from cryptography.exceptions import InvalidSignature

from loomwork.aci.manifest import Manifest


@dataclass
class SigningKey:
    """Ed25519 private key + helper to derive the verifying key."""

    private_key: Ed25519PrivateKey
    key_id: str  # short identifier (first 16 hex chars of public key)

    @classmethod
    def generate(cls) -> "SigningKey":
        sk = Ed25519PrivateKey.generate()
        pk_bytes = sk.public_key().public_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PublicFormat.Raw,
        )
        key_id = pk_bytes.hex()[:16]
        return cls(private_key=sk, key_id=key_id)

    @classmethod
    def from_pem(cls, pem: bytes, password: bytes | None = None) -> "SigningKey":
        sk = serialization.load_pem_private_key(pem, password=password)
        if not isinstance(sk, Ed25519PrivateKey):
            raise TypeError(f"Expected Ed25519PrivateKey, got {type(sk).__name__}")
        pk_bytes = sk.public_key().public_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PublicFormat.Raw,
        )
        return cls(private_key=sk, key_id=pk_bytes.hex()[:16])

    def to_pem(self, password: bytes | None = None) -> bytes:
        enc = (
            serialization.NoEncryption()
            if password is None
            else serialization.BestAvailableEncryption(password)
        )
        return self.private_key.private_bytes(
            encoding=serialization.Encoding.PEM,
            format=serialization.PrivateFormat.PKCS8,
            encryption_algorithm=enc,
        )

    def verifying_key(self) -> "VerifyingKey":
        pk = self.private_key.public_key()
        pk_bytes = pk.public_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PublicFormat.Raw,
        )
        return VerifyingKey(public_key=pk, key_id=pk_bytes.hex()[:16])


@dataclass
class VerifyingKey:
    """Ed25519 public key."""

    public_key: Ed25519PublicKey
    key_id: str

    @classmethod
    def from_pem(cls, pem: bytes) -> "VerifyingKey":
        pk = serialization.load_pem_public_key(pem)
        if not isinstance(pk, Ed25519PublicKey):
            raise TypeError(f"Expected Ed25519PublicKey, got {type(pk).__name__}")
        pk_bytes = pk.public_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PublicFormat.Raw,
        )
        return cls(public_key=pk, key_id=pk_bytes.hex()[:16])

    def to_pem(self) -> bytes:
        return self.public_key.public_bytes(
            encoding=serialization.Encoding.PEM,
            format=serialization.PublicFormat.SubjectPublicKeyInfo,
        )


# ── Manifest signature envelope ──────────────────────────────────────────────
def _cosign_envelope(subject: str) -> dict:
    """Build the cosign-style envelope metadata."""
    return {
        "critical": {
            "identity": {"docker-reference": f"loomwork.dev/{subject}"},
            "type": "cosign container image signature",
        },
        "optional": {"issuer": "loomwork-local", "subject": subject},
    }


def sign_manifest(manifest: Manifest, signing_key: SigningKey) -> dict:
    """Sign a manifest. Returns the cosign-style signature envelope.

    The envelope is what gets stored at signatures/manifest.sig. The matching
    cert (just the public key in our reference impl) goes to
    signatures/manifest.cert.
    """
    payload = manifest.to_canonical_json()
    signature = signing_key.private_key.sign(payload)
    envelope = _cosign_envelope(signing_key.key_id)
    envelope["base64Signature"] = base64.b64encode(signature).decode("ascii")
    envelope["payloadHash"] = f"sha256:{__import__('hashlib').sha256(payload).hexdigest()}"
    return envelope


def verify_manifest(manifest: Manifest, signature_envelope: dict, cert_pem: bytes) -> bool:
    """Verify a manifest signature against a certificate (public key).

    Returns True if the signature is valid, False otherwise.
    """
    try:
        vk = VerifyingKey.from_pem(cert_pem)
    except Exception:
        return False

    # Check that the key_id in the envelope matches the cert
    expected_key_id = signature_envelope.get("optional", {}).get("subject")
    if expected_key_id != vk.key_id:
        return False

    payload = manifest.to_canonical_json()
    signature_b64 = signature_envelope.get("base64Signature", "")
    try:
        signature = base64.b64decode(signature_b64)
    except Exception:
        return False

    try:
        vk.public_key.verify(signature, payload)
        return True
    except InvalidSignature:
        return False


# ── ACI-level helpers ────────────────────────────────────────────────────────
def sign_aci(aci_dir: str | Path, signing_key: SigningKey) -> Path:
    """Sign an ACI in-place: writes signatures/manifest.{sig,cert}.

    Does NOT write SLSA attestation — use `loomwork.aci.slsa.build_slsa_attestation`
    for that.
    """
    aci_dir = Path(aci_dir)
    manifest_path = aci_dir / "manifest.json"
    if not manifest_path.exists():
        raise FileNotFoundError(f"manifest.json not found in {aci_dir}")

    manifest = Manifest.from_json(manifest_path.read_bytes())
    envelope = sign_manifest(manifest, signing_key)

    sig_dir = aci_dir / "signatures"
    sig_dir.mkdir(exist_ok=True)
    (sig_dir / "manifest.sig").write_text(json.dumps(envelope, indent=2) + "\n")
    (sig_dir / "manifest.cert").write_bytes(signing_key.verifying_key().to_pem())

    return sig_dir / "manifest.sig"


def verify_aci_signature(aci_dir: str | Path) -> bool:
    """Verify the signature on an unpacked ACI directory.

    Returns True if signature is valid AND matches the manifest. Does NOT
    check SLSA attestation — use `loomwork.aci.slsa.verify_slsa` for that.
    """
    aci_dir = Path(aci_dir)
    manifest_path = aci_dir / "manifest.json"
    sig_path = aci_dir / "signatures" / "manifest.sig"
    cert_path = aci_dir / "signatures" / "manifest.cert"

    if not (manifest_path.exists() and sig_path.exists() and cert_path.exists()):
        return False

    manifest = Manifest.from_json(manifest_path.read_bytes())
    envelope = json.loads(sig_path.read_text())
    cert_pem = cert_path.read_bytes()

    return verify_manifest(manifest, envelope, cert_pem)
