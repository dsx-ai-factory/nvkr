# NKD — JIRA Epics (create these 4)

> [!CAUTION]
> **Historical planning export.** The stories and completion assumptions below are not the current support or
> release contract. Reconcile future work through the proposed ADRs and `docs/adr/reconciliation.md`.

Paste-ready content for the four NKD Epics. **Epics only** — create the stories separately and link them via
**Epic Link**. Field names map to standard JIRA Epic fields; adjust project key, priorities, and labels to your board.

Suggested board conventions used below:
- **Component:** `release_eng` on every epic. **Label:** `nke_nkd` on every epic.
- **Links:** `Depends on` / `Blocks` express the E1 → E2 → E3 → E4 order.
- Requirement IDs (FR/SEC/UC) are listed in each description; add them as labels or requirement links if your board tracks traceability.

---

## E1 — NKD Design & Contracts

| Field | Value |
|---|---|
| **Issue type** | Epic |
| **Epic name** | NKD Design & Contracts |
| **Summary** | NKD Design & Contracts: shared schemas, interfaces, invariants, and de-risking spikes |
| **Priority** | Highest |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Depends on** | — |
| **Blocks** | E2, E3, E4 |
| **Story count** | 8 (design domains A–H; 54 decisions) |

**Description**

Settle, before any critical-path code, the shared schemas, interfaces, invariants, and spikes that the implementation epics build on: the canonical serialization + digest-addressing codec, the IR-purity boundary, the frozen coded-error/exit-code table, the trust-contract types (Manifest, Report, VerifyAgainst seam, ValidationVector, ReleaseSet), the executed-cosign Signer/Verifier seam, the reproduction-identity tuple, and the composition merge algebra — plus the technical spikes that de-risk them.

Outcome: the trust cluster becomes a DAG instead of a cyclic knot, nothing is designed twice, and E2–E4 bind to stable contracts.

In scope — 8 design stories, one per domain:
- **E1-A** Trust Contracts & Data Model — canonical codec, digest-addressing, IR-purity boundary, exit-code registry, trust-object schemas
- **E1-B** Signing & Verification Architecture — executed cosign seam, trust-store precedence, offline bundle, verifier isolation
- **E1-C** Reproducibility & Builder-Profile Model — reproduction tuple, profile signing, hard-fail policy, two-machine bake-off protocol
- **E1-D** Canonical Disk & Delivery Model — RAW-canonical disk manifest, envelope decode-verify, pre-encryption/FDE boundary, role SKUs
- **E1-E** Composition & Resolution Semantics — merge algebra + sibling-ambiguity predicate, golden fixtures, scheme allowlist, AICR axes
- **E1-F** Supply-Chain Evidence & Serialization — SBOM identity, SLSA predicate, serialization profile, external-evidence refs, vuln determinism
- **E1-G** CLI / SDK & Error Contract — coded-error table, client/v1 facade, ctx + cleanup, output contract, selector grammar, package topology
- **E1-H** Air-gap & Release-Lifecycle Flows — pinned-input/mirror model, vendored-tree repro, toolchain mirrorability, conformance handoff, digest-first selection, status declarations

**Definition of done**
- The core design decisions are ratified as ADRs/schemas: canonical codec, IR-purity partition + guard, schemaVersion/migration, the banded exit-code table, the reproduction tuple, the cosign Signer/Verifier interface, trust-store precedence, the release object-family skeleton, the SDK facade + ctx/output contracts, package topology, and the composition merge algebra + sibling-ambiguity predicate.
- The five de-risking spikes are run with recorded outcomes: two-machine bake-off, vendored-tree reproducibility, qemu-img decode determinism, ext4 residual (eliminate vs bound), cancel-cleanup.
- Every shared contract is single-owned (no schema, identity, or exit code decided twice) and referenced by the E2–E4 stories that implement it.
- The non-normative schemas (ValidationVector / ReleaseSet / NKE inventory) carry a `schemaVersion`.

---

## E2 — NKD Build & Reproducibility

| Field | Value |
|---|---|
| **Issue type** | Epic |
| **Epic name** | NKD Build & Reproducibility |
| **Summary** | NKD Build & Reproducibility: recipe → IR, hermetic + no-compile build across CI engines, signed builder profile |
| **Priority** | High |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Depends on** | E1 |
| **Blocks** | E3, E4 |
| **Story count** | 40 (5 sub-areas) |

**Description**

Turn a criteria query into a single deterministic, digest-identified IR and build or acquire every component reproducibly and hermetically — the foundation substrate (contracts implemented), the recipe/composition/catalog front end, the from-source and no-compile lanes lowered identically to every CI engine, and the signed-builder-profile reproducibility boundary with a two-machine bake-off gate.

Outcome: an operator can resolve one recipe and reproduce every Kubernetes-substrate component byte-for-byte in their own CI on whichever engine they run — the trustworthy build half that everything downstream is measured against.

Sub-areas: Foundation (canonical codec, coded errors, SDK skeleton, trust-contract types, context/IO) · Recipe, Composition & Catalog · Hermetic From-Source Build & CI Portability · No-Compile (Prebuilt) Acquisition Lane · Reproducibility Boundary & Builder Profile.

**Requirements owned** — FR-01, FR-02, FR-03, FR-04, FR-05, FR-06, FR-07, FR-10, FR-12, FR-13, FR-20, FR-21; SEC-09, SEC-10, SEC-11.
**Use cases advanced** — UC-01, UC-02, UC-03, UC-14.

**Definition of done**
- A criteria query resolves and composes to exactly one deterministic, digest-identified IR; unpinned/tag/ambiguous inputs fail closed with distinct coded exit codes.
- Every Tier-1 component builds from source hermetically (`--network=none`, pinned-digest toolchains), byte-identical across all five CI engines and the local runner.
- The no-compile lane acquires and digest-verifies prebuilt artifacts, fail-closed on mismatch, with no builder-profile digest.
- A signed builder profile bounds a passing two-machine bake-off; reproducibility is asserted only within the recipe × engine × input-manifest × profile tuple.
- No host/clock/random value enters `recipeDigest`; the IR-purity guard is enforced by test.

---

## E3 — NKD Trust, Verify & Evidence

| Field | Value |
|---|---|
| **Issue type** | Epic |
| **Epic name** | NKD Trust, Verify & Evidence |
| **Summary** | NKD Trust, Verify & Evidence: executed signing + fail-closed verify + variant lane + SBOM/provenance/vuln + offline |
| **Priority** | High |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Depends on** | E1, E2 |
| **Blocks** | E4 |
| **Story count** | 35 (5 sub-areas) |

**Description**

Make the zero-binary trust model real and enforceable — executed detached keyless signing + a trust store, a fail-closed isolated verifier that names the first divergent node, the sanctioned expiring ValidatedVariant lane, deterministic CycloneDX SBOM + SLSA provenance + vulnerability evidence, and equivalent-trust offline / air-gapped operation.

Outcome: a publisher ships only a signed, detached expected-digests manifest; any operator or auditor independently verifies byte-identity, signatures, provenance, and SBOM — online or air-gapped — and a sanctioned representation-only variance is governed, not faked.

Sub-areas: Attestation, Detached Signing & Trust · Verification Engine · ValidatedVariant Lane · Supply-Chain Evidence (SBOM, Provenance, Vulnerability) · Offline / Air-Gapped Operation.

> **Critical path:** story **E5-S1 (execute cosign behind a testable Signer/Verifier seam)** is the single biggest unlock and gates E4-S3, E6-S1, E7-S5, E9-S1, E11-S5, E12-S4/S5. Prioritize it as soon as E2's foundation + build land.

**Requirements owned** — FR-08, FR-09, FR-11, FR-15, FR-19; SEC-01, SEC-02, SEC-03, SEC-04, SEC-05, SEC-06, SEC-07, SEC-14, SEC-15, SEC-18.
**Use cases advanced** — UC-01, UC-02, UC-07, UC-09, UC-12, UC-13, UC-16.

**Definition of done**
- Cosign is executed and tested end-to-end (detached keyless sign → verify → tamper-fails); a trust store pins the signer identity/issuer and fails closed on an untrusted signer.
- `verify` recomputes `recipeDigest`, diffs digests, names the first divergent node, exits non-zero, runs as the unconditional terminal node on every engine, and is isolated from the build stages.
- The ValidatedVariant lane accepts only a pre-classified representation-only cause (SBOM/vuln/integrity/policy pass) into a distinct, expiring, non-reproduction identity.
- Deterministic per-artifact SBOM + SLSA provenance + vulnerability evidence, bound by digest.
- Offline verify preserves equivalent semantics (mirrored vector + offline Rekor inclusion proof), fail-closed.

---

## E4 — NKD Release, Delivery & CLI

| Field | Value |
|---|---|
| **Issue type** | Epic |
| **Epic name** | NKD Release, Delivery & CLI |
| **Summary** | NKD Release, Delivery & CLI: atomic release sets + gates + inventory, AICR, pinning/publication, packages/images/disk, CLI/SDK |
| **Priority** | High |
| **Components** | release_eng |
| **Labels** | `nke_nkd` |
| **Depends on** | E1, E2, E3 |
| **Blocks** | — |
| **Story count** | 35 (5 sub-areas) |

**Description**

Publish and deliver — atomic, digest-selectable release sets with compatibility + two independent promotion gates + rollback + the pinned NKE inventory, the shelled accelerated-runtime (AICR) integration, constraint-driven pinning + immutable publication + supersede/withdraw, the reproducible packages/images/bootable-disk delivery surface, and the CLI + SDK capstone.

Outcome: a complete, compatible, digest-verified release set is promoted all-or-nothing, delivered as installable packages / images / bootable node artifacts, kept current by constraint-driven pins, and consumed by any control-plane engine (NKE or an alternative) through a clean, agnostic contract.

Sub-areas: Accelerated-Runtime (AICR) Integration · Constraint-Driven Pinning, Publication & Release Maintenance · Release Lifecycle (Sets, Compatibility, Gates, Rollback, Inventory) · Substrate Delivery (Packages, Images, Bootable Disk) · CLI Capstone & External Interface Seams.

> **Boundary discipline:** the release-lifecycle stories *produce* evidence and *re-selectable* verified sets. nkdctl consumes a handed-off conformance result and emits a clearance/verification result; it does **not** run the conformance suite, perform infrastructure rollback, or own cluster instantiation.

**Requirements owned** — FR-14, FR-16, FR-17, FR-18; SEC-08, SEC-13, SEC-16, SEC-17.
**Use cases advanced** — UC-01, UC-04, UC-05, UC-06, UC-08, UC-10, UC-11, UC-12, UC-14, UC-15, UC-16.

**Definition of done**
- Atomic, digest-addressable release sets with an all-or-nothing completeness gate; partial sets are not promotable.
- Two independent promotion gates (byte reproduction × handed-off runtime conformance) plus rollback to a previously verified set by immutable digest; the pinned NKE inventory is bound to the release.
- The AICR bundle is rendered via the shelled flow, digest-verified against the pinned profile, and folded into the trust model; AICR is never linked.
- Constraint-driven pins to a machine-managed lock; immutable publication (correction = new identity) with signed supersede/deprecate/withdraw.
- Reproducible `.deb` packages, OCI images, and RAW-canonical bootable disks (with derived envelopes verified by decoded logical-disk digest); the CLI/SDK surface is complete (`query`, `diff`, `trust`, `catalog author`/`publish`, external-interface seams) with uniform `--data` and coded exit codes.

---

## Create-order summary

| Order | Epic | Priority | Depends on | Stories |
|---|---|---|---|---|
| 1 | E1 — NKD Design & Contracts | Highest | — | 8 |
| 2 | E2 — NKD Build & Reproducibility | High | E1 | 40 |
| 3 | E3 — NKD Trust, Verify & Evidence | High | E1, E2 | 35 |
| 4 | E4 — NKD Release, Delivery & CLI | High | E1, E2, E3 | 35 |
