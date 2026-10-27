# NVKR Domain Language

NVKR describes product outputs, records their production, and connects immutable artifacts to verifiable evidence.
The public lifecycle is Definition → Run → Release. Its six public objects are listed below; internal trust
vocabulary preserves the distinctions needed to authenticate them.

## Public objects

**Definition**:
Authored product intent declaring named outputs, their exact catalog producers, target platforms and packaging
intent. An output directly selects components. Packaging intent may bind catalog-declared Helm image slots to named image outputs; the resulting digest is content, while its
deployment repository remains transport configuration. Catalog-owned digest representation
may retain the full OCI digest or supply SHA-256 hex to a template that owns the algorithm prefix;
this does not change the authenticated image identity or rewrite templates.
_Avoid_: Factory, FactoryDefinition, cluster definition

**Environment**:
Separately selected execution settings, publication namespace, credential references, and signing identity policy.
Native hosted platform routes are explicit execution settings; they do not change logical Build identity
or prove provider allocation. Tool releases are catalog-owned dependencies selected by purpose in the Definition. GitHub action releases
are also catalog dependencies; the workflow adapter consumes their locked commits.
An explicit GitHub development setting selects branch dispatch. Signed development uses the same admitted
native disk and pipeline-attested stages with separate signer policy, credentials and disposable run-attempt
registry namespaces. Cleanup admits only internal-visibility packages in those namespaces.
Unsigned development remains static-unqualified. Neither setting grants production
acceptance, provider access or qualified evidence.
_Avoid_: Delivery profile, component catalog, build identity

**Run**:
One execution attempt against frozen inputs and required outputs, recording progress, observed artifacts, evidence, and failures.
Live progress and file-recorded inspection are descriptive views. Producer completion,
activity and terminal presentation do not establish retained output membership,
release verification or runtime health; the owning result and error remain separate.
_Avoid_: Plan, release, proof of qualification

**Artifact**:
Immutable bytes produced or acquired for delivery, such as an executable, image, chart, or packaged collection.
_Avoid_: Producer, expected output, release

**Release**:
An immutable selection of artifacts and evidence accessible through one pinned reference, with an explicit evidence lane. Acceptance depends on the consumer's Verification Policy.
Metadata inspection can expose checked artifact/evidence membership without establishing payload integrity,
signature or policy acceptance.
Diagnostic output pulling additionally checks one selected output's complete payload closure and byte integrity
without establishing signature or policy acceptance.
A release description is a versioned, detached metadata view derived solely from a pinned publication;
it is not another lifecycle object or a verified Release. It retains every named artifact, platform and
bound source/evidence record, including aggregate image-index children. Source repository/commit belongs
to its exact evidence subject rather than one release-wide revision; a normalized component version is
not a Git tag. Consumers own deployment-role mapping, source compatibility and installation completeness.
Export needs no separate Plan, mapping or producer workspace. Publisher-declared qualification is distinct
from the description's metadata inspection scope, and the description adds no canonical identity.
_Avoid_: Bundle, mutable tag, clearance

**Verification Policy**:
Consumer-owned requirements defining the acceptance scope, identities, evidence, and lifecycle state required for a Release. Acceptance of build evidence does not establish full release qualification or clearance.
_Avoid_: Signing configuration, verification result, producer assertion

## Definition and production relationships

**Named output**:
A Definition's declaration of exact selected producers, concrete target platform or explicit aggregate image
platform set, and the artifact format they must produce. Each named output is an independent required use, even when two outputs share a Build Digest.
_Avoid_: Observed artifact, delivery route

**Disk-image output**:
A named output containing exactly one disk-producing catalog root plus every explicitly selected component in
that root's material closure. The disk format and encryption metadata describe the delivered bytes independently
of the builder that produces them.
An explicitly public bootstrap FDE disk can be retained diagnostically or published with build-provenance
evidence. Neither grants inspected-content, reproduction or deployed confidentiality claims.
TPM keyslot enrollment does not rotate the volume key shared by clones of that disk.
_Avoid_: Container image, builder type, running node

**Aggregate OS producer**:
A catalog producer that emits one baked disk containing several stack layers. Embedded components retain
their own material identities and evidence relationships; one disk does not establish independent production
or verification of those layers. Reproduced or digest-verified inputs do not establish exact-byte reproduction
for the aggregate disk. Compiler-only disk lowering grants no execution or evidence capability.
_Avoid_: Independent layer outputs, qualified release evidence

**Required output slot**:
A frozen requirement established from resolved intent before execution and later matched to observed artifact
bytes for the exact named output and target. A successful sibling cannot satisfy it through digest equality.
_Avoid_: Successful output, observed inventory

**Generated build graph**:
The compiler-derived build or acquisition closure for one output's exact producer selection, target and pins.
It is validated as a complete executable graph before its logical identity is projected.
_Avoid_: Authored selection group, artifact, release

**Build Digest**:
The typed identity of a generated build graph's transport-independent logical content under `nvkr.build`.
Output names and execution settings are bound by their
owning records and excluded from this identity. Equal Build Digests do not merge required output uses or their evidence.
`nvkr.recipe` is reserved and never minted. Historical `nkd.recipe` signed bytes are not renamed or converted.
_Avoid_: Artifact digest, graph-file hash

**Target platform**:
The concrete platform for an output's bytes, currently `linux/amd64` or `linux/arm64`. Catalog support and pin
selection must admit it explicitly, independently of the host on which a producer executes.
_Avoid_: Host platform, query criterion

**Execution plan**:
The complete generated closure of admitted Definition and Environment values, independently required outputs,
packaging, routes and executable dependencies for a Run. Producer operations reference the single executable
build instruction; observations are recorded separately. There is no authored compiler-handoff document.
Readable views, linked graph exports and semantic comparisons describe the validated Plan; they do not replace
its canonical stored bytes or grant execution or evidence authority.
_Avoid_: Authored definition, execution observation

**Generated execution bundle**:
The exact generated files and commit manifest that carry an execution plan to a selected execution engine.
The maintained hosted engine is GitHub; no separate qualified renderer or authored transport-plan model is retained.
Its entry workflow, shared composite templates and generated invocation data share one ownership manifest and
byte-exact drift gate. Actions expose fixed semantic interfaces for Plan operations; grouped work repeats those
interfaces without generating group-size or output-count variants. Named invocation parameters are recomputed
from the admitted Plan at runtime. Identical admitted step sequences can share descriptive YAML aliases;
expansion preserves independent job parameters, policies and exact result membership. Every job retains its
ID, runner, timeout, permissions and condition in the entry workflow. Redundant scheduling edges may be
removed while preserving the prerequisite closure.
Large finalizers collect the exact original per-job results through fixed, bounded diagnostic buckets;
collector success never substitutes for producer success. Neither scripts supplied as data nor separately
authored lifecycle inputs are admitted.
Production and development partial retries may transport retained same-Run artifacts from earlier attempts while preserving
their original source bindings. Same-name candidates are bounded and authenticated before selecting the
newest source attempt, with ties rejected. Transport admission does not replace consumer evidence or completeness checks;
explicit cross-Run transfers remain exact-attempt. Qualification independently checks original source attempts.
The workflow can remain byte-identical across routine pin updates when scheduling, labels/interfaces and
visible policy and required inline action pins stay unchanged. Stable provider scheduling keys do not replace exact executable Plan IDs;
the complete bundle still requires regeneration and checking.
Edit locality additionally preserves existing output jobs when an independent output is added. Ordinary
grouping can append offline observation, SBOM and packaging to a producer when that one stage owns all
inputs and runtime/target constraints agree; otherwise one BuildTarget's observation and SBOM may pair.
Other nodes keep separate jobs. Signing, publication and remote verification retain their boundaries. A shared pinned root tool may be an additional prerequisite for the pair,
which waits for its acquisition and restoration before observation. Required aggregate accounting changes,
and an identical YAML anchor may gain an earlier owner without changing expanded execution. The current Plan determines this projection; previous
generated files do not supply scheduling state.
Payload carriers use an unlinked private/internal GHCR package with separate storage credentials.
Actions stores their JSON locators and separately validated metadata records with exact schemas and file
inventories. Metadata delivery uses workflow identity, preserves exact bytes and grants no release authority.
Unknown/mixed contents remain private; control layouts cannot carry output payload blobs.
Carrier and GHCR publication admission use the automatic repository token for source metadata separately
from package credentials; that token is not added to producer execution.
Native build-only disk bundles can retain raw outputs and a static-unqualified Run without producing a Release.
Development and production use separate fixed bundle layouts selected by their Environment, with the same
compiler and independent ownership manifests. A development bundle is not a release authorization.
_Avoid_: Release, binary bundle, editable workflow scaffold

**Catalog maintenance candidate**:
A review-only lock and regenerated execution-bundle change under explicit source-selection constraints.
Static checks and optional complete local diagnostic execution precede candidate handoff; neither is release
acceptance. Scheduling and source-role alignment are operational policy, not additional lifecycle objects.
_Avoid_: Release, qualified reproduction, automatic promotion

**Runnable OCI image index**:
An Artifact representation: a standard OCI index selecting exact native child image manifests by explicit platform. Its blob digest is
separate from each child's identity and from logical Build identity. Delivery can hold and verify its byte
closure. One named output can freeze an explicit platform set, an aggregate logical Build Digest and each
child's native build and evidence bindings (ADR-11/12/20). The aggregate logical Build identity remains distinct
from the runnable index digest.
_Avoid_: Release publication root, native image manifest, qualification receipt

**Binary bundle**:
An Artifact format containing exact executable files and their installation metadata.
_Avoid_: Release, arbitrary archive

## Evidence and authority

**External test producer**:
An external implementation or NVKR-supplied diagnostic adapter providing original reports and descriptive
metadata, distinct from a Component producer that builds delivery artifacts. Lane: none (descriptive); [ADR-16 §6](docs/adr/0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
owns its execution and authority boundary.
_Avoid_: Component producer, qualified verifier, conformance receipt

**Validation profile**:
Explicit diagnostic configuration selecting a product publication and named check instances with
separate suite/harness protocols and configuration. Tools are independently selected exact members
of authenticated catalog-produced executable outputs. Its source and tools policy paths reference the existing
qualification-policy format for artifact authentication; the profile does not replace those policies.
Top-level `validate` runs diagnostics, whereas `definition validate` checks static authored-input shape.
Outputs remain descriptive and cannot authorize qualification or promotion.
_Avoid_: Recipe, test dispatcher framework, verification policy

**Validator catalog (proposed)**:
An explicit index of reusable diagnostic validator entries, organized by project, artifact and suite.
Proposed [ADR-24](docs/adr/0024-imported-validator-catalog.md) separates entries from profiles selecting named
instances and artifact-role bindings. The provisional CLI slice implements strict discovery, local-plan
admission and frozen dispatch for the existing process-contract and mixed CLI suites. Shared schemas remain
provisional; published adapters, controller/chart checks and broader migration are deferred. Existing legacy
validation routes retain their separate admission and capture contracts.
_Avoid_: Producer catalog, upstream test implementation, executable plugin, qualification policy

**Release authenticity**:
Acceptance of a Release's signed membership, each named output's signatures and production provenance, and required inventories under consumer policy, without checking artifact payload bytes. It establishes neither payload integrity nor release qualification.
_Avoid_: Payload verification, deployment readiness, full release acceptance

**Evidence lane**:
The contract governing an artifact's evidence: pipeline-attested production or qualified reproduction and release authorization. Static or dry-run output is unqualified.
_Avoid_: Build category, signing mode, release status

**Authenticated receipt**:
Proof issued by an owning verifier after authenticating exact bytes, subjects, identities, and policy; a serialized description cannot recreate it.
_Avoid_: Parsed document, caller-set verified flag, digest string

**Reproduction expectations**:
Publisher-owned signed expected artifact identities against which an independent reproduction is compared; the legacy wire record is the ValidationVector.
_Avoid_: Reproduction observation, generic validation result

**Producer evidence scope**:
The evidence-supported statement for one exact producer output. A from-source output may carry independently
demonstrated exact-byte reproduction evidence; a byte-preserving acquisition may be checked against its pinned
digest without making an upstream build claim; and a non-reproducible producer may be rebuilt with authenticated
dependencies and have its resulting contents inspected. The last case is content validation, not a `Reproduced`
verdict. These are claim scopes, not a new public object or caller-selected wire enum.
_Avoid_: Source lane, aggregate release verdict, caller assertion

**Aggregate evidence floor**:
The rule that an aggregate artifact cannot inherit a stronger claim from its inputs. Component evidence remains
individually inspectable, but a wrapped or merely content-validated root keeps the aggregate output in the
pipeline-attested lane. Exact delivery identity, material equality, SBOM agreement, and content validation do
not substitute for an independent exact-byte reproduction of the aggregate claim subject.
_Avoid_: Transitive reproduction, majority evidence, semantic reproduction

**Disk inspection scope**:
The signed statement of whether the exact disk's installed contents were inspected. Producer-provenance
documents report `nvkr:diskInspection: not-performed`; this supplies no installed inventory. M2 and the M3 MVP
retain producer-only disk evidence. Strict inspection and material reconciliation are deferred hardening.
Embedded-component declarations are optional: omission is not applicable, while any authored declaration must
match its referenced pinned upstream metadata. That comparison does not establish effective customized or
installed composition; exact source, patch, configuration and material bindings remain required.
Historical signed `deferred-m3` documents retain their original bytes and uninspected scope.
_Avoid_: Milestone verdict, declaration-only inventory, inferred inspection

**Build/evidence qualification**:
Checks for the complete selected artifact set against its producing Run, original attempt identities, commit and inputs,
including schema, pin, source, signature/provenance and applicable delivered-tool smoke results. Every output
has a pass or fail verdict. Independent qualification evidence is issued only after all required results pass;
the authenticated check receipt itself grants no signing authority. Artifact checking selects one exact
successful completion attempt of a Run and an independent consumer policy. Production and development outputs may retain authenticated earlier
attempts of that same workflow run when their exact inputs and original evidence remain bound; retrying does
not relabel production. Development publications retain each original attempt namespace within the same source run.
The generated handoff carries original inputs and evidence for
recompilation and verification; it is descriptive transport, not a new authored lifecycle object or trust policy.
Hosted checks verify all payloads sequentially and release non-executable scratch after each output. The check
receipt retains verified controls, evidence, exact executable bundle bytes and the frozen Plan until closure revokes
copies, cancels and joins registered operations, then releases held bytes. It cannot export discarded payloads.
This lifetime grants no smoke-execution authority.
Native workers exercise every selected delivered tool with fixed version checks in isolated containers and
sign exact member observations. The final qualifier authenticates complete native coverage under the same
independent policy before signing. A successful worker from an earlier attempt of that same qualification
run may be reused with its original authenticated invocation, unchanged inputs and policy; a later failed
worker prevents fallback to its earlier success. Worker reports remain generated transport;
they are not caller-authored pass records or release authorization.
An exact runtime-assets archive member may be outside version smoke only when the Plan declares a
source-built tarball and the delivered member is non-executable. Its artifact checks remain required;
the rule does not exempt sibling tools or classify the archive's internal contents.
The local qualification rehearsal specified in ADR-16 exercises the same
build/evidence checks with explicit local origins and distinct local signing roles. Its results retain
the unqualified local lane and cannot replace authenticated hosted acceptance or grant release authority.
`run qualify-local` owns the retained-attempt rehearsal through independent registry read-back; its
canonical result describes that local check only. An explicitly configured demo gates deployment inputs
on successful qualification of its built attempt. The demo’s explicit `qualifyLocal` opt-in owns preparation and registry shutdown around qualification.
Explicit demo setup can also generate disposable local keys
and a Plan-bound policy from the maintained complete demo scope; this is local operator trust, not
independent organizational approval. Setup files grant no verification receipt. Reconciliation records actual demo-tool execution,
consumer verification, negative cases and the scope of deployment-gate testing separately.
External suites outside the MVP are reported as not evaluated, never passed; a required suite needs authenticated
subject-bound evidence.
_Avoid_: Qualified reproduction, disk-content inspection, cluster conformance, build success

**Release manifest**:
The pipeline-attested document naming one Run's complete qualified artifact set: the Definition and exact
inputs, originating Run, each member's exact digest, source lane and verified build provenance, and the exact
qualification evidence. It is issued only after that evidence is authenticated on every member under the
independently selected qualification identity, and is attested to the publication root under that identity.
A manifest attachment alone is not a published release; tags, channels and revocation are separate.
_Avoid_: Release clearance, ReleaseSet, qualified release, tag

**Inventory**:
The internal qualified-lane declaration of required component membership, exact artifact identities, and compatibility requirements.
_Avoid_: List of successful outputs, component catalog

**ReleaseSet**:
The internal qualified-lane atomic aggregate of exact release members bound to an Inventory.
_Avoid_: Public Release synonym, output list, binary bundle

**Reproduction, compatibility, and conformance evidence**:
Distinct independently authenticated release claims covering exact reproduction, compatibility requirements, and runtime behavior for their bound subjects.
Qualified release-gate acceptance is deferred; no retained clearance implementation consumes these claims.
_Avoid_: Generic pass result, cluster qualification

**Downstream test coordinator**:
A separately authored repository CI adapter outside the NVKR product lifecycle
that joins exact project producer Runs, consumes authenticated publication bytes
and invokes NVKE's suite in a separate job. NVKE owns guest/cluster provisioning,
assertions and teardown; the parent harness owns host admission, outer leases,
disk service and network-control restoration. Its unsigned reports are diagnostics,
not qualified conformance receipts or a seventh public lifecycle object (ADR-16/18).
_Avoid_: Nightly qualification, qualified runtime, product fleet operator

**Clearance**:
Authenticated authorization for one exact qualified ReleaseSet after its required evidence, policy, and lifecycle checks pass.
This is a deferred contract; the unused authorization implementation has been removed.
_Avoid_: Release reference, publication root, approval flag

**Publication root**:
The internal digest-pinned OCI entry point through which a consumer locates release content and its evidence closure.
One release index owns deliverable names/digests and stage-bound SBOM membership. Each document appears once
inside a signed attestation bundle in its owning artifact repository;
per-build indexes remain internal handoffs. The root signature covers these bytes without making SBOMs new
authored lifecycle objects or qualified evidence receipts. Run completion remains a separate claim.
_Avoid_: Mutable tag, clearance, separately authored product object

**Lifecycle status**:
Authenticated acceptance or withdrawal state evaluated with the lane's required freshness and time authority.
_Avoid_: Build status, local clock, mutable release contents

## Catalog language

**Component catalog**:
Reusable producer, source, tool, and pin declarations from which Definitions select their inputs.
_Avoid_: Inventory, registry destination

**Component producer**:
A catalog declaration describing how to build or acquire a named component from pinned inputs and which target
platforms it supports. It carries build or acquisition instructions, never a deployment placement taxonomy.
_Avoid_: Named output, artifact, authored build group

**Component catalog overlay**:
An ordered, predecessor-bound catalog change replacing a complete component producer or performing an exact
toolchain-pin substitution for an explicit sorted component set and host architecture. Substitution fails if
any selected producer does not carry the expected old pin; it is not a partial producer merge.
_Avoid_: Output selection group, mixin, deployment topology

**Derived pin set**:
An explicit catalog mapping from one pinned upstream declaration to source or acquired-artifact versions.
Re-pinning the umbrella updates its members atomically. Loading re-derives their versions from the pinned tree;
unknown or missing members in selected collections fail closed. This checks declared version agreement.
Local chart versions come from pinned source metadata when the deployment declaration omits them.
Published package versions remain explicit producer inputs; neither is a release-acceptance claim.
_Avoid_: Dynamic output selection, qualification record, independently bumpable member

**Execution requirements**:
A deterministic per-step request for minimum CPU, memory, and ephemeral storage, eligible runner-host
platforms, typed host capabilities, and an executor wall-time budget. It is carried once in the full IR embedded
in the Plan; scheduler operations reference that instruction. It does not enter Build identity, and is not
proof that a runner satisfies it.
_Avoid_: Runner observation, target/toolchain platform, build network, builder-specific settings, output size

**Execution class**:
An Environment declaration binding one named route to host platform, allocatable resources, exposed capabilities,
and a timeout ceiling. It is a deterministic planning input, not proof about the host eventually selected.
_Avoid_: Runtime observation, target/toolchain platform, provider attestation

**Execution binding**:
The compiler-selected exact execution class and resolved preferred-capability choice for one requested Plan node.
Plan validation recomputes the selection from the Environment inventory; the binding remains outside Build identity.
_Avoid_: Runtime proof, authored requirement, mutable runner label

**Local host observation**:
Point-in-time diagnostic Linux facts about host platform, cgroup-v2-capped CPU and memory, available workspace
capacity, and KVM exposure. Observation is relative to the executor's already-open workspace-directory
capability; the observer lifetime-protects its duplicate, checks write and traversal access, and closes every
retained handle before returning. It does not accept execution requirements or a selected binding, authenticate
Environment membership, own an execution deadline, reserve resources, or authorize work.
_Avoid_: Execution admission, runtime proof, execution class, provider attestation, resource reservation

**Local VM admission**:
An opaque, single-use, same-process capability binding the exact current Plan bytes and one selected VM build,
its execution request and binding, cgroup-capped Linux host observations, and retained workspace/KVM handles.
Capacity is re-observed inside the authored callback deadline immediately before work. It is current-availability
authority reused by the local VM adapter and the explicitly operator-trusted native ARC disk route, not provider allocation, resource reservation, qualification evidence,
or proof that a subprocess consumed the held KVM descriptor.
_Avoid_: Serialized receipt, hosted-runner attestation, resource reservation, KVM-consumption evidence

**VM-image producer form**:
The catalog-selected invocation contract within a VM-image component: either an exact command argument vector or
a direct-Packer project, build name, and public variable bindings. Material bindings are shared authoring data,
but only direct Packer currently names an explicit manifest handoff. A command producer without materials lowers;
a material-bearing command/Make producer remains unavailable until it gains a typed handoff instead of an
inferred argument or environment variable.
_Avoid_: Builder type, output format, evidence lane

**Build-material manifest**:
A deterministic description of one producer's declared component inputs: binding name, observed component name,
output kind, selected architecture, exact artifact digest, and a slash-relative path beneath a private staging
root. A producer-owned projection may replace only the envelope schema and per-binding kind vocabulary; it does
not replace the authenticated input fields. The manifest is not an authenticated observation or a host-path
authority.
_Avoid_: Node-SKU staging manifest, producer receipt, absolute staging path

**Guest package capture**:
A source-reported record of package archives retained during a guest installation event, distinct from a
catalog component acquisition or build-material input. Checking its held archive proves declared member-byte
correspondence, not acquisition origin, installed content, a complete closure or live-Run authority.
The optional producer `acquisitionArchive` selects one output-directory-relative carrier. Collection holds
that carrier under the original Packer artifact owner before cleanup; an independently supplied path cannot
attach capture authority to a disk. The selection is a producer input, not a new public lifecycle object.
_Avoid_: Full disk SBOM, authenticated download trace, build-material manifest

**VM-image builder**:
A from-source builder capability for a disk-image output. The catalog binds one VM-image producer form and its
artifact path. Generic build requirements use the shared execution contract. Typed IR/Plan lowering is admitted;
VM execution remains a separately admitted capability.
_Avoid_: Disk format, public output format, virtual machine artifact
