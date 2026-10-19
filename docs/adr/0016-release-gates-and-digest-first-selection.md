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

- For qualification and promotion, NVKR **emits the reproduction verdict** (ADR-04) and **consumes a
  handed-off independently signed external conformance result**. It never runs that suite to create its
  own conformance evidence. Explicit local diagnostics in §6 have no qualification or promotion authority.
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
output against its producing Run, original attempt identities, commit, Definition and relevant input identities. Strict input and
evidence schemas, exact output membership, applicable pins/digests and source consistency, and cryptographically
verified build signatures and provenance are mandatory. Missing, duplicate, extra, substituted or mismatched
members/evidence fail with stable coded errors. Acquired payload identity and OCI delivery-container identity
are checked through their respective bindings; equality between those distinct digest domains is not required.
Embedded-component declarations are currently optional. Omission is
`not-applicable`; an explicitly authored declaration
must agree with its referenced upstream manifest at the pinned commit, and missing or invalid referenced data
fails. This raw-source metadata check establishes neither effective customized composition nor installed contents.
Source, patch, configuration and material identity/provenance checks remain mandatory. Qualification requires
no new upstream component manifest. The initial scope decision
establishes producer-only disk evidence and defers disk inspection and material reconciliation.

The manual artifact-check entrypoint takes an exact GitHub Run attempt URL and independently authored
qualification policy. The policy approves the producer repository, workflow and default branch and pins the
consumer verifier; neither the source Environment nor downloaded records can supply those decisions. The
provider discovers the commit from the selected attempt and authenticates its successful default-branch
execution. A Run URL without an attempt is ambiguous and is rejected.

`lifecycle.CheckQualificationReference` additionally accepts an explicitly selected immutable publication
root and independently selected qualification policy. It authenticates the root against that policy,
reads the signed source `InvocationID`, discovers the producing Run internally, authenticates its exact
handoff and recompiles the original inputs through the existing Run owner. The resulting receipt must
bind the originally selected root; source records cannot select trust policy or substitute a publication.
An error closes any returned receipt. This adds publication-based discovery, not a new trust lane.

The production real-signing `per-output-v1` producer saves one bounded generated handoff under an exact Run/attempt artifact name.
The development Environment cannot supply this qualification handoff; admission rejects it before
qualification evidence collection while its separate signed build/publication workflow remains available.
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
inputs and build evidence. Hosted checks independently verify every output's complete payload in sequence;
non-executable payload scratch is released before the next output. The delivery owner retains the checked
control documents, evidence and exact executable bundle bytes, and issues no complete-set capability until every
output and the aggregate content identity pass. This bounds transient payload storage by the current output
instead of the sum of all outputs; one output must still fit the available scratch capacity. Discarded bytes
cannot be exported through this owner. Ordinary full-publication verification retains its separate export
capability. The check receipt retains this verified owner and frozen Plan. Closing it revokes all
copies, cancels and joins registered operations, then releases the retained bytes and any owned source
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
nvkectl, Helm, kubectl, Kind, clusterctl or ORAS version profiles by exact component name. Every delivered executable member must be exercised;
expected versions and source stamps come from A's frozen Plan. Kind uses its quiet version response;
clusterctl and ORAS additionally require the exact source commit, clean tree and native platform in
their responses. A profile cannot substitute usage text or unsuccessful execution for a version check.
nvkectl requires exactly one legacy `NKECTL_VERSION`/`NKECTL_COMMIT` or current
`NVKECTL_VERSION`/`NVKECTL_COMMIT` stamp pair; mixed families fail closed. Both bind the
normalized source version and a checked prefix of the exact source commit.
OCI/APT non-tool scope requires an
exact named output, kind, component set and deployment-image/package-repository role. It does not assert
that those artifacts contain no tools. Disk and Helm use the explicit non-tool format rule. An explicit
member rule may select a source-built tarball as a `runtime-assets-archive` by exact output and member
name inside a mixed executable bundle. Its delivered mode must be non-executable, its bytes remain
covered by A's complete artifact checks, and at least one sibling tool must still be exercised. This
rule makes no claim about executables embedded inside the archive and does not waive archive or
provenance checks. The full policy binds the rule; missing, extra, duplicate or tool-overlapping
member rules fail admission. The rule cannot classify a host-package producer as an archive.
A pure lifecycle admission operation joins the frozen Plan and smoke policy before generation and runtime bundle
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

Hosted aggregation independently rechecks A and the current invocation and selects the latest worker job
for each required native platform within the same qualification Run, no later than the current attempt.
Every selected job must succeed; a later failed or unfinished worker cannot fall back to an earlier success.
An earlier successful worker is reusable only with the same repository, workflow, commit, branch, exact A
results and full independent policy. The same verified certificate must match the independently selected
worker SAN and GitHub issuer and bind that worker's original top-level workflow URI, workflow commit and
Run attempt. Artifact names and provider jobs select the original attempt; neither authenticates the worker
document without signature verification. Original worker invocations are retained, while aggregation and
qualification signing bind the current invocation.
The owner recomputes complete disjoint native member coverage, exact payload sizes/digests, expected and
observed versions, image selections and the full policy identity before issuing a B receipt. B borrows
A and the invocation; closure revokes copies. Caller-authored reports and booleans cannot restore it.

Production hosted qualification takes one independently selected policy; its source is derived only from the trusted
`workflow_run` event and provider API. Native cosign byte pins are independent of producer tool selection.
The pinned release version is a bootstrap download locator, never authority in place of those byte pins.

For production, the authenticated successful source attempt is the completion boundary for the producer workflow run.
Root and output production predicates may name current or earlier attempts of that same run under ADR-20's
publication retry contract. The repository, server, workflow builder and exact Plan/input bindings must
match. Output preparation must precede or equal its signing, which must precede or equal aggregate assembly;
aggregate assembly must precede or equal aggregate signing, which must precede or equal source completion.
Every output predicate must match its authenticated original signing invocation. Different runs, future
attempts, missing or inconsistent origins, changed inputs or missing evidence fail closed. An earlier attempt need not
have succeeded as a whole when its authenticated successful output is reused by a later complete attempt.
The consumer verifies the full artifact and evidence closure and retains original predicates and origins;
retrying never relabels earlier bytes as current-attempt production. This admits producer recovery, not
reuse of an earlier qualification verdict in place of current complete-set verification.

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

The current scope permits merging the reviewed implementation and concrete candidate
policy/workflows before real acceptance runs. After merge, a complete producer run with its original evidence
and a handoff for its successful completion attempt, hosted positive/negative qualification and
independent-consumer checks must establish acceptance. Authenticated partial retries are permitted.
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
presence of one attachment cannot prove success for the set.

The M4 release manifest is that consumer and grants no qualified-lane clearance. Issuance runs in the
qualification process while it holds the live artifact-check receipt; decoded evidence, a publisher's
`Successful` field or a caller-supplied digest cannot substitute for it. The receipt also exposes, for each
output, the source lane and exact predicate digest of its already verified production provenance. The issuer
takes the exact canonical qualification evidence bytes and an independently selected policy naming the exact
qualification certificate identity and qualification-policy digest; the evidence cannot name its own trusted
signer. Before constructing a signer it requires the strict v2 evidence to validate with every verdict
passing, to describe exactly the receipt's artifact checks, and to carry the selected identity and policy.
It then authenticates those exact evidence bytes on every member subject under that identity and the GitHub
issuer. A missing, failed, incomplete, wrong-signer or mismatched member refuses issuance, so an earlier
output's surviving attachment cannot stand in for a later failed or canceled one. Only then does it attest the
canonical manifest (ADR-01) to the publication root under the qualification identity and verify the exact
attached bytes again. The receipt is rechecked and cancellation is observed between every step. Issuance uses
M2's existing bytes and never rebuilds, repacks or relabels a member.

An opaque manifest receipt is returned only after that verification; decoding a manifest cannot restore it.
Attestation and verification are not atomic: an operational failure may leave a manifest attachment and is
reported as a failed issuance. The release tag rule, tag creation, publisher exclusion, consumer manifest
verification and interruption handling belong to the separate M4 publication step; a manifest attachment alone
does not establish a release.

Full disk inspection/material reconciliation, waivers, offline pull-request qualification, a dedicated
verify-only dispatch and cross-run qualification reuse are deferred hardening. External qualification suites such as
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

### Development qualification

An explicit development policy admits a manual qualifier on a reviewed nondefault branch. It authenticates
one successful manual development producer attempt through GitHub, replays that attempt's exact committed
inputs and recovers its original public metadata controls. A production handoff is not required in this
separate path. Recovery supplies descriptions only: the same artifact verification, native tool smoke,
complete-set receipts, signing and attachment read-back remain mandatory. Source and qualifier branches
may differ; both exact commits and invocations are retained without relabeling the original build.
The development publication remains bound to one exact source attempt. Within a qualification run,
successful native workers may be retained from earlier attempts under the same worker-retry checks above;
original branch, source, policy, artifact and certificate bindings remain mandatory.

Development evidence uses `nvkr.dev/development-qualification-evidence/v1`, gate
`nvkr.dev/development-qualification-gate/v1` and the separate predicate registered in ADR-01. Every subject
must remain inside the original source Run/attempt namespace. The production decoder and publisher reject
development evidence and receipts. Development success grants no production acceptance, reproduction,
full disk inspection or external-suite acceptance. Hosted execution remains separately evidenced.

### Local qualification rehearsal — implementation in progress

The user-requested local NVKE demo must exercise the complete M3 build/evidence flow on its
actual locally built outputs: artifact/input checks, separate build signatures and provenance,
isolated delivered-tool smoke, independently signed qualification evidence, publication/read-back,
independent consumer verification and controlled negative cases. A smoke-only or unsigned diagnostic
does not satisfy this request. This local rehearsal remains `unqualified-local-execution`; it cannot
claim GitHub default-branch execution, OIDC identity, organizational approval or qualified reproduction.
Hosted M3 acceptance still requires the provider exercises above.

The local source owner must bind the completed immutable attempt, original Plan/input closure and
every selected named output to independently re-opened and measured payloads. Caller-provided paths,
digests and a serialized successful Run are descriptions, never receipts. A missing, substituted or
partially committed attempt fails before signing. Rehearsal admission must cover all selected formats
and smoke profiles before starting the build or publication; it cannot silently omit unsupported tools.

Retained disk capture rechecks the exact compiled producer and saved Run subject against observed bytes, admits disk-backed
scratch headroom, and copies into the existing standard OCI disk transport. A held descriptor owns
the private snapshot after temporary paths are removed; each stream rechecks size and digest, and
closure revokes shared handles. The snapshot supplies transport integrity only. It does not restore
successful execution, material provenance or signing authority from the retained Run description.
The live disk producer retains its result in memory, so later capture must not require a producer-result
file that execution never persists. Capture measures the committed delivery file against the original
Run's disk subject and compiled producer; complete material-export validation remains mandatory before
signing. Neither saved description is an execution receipt.
For local transport, that held disk can export a standard OCI layout without reopening producer
paths. Export rechecks the standard wrapper identity and exact held payload stream, holds the owner
through the no-replace layout transaction, and preserves manifest and raw payload digests separately.
The caller admits disk capacity. This does not enable the scheduler's unsupported disk publication
graph or grant hosted publication authority.
The standalone disk registry adapter publishes the standard wrapper by manifest digest without tags.
It exports held bytes into caller-admitted scratch before upload. Pull requires a canonical digest
reference, expected platform and payload bound, checks metadata and the standard wrapper before payload
transfer, bounds the stream and rechecks the full artifact before committing its layout. Both operations
inherit caller cancellation with a 30-minute maximum. An upload error may leave registry blobs; the
adapter reports the attempted immutable reference. This transport remains separate from release signing.

Input-capturing local execution retains disk material descriptions before releasing the original
factory Run. The `nvkr.dev/local-disk-materials/v1` export comes only from that live Run's observed
producer-consumption graph and binds the named output, Plan, Build and disk bytes. Its sorted members
and edges describe staged producer outputs and the exact consumed manifest identities. Network-enabled
builds remain explicit, and coverage excludes private source vendoring and guest/network acquisitions.
Lifecycle stores the bounded exports in an immutable `nvkr.dev/local-run-materials/v1` envelope bound
to the attempt and Plan. Missing observations, cancellation or retention failure prevents a complete Run.
These records preserve descriptions for later verification and signing; decoding them cannot recreate
the live consumption receipt, and they do not authenticate production or authorize qualification.

Use explicitly independent local build, smoke and qualification signing keys with an independently
selected verifier policy. Local public-key authentication must use an explicit local evidence schema
and origin, never fabricated GitHub responses, workflow environment or certificate claims. Reuse
the existing canonical identity, packaging, version evaluation and isolated runtime owners. Keep
provider admission separate from those common checks. Local receipts must be distinct from hosted
receipts and cannot be passed to the hosted qualification publisher. The local smoke process receives
neither build nor qualification keys or registry credentials.

The local policy uses `nvkr.dev/local-qualification-policy/v1`, the explicit local unqualified lane,
exact Definition/Environment/Plan identities, one native Linux platform, a pinned cosign executable,
three distinct public keys and complete shared smoke scope. It rejects hosted worker identities.
Its digest uses the existing qualification-policy family with this distinct schema, normalized public
keys and sorted scope sets. Role assignment changes identity. Loading the bounded JSON document
rejects duplicate or unknown fields and trailing documents; no candidate record supplies trust defaults.
Policy validation and matching a Plan are descriptive admission, not artifact or signing receipts.

The local build-evidence document uses `nvkr.dev/local-build-evidence/v1` with origin
`retained-local-run` and the unqualified lane. It binds the independent policy identity, original
attempt/input identities, content subject, every named output and its transport identity. Exact-byte
digest/size references identify the accompanying compiler-read closure, original Plan and Run, and
disk-material envelope where applicable. The Plan retains source, toolchain, acquisition, patch and
configuration bindings; consumers must replay its referenced compiler inputs rather than trust those
descriptions alone. Raw blob hashes supply transport integrity only. Preparation requires still-open
captured payload owners and emits bounded canonical bytes, not a signing or execution receipt. Builder
signing, complete attachment transport and independent verification remain separate required operations.

Local execution hands the captured compiler closure to factory as descriptive bytes. Factory replays
it against the executing Plan, establishes workspace ownership, then commits the snapshot without
replacement before runner preparation or producers. Lifecycle does not populate an unowned factory
directory. A failed or interrupted retention cannot authorize execution or reuse of a lifecycle attempt.

Local native smoke uses a separate `nvkr.dev/local-tool-smoke/v1` report bound to exact signed
build-evidence bytes, independent policy and the observed native runtime. It covers every selected
tool through the shared fixed-profile executor and evaluator; nonnative members cannot be omitted.
A process-local receipt is issued only after all checks and runtime cleanup succeed. It borrows the
verified build lifetime: closing that build cancels and joins active smoke before releasing payloads,
and revokes later use of the receipt. Serialized reports do not restore execution authority. Smoke-key
signing and independent complete-report authentication remain required before qualification signing.
The smoke signer admits only the live execution receipt and the policy's smoke key; the verifier
accepts only bounded canonical reports with exact source, policy, native platform, runtime and member
bindings. Authenticated reports receive a distinct borrowed owner, never a reconstructed execution
receipt. Both signing and verification participate in build-owner cancellation and joining.

NICoSim version smoke admits two explicit profiles. `nicosim-version-json` requires an unpatched
source build and a `clean` tree stamp; `nicosim-patched-version-json` requires declared source patches
and a `patched` tree stamp. Both execute only `version --output=json` and compare version, full source
commit and native platform with the frozen Plan. The tree stamp is a consistency check, not patch
authentication: the source owner authenticates the ordered patch bytes and resulting source inputs.
Unknown or dirty tree stamps cannot satisfy either profile.

Executable bundles may contain `raw` or `host-package` tool producers. Both require executable delivered
mode and native ELF validation; a raw declaration alone proves neither. The NVKE CLI profile accepts a
commit version only when the normalized producer version is its exact full source commit and its version
stamp matches that commit without a prefix. Release versions retain their exact `v`-prefixed expectation.

The local qualification gate uses `nvkr.dev/local-qualification-evidence/v1`, gate version
`local-m3-v1`, and the unqualified lane. It binds the content subject, attempt, independent policy
and exact-byte references to the build document/signature and smoke document/signature. Only live
independently verified build and smoke owners for the same captured source may reach the qualifier
key. Verification recomputes those references and accepts only the qualifier key; a builder or smoke
signature is not qualification authority. Closing either borrowed source revokes later use. The
signed gate document is not end-to-end success until required publication and independent read-back
complete, and never upgrades the evidence lane to qualified reproduction.

The verified local gate retains the exact qualifier signature for transport. Its evidence export
requires live borrowed owners and emits defensive copies of all three documents and signatures,
the original Plan, Run and compiler-read closure, and disk materials when present. These exported
bytes carry no process-local authority. Publication must also carry every payload, and a fresh
consumer must independently authenticate the complete handoff against its own policy.
The retained-attempt consumer admits a bounded exact evidence file set, authenticates each signing
role, replays inputs and captures payloads through its own build owner, then compares every transported
metadata byte to the verified set. It accepts no producer receipts or signing keys. A failed phase
closes all acquired owners; successful consumers own their snapshots independently of the producer.
This retained-file operation makes no registry read-back claim.
The layout consumer instead accepts the evidence file set, one ordinary OCI closure and exact named
disk layouts. It authenticates the builder statement before layout I/O, replays the same bounded compiler
inputs without a producer workspace, checks every layout against the signed transport identities, and
reconstructs the exact build statement before smoke and qualification verification. Missing or extra
locators fail admission; any later failure closes acquired owners. Imported disk owners retain their
checked inode and rehash subsequent streams. Layout verification alone does not prove registry transport.
The evidence-only carrier uses canonical `nvkr.dev/local-evidence-bundle/v1` JSON with exact file
bytes, bounded to 128 MiB. The writer stages beneath a held destination parent, syncs the protected
file, publishes without replacement and syncs the parent. A post-publication durability error reports
the surviving destination rather than claiming success. Readers reject linked files and noncanonical
or incomplete envelopes; decoding supplies bytes only, which still require independent authentication.
Payload transport remains separate from this evidence carrier.
Registry evidence transport carries the envelope as one bounded OCI document layer with config and
layer media type `application/vnd.nvkr.local-evidence-bundle.v1+json`. It publishes only a digest
reference, never a discovery tag. Download checks the requested digest, family, single-layer membership,
size and exact reconstructed wrapper before returning bytes; operations inherit cancellation with a
five-minute maximum. The local gate owner remains live through upload. An attempted immutable reference
is retained on upload failure because remote bytes may survive. Downloaded envelopes remain descriptions
until the independent consumer verifies their signatures, inputs and complete payload set.
The local publication owner admits canonical repositories, registry options, the actual pinned consumer
verifier and disk-backed scratch headroom before upload. Capacity observation includes three copies of
held payload bytes plus metadata headroom; it does not reserve storage. Under the source owner's joined
lifetime and a two-hour limit, it publishes ordinary outputs, sorted named disks and evidence, downloads
the complete set, and opens the independent layout consumer. Success requires consumer verification and
scratch cleanup. Failure reports attempted immutable references without acceptance; remote artifacts are
not rolled back. The returned consumer owns its imported payloads independently of temporary paths.

All required checks and smoke observations must pass before qualification signing. Attachment failure
leaves the rehearsal unsuccessful and reports any surviving attachments. The independent consumer
must verify exact complete-set membership and all required signatures, provenance and qualification
evidence against the selected keys; signatures alone do not authenticate the inputs or payloads.
Negative demonstrations use separate attempt-owned scratch publications, preserve the successful
candidate, assert specific coded refusals and prove no qualification evidence was issued after a
failed check. Disk evidence remains producer-only, and external runtime suites remain outside M3 MVP.

`run qualify-local` delegates to `lifecycle.QualifyLocalRun` for a completed captured attempt. The owning
operation admits the policy, native platform and distinct private keys, signs checked build evidence,
executes smoke, verifies both signed reports, retains a new evidence envelope, and invokes publication
with independent read-back. Private keys are explicitly read with bounds and never passed to the smoke
runtime. Canonical `nvkr.dev/local-qualification-result/v1` output reports the local lane, status and
attempted references, including failures. All owners must close successfully before `passed` is reported.
It runs without tenant provisioning. When the demo explicitly configures complete local qualification,
its build adapter invokes this command on the newly built attempt before returning deployment inputs.
Before producers execute, `lifecycle.PreflightLocalQualification` validates the complete prepared Plan,
independent policy, registry destinations, evidence destination and preliminary disk-backed scratch,
then authenticates each private key against its policy role with fixed non-artifact challenges through
the pinned cosign owner. It publishes nothing and issues no reusable receipt. The final operation repeats
admission and checks capacity against the actual payloads. Runtime/registry availability and storage
reservation are not established by this preflight.
The demo's explicit prepared-build mode allows policy authoring after bootstrap/toolchain selection.
It requires a new producer Run destination and an explicit policy, validates the fixed prepared
invocation, and recompiles current inputs through the same executable before policy admission.
Stored previews and bootstrap paths confer no authority; final qualification still checks the
execution's captured inputs. This is a prepare/execute split, not general Run resumption.
The downstream demo may automate preparation and local setup through an explicit `qualifyLocal`
opt-in on a fresh build or e2e invocation. It rejects combination with prepared or caller-authored trust
settings. It owns the registry through independent read-back and attempts bounded shutdown on success,
failure and cancellation before returning deployment inputs; shutdown failure blocks handoff. It retains
private files and registry data, while registry references are offline after shutdown. Explicit setup for a
prepared attempt remains available for operators who need a longer-lived registry. Setup selects a maintained,
independently authored complete demo scope, generates three distinct local keys, and binds a new public
policy to the prepared input identities. It must not infer smoke exemptions or trusted signer identities
from candidate artifacts. These keys represent one local operator's disposable trust, not independent
organizational actors. Recompilation, complete-scope admission and private-key preflight remain mandatory;
setup descriptions confer no signing or execution receipt. A fresh private directory owns the pinned
cosign download, keys, policy, scratch, diagnostics and disk-backed registry data. The registry binds only
loopback, uses a pinned image and a dynamic port, and remains available for consumer read-back. Explicit
stop checks exact container ownership, image, mount and network configuration and retains data and evidence.
Existing trust and setup directories cannot be overwritten. Failure after registry startup attempts bounded
owned cleanup; interruption or cleanup failure never establishes readiness. Full qualification repeats the
existing admission and verification, and no generated setup file upgrades the unqualified local lane.
The owned child must exit successfully and report the exact attempt, local lane and passed status;
decoding a successful result cannot replace successful process completion. The demo retains a separate
diagnostic transcript, reserving its destination before child execution without overwriting previous
bytes. An interruption may leave an empty diagnostic file, which does not establish success and
prevents reuse of the attempt's transcript. This workflow gate grants no durable receipt
or deployment qualification; the downstream deployment consumer still checks its inputs independently.
Interrupted runs, reused evidence destinations,
cancellation and cleanup failures must fail closed. Reconciliation owns the implementation and live
verification status; command availability does not establish a successful full NVKE rehearsal.

### 6. Passive external qualification handoff — deferred producer protocol

External conformance evidence used for qualification remains a passive handoff: the independent suite
owns execution, provisioning, process context and lifecycle. NVKR never runs that suite to manufacture
its own conformance evidence. A future qualification adapter must authenticate an already-produced independently signed observation containing pinned suite,
suite policy, harness and environment identities, exact raw result bytes, subject and signature. It recomputes
`resultsDigest`, checks role and subject, and applies coverage policy before issuing a receipt. Unsigned input,
unpinned identities, caller-provided pass booleans, wrong roles, expired evidence or mismatched subjects fail
closed with coded errors and no partial output. Release-set conformance remains exhaustive. Cluster topology and post-clearance deployment qualification
are deferred outside the current artifact model (ADR-19); they cannot authorize promotion.

The repository's separately authored NVKE E2E adapter is downstream CI test infrastructure,
outside the NVKR product lifecycle and v1 qualification acceptance. ADR-18 §3 records its
runner and trust constraints. It uses artifact-consumer APIs and issues no conformance receipt;
it does not change this passive product boundary or establish named-decider ratification.

The separate user-invoked `validate` command runs explicit named diagnostic checks in lane **none
(descriptive)**. `validation` owns profile admission and dispatch; `conformance` implements the
`kube-conformance` / `hydrophone` adapter. `testresults` owns descriptions and serialization, not
execution or interpretation. The [operator reference](../external-test-output-contract.md) owns
configuration, prerequisites and field limits. This is an implemented diagnostic path under this
proposed contract, not organizational ratification or production cluster qualification.

The profile independently selects product and tools publication roots and qualification-policy documents.
Lifecycle authenticates each root and its exact producing Run, retains live receipts, and exports selected
executable members through the delivery owner. The adapter runs those held Linux executables with an
embedded kubeconfig and bounded child-process lifetimes. Tools and suite images are ordinary catalog
outputs; caller-authored binary paths, payload digests or raw image references cannot restore authority.
The operator must explicitly declare an existing disposable local cluster. That declaration proves
neither locality nor exclusive ownership. This host execution is not a sandbox (ADR-18 §3).

`targets` is a caller-owned output-to-workload mapping under ADR-19 §3. Before/after API assertions
compare selected named OCI artifacts, workload identities and observed image identities; they do not
own deployment roles, provision a cluster, prove continuously installed bytes or cover unmapped outputs.
The adapter creates test resources and joins cleanup; lifecycle owns receipt closure outside registered
callbacks. Cancellation, receipt revocation, unknown resource ownership or failed cleanup cannot become
a complete capture. Fleet lifecycle, site/tenant operation and automatic applicability remain unsupported.

Normal process exit status is retained as a harness fact with an unknown cause. Complete capture means
original report retention, terminal wrapper cleanup, matching subject observations and live bindings;
it means neither valid reports, suite completeness nor test/harness success. A normal nonzero exit may
therefore accompany complete capture and permit the next configured check. Signals, launch failures,
timeouts/cancellation, capture I/O errors, missing results, identity drift and failed wrapper cleanup
remain partial/error. No JUnit verdict parser or exit-code-to-test-verdict protocol is introduced.

Unsigned diagnostic descriptions cannot authorize qualification, signing or promotion, or replace ADR-05's
publisher evidence and authenticated later observations. This does not change §5's separately isolated
delivered-tool smoke workers. Passive external conformance admission remains deferred as described above.

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
  Qualification consumes independently produced external conformance evidence; §6's explicit diagnostic
  execution grants no qualification authority. NVKR performs no infrastructure rollback or fleet lifecycle management.
  The unqualified artifact diagnostics proposed in ADR-23 do not cross this qualification boundary.

## Alternatives considered

- **Infer conformance from reproduction (single gate).** Rejected — SEC-08 requires two independent gates.
- **Run the conformance suite as its own qualification evidence.** Rejected — it breaks verifier isolation and the produce-vs-operate
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
