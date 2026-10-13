# ADR-16 — Release gates & digest-first selection

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-16 in the consolidated NVKR ADR set.
- **Relates to:** ADR-05 (ReleaseSet / Inventory), ADR-06 (status / selection), ADR-04 (verify / signing),
  ADR-19 (deployment-scope boundary).
- **Satisfies:** FR-18 (atomic promotion), FR-24 (incremental validation), SEC-08 (independent gates), SEC-13
  (registry integrity), SEC-17 (downstream verification). Advances UC-05/08/10/14/15/16.

- **Scope:** Sections 1–4 specify deferred qualified release authorization. Current implementation retains
  strict Inventory, ReleaseSet and PromotionPolicy documents, required-member/artifact completeness, and
  authenticated ReleaseSet/policy diagnostics. Section 5 governs evidence tiers and deferred v1 full-release acceptance.

## Context

Above per-component verification sits the release lifecycle: promoting a complete, compatible, digest-verified
set under two independent gates, selecting and rolling back by immutable digest, and handing a consumable result
to a downstream consumer. Throughout, NVKR **produces** evidence and re-selectable verified sets and **does not
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
gap reason and ADR-13's authenticated waiver receipt binds the artifact, build, profile, status checkpoint, and
promotion-policy digest.

### 2. Authenticated typed inputs + two independent gates (SEC-08) — deferred

Reproduction and runtime conformance are **two independent, mandatory gates**; neither substitutes for the other.

The unused authorization evaluator, reproduction/compatibility/conformance receipts, evidence waivers,
clearance authentication and publication-manifest implementation have been removed. No current operation
issues or consumes release clearance. The following is the required future protocol, not a list of shipped APIs.

Qualified promotion must accept typed, authenticated receipts rather than caller-supplied booleans:

- one **`ReleaseSetReceipt`**, narrowed from the exact canonical ReleaseSet document authenticated under the
  dedicated release-set role; a parsed structure plus a generic signature callback cannot mint it;
- one **`PromotionPolicyReceipt`**, narrowed from an authenticated canonical document under the dedicated
  promotion-policy role and rechecked for exact bytes, digest, effective time, artifact-kind coverage, evidence,
  waiver, compatibility, and conformance policy bindings;
- a **`ReproductionReceipt` per release member**, binding the exact build, ValidationVector, builder-profile,
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
opaque qualified capability, but only while it remains in that process and only when the owning release gate revalidates its
private state; it cannot be serialized or reconstructed from public fields. A filename, transport URL, mutable
reference, untyped `verified` or `complete` flag, decoded report, or receipt or capability for a different
artifact, evidence document, or policy is rejected.

- NVKR **emits the reproduction verdict** (ADR-04) and **consumes a handed-off conformance result**. It **never
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

The removed implementation used `nvkr.dev/reproduction-result/v3`, `nvkr.dev/evidence-waiver/v3` and
`nvkr.dev/release-authorization/v9`. Their canonical families, along with compatibility, conformance and
publication-manifest families, remain reserved and nonminting. No current issuer or reader supplies these
qualified operations. A future implementation must explicitly define schema admission and retain exact
component, build, artifact, platform, policy and signer bindings without deployment metadata.
Inventory/ReleaseSet v4 and PromotionPolicy v1 remain implemented with unchanged canonical document schemas.

The `PublicationManifest` is exhaustive. For each OCI subject it lists the cleared artifact and every required
referrer, with both its NVKR semantic identity (domain-separated canonical digest) and its raw OCI blob digest and
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
`publicationIdentity = Digest("nvkr.publication", exact canonical ReleaseSet bytes)`, not the raw `releaseSetDigest`.
A future qualified clearance protocol must bind both identities; they cannot be caller-selected.
The current ReleaseSet authentication operation checks signed document bytes and grants no clearance.

The maintained artifact-publication consumer requires a digest-pinned reference at admission. Qualified
ReleaseSet selection and tag resolution remain deferred; no standalone selector helper is shipped.

### 4. Incremental validation, rollback, and clearance — produce, don't operate

- **Incremental validation (FR-24).** A declaration-level diff may identify an unchanged member as a **reuse
  candidate**, but equality of public member fields is not evidence authority. Skipping producer work requires an
  authenticated prior clearance plus the exact prior typed receipts, all rebound to the current release member
  and still acceptable under the current promotion, evidence, compatibility, and conformance policies. Status and
  trusted-time checks are always fresh. That authenticated cache protocol is not implemented, and the unused
  declaration-diff helper is removed. Equal Build Digests across named
  outputs likewise grant no execution/evidence reuse: every frozen output use must independently complete.
- **Rollback (UC-15, deferred).** A future rollback adapter must make a **previously verified set re-selectable
  and re-verifiable by immutable digest** and re-run the clearance path using a fresh opaque
  `CheckpointReceipt`. The checkpoint must target the exact candidate and must already have passed current
  signer-policy, completeness, freshness, and monotonic high-water validation under ADR-06; raw declarations or a
  caller-selected signer policy cannot bypass that path. NVKR performs **no infrastructure rollback** and is
  idempotent: re-running to the same digest is a no-op. The standalone command currently rejects this operation;
  the unused same-process rollback helper is removed.
- **Downstream clearance (SEC-17, deferred).** A future qualified publisher must emit a consumable, digest-addressable clearance result — asserting
  that identity, signature, provenance, completeness, and compatibility all passed — which a downstream consumer
  checks **before** mutating infrastructure. NVKR does **not** gate or authorize the mutation itself.

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

For one aggregate image output, completeness includes every predeclared native child and its exact
producer result, terminal observation and SBOM. The required platform set and native Build vector are frozen
in the Definition/Plan/Run before execution. Each child evidence key includes output, platform, stage and
component; its manifest subject and scanned OCI-export archive remain distinct. The aggregate index does
not replace either native inventory. A missing child fails the whole named output, even when another
output has equal bytes or an equal Build Digest. This is the pipeline artifact-publication membership
contract, not qualified reproduction or external runtime acceptance.

Evidence strength is subject-scoped. A Release may expose independently reproduced or digest-verified component
evidence beside a pipeline-attested aggregate artifact, but the stronger component evidence cannot upgrade that
aggregate or the Release lane. A content-validated rebuild proves neither exact-byte reproduction nor upstream
construction. Qualified promotion under §1–§4 remains all-or-nothing for its exact required release members;
there is no mixed-evidence shortcut around a missing reproduction receipt.

The current M3 MVP is a build/evidence qualification gate over the exact complete M2 publication. It authenticates every named
output against its producing Run, attempt, commit, Definition and relevant input identities. Strict input and
evidence schemas, exact output membership, applicable pins/digests and source consistency, and cryptographically
verified build signatures and provenance are mandatory. Missing, duplicate, extra, substituted or mismatched
members/evidence fail with stable coded errors. Acquired payload identity and OCI delivery-container identity
are checked through their respective bindings; equality between those distinct digest domains is not required.
The current scope directed by the user makes embedded-component declarations optional. Omission is
`not-applicable`; an explicitly authored declaration
must agree with its referenced upstream manifest at the pinned commit, and missing or invalid referenced data
fails. This raw-source metadata check establishes neither effective customized composition nor installed contents.
Source, patch, configuration and material identity/provenance checks remain mandatory. Qualification requires
no new upstream component manifest. The [October 12 decision](https://nvidia.atlassian.net/browse/NKX-15959?focusedCommentId=25021826)
establishes producer-only disk evidence and defers disk inspection and material reconciliation.

The manual diagnostic entrypoint takes an exact GitHub Run attempt URL and independently authored
qualification policy. The policy approves the producer repository, workflow and default branch and pins the
consumer verifier; neither the source Environment nor downloaded records can supply those decisions. The
provider discovers the commit from the selected attempt and authenticates its successful default-branch
execution. A Run URL without an attempt is ambiguous and is rejected.

The real-signing `per-output-v1` producer saves one bounded generated handoff under an exact Run/attempt artifact name.
It carries original Plan bytes, the compiler-read authored file closure and original root/output predicates.
Predicates are keyed by the aggregate root and each delivered payload reference. The collector validates
the bounded aggregate metadata against the Plan and joins its exact output origins to the saved output-route
publication and signing records before copying the unchanged predicate bytes; it does not read payloads.
Factory owns capture and replay of the Definition, Environment, base and per-output catalogs, overlays,
registries, locks, patches and directory facts. Replay preserves logical paths, rejects incomplete or unsafe
closures and reads authored files only from memory. Catalog-owned pinned upstream Git authentication remains
required; the handoff does not create an offline mode. The consumer independently authenticates the source,
transport integrity, recomputed Plan and complete published bytes before accepting evidence. Missing handoffs
or original predicates are errors; consumers never rebuild or relabel evidence to fill the gap.
Legacy aggregate publication remains supported as a producer route, but lacks invocation-bound release
metadata and the per-output route records required by qualification. It emits no qualification handoff and qualification
admission returns a capability error before publication work; it cannot claim complete M3 transport.

The owning lifecycle module issues an opaque, subject-bound check receipt after authenticating its exact
inputs and build evidence. It retains the held verified publication and frozen Plan. Closing it revokes all
copies, cancels and joins registered operations, then releases the held publication and any owned source
receipt. A decoded report, caller-supplied digest or passed boolean cannot restore the
receipt, and the receipt itself grants no qualification signing authority. M3-B supplies applicable
delivered CLI/operator version smoke results from those exact payload bytes with networking disabled.
Smoke startup failure, unsuccessful execution or a conflicting version fails the output; non-tool outputs
use an explicit not-applicable rule. The publisher must reject missing required smoke results. No placeholder
or absent implementation may mint passing smoke evidence.

The native MVP uses one fixed same-repository reusable GitHub Actions smoke workflow invoked at the
qualifier's exact commit. The existing `run generate --qualification-policy` operation emits this worker
and the qualifier alongside the producer workflow; the worker is generated infrastructure, not a separately
authored operator workflow. The independent policy selects native Linux AMD64/ARM64 image pins and fixed
nvkectl, Helm or kubectl version profiles by exact component name. Every delivered executable member must be exercised;
expected versions and nvkectl source stamps come from A's frozen Plan. OCI/APT non-tool scope requires an
exact named output, kind, component set and deployment-image/package-repository role. It does not assert
that those artifacts contain no tools. Disk and Helm use the explicit non-tool format rule. One pure
lifecycle admission operation joins the frozen Plan and smoke policy before generation and runtime bundle
reads. It rejects missing or extra tool profiles, non-tool membership and native execution images, and
validates expected versions/source stamps. Generated workers cover exactly the selected executable
platforms; the final AMD64 signer additionally requires its independently pinned cosign executable.

Pipeline owns native ELF and image admission, bounded execution in a nonroot network-disabled container,
clean payload environment, no host mounts or credentials, bounded output and stop/join cleanup. Lifecycle
streams exact held members and evaluates fixed version responses; it issues a native receipt only after
cleanup. The worker signs only that live receipt's canonical report under its separate worker identity.
No worker has final qualification publication credentials. Failed native reports retain actual observations
and enumerate known member results as completed pass, attempted failure or not evaluated. A late cleanup,
cancellation or revocation error fails the report without rewriting completed checks. Failure-only result
rows cannot enter successful canonical worker evidence. When final aggregation cannot authenticate the
smoke set, it emits a failed `delivered-tool-smoke-authentication` verdict for every authenticated A output,
preserves A's results and returns the original coded error; no qualification evidence is issued. The generated
final owner therefore runs after completed worker failure as well as success, subject to the same trusted
source condition and cancellation refusal. Failed or missing worker evidence cannot issue a B receipt.

Hosted aggregation independently rechecks A and the current invocation, admits every required successful
worker job from that exact attempt and authenticates each worker document. The same verified certificate
must match the independently selected worker SAN and GitHub issuer and bind the top-level workflow URI,
workflow commit and exact Run attempt. Provider artifact metadata and job status alone are insufficient.
The owner recomputes complete disjoint native member coverage, exact payload sizes/digests, expected and
observed versions, image selections and the full policy identity before issuing a B receipt. B borrows
A and the invocation; closure revokes copies. Caller-authored reports and booleans cannot restore it.

Hosted qualification takes one independently selected policy; its source is derived only from the trusted
`workflow_run` event and provider API. Native cosign byte pins are independent of producer tool selection.
The pinned release version is a bootstrap download locator, never authority in place of those byte pins.

The MVP requires each root/output production predicate's invocation to equal the authenticated source Run
attempt exactly. Where an output carries a publication-origin record, both its preparation and signing
invocations must equal that source attempt. ADR-20's existing publication retry/reuse behavior is unchanged,
but an earlier-attempt publication cannot satisfy this qualification gate. Reusing bytes never permits
relabelling their producing attempt; authenticated qualification reuse remains deferred.

Every output has exactly one overall pass/fail verdict and every applicable check has a result. The overall
verdict includes final source-receipt admission: if revocation is detected after artifact checks complete,
failure diagnostics add a separate `source-lifetime` result to every output and mark its overall verdict
failed. The completed artifact checks retain their actual results. This failure-only diagnostic uses the
existing check list; successful reports retain their six checks and unchanged canonical shape. No check
receipt is issued, and the original source error is returned. All required gate and smoke results are
collected before qualification signing begins. Any failed verdict prevents all qualification attestations
for that invocation. Only a complete passing set can receive evidence for every
selected subject under a qualification workflow identity distinct from the builder and an explicit issuer
policy. The source build's successful default-branch invocation is authenticated separately from the
qualification invocation (ADR-18); a green build or the publisher's own identity alone is insufficient.
Smoke execution receives no qualification signing authority.

The current scope directed by the user permits merging the reviewed implementation and concrete candidate
policy/workflows before real acceptance runs. After merge, a fresh complete producer attempt with its original
handoff and hosted positive/negative qualification and independent-consumer checks must establish acceptance.
Until those results are recorded, production readiness remains unproved; merge is not operational acceptance.
All runtime gates remain mandatory for those runs. External acceptance must retain immutable Run/artifact
references for a real `workflow_run` exercise: the certificate SAN and OIDC issuer from the verified bundle,
the exact source and qualification invocations, subject-bound attachment/read-back results, and rejection
under a wrong expected signer identity. The observed certificate identity must match the independently
authenticated qualification workflow identity. [Fulcio's GitHub identity mapping](https://github.com/sigstore/fulcio/blob/main/docs/oidc.md#github)
uses `job_workflow_ref` for the SAN; a reusable signing workflow can therefore differ from its caller.
The admitted same-commit smoke worker must establish its distinct SAN, caller workflow/commit and exact
Run claims, and the exact generated native job names, in hosted acceptance. Any future reusable final signer
must establish its own exact identity binding before admission. A mismatch remains
a rejection; broadening the identity expression does not satisfy acceptance. Local fixtures do not establish
the hosted certificate identity or complete this external acceptance requirement.

Qualification evidence binds its schema and gate version, exact subject membership, producing Run/attempt/commit,
Definition and relevant input identities, source build identity, qualification invocation and per-check results.
Version 2 additionally requires the full independent policy and authenticated per-member native worker
observations; the v1 placeholder shape cannot satisfy this gate. Its canonical schema and family are registered
in ADR-01 before use. Signing and attachment use the admitted
signing machinery. All-verdicts-before-signing is a preflight guarantee, not a transaction across registry
attachments: an operational failure marks the invocation unsuccessful and blocks release even when earlier
attachments remain. The release consumer must authenticate complete successful qualification publication;
presence of one attachment cannot prove success for the set. M4's release manifest remains a separate deferred
consumer and grants no qualified-lane clearance. Its acceptance tests must reject an earlier output's surviving
attachment when a later attachment fails or the qualification invocation is canceled, even if the surviving
predicate records a complete passing preflight set.

Full disk inspection/material reconciliation, waivers, offline pull-request qualification, a dedicated
verify-only dispatch and incremental reuse are deferred hardening. External qualification suites such as
NVKE's have a reserved integration boundary: results must be independently authenticated and bound to exact
artifact subjects, invocation and policy before inclusion. A suite outside this MVP is explicitly
`not evaluated—outside MVP scope`, never passed. Once policy requires a suite, absent or failing authenticated
results block qualification. NVKR does not build cluster test infrastructure for this MVP. This gate grants
neither runtime conformance, deployment qualification nor exact-byte reproduction.

Merge of the reviewed bump/regeneration change to the default branch is the v1 release decision; it is not
gated on reproduction. Unattended merge requires separate security authorization. ADR-06 owns immutable tags,
rerun behavior and revocation; ADR-04 owns explicit consumer scopes. An artifact-publication verification result
does not establish full-release acceptance. Reconciliation owns implementation and external-acceptance status;
the approved scope and these contracts do not establish milestone completion or organizational ratification.

### 6. Passive external qualification handoff — deferred producer protocol

The external suite owns execution, provisioning, process context and lifecycle. NVKR never initiates or
supervises it. A future qualification adapter must authenticate an already-produced independently signed observation containing pinned suite,
suite policy, harness and environment identities, exact raw result bytes, subject and signature. It recomputes
`resultsDigest`, checks role and subject, and applies coverage policy before issuing a receipt. Unsigned input,
unpinned identities, caller-provided pass booleans, wrong roles, expired evidence or mismatched subjects fail
closed with coded errors and no partial output. Release-set conformance remains exhaustive. Cluster topology and post-clearance deployment qualification
are deferred outside the current artifact model (ADR-19); they cannot authorize promotion.

## Consequences

**Positive**
- A release is complete, compatible, and digest-verified before promotion; partial sets cannot be promoted.
- The two-gate model prevents shipping a build that reproduces but fails conformance, or the reverse, with the
  conformance result independently signed.
- Digest-first selection defeats a compromised-registry tag swap.
- The model permits a future authenticated evidence cache to keep producer cost proportional to change, and
  rollback restores a known-good candidate only after current authorization and status checks pass.

**Negative / costs**
- The conformance-result contract and its signer identity are an external dependency — the suite producer, NVKE.
- The clearance result is a new consumable object the downstream must agree to check.

**Neutral**
- The boundary is load-bearing: NVKR consumes a conformance result and emits a clearance or verification result.
  It runs no suite, performs no infra rollback, and authorizes no mutation.

## Alternatives considered

- **Infer conformance from reproduction (single gate).** Rejected — SEC-08 requires two independent gates.
- **Run the conformance suite inside NVKR.** Rejected — it breaks verifier isolation and the produce-vs-operate
  boundary.
- **Select releases by tag.** Rejected — a mutable tag must never bypass digest and signature verification.
- **Rollback that mutates running infrastructure.** Rejected — NVKR re-selects a verified set; the engine makes
  any infra change.

## Follow-ups

- **ADR-05:** the ReleaseSet and Inventory the gates read, and the ValidationVector evidence the conformance
  result populates.
- **ADR-06:** status referrers consulted during selection.
- **ADR-19:** deployment topology and cluster qualification are outside the current artifact model.
- **ADR-13:** owns typed evidence and waiver receipts; promotion consumes but does not reinterpret them.

## Acceptance evidence

Current module evidence in `pkg/nvkr/release/*_test.go`, `pkg/nvkr/status/checkpoint_test.go`, and
`pkg/nvkr/trust/document_test.go` covers exact authenticated ReleaseSet and promotion-policy documents, strict
schemas and artifact-kind rules, canonical ordering, required-member/artifact completeness, role rejection,
cancellation and diagnostic checkpoint rollback rejection. Command tests separately cover durable status
high-water CAS. These tests do not establish release authorization, evidence-waiver evaluation, trusted-time
freshness, qualified receipt issuance or a downstream clearance round trip.

Acceptance additionally requires a full fixture that starts with exact artifact bytes and canonical evidence,
authenticates all typed receipts under distinct trust roles, promotes one complete compatible set, and verifies
the resulting clearance. Negative variants must cover a caller-forged boolean, missing `evidence:none` waiver,
receipt replay to a different artifact/vector/policy, unauthorized signer, expired conformance receipt,
compatibility mismatch, incomplete or unclassified artifact set, unknown promotion-policy schema, disallowed
waiver reason, an attempted `validated-variant` reproduction receipt without semantic-comparison authority,
stale or equivocating status checkpoint, and downstream rejection of a substituted clearance.
