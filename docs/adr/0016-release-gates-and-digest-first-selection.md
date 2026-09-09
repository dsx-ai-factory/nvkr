# ADR-16 — Release gates & digest-first selection

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-16 in the consolidated NKD ADR set.
- **Relates to:** ADR-05 (ReleaseSet / Inventory), ADR-06 (status / selection), ADR-04 (verify / signing),
  ADR-19 (ClusterDefinition / consumer).
- **Satisfies:** FR-18 (atomic promotion), FR-24 (incremental validation), SEC-08 (independent gates), SEC-13
  (registry integrity), SEC-17 (downstream verification). Advances UC-05/08/10/14/15/16.

- **Scope:** Sections 1–4 govern qualified release; §5 governs evidence tiers and deferred v1 full-release acceptance.

## Context

Above per-component verification sits the release lifecycle: promoting a complete, compatible, digest-verified
set under two independent gates, selecting and rolling back by immutable digest, and handing a consumable result
to a downstream consumer. Throughout, NKD **produces** evidence and re-selectable verified sets and **does not
operate the fleet**. This ADR fixes those gates, the selection model, and that boundary.

## Decision

### 1. Atomic release set + completeness gate (FR-18)

A release is promoted **all-or-nothing**. Unless the whole set is present and verified, the completeness gate
fails closed with `release-incomplete` and **names every missing required member, artifact, and evidence item**;
a **partial set is not promotable**. The **required set is derived from the Inventory** (ADR-05), the one source
of truth for completeness and compatibility. Every required artifact has an exact, closed `artifactKind` binding
in that Inventory; an unclassified artifact fails closed rather than borrowing a caller-supplied kind.

The promotion policy is itself a canonical, immutable, digest-addressed document authenticated under the
dedicated `promotion-policy` trust role. Its versioned rules map every used artifact kind to typed evidence
requirements and, per evidence type, to a closed allow-list of waivable gap reasons. The same policy names the
exact compatibility and conformance policy digests and carries an effective/expiry interval. A raw policy digest
or a decoded policy value is not a capability. An `evidence:none` declaration is absence, not satisfaction: it
passes only where the authenticated policy does not require that evidence type, or explicitly permits that exact
gap reason and ADR-13's authenticated waiver receipt binds the artifact, recipe, profile, status checkpoint, and
promotion-policy digest.

### 2. Authenticated typed inputs + two independent gates (SEC-08)

Reproduction and runtime conformance are **two independent, mandatory gates**; neither substitutes for the other.

Promotion accepts typed, authenticated receipts rather than caller-supplied booleans:

- one **`ReleaseSetReceipt`**, narrowed from the exact canonical ReleaseSet document authenticated under the
  dedicated release-set role; a parsed structure plus a generic signature callback cannot mint it;
- one **`PromotionPolicyReceipt`**, narrowed from an authenticated canonical document under the dedicated
  promotion-policy role and rechecked for exact bytes, digest, effective time, artifact-kind coverage, evidence,
  waiver, compatibility, and conformance policy bindings;
- a **`ReproductionReceipt` per release member**, binding the exact recipe, ValidationVector, builder-profile,
  resolved-input-manifest, and sorted artifact digests to a reproduction verdict;
- the **`EvidenceReceipt` set** from ADR-13, binding every policy-required evidence type to the exact artifact
  and evidence bytes, or to an exact policy-authorized waiver;
- one **`CompatibilityReceipt`**, binding `releaseSetDigest`, `inventoryDigest`, `compatibilityPolicyDigest`, the
  evaluated member and artifact digests, and the verdict; and
- one **`ConformanceReceipt`**, binding the exact ReleaseSet and ValidationVectors exercised by the external
  suite.

Every receipt that crosses a persistence, process, or organization boundary is canonical, digest-addressed under
its own registered family kind, detached-signed, schema- and version-checked, and authorized against the trust
role for that receipt type before its verdict is read. A same-process evidence input may instead be ADR-13's
opaque qualified capability, but only while it remains in that process and only when `Authorize` revalidates its
private state; it cannot be serialized or reconstructed from public fields. A filename, transport URL, mutable
reference, untyped `verified` or `complete` flag, decoded report, or receipt or capability for a different
artifact, evidence document, or policy is rejected.

- NKD **emits the reproduction verdict** (ADR-04) and **consumes a handed-off conformance result**. It **never
  runs the conformance suite**; running it would break verifier isolation and cross into fleet operation.
- `ConformanceReceipt { releaseSetDigest, inventoryDigest, vectorDigests[], suite, suitePolicyDigest, verdict,
  resultsDigest, effectiveEpoch, expiresEpoch }` is **independently signed** by a **conformance-signer identity
  distinct from the build/publish identity**. `vectorDigests` is sorted and exhaustive for the promoted set.
- A set is promotable **only if** every reproduction receipt says `reproduced`, every required evidence and
  compatibility receipt is accepted, **AND** the matching conformance receipt says `passed`.
  Divergent bytes are not reproduction. The unused variant classifier and approval API have been removed;
  no fallback verdict may stand in for a reproduced receipt.
  Missing, expired, inexact, unauthorized, or rejected receipts produce `gate-unmet`; one verdict is never
  inferred from another.
- Successful promotion emits a signed, digest-addressed **`PromotionReceipt`/clearance** under a dedicated
  clearance trust role. It binds the `releaseSetDigest`, its ADR-06 `publicationIdentity`, `inventoryDigest`,
  `promotionPolicyDigest`, the accepted receipt digests, the accepted status checkpoint, and one canonical
  `PublicationManifest`. Downstream consumers authenticate and rehydrate this exact object rather than
  reconstructing success from flags or trusting a same-process capability.

The `PublicationManifest` is exhaustive. For each OCI subject it lists the cleared artifact and every required
referrer, with both its NKD semantic identity (domain-separated canonical digest) and its raw OCI blob digest and
media type. Publication rejects a missing, extra, type-confused, or substituted attachment; transport
configuration may change the destination but never this authorized set. The signed clearance is the root commit
over the manifest and so is not recursively listed as its own referrer.

### 3. Digest-first selection + registry integrity (SEC-13)

Selection has a single **resolve choke-point**. `name@sha256:…` is used directly; `name:tag` is resolved to its
**current digest**, and the **resolved digest becomes the effective selection** — the tag string is never
persisted as identity. After resolution, **digest and signature are verified** (fail closed with
`unverified-ref`) and **status referrers are consulted through ADR-06's fresh monotonic checkpoint contract**
before the release is accepted. A compromised registry cannot let a mutable tag bypass digest and signature
verification: consumers always select by immutable digest and reject a lifecycle checkpoint below their trust
minimum or high-water mark.

For a ReleaseSet, the status target is
`publicationIdentity = Digest("publication", exact canonical ReleaseSet bytes)`, not the raw `releaseSetDigest`.
Both identities are bound by the authenticated ReleaseSet receipt and cannot be caller-selected.

### 4. Incremental validation, rollback, and clearance — produce, don't operate

- **Incremental validation (FR-24).** A declaration-level diff may identify an unchanged member as a **reuse
  candidate**, but equality of public member fields is not evidence authority. Skipping producer work requires an
  authenticated prior clearance plus the exact prior typed receipts, all rebound to the current release member
  and still acceptable under the current promotion, evidence, compatibility, and conformance policies. Status and
  trusted-time checks are always fresh. Until that authenticated cache protocol exists, NKD re-runs `Authorize`;
  `IncrementalPlan` is diagnostic only and cannot mint or reuse evidence.
- **Rollback (UC-15).** `rollback --to <releaseSetDigest>` makes a **previously verified set re-selectable and
  re-verifiable by immutable digest** and re-runs the clearance path at the as-of epoch from a fresh opaque
  `CheckpointReceipt`. The checkpoint must target the exact candidate and must already have passed current
  signer-policy, completeness, freshness, and monotonic high-water validation under ADR-06; raw declarations or a
  caller-selected signer policy cannot bypass that path. NKD performs **no infrastructure rollback** and is
  idempotent: re-running to the same digest is a no-op.
- **Downstream clearance (SEC-17).** NKD **emits a consumable, digest-addressable clearance result** — asserting
  that identity, signature, provenance, completeness, and compatibility all passed — which a downstream consumer
  checks **before** mutating infrastructure. NKD does **not** gate or authorize the mutation itself.

### 5. Evidence tiers and pipeline-attested release acceptance

Evidence uses a closed typed enum. Unknown values fail admission.

| Tier | Meaning and authority |
|---|---|
| `static-unqualified` | Plan or development execution without authenticated producer evidence; authorizes no release claim |
| `pipeline-attested` | Keyless signature and SLSA v1 production provenance from the producing hosted workflow; trusts that identity, not reproduction |
| `qualified` | Exact-byte reproduction, signed profile and authenticated execution/evidence contracts required by §1–§4 |

A plan can request `pipeline-attested` only with cosign signing. Rendered intent is not observed evidence;
dry-run stays static-unqualified. Pipeline-attested never satisfies qualified reproduction, a ValidationVector,
Inventory, ReleaseSet, clearance, or stage-two receipt. The hosted design targets SLSA Build L2; that is an
acceptance target, not a certification established by local tests.

V1 full-release acceptance is a **deferred** recipe/evidence qualification gate and versioned release manifest.
Qualification checks schema, pins (including embedded-component consistency), signatures, production attestations,
and a tooling smoke test in a network-disabled container. Every subject receives one pass/fail/waived verdict;
a waiver binds component, reason and approver. Failing evidence is never attested. This gate is neither runtime
conformance nor ADR-19 cluster qualification.

The separate qualification workflow signs `nkd.qualification-evidence/v1`; the build workflow cannot sign in
that role. A versioned `nkd.release-manifest/v1` lists every selected artifact from one default-branch Run with
its source lane, digest, signature, provenance, qualification evidence, and explicit deferred members. A fail
verdict makes the Run fail and prevents manifest issuance. Both documents require ADR-01 canonical schema and
family registration before implementation. The public Release view does not turn them into caller-authored
objects and is not itself proof that these gates passed.

Merge of the reviewed bump/regeneration change to the default branch is the v1 release decision; it is not
gated on reproduction. Unattended merge requires separate security authorization. ADR-06 owns immutable tags,
rerun behavior and revocation; ADR-04 owns explicit consumer scopes. An artifact-publication verification result
does not establish full-release acceptance. M3/M4 remain unimplemented.

### 6. Passive external qualification handoff — deferred producer protocol

The external suite owns execution, provisioning, process context and lifecycle. NKD never initiates or
supervises it. NKD authenticates an already-produced independently signed observation containing pinned suite,
suite policy, harness and environment identities, exact raw result bytes, subject and signature. It recomputes
`resultsDigest`, checks role and subject, and applies coverage policy before issuing a receipt. Unsigned input,
unpinned identities, caller-provided pass booleans, wrong roles, expired evidence or mismatched subjects fail
closed with coded errors and no partial output. Release-set conformance remains exhaustive; post-clearance
cluster qualification has the separate ADR-19 subject and cannot authorize promotion.

## Consequences

**Positive**
- A release is complete, compatible, and digest-verified before promotion; partial sets cannot be promoted.
- The two-gate model prevents shipping a build that reproduces but fails conformance, or the reverse, with the
  conformance result independently signed.
- Digest-first selection defeats a compromised-registry tag swap.
- The model permits a future authenticated evidence cache to keep producer cost proportional to change, and
  rollback restores a known-good candidate only after current authorization and status checks pass.

**Negative / costs**
- The conformance-result contract and its signer identity are an external dependency — the suite producer, NKE.
- The clearance result is a new consumable object the downstream must agree to check.

**Neutral**
- The boundary is load-bearing: NKD consumes a conformance result and emits a clearance or verification result.
  It runs no suite, performs no infra rollback, and authorizes no mutation.

## Alternatives considered

- **Infer conformance from reproduction (single gate).** Rejected — SEC-08 requires two independent gates.
- **Run the conformance suite inside NKD.** Rejected — it breaks verifier isolation and the produce-vs-operate
  boundary.
- **Select releases by tag.** Rejected — a mutable tag must never bypass digest and signature verification.
- **Rollback that mutates running infrastructure.** Rejected — NKD re-selects a verified set; the engine makes
  any infra change.

## Follow-ups

- **ADR-05:** the ReleaseSet and Inventory the gates read, and the ValidationVector evidence the conformance
  result populates.
- **ADR-06:** status referrers consulted during selection.
- **ADR-19:** generated cluster realization binds exact intent to accepted Release contents; the legacy
  ClusterDefinition wire references a cleared ReleaseSet by digest.
- **ADR-13:** owns typed evidence and waiver receipts; promotion consumes but does not reinterpret them.

## Acceptance evidence

Current module evidence in `pkg/nkd/release/*_test.go`, `pkg/nkd/status/checkpoint_test.go`, and
`pkg/nkd/trust/document_test.go` covers exact authenticated ReleaseSet and promotion documents, opaque
reproduction, compatibility, and conformance capabilities, same-process qualified evidence, distinct digest
families, exact artifact-kind policy dispatch, policy-derived evidence and waiver behavior, role and policy
substitution, expiry, mutation isolation, status-checkpoint rollback, completeness, digest-first selection, and
race-enabled deterministic authorization. This is module/contract evidence: it does **not** prove a real
Sigstore, scanner, or registry publication and downstream-consumer round trip.

Acceptance additionally requires a full fixture that starts with exact artifact bytes and canonical evidence,
authenticates all typed receipts under distinct trust roles, promotes one complete compatible set, and verifies
the resulting clearance. Negative variants must cover a caller-forged boolean, missing `evidence:none` waiver,
receipt replay to a different artifact/vector/policy, unauthorized signer, expired conformance receipt,
compatibility mismatch, incomplete or unclassified artifact set, unknown promotion-policy schema, disallowed
waiver reason, an attempted `validated-variant` reproduction receipt without semantic-comparison authority,
stale or equivocating status checkpoint, and downstream rejection of a substituted clearance.
