"""ACI — Agent Container Image format.

Spec: §4 of Loomwork PRD v0.1.
"""

from loomwork.aci.manifest import (
    Manifest,
    Metadata,
    PersonaRef,
    SkillsRef,
    ToolsRef,
    MemoryRef,
    SandboxRef,
    Digests,
)
from loomwork.aci.archive import (
    AciArchive,
    AciArchiveError,
    pack_aci,
    unpack_aci,
)
from loomwork.aci.signing import (
    SigningKey,
    VerifyingKey,
    sign_manifest,
    verify_manifest,
)
from loomwork.aci.slsa import SlsaAttestation, build_slsa_attestation
from loomwork.aci.lifecycle import LifecycleState, LifecycleEvent, LifecycleMachine
from loomwork.aci.conformance import ConformanceChecker, ConformanceResult

__all__ = [
    "Manifest",
    "Metadata",
    "PersonaRef",
    "SkillsRef",
    "ToolsRef",
    "MemoryRef",
    "SandboxRef",
    "Digests",
    "AciArchive",
    "AciArchiveError",
    "pack_aci",
    "unpack_aci",
    "SigningKey",
    "VerifyingKey",
    "sign_manifest",
    "verify_manifest",
    "SlsaAttestation",
    "build_slsa_attestation",
    "LifecycleState",
    "LifecycleEvent",
    "LifecycleMachine",
    "ConformanceChecker",
    "ConformanceResult",
]
