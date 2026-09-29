"""ACI Archive — pack/unpack .aci tar files.

Spec: §4.2 of Loomwork PRD v0.1.

Layout (fixed; runtimes MUST reject archives missing required files):

    my-agent.aci (gzipped tar)
    ├── manifest.json
    ├── persona/
    │   ├── system_prompt.md
    │   └── few_shot.jsonl        (optional)
    ├── skills/
    │   ├── graph.json
    │   └── src/                  (optional)
    ├── tools/
    │   └── bindings.json
    ├── memory-schema.json
    ├── sandbox.json
    ├── signatures/
    │   ├── manifest.sig
    │   ├── manifest.cert
    │   └── slsa.intoto.json
    └── README.md                 (optional)
"""
from __future__ import annotations

import io
import json
import tarfile
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator

from loomwork.aci.manifest import Manifest, sha256_bytes, sha256_file


class AciArchiveError(Exception):
    """Raised when an ACI archive is malformed."""


@dataclass
class AciArchive:
    """In-memory representation of an unpacked ACI."""

    manifest: Manifest
    files: dict[str, bytes]  # path -> raw bytes
    signatures: dict[str, bytes]  # signatures/* files

    @classmethod
    def from_directory(cls, dir_path: str | Path) -> "AciArchive":
        """Build an AciArchive from a source directory.

        Walks dir_path, reads manifest.json, validates it, computes digests for
        every other file referenced by the manifest.
        """
        dir_path = Path(dir_path)
        if not dir_path.is_dir():
            raise AciArchiveError(f"Not a directory: {dir_path}")

        manifest_path = dir_path / "manifest.json"
        if not manifest_path.exists():
            raise AciArchiveError("manifest.json not found")

        manifest = Manifest.from_json(manifest_path.read_bytes())

        # Walk all files in the directory, store by relative path
        files: dict[str, bytes] = {}
        for path in sorted(dir_path.rglob("*")):
            if path.is_file() and not path.is_symlink():
                rel = path.relative_to(dir_path).as_posix()
                files[rel] = path.read_bytes()

        # Separate signatures from regular files
        signatures = {p: b for p, b in files.items() if p.startswith("signatures/")}
        files = {p: b for p, b in files.items() if not p.startswith("signatures/")}

        # Verify digests in the manifest match actual file contents
        for path, expected_digest in manifest.digests.items():
            if path not in files:
                raise AciArchiveError(
                    f"Manifest references {path!r} but file is missing from archive"
                )
            actual = sha256_bytes(files[path])
            if actual != expected_digest:
                raise AciArchiveError(
                    f"Digest mismatch for {path!r}: manifest says {expected_digest}, "
                    f"file is {actual}"
                )

        return cls(manifest=manifest, files=files, signatures=signatures)

    def get_file(self, path: str) -> bytes:
        """Get a file's bytes by path. Raises if not present."""
        if path not in self.files:
            raise AciArchiveError(f"File not in archive: {path}")
        return self.files[path]

    def iter_files(self) -> Iterator[tuple[str, bytes]]:
        """Iterate (path, bytes) for all non-signature files."""
        yield from self.files.items()

    def to_tar_gz(self) -> bytes:
        """Serialize to a gzipped tar archive (the .aci format)."""
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w:gz") as tar:
            # Add manifest first
            manifest_bytes = self.manifest.to_json()
            self._add_to_tar(tar, "manifest.json", manifest_bytes)

            # Add all referenced files
            for path, data in sorted(self.files.items()):
                if path == "manifest.json":
                    continue
                self._add_to_tar(tar, path, data)

            # Add signature files
            for path, data in sorted(self.signatures.items()):
                self._add_to_tar(tar, path, data)

        return buf.getvalue()

    @staticmethod
    def _add_to_tar(tar: tarfile.TarFile, name: str, data: bytes) -> None:
        info = tarfile.TarInfo(name=name)
        info.size = len(data)
        tar.addfile(info, io.BytesIO(data))

    @classmethod
    def from_tar_gz(cls, data: bytes) -> "AciArchive":
        """Parse a gzipped tar archive into an AciArchive."""
        files: dict[str, bytes] = {}
        signatures: dict[str, bytes] = {}

        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as tar:
            for member in tar.getmembers():
                if not member.isfile():
                    continue
                f = tar.extractfile(member)
                if f is None:
                    continue
                content = f.read()
                if member.name.startswith("signatures/"):
                    signatures[member.name] = content
                else:
                    files[member.name] = content

        if "manifest.json" not in files:
            raise AciArchiveError("manifest.json not found in archive")

        manifest = Manifest.from_json(files.pop("manifest.json"))

        # Verify digests
        for path, expected in manifest.digests.items():
            if path not in files:
                raise AciArchiveError(f"Referenced file missing: {path}")
            actual = sha256_bytes(files[path])
            if actual != expected:
                raise AciArchiveError(
                    f"Digest mismatch for {path!r}: expected {expected}, got {actual}"
                )

        return cls(manifest=manifest, files=files, signatures=signatures)


# ── Top-level convenience functions ──────────────────────────────────────────
def pack_aci(source_dir: str | Path, output_path: str | Path) -> Path:
    """Build an .aci file from a source directory.

    Steps:
      1. Load AciArchive from source_dir (validates manifest + digests)
      2. Serialize to tar.gz
      3. Write to output_path

    Does NOT sign. Use `loomwork.aci.signing.sign_aci` to add a signature.
    """
    archive = AciArchive.from_directory(source_dir)
    data = archive.to_tar_gz()
    output_path = Path(output_path)
    output_path.write_bytes(data)
    return output_path


def unpack_aci(aci_path: str | Path, output_dir: str | Path) -> Path:
    """Unpack an .aci file into a directory.

    Verifies digests but does NOT verify signatures (use
    `loomwork.aci.signing.verify_aci_signature` for that).
    """
    archive = AciArchive.from_tar_gz(Path(aci_path).read_bytes())
    output_dir = Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    # Write manifest
    (output_dir / "manifest.json").write_bytes(archive.manifest.to_json())

    # Write all other files
    for path, data in archive.files.items():
        if path == "manifest.json":
            continue
        full = output_dir / path
        full.parent.mkdir(parents=True, exist_ok=True)
        full.write_bytes(data)

    # Write signatures
    for path, data in archive.signatures.items():
        full = output_dir / path
        full.parent.mkdir(parents=True, exist_ok=True)
        full.write_bytes(data)

    return output_dir
