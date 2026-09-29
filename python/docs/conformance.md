# ACI Conformance Checklist — v0.1

The keywords MUST, MUST NOT, REQUIRED, SHALL, SHALL NOT, SHOULD, SHOULD NOT, RECOMMENDED, MAY, and OPTIONAL are to be interpreted as described in RFC 2119.

| #  | Requirement | Level |
|----|-------------|-------|
| C1 | Runtime MUST verify manifest signature before loading ACI. | MUST |
| C2 | Runtime MUST verify all file digests against the manifest's digests map. | MUST |
| C3 | Runtime MUST reject ACI whose manifest fails JSON Schema validation. | MUST |
| C4 | Runtime MUST enforce every clause of sandbox.json. | MUST |
| C5 | Runtime MUST emit a signed event on every lifecycle transition. | MUST |
| C6 | Runtime MUST refuse LLM calls that exceed persona.tokenBudget.hardLimit. | MUST |
| C7 | Runtime MUST refuse tool calls not declared in tools/bindings.json. | MUST |
| C8 | Runtime SHOULD cache warm ACIs for at least 5 minutes after last activity. | SHOULD |
| C9 | Runtime SHOULD persist a sealed log bundle on termination. | SHOULD |
| C10 | Runtime MAY support ACIs without SLSA attestation if user opts in. | MAY |
| C11 | Runtime MAY support multiple ACI versions concurrently. | MAY |

The reference runtime implements C1, C2, C3, C4, C5, C9 in `loomwork/aci/conformance.py` and `loomwork/runtime/`. C6 is enforced by the runtime's LLM dispatch hook (not in the v0.1 reference because no LLM is bundled). C7 is enforced by `loomwork.runtime.sandbox.Sandbox`. C8 is a tuning parameter. C10 and C11 are runtime policy.
