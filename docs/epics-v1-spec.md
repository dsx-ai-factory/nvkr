# NVKR v1 acceptance specification

This specification owns M1–M4 artifact scope, numbered acceptance criteria, approved fallbacks and
completion requirements. The [roadmap](milestones.md) summarizes outcomes; the
[implementation record](adr/reconciliation.md) owns current support and operational evidence. Current
ADRs own the engineering contracts pending named-decider ratification. Candidate success is not
organizational approval or complete milestone acceptance.

**What NVKR v1 is.** A generator and evidence layer. From a digest-pinned Definition of NVKE artifacts and a
separately selected Environment it emits committed GitHub Actions workflows that build or acquire each artifact, publish it to an OCI
registry, sign it, attach production provenance, qualify it, and tag releases. NVIDIA runs the generated
workflows; consumers verify the results with standard tooling and no NVKR software.

**Facts the specification rests on.**
- The NVKE OS image build bakes in Kubernetes (node side and control-plane images), CNI, and node-agent. NVKR wraps
  that build using selected material producers; embedded components need not be separately delivered.
  M2 and the M3 MVP require exact-subject producer provenance. Full-disk inventory and material reconciliation
  are deferred hardening. Embedded-component declarations are optional; if authored, they must match their
  referenced pinned upstream metadata. Omission is not applicable. The comparison does not establish customized
  or installed composition; the NVKE OS image build team owns embedded software correctness and runtime validation.
- V1 production acceptance retains its AMD64-only product floor. M2 requires producer provenance; M3 adds
  authenticated build/evidence checks while retaining producer-only disk scope. The
  approved initial scope
  defers strict inspection and material reconciliation; scope approval is not organizational ADR ratification.
  ARM64 is an implementation extension recorded in the
  [implementation record](adr/reconciliation.md#native-arc-disk-route). Any additional architecture explicitly selected
  by a Definition remains mandatory for that Run and Release; the product floor cannot waive its completeness.
  The selected execution route must admit the OS build's
  virtualization and privilege requirements; broader upstream architecture support does not expand this scope.
- Every artifact is delivered in OCI, signed keyless with cosign from the workflow run that produced it, and
  carries an SLSA v1 provenance attestation describing how the bytes were produced. No reproduction or
  byte-identity claim is made anywhere in v1, and the earlier builder-profile preflight and bake-off requirements
  are relaxed for these milestones: a wrapped build's declared network and privilege are recorded, not forbidden.
- v1 evidence remains scoped to its exact producer output. Digest-verified dependencies retain their own
  evidence, but do not make an aggregate OS image reproducible. v1 does not consume qualified reproduction
  evidence. A rebuilt disk with matching traced materials and inspected contents is content-validated; its exact
  QCOW2 digest is still a new Artifact identity.
- Verification by a consumer means `cosign verify` and `cosign verify-attestation` (provenance and, from M3,
  qualification evidence) succeed on a machine with only cosign installed, using the published signer identities
  and issuer. A Verification Policy names its acceptance scope explicitly: *artifact publication* (exact
  published bytes, build identity, signature and production provenance; the M1 scope) or *release* (artifact
  publication plus qualification identity and verdicts, manifest membership and revocation state; the M3/M4
  scope). An unavailable scope fails; it never falls back to the weaker one.
- Every built artifact has a per-artifact software bill of materials (CycloneDX), bound to the observed digest
  and published as a signed attestation. M2 disk documents describe exact producer/material subjects and state
  that disk inspection was not performed; M3 preserves that explicit scope. The signed release
  index binds required evidence membership; Run completion is a separate record. SBOMs verify no upstream signature.
- Workflows use generic GitHub-hosted runners, with only the narrowly admitted operator-trusted native
  virtualization-pool exception for disk jobs. Pool isolation and capacity require separate evidence.
- Evidence for every acceptance criterion is a workflow run URL plus, where a consumer can check the criterion, a
  consumer-side verification transcript, and, for negative criteria, the run log showing the refusal and its error
  code. All of it is recorded in the evidence log and referenced from the implementation record.

**Public objects.**

| Object | Responsibility |
|---|---|
| Definition | Authored named outputs with direct component selections, target platforms and packaging |
| Environment | Execution settings, publication namespace, credential references and signing policy |
| Run | One immutable attempt with frozen requirements, observed artifacts, evidence and failures |
| Artifact | Immutable executable, image, chart or packaged bytes |
| Release | Immutable selected artifacts and evidence addressed by one pinned reference |
| Verification Policy | Consumer requirements for signer identities, evidence and lifecycle state |

Deployment topology and node placement belong to downstream consumers. The Release binds the exact named
outputs and their evidence. Bundle denotes an Artifact format. Lockfiles hold resolved pins; manifests and per-step observations are generated records.
Environment changes do not change build identity. Workflow generation binds the Definition, Environment and
generator identities together, so execution configuration remains accountable without becoming product intent.
Each output's build closure is derived from its exact selected producers and pins. It is not another authored
object. Outputs with equal build identities remain independent required uses with separate completion checks.

*Source lane* records how bytes were obtained: hermetic build, wrapped external build, or digest-pinned
acquisition. *Evidence tier* records the accepted claim: production output uses `pipeline-attested` and targets
SLSA Build L2; a plan or dry run alone supplies no such evidence. Build/evidence qualification in M3 and the
signed Release manifest in M4 strengthen v1 acceptance without claiming qualified reproduction.

The *decision records* state the current engineering contracts directly, pending named-decider approval; each
v1 contract lives in the record that owns the topic (signing, publication, provenance, source lanes, evidence
tiers, generated pipelines, release tags). The *implementation record* tracks support, the *evidence log* records
actual runs, and *consumer instructions* describe independent verification.

## Artifact scope

V1 acceptance targets one amd64 variant per delivered artifact, including one OS image. Embedded
components retain rows for traceability without implying separate NVKR builds or delivery. Every
separately delivered artifact is signed and attested; the Release binds exact named outputs.

| Artifact or capability | Production source | Delivery / milestone |
|---|---|---|
| GitHub workflow generator | NVKR | Committed workflows / M1 |
| NVKE CLI | Pinned NVKE source, hermetic build | OCI executable package / M1 |
| nvke-agent | NVKE OS build | Inside OS image / M2 |
| CWE controller | Pinned NVKE build; recorded acquisition fallback | OCI image / M2 |
| NVKE container images | OS-embedded or separately built/acquired, enumerated in Definition | Inside OS image or OCI images / M2 |
| NVKE OS image | Pinned wrapped NVKE build; recorded prebuilt-image fallback | OCI disk-image artifact / M2 |
| Kubernetes node and control-plane components | NVKE OS build | Inside OS image / M2 |
| CNI | NVKE OS build | Inside OS image / M2 |
| Helm | Digest-pinned acquisition | Operator tooling package / M2 |
| Spire-agent | Placement decision: embedded or acquired | Inside OS image or OCI artifact / M2 |
| Third-party container images | Digest-pinned acquisition when outside OS image | OCI images / M2 |
| NVKE Helm charts | Pinned chart sources | OCI packages / M2 |
| Operator tooling | Acquired kubectl and Helm | OCI package / M2 |

A deferred member stays explicit in authored requirements and planning; the M3 MVP accepts no waivers and
requires every selected member to complete. A member must never disappear because a build failed.
Disk inventory proves composition for the exact disk subject,
not reproduction of the QCOW2, decoded RAW stream or encrypted delivery bytes.

---

## Epic M1 — Vertical slice

**Goal.** One generated workflow builds the NVKE CLI from a pinned NVKE commit, signs it, attaches provenance,
pushes everything to OCI, and verifies it. Acceptance requires every signing and publication mechanism to run against the real services. Later milestones add build types, the gate and release tagging.

**Delivers**
- **The generator.** Turns the Definition and Environment into committed workflows whose third-party actions
  are pinned to commit SHAs. Regeneration with the same pinned inputs and generator must match byte for byte.
- **The pipeline helper.** Each job downloads it, verifies its digest against the pinned value, and fails otherwise.
- **The pinned source model.** A source is a repository, a commit, a tree hash, a toolchain image digest, and a
  build command. Private NVKE sources are reachable from hosted runners directly or through a configured mirror, with vendored inputs and a
  read-only fetch credential that is present only on fetch steps.
- **The hermetic build step.** Runs the pinned toolchain container with the network disabled, re-hashes the
  produced bytes, and writes the run record before anything downstream may use them.
- **Software bill of materials.** After the build is observed, one CycloneDX document per built artifact,
  bound to its observed digest and published as a signed attestation. The signed release index binds exact
  subject, document and output membership. Source and final-package composition remain distinct claims.
- **Publication.** Pushes the artifact and records the digest-pinned reference.
- **Signing and attestation.** A keyless cosign signature and an SLSA v1 production-provenance attestation whose
  builder identity is the committed workflow (path and ref), whose invocation identifies the run with start and
  finish times, whose build type names the source lane, whose external parameters name the Definition,
  Environment and plan identities, whose resolved dependencies name the source commit, tree hash and toolchain
  digests, and whose internal parameters record the generator version, the signing mode and the pipeline helper
  digest. The signing step re-derives the digest from the bytes it holds and refuses to sign unless it equals
  the run record's digest for that subject. The cosign executable is pinned by digest in the catalog and selected by the Definition; a
  version string or an executable found on the path is not a pin.
- **Run completion.** A finalization step runs unconditionally and refuses to complete the Run when any producer
  failed, any required output is missing or differs from its observed digest after job transport, any SBOM or
  observation is malformed or substituted, or the provenance predicate does not match the admitted plan. Job
  success and a successful artifact download are not completion.
- **Verification step.** `cosign verify` and `cosign verify-attestation` against the published reference with the
  pinned identity and issuer; the workflow fails if either fails.
- **Trigger and credential model.** Pull requests from branches in this repository build and package only; the
  only credential they hold is the read-only mirror-fetch credential, present on fetch steps only; they request no
  OIDC token and have no registry-push credential or environment access. Pull requests from forks run the
  regeneration check and schema validation only and hold no credential. Pushes to the default branch and manual
  dispatch publish, sign, attest, and verify using the workflow's own token and OIDC identity inside a protected
  environment. All signing runs execute on the default-branch ref, so the keyless identity is one fixed workflow
  path and ref. Branch protection and code ownership cover the workflows, the generator, the pipeline helper, and
  the signing code.

**Does not deliver.** Invocation of an upstream repository's reusable workflow: v1 runs pinned source
entrypoints instead, as the decision records state; an upstream reusable workflow would need its own execution
and evidence contract. Git-tag-triggered runs. Reproduction or byte-identity claims. Signing or publication on
any CI engine other than GitHub Actions.

**Acceptance criteria**
1. A run of the generated workflow on a GitHub-hosted runner, triggered by a push to the default branch, is green end to end.
2. The run's provenance names the NVKE repository, commit, and tree hash, each equal to the value in the committed definition; the run log shows the tree-hash check passing before the build step; the built NVKE CLI is the subject of the published reference.
3. The published reference carries a keyless cosign signature and an SLSA v1 production-provenance attestation whose builder identity is the committed workflow path and ref, whose invocation identifies the run, whose build type is `hermetic-build`, whose external parameters name the Definition, Environment and plan identities, whose resolved dependencies name the NVKE commit, tree hash and toolchain image digest, and whose internal parameters record a real (not dry-run) signing mode and the pipeline helper digest. The Run and its Release record the evidence tier `pipeline-attested`.
4. On a clean machine with only cosign, `cosign verify` and `cosign verify-attestation --type slsaprovenance1` succeed against the published digest with the published identity and issuer. The same publication passes the artifact-publication acceptance scope; the release scope is reported as unavailable, not as passed.
5. The committed workflow is pinned: the regeneration check passes; every action reference is a full commit SHA; regenerating from the same Definition, Environment and generator version yields byte-identical workflows; and every job verifies the pipeline helper's digest before running it, shown once by a job that fails on a wrong digest.
6. A pull request run (including one from a fork) completes with read-only token permissions, runs no publish, sign, attest, or verify step, requests no OIDC token, and the registry shows no new digests or tag changes afterwards; the mirror-fetch credential is absent from every build, package, and check step's environment.

**Definition of done**
- Continuous integration is green on Linux: the full automated test suite including concurrency-safety checks, static analysis, and the regeneration check. Tests cover the signing step refusing a digest that differs from the run record, the build step's re-hash rejecting altered bytes, the fetch credential being absent from every build step, and finalization refusing a Run whose required output is missing or altered after job transport or whose provenance predicate does not match the plan.
- An independent review of the trust-bearing code (pipeline helper, signing adapter, provenance predicate, finalization, signing and verification steps, trigger and credential model) is closed with no open critical findings.
- The decision records that define the `pipeline-attested` tier and the three source lanes, the production-provenance predicate, the keyless signing model and the cosign executable pin, the trigger, identity, and credential rules, the private-mirror arrangement, and the v1 relaxation of builder-profile and byte-identity requirements are approved by named deciders; contributor guidance points to them.
- Repository settings are captured in the evidence log: branch protection requiring review on the default branch; code ownership over the workflows, generator, helper, and signing code; publish and sign jobs confined to a protected environment restricted to the default branch and manual dispatch.
- Consumer instructions are published: the exact commands, the build-workflow identity, the issuer, and the registry access needed.
- The evidence log records the run URL, commit, definition digest, published reference, cosign version and digest, the consumer transcript, and the transparency-log index.

---

## Epic M2 — Breadth

**Goal.** The in-scope AMD64 artifact set is delivered, including
the native AMD64 NVKE OS image built from its pinned source and published as a signed disk-image
artifact with exact-subject producer provenance and authenticated material evidence. The image already contains
Kubernetes (node side and control-plane images), CNI, and node-agent. Around it: the CWE controller image and the
NVKE cluster images that are not baked into the OS image (enumerated by name in the Definition by the source owner), the operator tooling package, and chart packages. The Release binds
these named outputs to the exact delivered artifacts.

**Delivers**
- **Wrapped external builds.** A source lane for pinned external builds: a declared entrypoint from a pinned
  repository commit, run in a pinned container with declared privilege and network access. Build, re-hash,
  run record and disk-evidence packaging retain the same live producer ownership; publication, signing and
  verification use separate credential-scoped jobs. The provenance build
  type is `wrapped-build` and records the declared network and privilege, so a consumer can see NVKR makes no
  hermeticity claim; no builder-profile preflight or byte-identity gate applies in v1. A wrapped build holds no
  credential of any kind during its build phase, network or not: private inputs are fetched into the workspace by
  a preceding fetch step that holds only the fetch credential, and registry and OIDC credentials are introduced
  only after the build phase has exited and the run record is written.
- **The OS image artifact.** One AMD64 disk-image artifact, with provenance naming the NVKE
  commit and tree hash. Pushed as a single-layer artifact if the registry's blob limit permits,
  otherwise chunked, with the whole-image digest in the run record and provenance.
  M2 binds those bytes to the successful producer and authenticated materials. M3 retains that producer-only
  scope; installed-content inventory and reconciliation are deferred hardening. NVKE owns the correctness and runtime
  validation of embedded software; NVKR attests production and composition under the admitted evidence contract.
  Matching dependency or inventory evidence never promotes this wrapped disk to a reproduction claim.
- **Composition inputs and optional metadata.** The OS image's selected source and material inputs account for
  its required components (node-agent, node-side Kubernetes, control-plane images, CNI, and spire-agent if so
  decided), without requiring separate delivery. Exact source, patch, configuration and material identities
  remain bound to production evidence. An embedded-component metadata declaration is optional. If
  authored, its complete member list, versions and known source pins must match the referenced upstream metadata
  at the pinned commit; omission is not applicable. This comparison does not apply build customizations or
  establish installed contents. M2 and M3 MVP producer evidence report inspection as not performed. Read-only
  final-disk inspection and material reconciliation remain deferred hardening.
- **Image and blob publication.** Pushing container-image layouts by digest with the pinned digest preserved,
  and raw blobs (tarballs, images) as OCI artifacts; every push refuses to move an existing tag to a different
  digest and skips content that is already present, so re-runs are cheap and mirrors are idempotent.
- **Acquisition.** Digest-pinned acquisition of kubectl, Helm, and the
  required third-party images selected from the pinned NVKE deployment configuration that are not inside the OS image. Pins live in the
  lockfile and are written by the pin-resolution command (resolve an upstream version to a digest and rewrite the
  lockfile), which M4 schedules and extends. Each acquired artifact carries an `acquired` provenance attestation
  recording the upstream reference, the lockfile pin, and the fetch time, and making no build claim.
- **Spire-agent.** Placement requires a recorded owner decision in the Definition: inside the OS
  image (its matrix row then reads "inside the OS image" and its selected production inputs account for it) or
  acquired by digest as a named output in the Release. Until decided it is a dated deferral, not an omission.
- **Packages.** An operator tooling package (kubectl, Helm); NVKE chart packages where sources are available. Each is one signed,
  attested OCI artifact.
- **Release membership.** Every required named output is bound to its exact published artifact digest and
  evidence. Consumer verification rejects omitted, extra or substituted outputs. A Definition describes a
  deliverable set; downstream systems own deployment topology and node placement.

**Does not deliver.** Separate delivery of anything the definition marks "inside the OS image" (node-agent,
node-side Kubernetes, control-plane images, CNI); independent runtime or functional validation of embedded
software (NVKE OS image build team); NVKR-built Kubernetes; vulnerability scanning; verification of upstream signatures on acquired
artifacts (acquisition is digest-pinned trust on first use); deployment topology or node-role assignment;
ARM64 or any other architecture as a product acceptance requirement; deep disk inspection and material reconciliation.
An additional architecture selected by a Definition remains subject to the same complete-output contract.

**Pre-authorized fallbacks.** Mirror the prebuilt signed QCOW2 by digest (`acquired`) if the OS build cannot run
on an admitted execution route. The acquired-disk fallback must preserve the exact acquired bytes and carry
acquisition provenance; M3 preserves the producer-only scope without claiming disk inspection.
Mirror NVKE-published CWE and NVKE images (`acquired`) instead of building them. Enclose chart
tarballs in the signed package instead of publishing CNCF OCI charts. Any approved deferred artifact remains
explicit in the requirements and planning with its reason and decision. Every selected output must complete
before the M3 MVP can pass; it accepts no qualification waivers. A missing required output still makes its Run
incomplete; a build failure cannot create an implicit deferral.

**Acceptance criteria**
1. The native AMD64 OS image is published as one signed, attested disk-image artifact built from its pinned NVKE commit. Its provenance identifies the source commit, tree hash and wrapped-build scope; exact-subject producer/material evidence and consumer verification succeed. An admitted acquired-disk fallback preserves the pinned bytes and carries acquisition provenance. Inspection is reported honestly and is outside the M3 MVP. Any additional disk selected by the Definition independently satisfies the same delivery and evidence checks.
2. Every artifact-matrix row assigned to M2 that is not inside the OS image and not deferred by a recorded decision (CWE controller, NVKE container images, Helm, spire-agent if acquired, third-party images, NVKE chart packages, operator tooling package) is published under a digest-pinned reference, signed, carries provenance for its source lane, passes the workflow's verification step, and passes consumer verification. Every acquired artifact's lockfile pin equals its published digest; every built artifact's run-record digest equals its published digest. Each selected disk's provenance binds its selected source and material inputs; every required inside-image component is accounted for in those production inputs. Missing producer/material evidence or a digest mismatch fails acceptance; producer-only evidence cannot claim an inspected inventory.
3. CWE controller and NVKE container images are either built (`wrapped-build`, provenance naming the NVKE commit and tree hash) or, under the Sep 10 fallback, mirrored by digest and recorded as `acquired`. For each CWE, NVKE, and third-party image, `docker pull` by digest succeeds and `cosign verify` passes. A second default-branch run with no pin changes skips every already-present image and blob (each skip in the run log) and every pre-existing tag resolves to the same digest afterwards.
4. The operator tooling package is published as one signed, attested OCI artifact and passes consumer verification. The same Run's Release binds every required output to its exact published digest and evidence. Consumer verification rejects any missing, extra or substituted output. An approved deferred artifact remains explicitly recorded in the requirements and qualification evidence; it cannot be reported as a successful output.
5. Criteria 1-4 are satisfied by a single default-branch run identified by one run URL. Partial retries may retain authenticated successful outputs from earlier attempts of that run, with unchanged inputs and original evidence. Artifacts from different runs cannot be combined to satisfy this criterion.

**Definition of done**
- Tests cover tag-move refusal for image, blob, and package pushes; skip-if-present idempotency; wrapped-build policy rejecting undeclared privilege, network or credentials before any output is written; the credential-free build phase; release membership rejecting a member digest that differs from the published digest; optional embedded metadata that is omitted, agrees with its pinned upstream reference, or fails because referenced upstream metadata is missing, malformed or inconsistent.
- The evidence log has one row per artifact-matrix row: published reference and digest with its source lane, "inside the OS image" (node-agent, Kubernetes, CNI, and spire-agent if so decided), or the deferral reason and date; plus the single run URL from criterion 5 and the consumer transcripts from criteria 1-4.
- Every fallback taken and every deferral is recorded in the owning decision record, the implementation record, and this specification's artifact source/delivery matrix.
- An independent review of the publication code, the wrapped-build policy (where privilege and network are allowed, and that no credential is present), and the definition and lockfile changes is closed.
- The OS-build feasibility result (entrypoint, container, privilege, network, disk, wall time, output size, registry blob limit) and the owner decisions (OS build path, NVKE image path, spire-agent placement, NVKE images outside the OS image) are recorded.

---

## Epic M3 — Qualification

**Goal.** Every artifact selected by a Definition is checked against its producing Run and build evidence.
Successful qualification produces independently verifiable evidence under an identity distinct from the
builder. Release issuance requires successful qualification and evidence publication for the complete selected set.

This specification records the current scope, including optional embedded-component
metadata. The initial scope decision
establishes producer-only disk evidence and the deferred capabilities described below. The
implementation plan separates artifact
authentication, delivered-tool smoke, independent evidence publication and acceptance demonstrations.
Scope approval does not establish completed implementation or organizational ADR ratification. The reviewed
implementation and candidate configuration may merge before real operational acceptance. A fresh complete
producer execution, hosted positive and negative qualification, and independent consumer verification follow
merge. Their evidence remains required before production-readiness claims or completion of this milestone;
merge and repository tests do not satisfy those criteria.

**Delivers**
- **Authenticated candidate.** Strict input and evidence schemas; complete exact output membership; applicable
  pin, digest and source consistency; cryptographically verified build signatures and provenance under the
  expected signer and issuer. Payload and delivery-container identities retain their distinct bindings.
  Source, patch, configuration and material identities remain bound to production evidence. Embedded-component
  declarations are optional; omission is not applicable, while any authored declaration must match its referenced
  upstream metadata at the pinned commit. This does not establish effective customized or installed composition.
- **Delivered-tool smoke.** Each selected CLI and operator executable performs its documented version check
  using delivered bytes with networking disabled and without qualification signing authority. Non-tool outputs
  follow an explicit applicability rule.
- **Independent evidence.** All required results are collected before any qualification signing begins. A
  complete passing set receives subject-bound evidence under a separate qualification identity. A failed
  verdict prevents all qualification attestations for the invocation. Later signing or attachment failure
  marks qualification unsuccessful even if earlier attachments remain.
- **Trusted source and invocation.** Qualification admits only successful approved builds from the intended
  repository and default branch, authenticating the source Run separately from the qualification invocation.
  It reuses the same published bytes and never rebuilds or substitutes them. Missing required outputs cannot
  be dropped or treated as passing. A retry may retain authenticated successful outputs from earlier attempts
  of the same producing workflow run. Successful tool checks may likewise be retained within the same
  qualification workflow run when their inputs, policy and complete membership are unchanged. Original
  attempt identities remain visible; later failures cannot be hidden by an earlier passing result.

**Deferred scope.** Full disk-content inspection and material reconciliation, including embedded disk-content
verification; waived verdicts; offline pull-request qualification; a dedicated verify-only dispatch; cross-run
qualification reuse. Disk evidence explicitly remains producer-only. Cluster end-to-end tests, boot validation,
vulnerability scanning and byte-identical reproduction are outside this MVP. External suites outside scope
are explicitly reported as not evaluated, never passed. Any later required suite must supply authenticated
results bound to exact artifact subjects; missing or failing required results block qualification.

**Acceptance criteria**
1. **Complete, exact candidate.** Qualification accounts for every selected output and authenticates its
   relationship to the originating Run, original attempt identities, commit, Definition and relevant input
   identities. Missing, extra, duplicate, substituted or mismatched members fail with a specific error. Unrelated Run evidence
   cannot satisfy the candidate.
2. **Authenticated checks.** Every output passes schema validation, applicable pin/digest consistency and
   verification of build signatures and provenance against the expected signer and issuer. Exact source, patch,
   configuration and material identities remain bound to the producing Run. Embedded-component declarations are
   optional: omission is not applicable; any authored declaration must agree with its referenced upstream metadata
   at the pinned commit, and missing, malformed or disagreeing referenced data fails. This metadata check does not
   establish effective customized or installed composition. Provenance identifies the expected source and exact
   subject. Payload and delivery-container identities are checked through their respective bindings.
3. **Delivered-tool smoke.** Every selected CLI and operator executable performs its documented version check
   using delivered bytes without network access. Startup failure, unsuccessful execution or a conflicting
   declared version fails that output. An explicit applicability rule records smoke as not applicable to non-tool outputs.
4. **Pass or fail only.** Every output receives exactly one overall pass or fail verdict; every applicable
   check has a result. No waivers are accepted. All checks finish before qualification signing begins. Any
   failed verdict blocks all qualification attestations for that invocation and preserves failure diagnostics.
5. **Independent signed evidence.** After all verdicts pass, evidence is signed for every selected artifact
   under a qualification identity distinct from the builder. Evidence binds exact subjects, originating Run
   and original attempt identities, Definition and relevant input identities, check results, gate version and
   qualification invocation. Disk evidence explicitly states that content inspection was not performed.
6. **Trusted invocation.** Only successful approved builds from the intended repository and default branch
   enter qualification. Records belong to the exact originating build. Smoke execution has no qualification
   signing authority. A green build alone does not establish qualification.
7. **Observed acceptance.** One complete selected set passes independent consumer verification. Controlled
   failures cover altered bytes, missing provenance, invalid or missing signatures, wrong pins, inconsistent
   explicitly authored embedded-component metadata, mismatched Run evidence and failed smoke. Each fails with
   a check-specific error and publishes no qualification attestations.
8. **Operational failure blocks release.** A signing or evidence-attachment failure marks qualification
   unsuccessful and prevents release issuance. Earlier attachments may remain after an operational failure;
   partial publication never counts as success.

**Definition of done**
- Tests cover the checks, applicability rules, exact identity/subject binding and failure boundaries.
- Independent review of gate placement, trusted invocation and credential separation is closed.
- Consumer instructions identify the build and qualification identities and issuer, and explain independent
  verification and rejection of absent, incomplete or mismatched evidence.
- The positive Run, per-artifact evidence, independent consumer transcript and controlled failures are linked.
- The qualification evidence schema and reduced milestone scope are recorded in the owning decisions,
  implementation record and this specification. M4 integration consumes the failure contract; implementing
  M4 first is not a prerequisite for closing M3 acceptance.

---

## Epic M4 — Automated releases

**Goal.** A pin update flows unattended from detection to a reviewable pull request; merging to the default
branch produces a signed, immutably tagged release; a manual dispatch re-issues a release under a chosen tag; a
signed revocation withdraws a release from the channel without erasing its evidence. Release publication behaves
predictably when two runs overlap or a run is interrupted part-way.

**Delivers**
- **Pin detection and regeneration.** A scheduled workflow detects new upstream versions, refreshes source pins,
  lockfile pins, and action pins in one pass, regenerates the workflows, and opens a pull request using a GitHub
  App installation token (the workflow's own token does not trigger pull-request workflows; a personal token is
  not used) scoped to contents, pull requests and workflows on this repository only, so a regenerated workflow
  travels in the same reviewed pull request. The App's private key is the only
  long-lived write secret the pin-update workflow holds and is absent from every other workflow. The App authors the PR,
  cannot approve it, and a code-owner approval is required to merge. The PR body lists, for every changed pin,
  the old and new tag, commit, tree hash, and digest. Merging to the default branch is the release decision for
  v1; the pin-update PR is the one that changes which pins are released.
- **The release manifest.** A versioned release-manifest document listing, for one default-branch run, every
  artifact whose qualification verdict is pass: its source lane, digest, signature, provenance attestation, and
  qualification-evidence attestation, with any deferred bundle members enumerated. An artifact with a fail
  verdict makes the run red and no manifest is issued. The manifest is signed and attested by the qualification
  workflow. It is explicitly distinct from any future reproduction-based release authorization.
- **Tagging.** The manifest is published under an immutable tag derived from the definition digest and the
  repository commit of the run, so every default-branch run has exactly one tag. A re-run of the same repository
  commit finds its tag present, publishes no new manifest, and succeeds with the recorded outcome
  "already released" and the existing manifest digest. One mutable channel tag tracks the most recent green
  default-branch release; the release step refuses to move any other existing tag. Immutability is enforced by
  the release step and, where the registry supports it, by registry-side tag immutability on the release
  repository; otherwise the release workflow's credential is the only one that can push there. Consumers resolve
  a tag once and pin the manifest by digest thereafter.
- **Overlap and interruption.** Tag creation is conditional: a release step creates an immutable tag only if it
  is absent, and two runs that race for the same tag produce exactly one manifest with the loser recording
  "already released". The channel tag advances only to a manifest whose run is green, by a recorded winner rule
  (the run whose repository commit is the descendant; a run for an older commit never moves the channel
  backwards). A run interrupted after the manifest is published but before the channel moves leaves the
  immutable tag present and the channel unchanged; a re-run of that commit records "already released" and
  completes the channel move. Publication, signing and tagging are separate effects with no atomic rollback;
  the run, plan and subject records needed to investigate a partial publication are kept in a durable store
  beyond CI log expiry.
- **Revocation.** A manual dispatch on the default branch, restricted to the qualification identity's
  environment, takes a manifest or artifact digest, a reason and a time. It attests a signed revocation marker
  to that digest under the qualification identity, removes the channel tag if it points at that digest, and
  changes nothing else: the immutable tag, the original signature and the transparency evidence remain. Release-
  scope consumer verification rejects a revoked digest even though its original signature still verifies.
  Revocation carries no trusted-time or freshness guarantee; those belong to the qualified lane.
- **Manual release.** A dispatch on the default branch takes a release-tag input and a source-manifest reference
  (the digest of a manifest already published by a default-branch run). It rebuilds nothing: it re-verifies every
  member of the source manifest, emits a new manifest with identical members under the requested tag, and signs
  and attests it. A dispatch naming a manifest whose members fail verification, or a tag that already exists, is
  refused with a distinct error code.

**Does not deliver.** Unattended merge (a pre-authorized option requiring security approval); verification of
upstream signatures in the pin loop; a scheduled release-audit job; reproduction-based release authorization;
any release registry, registration or lookup service beyond the OCI tag itself; trusted-time, freshness or
anti-rollback guarantees on revocation; cluster-scoped qualification.

**Acceptance criteria**
1. A scheduled run of the pin-update workflow (schedule enabled in the committed workflow; a dispatch against an injected upstream change may be used to demonstrate it) opens a pull request whose body lists, for every changed source, lockfile, and action pin, the old and new tag, commit, tree hash, and digest; the pull request's own build-and-qualify run starts automatically and is green.
2. Merging that pull request produces a signed, attested release manifest under the tag computed by the decision record's rule from the definition digest and repository commit; the manifest lists the source lane, digest, signature, provenance, and qualification evidence of every artifact published by that run; consumer verification of the manifest succeeds against the qualification identity; the channel tag resolves to the new manifest digest.
3. A dispatch with a release-tag input equal to an existing tag is refused with the tag-immutability error code; afterwards the existing tag still resolves to its prior digest and the channel tag is unchanged.
4. A dispatch with a new release-tag input and a source-manifest reference publishes a manifest under the requested tag whose member digests are identical to the source manifest's, without running any build or acquisition job; consumer verification of the new manifest succeeds.
5. Two default-branch runs started for different commits within the same window both complete: each creates its own immutable tag exactly once, no existing tag moves, the channel tag ends at the manifest chosen by the recorded winner rule, and the run logs show the conditional tag creation. A run for the same commit started while the first is still publishing records "already released" and publishes no second manifest.
6. A release run cancelled after the manifest is published and before the channel tag moves leaves the immutable tag resolving to the manifest and the channel unchanged; the re-run records "already released", moves the channel, and publishes nothing new. The run, plan and subject records for the interrupted attempt are retrievable from the durable store after the CI log has expired or been deleted.
7. A revocation dispatch against a released manifest digest attaches a signed revocation marker under the qualification identity, the channel tag no longer resolves to that digest, the immutable tag still does, `cosign verify` against the revoked digest still succeeds, and release-scope consumer verification of the revoked digest fails naming the revocation. A revocation dispatch from a pull-request ref or without the qualification environment is refused.

**Definition of done**
- Tests cover the pin refresh end to end on a fixture definition and lockfile, deterministic serialization of the release manifest, the "already released" no-op on a re-run, tag immutability and conditional tag creation against a registry, the channel winner rule, the interrupted-publication re-run, the revocation marker's shape and role, and release-scope verification rejecting a revoked digest.
- An independent review of the release step, the pin tooling, the revocation dispatch's identity and environment restriction, and the pin-update workflow (App permission scope, key storage and rotation, least privilege, code-owner approval required, and the repository settings that protect the pull-request branch) is closed before the release run is declared evidence; the final decision-record text is reviewed.
- The evidence log records the pull request URL, run URLs, manifest reference, consumer transcripts, the tag-move refusal, the overlapping-run and interrupted-run logs, and the revocation transcript.
- The owning decision records are final, including the release-manifest and revocation-marker schemas registered before implementation, the tag rule, the winner rule and the durable-record store and retention period; the implementation record, contributor guidance (including the explicit list of capabilities excluded from v1), and this specification's artifact source/delivery matrix match what shipped.

---

## Shared dependencies and limits

M1 precedes M2 artifact delivery; M3 needs their signed artifacts, and M4 needs the qualification gate.
Independent reviews, decision ratification and scoped evidence are required at every stage.

| Owner | Required decision or evidence |
|---|---|
| NVKE build owners | Buildable pinned entrypoints, vendored private inputs, external image/chart list, OS contents and Spire-agent placement |
| CI/release engineering | Admitted runner virtualization, privilege, disk, wall-time and isolation requirements, including the narrowly selected native route |
| Registry/identity owners | Registry blob limits and retention, protected publication environment, keyless identities, source access and public-transparency disclosure approval |
| Qualification owner | Separate signing identity, complete pass/fail evidence and independent positive/negative verification transcripts |
| Release owner | Pin-bot permissions, immutable-tag enforcement, rerun/concurrency behavior and channel policy |

The OS build's execution route and source lane, NVKE image build/acquisition choice, Spire-agent placement,
and images delivered outside the disk require recorded owner decisions. Unsupported execution may use
only the explicitly approved acquisition fallback; provenance and the acceptance record must identify it.
Production acceptance still requires the selected default-branch run and independent consumer evidence.
The native candidate route and a validation-branch dispatch do not satisfy those requirements by analogy.

Outside v1: non-OCI delivery, upstream from-source replacement builds, acquired upstream-signature
verification, vulnerability scanning, deployment topology, cluster E2E qualification, air gap,
builder-profile reproduction and byte-identity guarantees. GitHub is the sole hosted workflow,
publication and signing provider. Per-built-artifact SBOM composition remains in scope.

A downstream nightly end-to-end test is customer CI infrastructure, outside v1
product acceptance. It can exercise delivered artifacts without expanding the
build/evidence qualification milestone or supplying independently signed conformance.
