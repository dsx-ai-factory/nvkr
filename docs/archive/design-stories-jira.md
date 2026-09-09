# NKD — JIRA Stories: E1 Design & Contracts (create these 8)

Paste-ready content for the 8 design stories under Epic **NKD Design & Contracts** (NKX-14794). One story per
design domain (A–H). Field names map to standard JIRA Story fields; adjust to your board.

Conventions:
- **Issue type:** Story. **Epic Link:** NKX-14794. **Component:** `release_eng`. **Label:** `nke_nkd` on every story.
- **Priority** uses the NKX scheme (`P0 - Must have` / `P1 - Should have`). Domains carrying the most foundational,
  freeze-first decisions are P0; the rest are P1.
- Each story's decisions are listed inline so the ticket is self-contained.

---

## E1-A — NKD Design: Trust Contracts & Data Model

| Field | Value |
|---|---|
| **Issue type** | Story |
| **Summary** | NKD Design: Trust Contracts & Data Model |
| **Priority** | P0 - Must have |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Epic Link** | NKX-14794 |
| **Design decisions** | 7 (5 freeze-first) |

**Description**

Settle the canonical serialization + digest-addressing codec, the IR-purity boundary, the coded-error/exit-code registry, and the trust-object schemas (Manifest, Report, VerifyAgainst seam, ValidationVector, ReleaseSet, Inventory, publication ledger).

**Decisions**
- DD-A1 — One canonical serialization codec + a DigestAddressable seam for every trust object
- DD-A2 — The covered-bytes vs signed-metadata vs detached-record field partition (IR-purity boundary)
- DD-A3 — schemaVersion + additive-only migration discipline for all trust objects
- DD-A4 — ValidationVector as a first-class digest-addressable object + the ref string->digest migration
- DD-A5 — Atomic ReleaseSet identity + completeness predicate + NKE Inventory pinned-input schema
- DD-A6 — Immutable publication identity + append-only ledger + signed status-declaration object
- DD-A7 — Reserve the full coded-error / exit-code registry upfront (frozen contract)

**Freeze first (foundational, settle before dependent implementation):** DD-A1, DD-A2, DD-A3, DD-A4, DD-A7

**Definition of done**
- Every decision above is ratified as an ADR / schema / invariant / spike / migration / policy, with its recommendation, options, and open questions recorded.
- The freeze-first decisions are settled and frozen before dependent implementation begins.
- Each shared contract is single-owned — no schema, identity, or exit code decided twice.

---

## E1-B — NKD Design: Signing & Verification Architecture

| Field | Value |
|---|---|
| **Issue type** | Story |
| **Summary** | NKD Design: Signing & Verification Architecture |
| **Priority** | P0 - Must have |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Epic Link** | NKX-14794 |
| **Design decisions** | 7 (4 freeze-first) |

**Description**

Settle the executed cosign Signer/Verifier seam, the trust-store schema and precedence, the offline bundle format, and the verifier-isolation approach.

**Decisions**
- DD-B1 — Executed cosign Signer/Verifier seam (interface + local-key roundtrip + OIDC/Rekor-gated integration lane)
- DD-B2 — Trust-store schema + resolution precedence (CLI > operator store > catalog-advisory), closing the catalog fail-open hole
- DD-B3 — Trust-boundary exit-code taxonomy — distinct codes for signature-invalid vs untrusted-signer vs policy-missing vs digest-diverged
- DD-B4 — Offline mirrorable-bundle format + trusted-root mirror (NKD-OI-004 TBD)
- DD-B5 — Verifier-isolation approach — measure-only path in one binary vs a separate verifier binary/build-tag (SEC-07)
- DD-B6 — cosign integration contract — pinned version range, assert-exit-codes-not-stdout, OIDC issuer/identity configurable at BOTH sign and verify
- DD-B7 — ValidationVector/ReleaseSet digest-addressable objects + the SEC-02 digest-authentication chain

**Freeze first (foundational, settle before dependent implementation):** DD-B1, DD-B2, DD-B3, DD-B6

**Definition of done**
- Every decision above is ratified as an ADR / schema / invariant / spike / migration / policy, with its recommendation, options, and open questions recorded.
- The freeze-first decisions are settled and frozen before dependent implementation begins.
- Each shared contract is single-owned — no schema, identity, or exit code decided twice.

---

## E1-C — NKD Design: Reproducibility & Builder-Profile Model

| Field | Value |
|---|---|
| **Issue type** | Story |
| **Summary** | NKD Design: Reproducibility & Builder-Profile Model |
| **Priority** | P0 - Must have |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Epic Link** | NKX-14794 |
| **Design decisions** | 7 (3 freeze-first) |

**Description**

Settle the reproduction-identity tuple, builder-profile signing, the hard-fail field policy, the IR-purity guard, and the two-machine bake-off protocol.

**Decisions**
- DD-C1 — Reproduction-identity tuple: field selection + engineClass decoupled from CI adapter
- DD-C2 — Builder-profile signing + digest-identity + trust-chain authentication
- DD-C3 — Hard-fail vs advisory field classification + recipe-declared supported-profile set
- DD-C4 — IR-purity boundary invariant + automated guard
- DD-C5 — Detached build-record / SLSA provenance schema (where host/clock/engine land)
- DD-C6 — Two-machine bake-off protocol (split emit/compare, env isolation) + reproducibility spike
- DD-C7 — Canonical RAW logical-disk digest-addressing + envelope decode-verify contract

**Freeze first (foundational, settle before dependent implementation):** DD-C1, DD-C4, DD-C6

**Definition of done**
- Every decision above is ratified as an ADR / schema / invariant / spike / migration / policy, with its recommendation, options, and open questions recorded.
- The freeze-first decisions are settled and frozen before dependent implementation begins.
- Each shared contract is single-owned — no schema, identity, or exit code decided twice.

---

## E1-D — NKD Design: Canonical Disk & Delivery Model

| Field | Value |
|---|---|
| **Issue type** | Story |
| **Summary** | NKD Design: Canonical Disk & Delivery Model |
| **Priority** | P1 - Should have |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Epic Link** | NKX-14794 |
| **Design decisions** | 7 (1 freeze-first) |

**Description**

Settle the RAW-canonical disk manifest, the qemu-img envelope decode-verify seam, the pre-encryption/FDE boundary, role SKUs, and ISO/provisioning shapes.

**Decisions**
- DD-D1 — Canonical disk artifact manifest schema (RAW logical digest + block + content granularity)
- DD-D2 — Derived-envelope decode/convert seam (pinned qemu-img subprocess, fail-closed)
- DD-D3 — qemu-img decode/convert determinism + tool-pin spike
- DD-D4 — Pre-encryption/FDE boundary marker + new coded exit code for encrypted-bytes assertions
- DD-D5 — ext4 full-rootfs determinism residual — eliminate vs bounded-and-masked
- DD-D6 — Role-parameterized SKU identity model + deterministic staging manifest
- DD-D7 — ISO-as-second-canonical-class policy + PXE/iPXE/HTTP provisioning bundle schema

**Freeze first (foundational, settle before dependent implementation):** DD-D4

**Definition of done**
- Every decision above is ratified as an ADR / schema / invariant / spike / migration / policy, with its recommendation, options, and open questions recorded.
- The freeze-first decisions are settled and frozen before dependent implementation begins.
- Each shared contract is single-owned — no schema, identity, or exit code decided twice.

---

## E1-E — NKD Design: Composition & Resolution Semantics

| Field | Value |
|---|---|
| **Issue type** | Story |
| **Summary** | NKD Design: Composition & Resolution Semantics |
| **Priority** | P1 - Should have |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Epic Link** | NKX-14794 |
| **Design decisions** | 6 (2 freeze-first) |

**Description**

Settle the normative composition merge algebra and sibling-ambiguity predicate, golden-digest fixtures, the standard-interface scheme allowlist, the extension-surface classifier, and AICR axis matching.

**Decisions**
- DD-E1 — Ratify the normative layered-composition merge algebra (child-wins, scalar/slice-replace vs map-merge, enabled:false, mixin no-compose)
- DD-E2 — The NEW sibling-ambiguity fail-closed conflict predicate
- DD-E3 — Golden-digest fixture strategy + documented regen path
- DD-E4 — The 4-way operator-extension-surface classifier (published-tuple / declared-overlay / variant / fork)
- DD-E5 — Standard-interface scheme allowlist (per-field-role, permissive for operator equivalents)
- DD-E6 — AICR axis-matching semantics + the IR-purity boundary for the AICR profile

**Freeze first (foundational, settle before dependent implementation):** DD-E1, DD-E2

**Definition of done**
- Every decision above is ratified as an ADR / schema / invariant / spike / migration / policy, with its recommendation, options, and open questions recorded.
- The freeze-first decisions are settled and frozen before dependent implementation begins.
- Each shared contract is single-owned — no schema, identity, or exit code decided twice.

---

## E1-F — NKD Design: Supply-Chain Evidence & Serialization

| Field | Value |
|---|---|
| **Issue type** | Story |
| **Summary** | NKD Design: Supply-Chain Evidence & Serialization |
| **Priority** | P1 - Should have |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Epic Link** | NKX-14794 |
| **Design decisions** | 7 |

**Description**

Settle the per-artifact SBOM identity, the SLSA provenance predicate, the evidence-serialization profile, external-evidence references, and vulnerability-report determinism.

**Decisions**
- DD-F1 — SBOM identity model: per-artifact document (bound to artifact digest) + aggregate index, with migration of recipeDigest-keyed consumers
- DD-F2 — Declared schemaVersions registry + one canonical deterministic evidence-serialization profile (UUIDv5 serial numbers; omit clock fields)
- DD-F3 — SLSA provenance predicate shape + deterministic profile (in-toto v1; builder.id=profileDigest; materials=pinned sources+toolchains; omit build-time)
- DD-F4 — External-evidence reference schema for prebuilt / externally-supplied artifacts (per-source resolver vs explicit-declaration + graceful evidence:none)
- DD-F5 — Aggregate release evidence rows (artifact<->sbomDigest<->provenanceDigest) and the ownership seam with E12's ValidationVector
- DD-F6 — Vulnerability-report determinism: pin scanner DB/policy version as an explicit input; shelled scanner seam; missing-scanner policy
- DD-F7 — Advisory -> affected-recipe INDEX ownership boundary (nkdctl emits cveRemediation evidence over supplied inputs; it does not own the fleet inventory)

**Definition of done**
- Every decision above is ratified as an ADR / schema / invariant / spike / migration / policy, with its recommendation, options, and open questions recorded.
- Each shared contract is single-owned — no schema, identity, or exit code decided twice.

---

## E1-G — NKD Design: CLI / SDK & Error Contract

| Field | Value |
|---|---|
| **Issue type** | Story |
| **Summary** | NKD Design: CLI / SDK & Error Contract |
| **Priority** | P0 - Must have |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Epic Link** | NKX-14794 |
| **Design decisions** | 6 (3 freeze-first) |

**Description**

Settle the coded-error/exit-code table, the client/v1 SDK facade, context + subprocess cleanup, the output contract, the selector grammar, and package topology.

**Decisions**
- DD-G1 — Freeze the coded-error taxonomy + stable exit-code table (add Timeout/Unavailable/Usage + Retryable predicate)
- DD-G2 — client/v1 SDK facade surface: functional-options config vs per-call Request structs, ctx-first methods
- DD-G3 — context.Context threading + subprocess cleanup-on-cancel protocol (no orphaned containers / stale output)
- DD-G4 — stdout=results / stderr=structured-logs contract (slog, --log-level, NO_COLOR, --output json)
- DD-G5 — Bounded query/diff selector grammar + diff exit-code convention (not a JSONPath engine)
- DD-G6 — Package topology delta + single-owner rule: facade owns orchestration; reserve provenance/vuln/release/status/mirror

**Freeze first (foundational, settle before dependent implementation):** DD-G1, DD-G2, DD-G3

**Definition of done**
- Every decision above is ratified as an ADR / schema / invariant / spike / migration / policy, with its recommendation, options, and open questions recorded.
- The freeze-first decisions are settled and frozen before dependent implementation begins.
- Each shared contract is single-owned — no schema, identity, or exit code decided twice.

---

## E1-H — NKD Design: Air-gap & Release-Lifecycle Flows

| Field | Value |
|---|---|
| **Issue type** | Story |
| **Summary** | NKD Design: Air-gap & Release-Lifecycle Flows |
| **Priority** | P1 - Should have |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Epic Link** | NKX-14794 |
| **Design decisions** | 7 (1 freeze-first) |

**Description**

Settle the pinned-input/mirror model, vendored-tree reproducibility, toolchain mirrorability, the runtime-conformance handoff contract, digest-first selection, and status declarations.

**Decisions**
- DD-H1 — PinnedInput immutable-identity model + content-addressed mirror layout
- DD-H2 — IR relocation & vendored-tree reproducibility (zero-egress build spike)
- DD-H3 — Toolchain mirrorability policy — local image ID -> registry digest (fail-closed vs push)
- DD-H4 — The pkg/nkd/release object schema family (ValidationVector, ReleaseSet, Inventory, Clearance, Selection)
- DD-H5 — Runtime-conformance handoff contract (ConformanceResult object, binding, signer trust)
- DD-H6 — Digest-first selection + rollback-as-reselection invariant
- DD-H7 — Status-declaration schema + consumption trust surface (withdraw-DoS defense)

**Freeze first (foundational, settle before dependent implementation):** DD-H4

**Definition of done**
- Every decision above is ratified as an ADR / schema / invariant / spike / migration / policy, with its recommendation, options, and open questions recorded.
- The freeze-first decisions are settled and frozen before dependent implementation begins.
- Each shared contract is single-owned — no schema, identity, or exit code decided twice.

---

## Summary

| Story | Priority | Decisions | Summary |
|---|---|---|---|
| E1-A | P0 | 7 (5 freeze-first) | NKD Design: Trust Contracts & Data Model |
| E1-B | P0 | 7 (4 freeze-first) | NKD Design: Signing & Verification Architecture |
| E1-C | P0 | 7 (3 freeze-first) | NKD Design: Reproducibility & Builder-Profile Model |
| E1-D | P1 | 7 (1 freeze-first) | NKD Design: Canonical Disk & Delivery Model |
| E1-E | P1 | 6 (2 freeze-first) | NKD Design: Composition & Resolution Semantics |
| E1-F | P1 | 7 | NKD Design: Supply-Chain Evidence & Serialization |
| E1-G | P0 | 6 (3 freeze-first) | NKD Design: CLI / SDK & Error Contract |
| E1-H | P1 | 7 (1 freeze-first) | NKD Design: Air-gap & Release-Lifecycle Flows |
