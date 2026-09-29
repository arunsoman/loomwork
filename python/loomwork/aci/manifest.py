"""ACI Manifest — Pydantic models matching the JSON Schema in PRD §4.3.

The manifest is the ACI's root of trust. Every other file in the archive is
referenced by digest from the manifest, so a signature over the manifest
transitively covers the entire ACI.
"""
from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path
from typing import Any, Literal

from pydantic import BaseModel, Field, field_validator, model_validator

# ── Constants ────────────────────────────────────────────────────────────────
API_VERSION = "aci.loomwork.dev/v0.1"
KIND = "Agent"
DIGEST_PATTERN = r"^sha256:[0-9a-f]{64}$"
NAME_PATTERN = r"^[a-z0-9][a-z0-9-]*$"
VERSION_PATTERN = r"^v?\d+\.\d+\.\d+"

Architecture = Literal["amd64", "arm64", "wasm"]
OperatingSystem = Literal["linux", "darwin", "windows", "any"]


# ── Models ───────────────────────────────────────────────────────────────────
class Metadata(BaseModel):
    """ACI metadata block. Identifies the agent across runtimes."""

    name: str = Field(..., pattern=NAME_PATTERN, description="Lowercase, hyphenated")
    version: str = Field(..., pattern=VERSION_PATTERN, description="SemVer")
    architecture: Architecture
    os: OperatingSystem
    description: str | None = None
    license: str = "Apache-2.0"
    homepage: str | None = None


class PersonaRef(BaseModel):
    """Reference to the persona block (§4.4)."""

    systemPrompt: str = Field(..., description="Path to system prompt file")
    fewShot: str | None = None
    modelPrefs: dict[str, Any] = Field(default_factory=dict)
    capabilities: dict[str, bool] = Field(default_factory=dict)


class SkillNode(BaseModel):
    """A single node in the skills DAG (§4.5)."""

    name: str
    description: str
    inputs: dict[str, str]
    outputs: dict[str, str]
    requires: list[str] = Field(default_factory=list)
    impl: dict[str, str] | None = None


class SkillsRef(BaseModel):
    """Reference to the skills graph file."""

    graph: str = Field(..., description="Path to skills/graph.json")


class ToolBinding(BaseModel):
    """A single MCP tool binding (§4.6)."""

    name: str
    mcpServer: str
    allowedTools: list[str]
    scope: dict[str, Any] = Field(default_factory=dict)


class ToolsRef(BaseModel):
    """Reference to the tool bindings file."""

    bindings: str = Field(..., description="Path to tools/bindings.json")


class MemoryRef(BaseModel):
    """Reference to the memory schema file."""

    memory_schema: str = Field(..., alias="schema", description="Path to memory-schema.json")

    model_config = {"populate_by_name": True}


class SandboxRef(BaseModel):
    """Sandbox policy reference and inline defaults (§4.7)."""

    spec: str = Field(..., description="Path to sandbox.json")


class Digests(BaseModel):
    """Map of file path -> SHA-256 digest. The integrity anchor."""

    # Pydantic v2 doesn't easily support arbitrary-key dicts with pattern-validated
    # values, so we use a plain dict and validate in model_validator.
    pass  # implemented via Dict[str, str] at Manifest level


class Manifest(BaseModel):
    """ACI manifest root. Matches the JSON Schema in PRD §4.3."""

    apiVersion: str = Field(default=API_VERSION)
    kind: str = Field(default=KIND)
    metadata: Metadata
    persona: PersonaRef
    skills: SkillsRef
    tools: ToolsRef
    memory: MemoryRef
    sandbox: SandboxRef
    digests: dict[str, str] = Field(
        default_factory=dict,
        description="Map of file path (relative to archive root) -> sha256 digest",
    )

    @field_validator("apiVersion")
    @classmethod
    def _check_api_version(cls, v: str) -> str:
        if v != API_VERSION:
            raise ValueError(f"apiVersion must be {API_VERSION!r}, got {v!r}")
        return v

    @field_validator("kind")
    @classmethod
    def _check_kind(cls, v: str) -> str:
        if v != KIND:
            raise ValueError(f"kind must be {KIND!r}, got {v!r}")
        return v

    @field_validator("digests")
    @classmethod
    def _check_digests(cls, v: dict[str, str]) -> dict[str, str]:
        for path, digest in v.items():
            if not re.match(DIGEST_PATTERN, digest):
                raise ValueError(f"Invalid digest for {path!r}: {digest!r}")
        return v

    @model_validator(mode="after")
    def _check_digests_cover_required_files(self) -> "Manifest":
        """Every referenced file MUST have a digest entry."""
        required = [
            self.persona.systemPrompt,
            self.skills.graph,
            self.tools.bindings,
            self.memory.memory_schema,
            self.sandbox.spec,
        ]
        for path in required:
            if path and path not in self.digests:
                raise ValueError(f"Required file {path!r} missing from digests map")
        return self

    # ── Serialization ────────────────────────────────────────────────────────
    def to_canonical_json(self) -> bytes:
        """RFC 8785 JCS canonical serialization for signing.

        Uses canonicaljson (DE-9 serialization) — same shape as cosign uses.
        """
        import canonicaljson

        data = self.model_dump(mode="json", exclude_none=True, by_alias=True)
        return canonicaljson.encode_canonical_json(data)

    def to_json(self) -> bytes:
        """Pretty-printed JSON for human inspection."""
        return json.dumps(
            self.model_dump(mode="json", exclude_none=True, by_alias=True),
            indent=2,
            sort_keys=False,
            ensure_ascii=False,
        ).encode("utf-8")

    @classmethod
    def from_json(cls, data: bytes | str) -> "Manifest":
        """Parse a manifest from JSON."""
        if isinstance(data, bytes):
            data = data.decode("utf-8")
        return cls.model_validate_json(data)


# ── Helpers ──────────────────────────────────────────────────────────────────
def sha256_file(path: str | Path) -> str:
    """Compute sha256:<hex> digest of a file."""
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return f"sha256:{h.hexdigest()}"


def sha256_bytes(data: bytes) -> str:
    """Compute sha256:<hex> digest of a byte string."""
    return f"sha256:{hashlib.sha256(data).hexdigest()}"
