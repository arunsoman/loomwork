"""Tests for ACI archive pack/unpack."""
import json
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).parent.parent
EXAMPLES = REPO_ROOT / "examples"


def _build_aci(example_name: str, tmp_path: Path) -> Path:
    """Run compute_digests on a copy of the example, then pack_aci."""
    src = EXAMPLES / example_name
    dst = tmp_path / example_name
    shutil.copytree(src, dst)

    # Compute digests in-place
    result = subprocess.run(
        [sys.executable, str(REPO_ROOT / "scripts" / "compute_digests.py"), str(dst)],
        capture_output=True, text=True,
    )
    assert result.returncode == 0, f"compute_digests failed: {result.stderr}"

    # Pack
    out = tmp_path / f"{example_name}.aci"
    result = subprocess.run(
        [sys.executable, "-m", "loomwork.cli", "aci", "build", str(dst), "-o", str(out),
         "--trust-unsigned" if False else "--with-slsa"],  # --with-slsa needs --signing-key
        capture_output=True, text=True,
    )
    # Actually just use the API directly
    from loomwork.aci.archive import pack_aci
    return pack_aci(dst, out)


@pytest.mark.parametrize("example_name", ["research-agent", "coding-agent", "deploy-agent"])
def test_example_aci_pack_unpack_round_trip(example_name, tmp_path):
    """Pack each example ACI, unpack it, verify the unpacked dir matches."""
    aci_path = _build_aci(example_name, tmp_path)
    assert aci_path.exists()
    assert aci_path.stat().st_size > 0

    # Unpack
    from loomwork.aci.archive import unpack_aci, AciArchive
    unpack_dir = tmp_path / "unpacked"
    unpack_aci(aci_path, unpack_dir)

    # Verify manifest is present and valid
    manifest_path = unpack_dir / "manifest.json"
    assert manifest_path.exists()

    # Verify all referenced files exist
    from loomwork.aci.manifest import Manifest
    manifest = Manifest.from_json(manifest_path.read_bytes())
    for path in manifest.digests:
        assert (unpack_dir / path).exists(), f"Missing: {path}"

    # Verify via AciArchive.from_tar_gz
    archive = AciArchive.from_tar_gz(aci_path.read_bytes())
    assert archive.manifest.metadata.name == example_name


@pytest.mark.parametrize("example_name", ["research-agent", "coding-agent", "deploy-agent"])
def test_example_aci_conformance(example_name, tmp_path):
    """Each example ACI must pass C2 (digests) and C3 (schema). C1 (signature)
    is expected to FAIL because we haven't signed — that's the test's assertion."""
    aci_path = _build_aci(example_name, tmp_path)
    from loomwork.aci.conformance import ConformanceChecker
    result = ConformanceChecker().check_aci(aci_path)

    # C2 (digests) and C3 (schema) MUST pass
    c2 = next(c for c in result.checks if c[0] == "C2")
    c3 = next(c for c in result.checks if c[0] == "C3")
    assert c2[3], f"C2 failed: {c2[4]}"
    assert c3[3], f"C3 failed: {c3[4]}"

    # C1 (signature) MUST fail because we didn't sign
    c1 = next(c for c in result.checks if c[0] == "C1")
    assert not c1[3], "C1 should fail (no signature)"
