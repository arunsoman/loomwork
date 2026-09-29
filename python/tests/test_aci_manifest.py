"""Tests for ACI manifest validation."""
import json
import pytest
from pydantic import ValidationError

from loomwork.aci.manifest import Manifest, Metadata, sha256_bytes


def _base_manifest_data():
    return {
        "apiVersion": "aci.loomwork.dev/v0.1",
        "kind": "Agent",
        "metadata": {
            "name": "test-agent",
            "version": "v0.1.0",
            "architecture": "amd64",
            "os": "linux",
        },
        "persona": {
            "systemPrompt": "persona/system_prompt.md",
        },
        "skills": {"graph": "skills/graph.json"},
        "tools": {"bindings": "tools/bindings.json"},
        "memory": {"schema": "memory-schema.json"},
        "sandbox": {"spec": "sandbox.json"},
        "digests": {
            "persona/system_prompt.md": "sha256:" + "a" * 64,
            "skills/graph.json": "sha256:" + "b" * 64,
            "tools/bindings.json": "sha256:" + "c" * 64,
            "memory-schema.json": "sha256:" + "d" * 64,
            "sandbox.json": "sha256:" + "e" * 64,
        },
    }


def test_manifest_valid():
    m = Manifest.from_json(json.dumps(_base_manifest_data()))
    assert m.metadata.name == "test-agent"
    assert m.apiVersion == "aci.loomwork.dev/v0.1"


def test_manifest_rejects_bad_api_version():
    data = _base_manifest_data()
    data["apiVersion"] = "aci.loomwork.dev/v0.2"
    with pytest.raises(ValidationError):
        Manifest.from_json(json.dumps(data))


def test_manifest_rejects_bad_kind():
    data = _base_manifest_data()
    data["kind"] = "NotAgent"
    with pytest.raises(ValidationError):
        Manifest.from_json(json.dumps(data))


def test_manifest_rejects_uppercase_name():
    data = _base_manifest_data()
    data["metadata"]["name"] = "TestAgent"
    with pytest.raises(ValidationError):
        Manifest.from_json(json.dumps(data))


def test_manifest_rejects_bad_version():
    data = _base_manifest_data()
    data["metadata"]["version"] = "0.1"
    with pytest.raises(ValidationError):
        Manifest.from_json(json.dumps(data))


def test_manifest_rejects_bad_digest_format():
    data = _base_manifest_data()
    data["digests"]["sandbox.json"] = "md5:abc"
    with pytest.raises(ValidationError):
        Manifest.from_json(json.dumps(data))


def test_manifest_requires_digest_for_referenced_files():
    data = _base_manifest_data()
    del data["digests"]["sandbox.json"]
    with pytest.raises(ValidationError):
        Manifest.from_json(json.dumps(data))


def test_canonical_json_round_trip():
    """Canonical JSON serialization is deterministic."""
    m = Manifest.from_json(json.dumps(_base_manifest_data()))
    a = m.to_canonical_json()
    b = m.to_canonical_json()
    assert a == b


def test_sha256_bytes():
    h = sha256_bytes(b"hello")
    assert h.startswith("sha256:")
    assert len(h) == 71  # "sha256:" + 64 hex chars
