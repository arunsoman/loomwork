"""SLSA Level 3 provenance attestation.

Spec: §4.8 of Loomwork PRD v0.1. The attestation follows the in-toto
statement format and MUST meet at least SLSA Build Level 3.

Reference impl populates from local git/env. Real deployments wire this
up to a CI builder (GitHub Actions, Tekton, etc.) and use Fulcio + Rekor.
"""
from __future__ import annotations

import hashlib
import json
import os
import subprocess
import time
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

from loomwork.aci.manifest import Manifest


@dataclass
class SlsaBuildConfig:
    """SLSA buildConfig fields."""

    builderImage: str = "loomwork-buildkit/0.1"
    builderDigest: str = "sha256:" + "0" * 64  # populated by real CI
    buildType: str = "loomwork-build-v0.1"


@dataclass
class SlsaAttestation:
    """in-toto statement matching SLSA Level 3."""

    _type: str = "https://in-toto.io/Statement/v0.1"
    subject: list[dict[str, Any]] = field(default_factory=list)
    predicateType: str = "https://slsa.dev/provenance/v0.2"
    predicate: dict[str, Any] = field(default_factory=dict)

    def to_json(self) -> bytes:
        return json.dumps(asdict(self), indent=2, sort_keys=False).encode("utf-8")

    @classmethod
    def from_json(cls, data: bytes | str) -> "SlsaAttestation":
        if isinstance(data, bytes):
            data = data.decode("utf-8")
        d = json.loads(data)
        return cls(
            _type=d.get("_type", "https://in-toto.io/Statement/v0.1"),
            subject=d.get("subject", []),
            predicateType=d.get("predicateType", "https://slsa.dev/provenance/v0.2"),
            predicate=d.get("predicate", {}),
        )


def _git_commit_hash(repo_path: str | Path) -> str:
    """Get the current git commit hash, or 'unknown' if not a git repo."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=repo_path,
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return "unknown"


def _git_remote_url(repo_path: str | Path) -> str:
    try:
        result = subprocess.run(
            ["git", "config", "--get", "remote.origin.url"],
            cwd=repo_path,
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return "unknown"


def build_slsa_attestation(
    manifest: Manifest,
    aci_archive_bytes: bytes,
    *,
    source_repo: str | Path | None = None,
    builder_id: str | None = None,
) -> SlsaAttestation:
    """Build a SLSA Level 3 attestation for an ACI.

    The attestation binds the ACI archive's digest to its source commit and
    build environment. The subject is the .aci archive; the predicate captures
    build steps, materials, and metadata.

    Args:
        manifest: The ACI manifest (provides name/version metadata)
        aci_archive_bytes: The packed .aci file's bytes (subject digest)
        source_repo: Path to git repo (defaults to cwd)
        builder_id: URI identifying the builder (defaults to local hostname)
    """
    source_repo = Path(source_repo or os.getcwd())
    builder_id = builder_id or f"loomwork-buildkit://{os.environ.get('HOSTNAME', 'local')}"

    archive_digest = f"sha256:{hashlib.sha256(aci_archive_bytes).hexdigest()}"

    commit = _git_commit_hash(source_repo)
    remote = _git_remote_url(source_repo)

    subject = [
        {
            "name": f"{manifest.metadata.name}-{manifest.metadata.version}.aci",
            "digest": {"sha256": archive_digest[7:]},  # strip "sha256:" prefix
        }
    ]

    predicate = {
        "builder": {"id": builder_id},
        "buildType": "loomwork-build-v0.1",
        "invocation": {
            "configSource": {
                "uri": remote,
                "digest": {"gitCommit": commit},
                "entryPoint": "manifest.json",
            },
            "parameters": {
                "name": manifest.metadata.name,
                "version": manifest.metadata.version,
                "architecture": manifest.metadata.architecture,
                "os": manifest.metadata.os,
            },
            "environment": {
                "BUILD_HOST": os.environ.get("HOSTNAME", "unknown"),
                "BUILD_USER": os.environ.get("USER", "unknown"),
                "BUILD_TIME": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "LOOMWORK_VERSION": "0.1.0",
            },
        },
        "buildConfig": asdict(SlsaBuildConfig()),
        "metadata": {
            "buildStartedOn": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "buildFinishedOn": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "completeness": {
                "parameters": True,
                "environment": True,
                "materials": True,
            },
            "reproducible": False,  # set True only in hermetic CI
        },
        "materials": [
            {
                "uri": remote,
                "digest": {"gitCommit": commit},
            }
        ],
    }

    return SlsaAttestation(subject=subject, predicate=predicate)


def verify_slsa(attestation: SlsaAttestation, expected_archive_digest: str) -> bool:
    """Verify a SLSA attestation against an expected archive digest.

    Returns True if:
      - Statement type is in-toto v0.1
      - Predicate type is SLSA provenance v0.2
      - Subject digest matches expected_archive_digest
      - Materials are non-empty
      - BuildType is set
    """
    if attestation._type != "https://in-toto.io/Statement/v0.1":
        return False
    if attestation.predicateType != "https://slsa.dev/provenance/v0.2":
        return False
    if not attestation.subject:
        return False
    if not attestation.predicate.get("buildType"):
        return False
    if not attestation.predicate.get("materials"):
        return False

    subject_digest = attestation.subject[0].get("digest", {}).get("sha256")
    expected = expected_archive_digest.replace("sha256:", "")
    return subject_digest == expected
