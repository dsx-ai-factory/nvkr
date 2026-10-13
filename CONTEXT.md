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
_Avoid_: Delivery profile, component catalog, build identity

**Run**:
One execution attempt against frozen inputs and required outputs, recording progress, observed artifacts, evidence, and failures.
_Avoid_: Plan, release, proof of qualification

**Artifact**:
Immutable bytes produced or acquired for delivery, such as an executable, image, chart, or packaged collection.
_Avoid_: Producer, expected output, release

**Release**:
An immutable selection of artifacts and evidence accessible through one pinned reference, with an explicit evidence lane. Acceptance depends on the consumer's Verification Policy.
Metadata inspection can expose checked artifact/evidence membership without establishing payload integrity,
signature or policy acceptance.
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
Its workflow, shared composite templates and generated invocation data share one ownership manifest and
byte-exact drift gate. Actions expose fixed semantic interfaces for Plan operations; grouped work repeats those
interfaces without generating group-size or output-count variants. Named invocation parameters are recomputed
from the admitted Plan at runtime. Neither scripts supplied as data nor separately authored lifecycle inputs are admitted.
The workflow can remain byte-identical across routine pin updates when scheduling, labels/interfaces and
visible policy stay unchanged. Stable provider scheduling keys do not replace exact executable Plan IDs;
the complete bundle still requires regeneration and checking.
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
documents report `nvkr:diskInspection: not-performed`; this supplies no installed inventory. M2 accepts producer
provenance with its remaining delivery gates; M3 requires strict inspection and material reconciliation.
Historical signed `deferred-m3` documents retain their original bytes and uninspected scope.
_Avoid_: Milestone verdict, declaration-only inventory, inferred inspection

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
