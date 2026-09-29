"""Tests for ACI signing."""
import json
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).parent.parent


def _setup_example(tmp_path: Path) -> Path:
    """Copy research-agent, compute digests, return source dir."""
    src = REPO_ROOT / "examples" / "research-agent"
    dst = tmp_path / "research-agent"
    shutil.copytree(src, dst)
    result = subprocess.run(
        [sys.executable, str(REPO_ROOT / "scripts" / "compute_digests.py"), str(dst)],
        capture_output=True, text=True,
    )
    assert result.returncode == 0
    return dst


def test_signing_key_round_trip(tmp_path):
    from loomwork.aci.signing import SigningKey, VerifyingKey
    sk = SigningKey.generate()
    pem = sk.to_pem()
    sk2 = SigningKey.from_pem(pem)
    assert sk2.key_id == sk.key_id


def test_sign_and_verify_aci(tmp_path):
    """Sign an ACI, then verify the signature."""
    from loomwork.aci.signing import SigningKey, sign_aci, verify_aci_signature
    src = _setup_example(tmp_path)

    sk = SigningKey.generate()
    sign_aci(src, sk)

    # Verify
    assert verify_aci_signature(src)


def test_verify_rejects_tampered_manifest(tmp_path):
    """Tamper with the manifest after signing; verification should fail."""
    from loomwork.aci.signing import SigningKey, sign_aci, verify_aci_signature
    src = _setup_example(tmp_path)

    sk = SigningKey.generate()
    sign_aci(src, sk)
    assert verify_aci_signature(src)

    # Tamper
    manifest_path = src / "manifest.json"
    m = json.loads(manifest_path.read_text())
    m["metadata"]["description"] = "TAMPERED"
    manifest_path.write_text(json.dumps(m))

    # Verification should now fail
    assert not verify_aci_signature(src)


def test_slsa_attestation_round_trip(tmp_path):
    """Build a SLSA attestation, then verify it."""
    from loomwork.aci.manifest import Manifest
    from loomwork.aci.slsa import build_slsa_attestation, verify_slsa, SlsaAttestation
    import hashlib

    src = _setup_example(tmp_path)
    manifest = Manifest.from_json((src / "manifest.json").read_bytes())

    # Simulate packed archive bytes
    fake_archive = b"fake archive content"
    attestation = build_slsa_attestation(manifest, fake_archive, source_repo=src)

    expected_digest = f"sha256:{hashlib.sha256(fake_archive).hexdigest()}"
    assert verify_slsa(attestation, expected_digest)

    # Round-trip JSON
    json_bytes = attestation.to_json()
    att2 = SlsaAttestation.from_json(json_bytes)
    assert verify_slsa(att2, expected_digest)


def test_slsa_rejects_wrong_digest(tmp_path):
    """SLSA verification fails when archive digest doesn't match."""
    from loomwork.aci.manifest import Manifest
    from loomwork.aci.slsa import build_slsa_attestation, verify_slsa

    src = _setup_example(tmp_path)
    manifest = Manifest.from_json((src / "manifest.json").read_bytes())

    attestation = build_slsa_attestation(manifest, b"real content", source_repo=src)
    assert not verify_slsa(attestation, "sha256:" + "0" * 64)
