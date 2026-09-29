"""ACI Conformance Checker — runs C1-C11 from PRD §4.11.

The keywords MUST, MUST NOT, REQUIRED, SHALL, SHALL NOT, SHOULD, SHOULD NOT,
RECOMMENDED, MAY, and OPTIONAL are to be interpreted as described in RFC 2119.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

from loomwork.aci.archive import AciArchive
from loomwork.aci.signing import verify_aci_signature
from loomwork.aci.slsa import SlsaAttestation, verify_slsa


@dataclass
class ConformanceResult:
    """Result of running the conformance checker on an ACI."""

    aci_path: str
    checks: list[tuple[str, str, str, bool, str]] = field(default_factory=list)
    # (id, requirement, level, passed, message)

    @property
    def passed(self) -> bool:
        """True iff all MUST-level checks passed (SHOULD/MAY are informational)."""
        for cid, _, level, ok, _ in self.checks:
            if level == "MUST" and not ok:
                return False
        return True

    @property
    def must_failures(self) -> list[tuple[str, str, str]]:
        return [(cid, req, msg) for cid, req, lvl, ok, msg in self.checks if lvl == "MUST" and not ok]

    def summary(self) -> str:
        n_pass = sum(1 for *_, ok, _ in self.checks if ok)
        n_total = len(self.checks)
        n_must_fail = len(self.must_failures)
        return (
            f"Conformance: {n_pass}/{n_total} checks passed. "
            f"{n_must_fail} MUST-level failures."
        )


# All 11 conformance checks from PRD §4.11
CONFORMANCE_CHECKS = [
    ("C1",  "Runtime MUST verify manifest signature before loading ACI.", "MUST"),
    ("C2",  "Runtime MUST verify all file digests against the manifest's digests map.", "MUST"),
    ("C3",  "Runtime MUST reject ACI whose manifest fails JSON Schema validation.", "MUST"),
    ("C4",  "Runtime MUST enforce every clause of sandbox.json.", "MUST"),
    ("C5",  "Runtime MUST emit a signed event on every lifecycle transition.", "MUST"),
    ("C6",  "Runtime MUST refuse LLM calls that exceed persona.tokenBudget.hardLimit.", "MUST"),
    ("C7",  "Runtime MUST refuse tool calls not declared in tools/bindings.json.", "MUST"),
    ("C8",  "Runtime SHOULD cache warm ACIs for at least 5 minutes after last activity.", "SHOULD"),
    ("C9",  "Runtime SHOULD persist a sealed log bundle on termination.", "SHOULD"),
    ("C10", "Runtime MAY support ACIs without SLSA attestation if user opts in.", "MAY"),
    ("C11", "Runtime MAY support multiple ACI versions concurrently.", "MAY"),
]


class ConformanceChecker:
    """Static checker for ACI conformance (C1, C2, C3 — file/archive checks).

    Runtime conformance checks (C4-C9) are validated by the runtime's own
    test suite, not here. This checker validates that an ACI archive itself
    is well-formed and signed.
    """

    def check_aci(self, aci_path: str | Path) -> ConformanceResult:
        """Run all applicable conformance checks on an .aci file."""
        aci_path = Path(aci_path)
        result = ConformanceResult(aci_path=str(aci_path))
        data = aci_path.read_bytes()

        try:
            archive = AciArchive.from_tar_gz(data)
        except Exception as e:
            # C3-style: manifest failed to parse
            for cid, req, lvl in CONFORMANCE_CHECKS[:3]:
                result.checks.append((cid, req, lvl, False, f"Archive parse error: {e}"))
            return result

        # C1: signature verification
        # We need the unpacked dir for this; unpack to a temp location
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            from loomwork.aci.archive import unpack_aci
            unpack_aci(aci_path, tmp)
            sig_ok = verify_aci_signature(tmp)
            has_sig = (Path(tmp) / "signatures" / "manifest.sig").exists()
            if has_sig:
                result.checks.append((
                    "C1", CONFORMANCE_CHECKS[0][1], "MUST", sig_ok,
                    "Signature valid" if sig_ok else "Signature INVALID"
                ))
            else:
                result.checks.append((
                    "C1", CONFORMANCE_CHECKS[0][1], "MUST", False,
                    "No signature present"
                ))

            # C2: digest verification (already done by from_tar_gz; if we got
            # here, all digests matched)
            result.checks.append((
                "C2", CONFORMANCE_CHECKS[1][1], "MUST", True,
                f"All {len(archive.manifest.digests)} digests verified"
            ))

            # C3: manifest schema validation (already done by Pydantic in
            # Manifest.from_json; if we got here, schema is valid)
            result.checks.append((
                "C3", CONFORMANCE_CHECKS[2][1], "MUST", True,
                "Manifest schema valid"
            ))

            # SLSA attestation (informs C10)
            slsa_path = Path(tmp) / "signatures" / "slsa.intoto.json"
            if slsa_path.exists():
                att = SlsaAttestation.from_json(slsa_path.read_bytes())
                import hashlib
                expected = f"sha256:{hashlib.sha256(data).hexdigest()}"
                slsa_ok = verify_slsa(att, expected)
                result.checks.append((
                    "C10", CONFORMANCE_CHECKS[9][1], "MAY", slsa_ok,
                    "SLSA attestation present and valid" if slsa_ok else
                    "SLSA attestation present but invalid"
                ))
            else:
                result.checks.append((
                    "C10", CONFORMANCE_CHECKS[9][1], "MAY", False,
                    "No SLSA attestation (user must opt in to load)"
                ))

        # C4-C9 are runtime checks; mark as N/A here
        for cid, req, lvl in CONFORMANCE_CHECKS[3:9]:
            result.checks.append((cid, req, lvl, False, "N/A — runtime check"))

        # C11: multiple versions — N/A at archive level
        result.checks.append((
            "C11", CONFORMANCE_CHECKS[10][1], "MAY", False, "N/A — runtime check"
        ))

        return result
