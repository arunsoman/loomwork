#!/usr/bin/env python3
"""Compute digests for an ACI source directory and patch them into manifest.json.

Usage:
    python3 scripts/compute_digests.py examples/research-agent
"""
from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return f"sha256:{h.hexdigest()}"


def main(source_dir: str) -> int:
    root = Path(source_dir)
    if not root.is_dir():
        print(f"Not a directory: {root}", file=sys.stderr)
        return 1

    manifest_path = root / "manifest.json"
    if not manifest_path.exists():
        print(f"manifest.json not found in {root}", file=sys.stderr)
        return 1

    manifest = json.loads(manifest_path.read_bytes())

    referenced = [
        manifest["persona"]["systemPrompt"],
        manifest.get("persona", {}).get("fewShot"),
        manifest["skills"]["graph"],
        manifest["tools"]["bindings"],
        manifest["memory"]["schema"],
        manifest["sandbox"]["spec"],
    ]
    referenced = [p for p in referenced if p]

    digests = {}
    for rel in referenced:
        full = root / rel
        if not full.exists():
            print(f"Missing file: {rel}", file=sys.stderr)
            return 1
        digests[rel] = sha256_file(full)

    manifest["digests"] = digests
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
    print(f"Wrote {len(digests)} digests to {manifest_path}")
    for path, d in digests.items():
        print(f"  {path}: {d[:32]}...")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]) if len(sys.argv) > 1 else 1)
