# Loomwork Go runtime — code review

**Status: all items below have been fixed in the working tree** (see the Fix column). Regression tests: `go test ./...`, including `cli_e2e_test.go`, which exercises the CLI end to end.

Scope: `go/` (version 0.1.0), read in full apart from the Python implementation and the example agents. Items marked **[verified]** were reproduced by running code; the rest come from reading it.

## Critical / High

| # | Where | Defect | Evidence | Fix |
|---|---|---|---|---|
| 1 | `aci/signing.go` `VerifyArchiveSignature` | The public key used to verify is read from the archive itself (`signatures/manifest.cert`). Anyone can alter an ACI, re-sign it with their own key, and it verifies. There is no trusted-key check. | **[verified]** attacker-key archive verified: true | Signer must be in `~/.loomwork/trusted/` (`aci/trust.go`); `trust add/list/remove`; `run`/`verify` refuse untrusted signers |
| 2 | `aci/archive.go` `Unpack` | Entry names from the tar are joined to the target directory without checks, so `../../x` writes outside it (zip-slip). Unpack is currently unused, so this is latent. | **[verified]** file written outside the target | Entry names validated on load and unpack; unpack confirms the path stays inside the target |
| 3 | `package.go` / `aci/archive.go` `ArchiveFromDirectory` | Packaging includes every file in the directory, not only manifest-listed files. A `.env`, keys, earlier `.aci` files are shipped inside the shareable archive. | **[verified]** `.env` present in the `.aci` | `ArchiveFromDirectory` reads only manifest-listed files and the two signature files |
| 4 | `aci/archive.go` (both loaders) | Files not listed in `digests` are accepted and sit in `Files` unsigned and unchecked. | **[verified]** unlisted file loaded | Loader rejects any file the manifest does not list, unexpected `signatures/*` entries, duplicates |
| 5 | `amp/envelope.go:121` | `env.Method[:4]` panics for methods shorter than four characters, so a single crafted message crashes the process. | **[verified]** slice bounds panic | `strings.HasPrefix`; jsonrpc version check; 1 MiB message limit |
| 6 | `memory/revoke.go`, `Supersede` | `Supersede` sets the new record's `ParentID` to the old one, which creates a derivative edge. Revoking the old record then revokes its replacement. | **[verified]** replacement became revoked | `Supersede` no longer creates a derivative edge |
| 7 | `memory/revoke.go` | Revocation is a soft delete: the full record payload stays in the database and is readable. This contradicts the stated purpose of revocation. It also does not touch the conversation store, summaries or caches. | **[verified]** revoked record still readable | Revoke replaces the record with a tombstone, deletes the embedding, `secure_delete` on; un-revoke refuses |
| 8 | `memory_cmd.go:49` | The typed store is opened with an empty passphrase, so records are stored in plaintext. `sensitivity` has no effect. | Read; earlier run showed plaintext | Typed store uses the shared encryption key (`memory.OpenDefaultStore`); sensitivity=high needs the `sensitive` scope |
| 9 | `receipt.go` `printVerificationReceipt` | The receipt line is printed from the sidecar file without verifying its signature or that it belongs to this archive. A forged file shows `tests=PASSED`. | **[verified]** forged sidecar accepted | Receipt shown only after signature and archive-digest checks (`loadAttestation`) |

## Medium

| # | Where | Defect | Fix |
|---|---|---|---|
| 10 | `aci/slsa.go` | The provenance statement claims SLSA Level 3 but is unsigned, generated on the packaging machine, with a zero builder digest and `gitCommit: unknown`. `VerifySLSA` checks only the archive hash, which a forger can also supply. | Provenance statement signed by the ACI key and verified with `VerifySLSASigned`; no Level-3 claim, no fake builder digest |
| 11 | `verify/gate.go` | Attestation `Subject` is `name@version`, not the archive digest, so an attestation can be reused on a different archive. The "referenced files exist" check adds a passing entry even when files are missing. Property tests only test that JSON parses. | Attestation carries `archiveDigest`; property tests check real properties; SBOM lists manifest files only |
| 12 | `runtime/sandbox.go`, `runner.go` | Sandbox policy, tool bindings, token budgets, and `time.maxWall` are never enforced; nothing calls the evaluator. `verify` and `run` also skip the skills-graph check. | `Runner.Prepare` enforces sandbox (read, egress, wall time), tool bindings and token budgets; skills graph validated on load |
| 13 | `amp/*` | AMP is not reachable from the CLI. Capability tokens and provenance are never verified; credit receipts are never signature-checked. `Recv` reads one byte at a time with no size limit, and the goroutine leaks on timeout. | `amp serve/delegate/token` in the CLI; capability tokens and signed provenance verified; bounded, context-aware transport; receipts signed |
| 14 | `memory/store.go` | `decrypt` indexes `blob[:ns]` without a length check (panic on a short or corrupt value). Status, consent, provenance and retention are also stored in plaintext columns next to the encrypted payload. | Length-checked versioned encryption (`internal/secret`); metadata columns no longer duplicate consent/provenance/retention |
| 15 | `memory/contract.go` | `randHex` derives its "random" characters from the clock, so IDs are predictable and may collide. | IDs use crypto/rand |
| 16 | `memory/view.go` | Typed memory is not connected to `run`/`ask`. `List` over-fetches then filters, so results can be short; `limit=0` returns one record; the doc comment promises a sensitivity filter that does not exist; `Search` matches on JSON field names. | Typed memory wired into `run`/`ask`/`amp serve`; `List`/`Search` fixed; sensitivity filter implemented |
| 17 | `memory/*` | Evidence links in a belief are not derivative edges, so revoking evidence leaves dependent beliefs active. `Reject` reuses the `revoked` status and does not cascade. | Evidence and episode-input links create derivative edges; `Reject` has its own status and deletes content |
| 18 | `runtime/runner.go` `Ask` | History query has no kind filter, so `folder_index` entries (one per `ask --folder`) fill the ten-entry window and push out real conversation. Ordering uses one-second timestamps. Stored history is inserted into the system prompt, so stored text acts as instructions. | `QueryConversation` (turns only, RFC3339Nano, rowid tiebreak); history sent as chat turns, not in the system prompt |
| 19 | `runtime/runner.go` `IndexFolder` | `SkipDir` returned from a file callback at 200 files skips the rest of that directory, not the walk. Samples any `.json/.txt/.md` file, which can include secrets, and sends it to the model (a cloud model after fallback). | `filepath.SkipAll`; hidden files and secret-looking names skipped; cloud models get listing only |
| 20 | `ask.go` | The full folder index is appended to the question, so it is also persisted into memory on every call. | Question stored alone; folder index stored once per agent+folder |
| 21 | `init.go` | `init` overwrites existing files in the target directory without asking. | `init` refuses to overwrite without `--force` |
| 22 | `aci/manifest.go` | Canonical JSON is not fully RFC 8785: byte sort instead of UTF-16, and number handling overflows for large integers. Cross-implementation signatures could diverge for non-ASCII keys. `required` skips empty path fields. | UTF-16 key order, RFC 8785 numbers and escaping; empty references rejected |
| 23 | `aci/archive.go` | `io.ReadAll` per tar entry with no limits (decompression bomb). Duplicate entry names silently overwrite. | Size, count and duplicate limits on load |
| 24 | `cross_compat_test.go` | Previously depended on `/tmp/loomwork` (fixed in the working copy). | Test builds its own binary |

## Low

- `runtime/memory.go` returned ciphertext as text on a wrong key — fixed: encrypted values carry a prefix and a wrong or missing key is an error; only unprefixed legacy values read as plaintext.
- `newID` collisions in `runtime`/`amp` — fixed (random suffix).
- `DetectCycles` path aliasing — fixed.
- gofmt formatting — fixed.
- Source comments and docs that named external discussion threads — removed.

## Remaining known limits (not defects, documented in the PRD)
- CPU/memory limits in `sandbox.json` are declared, not applied.
- Conversation entries are not linked to typed records, so revoking a record does not remove text the model already saw in a stored conversation.
- Memory consent identity is the ACI name.
- The key that decrypts memory lives on the same machine as the database.
- No MCP tool execution, no key distribution service.
