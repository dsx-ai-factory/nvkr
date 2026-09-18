# NKD v1 — Milestone epics (specification, Jira-ready)

Revised 2026-09-17. The earlier version was posted to Jira 2026-09-09 as NKX-15957 (M1), NKX-15958 (M2),
NKX-15959 (M3), NKX-15960 (M4); these local revisions do not update those issues. Production acceptance for
M1–M4 uses the six-object public model. This specifies
required behavior; the implementation record separately identifies completed work and external evidence.
T-shirt sizes describe relative effort, not delivery guarantees. Owners and dates remain planning inputs.
The architecture description, milestone document and current decisions govern the local contract. Dated
posting and execution records retain their original scope.

**What NKD v1 is.** A generator and evidence layer. From a digest-pinned Definition of NKE artifacts and a
separately selected Environment it emits committed GitHub Actions workflows that build or acquire each artifact, publish it to an OCI
registry, sign it, attach production provenance, qualify it, and tag releases. NVIDIA runs the generated
workflows; consumers verify the results with standard tooling and no NKD software.

**Facts the specification rests on.**
- The NKE OS image build bakes in Kubernetes (node side and control-plane images), CNI, and node-agent. NKD wraps
  that build; it does not build or separately deliver those embedded components. Before disk delivery is admitted,
  NKD requires a full-disk inventory that reconciles traced build materials with read-only inspection of the
  delivered disk. This establishes composition, not correct behavior; the NKE OS image build team owns embedded
  software correctness and runtime validation. Disk production and its inventory adapters remain deferred.
- v1 production acceptance targets amd64 only. The selected GitHub-hosted runner must admit the OS build's
  virtualization and privilege requirements; broader upstream architecture support does not expand this scope.
- Every artifact is delivered in OCI, signed keyless with cosign from the workflow run that produced it, and
  carries an SLSA v1 provenance attestation describing how the bytes were produced. No reproduction or
  byte-identity claim is made anywhere in v1, and the earlier builder-profile preflight and bake-off requirements
  are relaxed for these milestones: a wrapped build's declared network and privilege are recorded, not forbidden.
- Verification by a consumer means `cosign verify` and `cosign verify-attestation` (provenance and, from M3,
  qualification evidence) succeed on a machine with only cosign installed, using the published signer identities
  and issuer. A Verification Policy names its acceptance scope explicitly: *artifact publication* (exact
  published bytes, build identity, signature and production provenance; the M1 scope) or *release* (artifact
  publication plus qualification identity and verdicts, manifest membership and revocation state; the M3/M4
  scope). An unavailable scope fails; it never falls back to the weaker one.
- Every built artifact has a per-artifact software bill of materials (CycloneDX), bound to the observed digest
  and published as a signed attestation. The signed release index binds required evidence membership; Run
  completion is a separate record. SBOMs assert composition and verify no upstream signature.
- Workflows run on GitHub-hosted runners only.
- Evidence for every acceptance criterion is a workflow run URL plus, where a consumer can check the criterion, a
  consumer-side verification transcript, and, for negative criteria, the run log showing the refusal and its error
  code. All of it is recorded in the evidence log and referenced from the implementation record.

**Public objects.**

| Object | Responsibility |
|---|---|
| Definition | Authored named outputs with direct component or AICR profile selections, target platforms and packaging |
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

---

## Epic M1 — Vertical slice

| Field | Value |
|---|---|
| **Jira key** | [NKX-15957](https://jirasw.nvidia.com/browse/NKX-15957) (created 2026-09-09) |
| **Issue type** | Epic |
| **Epic name** | NKD M1 Vertical Slice |
| **Summary** | One generated GitHub Actions run builds the NKE CLI from a pinned commit, keyless-signs it, attaches SLSA provenance, pushes to OCI; `cosign verify` passes from a clean machine |
| **Priority / Rank** | Highest / 1 |
| **T-shirt size** | **XS** — historical sizing; local CLI execution and personal-namespace hosted signing/consumption have recorded evidence. Current NVIDIA execution, acceptance gaps, and organizational review remain |
| **Components / Labels** | `release_eng` / `nke_nkd`, `nkd-v1`, `m1-vertical-slice` |
| **Milestone label** | M1 — Vertical slice |
| **Due date (best estimate)** | 2026-09-16 |
| **Linear status** | BUILDING |
| **Depends on** | — (external: GitHub org and registry namespace, OIDC identity, private NKE source access, product-security approval of public Sigstore) |
| **Blocks** | M2, M3, M4 |
| **Related** | NKX-14794 (NKD Design & Contracts) |
| **Eng PIC / Product PIC** | TBD / TBD |
| **Status note** | Earlier local CLI-only runs and subsequent personal-namespace hosted signing and independent consumption are recorded in the implementation record. They do not establish the current NVIDIA inputs or full milestone completion. Open: current-namespace execution, push/PR trigger requirements, the per-job helper digest negative demonstration, independent review and decision ratification. |

**Goal.** One generated workflow builds the NKE CLI from a pinned NKE commit, signs it, attaches provenance,
pushes everything to OCI, and verifies it. Acceptance requires every signing and publication mechanism to run against the real services. Later milestones add build types, the gate and release tagging.

**Delivers**
- **The generator.** Turns the Definition and Environment into committed workflows whose third-party actions
  are pinned to commit SHAs. Regeneration with the same pinned inputs and generator must match byte for byte.
- **The pipeline helper.** Each job downloads it, verifies its digest against the pinned value, and fails otherwise.
- **The pinned source model.** A source is a repository, a commit, a tree hash, a toolchain image digest, and a
  build command. Private NKE sources are reachable from hosted runners directly or through a configured mirror, with vendored inputs and a
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
2. The run's provenance names the NKE repository, commit, and tree hash, each equal to the value in the committed definition; the run log shows the tree-hash check passing before the build step; the built NKE CLI is the subject of the published reference.
3. The published reference carries a keyless cosign signature and an SLSA v1 production-provenance attestation whose builder identity is the committed workflow path and ref, whose invocation identifies the run, whose build type is `hermetic-build`, whose external parameters name the Definition, Environment and plan identities, whose resolved dependencies name the NKE commit, tree hash and toolchain image digest, and whose internal parameters record a real (not dry-run) signing mode and the pipeline helper digest. The Run and its Release record the evidence tier `pipeline-attested`.
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

| Field | Value |
|---|---|
| **Jira key** | [NKX-15958](https://jirasw.nvidia.com/browse/NKX-15958) (created 2026-09-09) |
| **Issue type** | Epic |
| **Epic name** | NKD M2 Breadth |
| **Summary** | The in-scope artifact set is delivered and signed in OCI with provenance, centred on the wrapped amd64 NKE OS image; the AICR package is one signed artifact and every required output has exact digest-bound release membership |
| **Priority / Rank** | High / 2 |
| **T-shirt size** | **M** — the wrapped-build lane and image/blob publication are real scope; OS-build feasibility still requires an admitted hosted environment. Re-size if capability testing fails, arm64 is pulled forward or wrapped-build isolation must be stricter |
| **Components / Labels** | `release_eng` / `nke_nkd`, `nkd-v1`, `m2-breadth` |
| **Milestone label** | M2 — Breadth |
| **Due date (best estimate)** | 2026-09-25 (signed OS image and the M2 core) |
| **Linear status** | PRIORITIZED |
| **Depends on** | M1; external: NKE OS build runnable from a single pinned entrypoint on a hosted amd64 runner, NKE image list and chart sources by 2026-09-12, spire-agent placement decision 2026-09-10 |
| **Blocks** | M3 (artifacts to qualify), M4 (artifacts to tag) |
| **Related** | NKX-14794 |
| **Eng PIC / Product PIC** | TBD / TBD |
| **Status note** | Not started. Shape is fixed at the 2026-09-10 checkpoint (wrapped OS build or QCOW2 mirror; built or mirrored NKE images; spire-agent placement; which NKE images sit outside the OS image). |

**Goal.** The in-scope artifact set is delivered. The centre is the NKE OS image for amd64, built by wrapping the
NKE node-images build at a pinned commit and published as one signed disk-image artifact; it already contains
Kubernetes (node side and control-plane images), CNI, and node-agent. Around it: the CWE controller image and the
NKE cluster images that are not baked into the OS image (enumerated by name in the definition as a Sep 10
checkpoint output), the operator tooling package, the AICR package, and chart packages. The Release binds
these named outputs to the exact delivered artifacts.

**Delivers**
- **Wrapped external builds.** A source lane for pinned external builds: a declared entrypoint from a pinned
  repository commit, run in a pinned container with declared privilege and network access, with build, re-hash,
  run record, publish, sign, and verify fused into one job because the output is gigabytes. The provenance build
  type is `wrapped-build` and records the declared network and privilege, so a consumer can see NKD makes no
  hermeticity claim; no builder-profile preflight or byte-identity gate applies in v1. A wrapped build holds no
  credential of any kind during its build phase, network or not: private inputs are fetched into the workspace by
  a preceding fetch step that holds only the fetch credential, and registry and OIDC credentials are introduced
  only after the build phase has exited and the run record is written.
- **The OS image artifact.** One disk-image artifact per OS image build, amd64, with provenance naming the NKE
  commit and tree hash. Pushed as a single-layer artifact if the registry's blob limit permits (verified at the
  Sep 10 checkpoint), otherwise chunked, with the whole-image digest in the run record and provenance. What is
  inside the image must be inventoried against its exact delivered bytes. NKE owns the correctness and runtime
  validation of embedded software; NKD attests production and composition under the admitted evidence contract.
- **Embedded-component declaration.** The OS image producer declares the components it embeds and NKD neither
  builds nor separately delivers (node-agent, node-side Kubernetes, control-plane images, CNI, and spire-agent
  if so decided), each with name, version and known source pin. The declaration flows into the provenance's
  resolved dependencies and the aggregate SBOM as dependency edges from the image to its members, and M3
  cross-checks it against NKE's own version manifest at the pinned commit. The declared list alone cannot supply
  the full-disk inventory: traced materials must reconcile with read-only final-disk inspection. Missing members,
  unreadable or unsupported disks, inspection failure or disagreement prevent delivery admission. These adapters
  remain deferred, and their eventual inventory does not establish runtime correctness.
- **Image and blob publication.** Pushing container-image layouts by digest with the pinned digest preserved,
  and raw blobs (tarballs, images) as OCI artifacts; every push refuses to move an existing tag to a different
  digest and skips content that is already present, so re-runs are cheap and mirrors are idempotent.
- **Acquisition.** Digest-pinned acquisition of kubectl, Helm, the AICR CLI, AICR chart tarballs, and the
  third-party images referenced by the AICR and NKE charts that are not inside the OS image. Pins live in the
  lockfile and are written by the pin-resolution command (resolve an upstream version to a digest and rewrite the
  lockfile), which M4 schedules and extends. Each acquired artifact carries an `acquired` provenance attestation
  recording the upstream reference, the lockfile pin, and the fetch time, and making no build claim.
- **Spire-agent.** Placement is decided at the Sep 10 checkpoint and recorded in the definition: inside the OS
  image (its matrix row then reads "inside the OS image" and it joins the embedded-component declaration) or
  acquired by digest as a named output in the Release. Until decided it is a dated deferral, not an omission.
- **Packages.** An operator tooling package (kubectl, Helm, AICR CLI); an AICR package (the rendered AICR
  deployment plus its chart tarballs); NKE chart packages where sources are available. Each is one signed,
  attested OCI artifact.
- **Release membership.** Every required named output is bound to its exact published artifact digest and
  evidence. Consumer verification rejects omitted, extra or substituted outputs. A Definition describes a
  deliverable set; downstream systems own deployment topology and node placement.

**Does not deliver.** Separate delivery of anything the definition marks "inside the OS image" (node-agent,
node-side Kubernetes, control-plane images, CNI); independent runtime or functional validation of embedded
software (NKE OS image build team); NKD-built Kubernetes; vulnerability scanning; verification of upstream signatures on acquired
artifacts (acquisition is digest-pinned trust on first use); deployment topology or node-role assignment; arm64 and any other architecture.

**Pre-authorized fallbacks.** Mirror the prebuilt signed QCOW2 by digest (`acquired`) if the OS build cannot run
on a hosted runner. Mirror NKE-published CWE and NKE images (`acquired`) instead of building them. Enclose chart
tarballs in the signed package instead of publishing CNCF OCI charts. Any approved deferred artifact remains
explicit in the requirements and evidence with its reason and decision. The M3 gate records the deferral as a
waiver, never a pass, and M4 preserves it in the release manifest. A missing required output still makes its Run
incomplete; a build failure cannot create an implicit deferral.

**Acceptance criteria**
1. The OS image is published as one signed, attested amd64 disk-image artifact built from the pinned NKE commit; its provenance names that commit and tree hash with build type `wrapped-build`; consumer verification succeeds. (Under the fallback: the prebuilt QCOW2 is mirrored by digest, signed, consumer-verified, its `acquired` provenance names the upstream reference and digest, and the row is recorded as `acquired`.)
2. Every artifact-matrix row assigned to M2 that is not inside the OS image and not deferred by a recorded decision (CWE controller, NKE container images, Helm, AICR CLI, spire-agent if acquired, third-party images, AICR package, NKE chart packages, operator tooling package) is published under a digest-pinned reference, signed, carries provenance for its source lane, passes the workflow's verification step, and passes consumer verification. Every acquired artifact's lockfile pin equals its published digest; every built artifact's run-record digest equals its published digest. The OS image's provenance lists its declared embedded components, and every embedded row in the milestone matrix appears in that declaration. Its software inventory binds the exact delivered disk and reconciles traced materials with read-only inspection; disagreement fails delivery.
3. CWE controller and NKE container images are either built (`wrapped-build`, provenance naming the NKE commit and tree hash) or, under the Sep 10 fallback, mirrored by digest and recorded as `acquired`. For each CWE, NKE, and third-party image, `docker pull` by digest succeeds and `cosign verify` passes. A second default-branch run with no pin changes skips every already-present image and blob (each skip in the run log) and every pre-existing tag resolves to the same digest afterwards.
4. The AICR package and the operator tooling package are each published as one signed, attested OCI artifact and pass consumer verification. The same Run's Release binds every required output to its exact published digest and evidence. Consumer verification rejects any missing, extra or substituted output. An approved deferred artifact remains explicitly recorded in the requirements and qualification evidence; it cannot be reported as a successful output.
5. Criteria 1-4 are satisfied by a single default-branch run identified by one run URL, not assembled from artifacts published by different runs.

**Definition of done**
- Tests cover tag-move refusal for image, blob, and package pushes; skip-if-present idempotency; wrapped-build policy rejecting undeclared privilege, network or credentials before any output is written; the credential-free build phase; release membership rejecting a member digest that differs from the published digest; and an embedded-component declaration that disagrees with the upstream version manifest.
- The evidence log has one row per artifact-matrix row: published reference and digest with its source lane, "inside the OS image" (node-agent, Kubernetes, CNI, and spire-agent if so decided), or the deferral reason and date; plus the single run URL from criterion 5 and the consumer transcripts from criteria 1-4.
- Every fallback taken and every deferral is recorded in the owning decision record, the implementation record, and the milestone document's source-lane column.
- An independent review of the publication code, the wrapped-build policy (where privilege and network are allowed, and that no credential is present), and the definition and lockfile changes is closed.
- The OS-build feasibility result (entrypoint, container, privilege, network, disk, wall time, output size, registry blob limit) and the Sep 10 decisions (OS build path, NKE image path, spire-agent placement, NKE images outside the OS image) are recorded.

---

## Epic M3 — Qualification

| Field | Value |
|---|---|
| **Jira key** | [NKX-15959](https://jirasw.nvidia.com/browse/NKX-15959) (created 2026-09-09) |
| **Issue type** | Epic |
| **Epic name** | NKD M3 Qualification |
| **Summary** | An automated, fail-closed build/evidence gate (schema, pin consistency, signature and provenance presence, tooling smoke) runs after signing on every artifact and emits signed qualification evidence under its own identity |
| **Priority / Rank** | High / 3 |
| **T-shirt size** | **S** — one gate module, a node kind, signed evidence, and tamper negatives; embedded-software correctness and runtime validation remain with the NKE OS image build team |
| **Components / Labels** | `release_eng` / `nke_nkd`, `nkd-v1`, `m3-qualification` |
| **Milestone label** | M3 — Qualification |
| **Due date (best estimate)** | 2026-10-01 |
| **Linear status** | PLANNING |
| **Depends on** | M1 (signing and verification), M2 (artifacts to qualify) |
| **Blocks** | M4 (release requires the gate) |
| **Related** | NKX-14794 |
| **Eng PIC / Product PIC** | TBD / TBD |
| **Status note** | Not started. Gate module can be built in parallel with M2 once M1's signing adapter exists. |

**Goal.** An automated, fail-closed gate runs on every delivered artifact after signing and emits signed
qualification evidence under its own identity. This checks producer selections and build evidence. It does not
establish cluster conformance or independently verify the OS image's runtime behavior.

**Delivers**
- **The gate.** Four checks over every published artifact: *schema* (the Definition, component selections, pins and Environment decode exactly and validate);
  *pin consistency* (every acquired artifact's published digest equals its lockfile pin; every built artifact's
  published digest equals its run-record digest and its provenance names the pinned source commit and tree hash;
  every required output digest equals the published digest of that output; every embedded component declared
  by a wrapped build agrees with the upstream version manifest at the pinned commit, and a missing or disagreeing
  member fails the check); *presence* (a cosign signature and
  a provenance attestation exist and verify for every artifact under the build identity; a missing attestation is
  a distinct failure); *smoke* (operator tooling binaries and the NKE CLI run `version` inside a
  network-disabled container).
- **Signed evidence.** A versioned qualification-evidence document naming the definition digest, the run record,
  each subject's published digest, every applicable check with its verdict, and any waivers (component, reason,
  approver named in the decision record). It is attested to every subject by the qualification workflow: a
  separate committed workflow with its own keyless identity that holds only the credential needed to attach
  attestations. Build provenance verifies against the build identity; qualification evidence verifies against
  the qualification identity; both are in the consumer instructions. A failing verdict is never attested.
- **Placement.** On default-branch runs the qualification workflow runs after the build workflow's verification
  step and is a must-pass condition for release (M4). On pull requests the gate runs inside the build workflow
  against the packaged outputs on the runner without contacting the registry: schema, pin consistency (against
  run-record digests), and smoke run; presence is recorded as not applicable because pull requests sign nothing;
  nothing is attested.
- **Verify-only dispatch.** A manual dispatch that runs verification and the gate against an already-published
  manifest, building and publishing nothing. Used for red-run rehearsal here and by M4's manual release.
- **Carry-over.** Any approved artifact deferral from M2 is resolved or explicitly retained with its qualification waiver under M2's criterion 4.

**Does not deliver.** Independent functional or runtime validation of OS image contents (owned by the NKE OS image build team);
new inventory-generation requirements beyond M2's disk-composition contract; vulnerability scanning; booting the image; cluster conformance; deployment topology and cluster-scoped qualification.

**Acceptance criteria**
1. The gate is green on the default-branch run for every selected producer and every published artifact.
2. On a clean machine, `cosign verify-attestation` with the qualification-evidence predicate type and the published qualification identity and issuer (distinct from the build identity) succeeds for each artifact; the decoded evidence names the definition digest, the run record, the subject digest, every applicable check with its verdict, and any waivers.
3. Negatives, each run against a scratch registry repository, each failing the run with an error code specific to the failed check and attaching no qualification attestation to the affected subject: a mutated blob; a deleted provenance attestation; a stripped signature; a lockfile pin edited to a wrong digest; an embedded-component declaration whose version disagrees with the upstream manifest at the pinned commit.
4. Every subject appears in the evidence with exactly one verdict: pass, fail, or waived. A waived verdict names the component, the reason, and the approver. A run containing any fail verdict attests no evidence.
5. A pull request run executes the gate against the packaged outputs: the gate job is present and green, the run log records no registry reads or writes, presence is recorded as not applicable, and no qualification attestation is pushed.
6. A verify-only dispatch pointed at the tampered scratch repository from criterion 3 is red with the same check-specific error codes. (Pre-authorized to slip to Oct 2.)

**Definition of done**
- Tests cover a passing gate, each failure mode in criterion 3, the not-applicable presence result on pull requests, and the invariant that failing evidence is never attested.
- An independent review of the gate, its placement, and the qualification workflow's credential scope is closed.
- Consumer instructions are extended with the qualification-evidence predicate type, the qualification identity and issuer, the command that decodes the evidence document, and the rule that an artifact without qualification evidence is not released.
- The evidence log records the run URL, per-artifact evidence references, the consumer transcript, and the log of the negative runs.
- The owning decision records scope this gate as build-level, keep deployment qualification outside NKD, register the qualification-evidence document schema before implementation, and distinguish NKD's disk inventory from NKE-owned embedded-software correctness and runtime validation.

---

## Epic M4 — Automated releases

| Field | Value |
|---|---|
| **Jira key** | [NKX-15960](https://jirasw.nvidia.com/browse/NKX-15960) (created 2026-09-09) |
| **Issue type** | Epic |
| **Epic name** | NKD M4 Automated Releases |
| **Summary** | A pin update flows unattended to a reviewable PR; merging produces a signed, immutably tagged release manifest; a manual dispatch re-issues a release under a chosen tag; a signed revocation withdraws a release from the channel |
| **Priority / Rank** | High / 4 |
| **T-shirt size** | **S** — bump chain, release manifest, immutable tag, dispatch and revocation marker are mostly definition; the GitHub App token and registry tag-immutability are configuration. The concurrent-run and interrupted-publication rules are the one design item; re-size to M if the registry offers no conditional tag create |
| **Components / Labels** | `release_eng` / `nke_nkd`, `nkd-v1`, `m4-automated-releases` |
| **Milestone label** | M4 — Automated releases |
| **Due date (best estimate)** | 2026-10-01 |
| **Linear status** | PLANNING |
| **Depends on** | M1 (trigger model), M2 (publication), M3 (gate); external: a GitHub App for the pin bot, registry-side tag immutability or a namespace whose only push credential is the release workflow's |
| **Blocks** | — |
| **Related** | NKX-14794 |
| **Eng PIC / Product PIC** | TBD / TBD |
| **Status note** | Not started. Pin detection primitives exist (source and action pin refresh with a check mode); the scheduled workflow, release manifest, and tagging are new. |

**Goal.** A pin update flows unattended from detection to a reviewable pull request; merging to the default
branch produces a signed, immutably tagged release; a manual dispatch re-issues a release under a chosen tag; a
signed revocation withdraws a release from the channel without erasing its evidence. Release publication behaves
predictably when two runs overlap or a run is interrupted part-way.

**Delivers**
- **Pin detection and regeneration.** A scheduled workflow detects new upstream versions, refreshes source pins,
  lockfile pins, and action pins in one pass, regenerates the workflows, and opens a pull request using a GitHub
  App installation token (the workflow's own token does not trigger pull-request workflows; a personal token is
  not used) scoped to contents and pull requests on this repository only. The App's private key is the only
  long-lived secret the pin-update workflow holds and is absent from every other workflow. The App authors the PR,
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
4. A dispatch with a new release-tag input and a source-manifest reference publishes a manifest under the requested tag whose member digests are identical to the source manifest's, without running any build or acquisition job; consumer verification of the new manifest succeeds. (Pre-authorized to slip to Oct 2.)
5. Two default-branch runs started for different commits within the same window both complete: each creates its own immutable tag exactly once, no existing tag moves, the channel tag ends at the manifest chosen by the recorded winner rule, and the run logs show the conditional tag creation. A run for the same commit started while the first is still publishing records "already released" and publishes no second manifest.
6. A release run cancelled after the manifest is published and before the channel tag moves leaves the immutable tag resolving to the manifest and the channel unchanged; the re-run records "already released", moves the channel, and publishes nothing new. The run, plan and subject records for the interrupted attempt are retrievable from the durable store after the CI log has expired or been deleted.
7. A revocation dispatch against a released manifest digest attaches a signed revocation marker under the qualification identity, the channel tag no longer resolves to that digest, the immutable tag still does, `cosign verify` against the revoked digest still succeeds, and release-scope consumer verification of the revoked digest fails naming the revocation. A revocation dispatch from a pull-request ref or without the qualification environment is refused. (Pre-authorized to slip to Oct 2.)

**Definition of done**
- Tests cover the pin refresh end to end on a fixture definition and lockfile, deterministic serialization of the release manifest, the "already released" no-op on a re-run, tag immutability and conditional tag creation against a registry, the channel winner rule, the interrupted-publication re-run, the revocation marker's shape and role, and release-scope verification rejecting a revoked digest.
- An independent review of the release step, the pin tooling, the revocation dispatch's identity and environment restriction, and the pin-update workflow (App permission scope, key storage and rotation, least privilege, code-owner approval required) is closed before the release run is declared evidence; the final decision-record text is reviewed.
- The evidence log records the pull request URL, run URLs, manifest reference, consumer transcripts, the tag-move refusal, the overlapping-run and interrupted-run logs, and the revocation transcript.
- The owning decision records are final, including the release-manifest and revocation-marker schemas registered before implementation, the tag rule, the winner rule and the durable-record store and retention period; the implementation record, contributor guidance (including the explicit list of capabilities excluded from v1), and the milestone document's source-lane column match what shipped.

---

## Cross-epic

- **Sizes and order:** M1 XS → M2 M → M3 S → M4 S. M2 is the only epic whose size rests on assumptions (amd64
  only; relaxed builder profile); if either tightens, it drifts toward L and should be re-sized before commitment.
- **M2 core:** the OS image, the operator tooling package, and the AICR package with exact output membership. Everything
  else in M2 is covered by the pre-authorized fallbacks.
- **Never cut:** the independent reviews and the decision-record and evidence-log work.
- **Checkpoints:** Thu Sep 10 decides the M2 shape (wrapped OS build or QCOW2 mirror; built or mirrored NKE
  images; spire-agent placement; which NKE images are outside the OS image). Tue Sep 16 is the M1 stop-loss; if
  M1 is not green on GitHub, the M2 fallbacks are taken immediately.
- **External dependencies that decide the outcome:** NKE source reachable from GitHub-hosted runners through
  private mirrors with vendored inputs; the NKE OS build runnable on a hosted amd64 runner from a single pinned
  entrypoint; a GitHub organization, registry namespace, OIDC identities, and a GitHub App for the pin bot;
  registry-side tag immutability on the release repository or a namespace whose only push credential is the
  release workflow's; product-security approval of public Sigstore before NKE bytes are signed.

---

## Posting to Jira

- **Generated artifacts.** `docs/jira/` holds the dry-run preview, the per-epic descriptions in Jira wiki
  markup, the request payloads and a posting script that prints every command unless given `--post`. Field
  names and allowed values were validated against project NKX on 2026-09-09.
- **One Epic per section above.** The field table maps directly: *Epic name* → Epic Name (required in NKX),
  *Summary* → Summary, *Priority / Rank* → Priority using NKX's scheme (Highest → `P0 - Must have`, High →
  `P1 - Should have`; rank in the backlog by the number), *Components / Labels* → Components and Labels, *Due
  date* → Due date, *Depends on* / *Blocks* → issue links of type "Blocks", *Related* → issue link of type
  "Relates". *T-shirt size*, *Eng PIC / Product PIC* and *Linear status* have no field on the NKX Epic screen
  and are carried in the description under *Planning metadata*; new epics start in Backlog, unassigned.
- **Description** is everything from **Goal** through **Definition of done** for that epic, converted to wiki
  markup. Keep the acceptance-criteria numbering; child stories reference criteria by epic and number (for
  example "M2 AC 3").
- **Labels on every epic:** `nke_nkd`, `nkd-v1`, plus the milestone label listed in the table.
- **Cross-epic** and **Facts** belong in the description of the parent initiative (NKX-14794) or a pinned
  comment there, not repeated in each epic.
- **Do not create child stories from the archived story draft**; it predates the amd64-only, OS-image and
  six-object decisions. Derive stories from the *Delivers* bullets and acceptance criteria of this document.
