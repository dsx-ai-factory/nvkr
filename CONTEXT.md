# NKD Domain Language

NKD describes product outputs, records their production, and connects immutable artifacts to verifiable evidence.
The public lifecycle is Definition → Run → Release; internal trust vocabulary preserves the distinctions needed to authenticate it.

## Public objects

**Definition**:
Authored product intent declaring named outputs, reusable recipe selections, packaging intent, and optional cluster views.
Packaging intent may bind a Helm chart to a named image output; the resulting digest is content, while its
deployment repository remains transport configuration.
_Avoid_: Factory, FactoryDefinition, cluster definition

**Recipe**:
Reusable instructions for building or acquiring components from pinned inputs, including the outputs they must produce.
Its declared builder mode distinguishes offline compilation from wrapped upstream builds with network access.
_Avoid_: Product definition, run, release

**Environment**:
Separately selected execution settings, publication namespace, credential references, and signing identity policy.
Tool releases are catalog-owned dependencies selected by purpose in the Definition. GitHub action releases
are also catalog dependencies; the workflow adapter consumes their locked commits.
_Avoid_: Delivery profile, recipe, build identity

**Run**:
One execution attempt against frozen inputs and required outputs, recording progress, observed artifacts, evidence, and failures.
_Avoid_: Plan, release, proof of qualification

**Artifact**:
Immutable bytes produced or acquired for delivery, such as an executable, image, chart, or packaged collection.
_Avoid_: Recipe, expected output, release

**Release**:
An immutable selection of artifacts and evidence accessible through one pinned reference, with an explicit evidence lane. Acceptance depends on the consumer's Verification Policy.
_Avoid_: Bundle, mutable tag, clearance

**Verification Policy**:
Consumer-owned requirements defining the acceptance scope, identities, evidence, and lifecycle state required for a Release. Acceptance of build evidence does not establish full release qualification or clearance.
_Avoid_: Signing configuration, verification result, producer assertion

## Definition and production relationships

**Named output**:
A Definition's declaration of selected components and the artifact format they must produce.
_Avoid_: Observed artifact, delivery route

**Disk-image output**:
A named output containing exactly one component selected by an `os` Recipe. The disk format and encryption
metadata describe the delivered bytes independently of the builder that produces them.
_Avoid_: Container image, builder type, running node

**Aggregate compatibility OS Recipe**:
The explicit temporary exception in which the current NKE OS Recipe produces one baked disk containing several
stack layers. Embedded components retain their own identities and evidence relationships while the target model
separates them into independently realized Recipe layers.
_Avoid_: Ordinary one-layer Recipe, qualified release evidence

**Cluster view**:
A named composition inside a Definition that maps recipe components to node roles and cluster-level requirements; its realization binds those selections to exact Release artifacts.
Members reference named outputs. Node roles and cluster type belong to this view, not to recipe selections.
_Avoid_: Separate ClusterDefinition, running cluster, provisioning plan

**Required output slot**:
A frozen requirement established from resolved intent before execution and later matched to observed artifact bytes.
_Avoid_: Successful output, observed inventory

**Resolved Recipe**:
The exact logical build or acquisition definition produced by resolving reusable recipe inputs and pins.
_Avoid_: Second authored recipe, artifact, release

**Recipe Digest**:
The typed identity of a Resolved Recipe's transport-independent logical graph.
_Avoid_: Artifact digest, graph-file hash

**Resolution Frame**:
Concrete values for recipe-selection axes, such as Kubernetes version, OS, and architecture.
_Avoid_: Node role, cluster type

**Role selection**:
The subset of a recipe's components selected for a node role such as control-plane, worker, or GPU worker.
_Avoid_: Node SKU, distinct recipe, machine image

**Execution plan**:
The generated closure of exact inputs, required outputs, environment settings, and executable dependencies for a Run.
_Avoid_: Authored definition, execution observation

**Generated execution bundle**:
The exact generated files and commit manifest that carry an execution plan to a selected execution engine.
_Avoid_: Release, binary bundle, editable workflow scaffold

**Binary bundle**:
An Artifact format containing exact executable files and their installation metadata.
_Avoid_: Release, arbitrary archive

**Projection**:
An engine-specific interpretation of a cluster view containing provider and operational details owned by the downstream engine.
_Avoid_: NKD Definition, Release, running cluster

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

**Inventory**:
The internal qualified-lane declaration of required component/role membership, exact artifact identities, and compatibility requirements.
_Avoid_: List of successful outputs, component catalog

**ReleaseSet**:
The internal qualified-lane atomic aggregate of exact release members bound to an Inventory.
_Avoid_: Public Release synonym, recipe list, binary bundle

**Reproduction, compatibility, and conformance evidence**:
Distinct independently authenticated release claims covering exact reproduction, compatibility requirements, and runtime behavior for their bound subjects.
_Avoid_: Generic pass result, cluster qualification

**Clearance**:
Authenticated authorization for one exact qualified ReleaseSet after its required evidence, policy, and lifecycle checks pass.
_Avoid_: Release reference, publication root, approval flag

**Publication root**:
The internal digest-pinned OCI entry point through which a consumer locates release content and its evidence closure.
One release index owns deliverable names/digests and stage-bound SBOM membership. Each document appears once
inside a signed attestation bundle in its owning artifact repository;
per-recipe indexes remain internal handoffs. The root signature covers these bytes without making SBOMs new
authored lifecycle objects or qualified evidence receipts. Run completion remains a separate claim.
_Avoid_: Mutable tag, clearance, separately authored product object

**Lifecycle status**:
Authenticated acceptance or withdrawal state evaluated with the lane's required freshness and time authority.
_Avoid_: Build status, local clock, mutable release contents

**Cluster qualification**:
A separately authenticated claim about the behavior of an exact cluster composition and its realized release artifacts under a named qualification policy.
_Avoid_: Release conformance, v1 recipe qualification, clearance

## Catalog language

**Component catalog**:
Reusable component, source, recipe, and pin declarations from which Definitions select their inputs.
_Avoid_: Inventory, registry destination

**Recipe layer**:
An authored base, overlay, or mixin contributing component selections and constraints to a reusable recipe.
_Avoid_: Partial release, component catalog overlay

**Component catalog overlay**:
An ordered, predecessor-bound catalog change replacing a complete component producer.
_Avoid_: Recipe layer, role selection

**Execution requirements**:
A deterministic per-step request for minimum CPU, memory, and ephemeral storage, eligible runner-host
platforms, typed host capabilities, and an executor wall-time budget. It is carried in the full IR and Plan,
does not enter Recipe identity, and is not proof that a runner satisfies it.
_Avoid_: Runner observation, target/toolchain platform, build network, builder-specific settings, output size

**Execution class**:
An Environment declaration binding one named route to host platform, allocatable resources, exposed capabilities,
and a timeout ceiling. It is a deterministic planning input, not proof about the host eventually selected.
_Avoid_: Runtime observation, target/toolchain platform, provider attestation

**Execution binding**:
The compiler-selected exact execution class and resolved preferred-capability choice for one requested Plan node.
Plan validation recomputes the selection from the Environment inventory; the binding remains outside Recipe identity.
_Avoid_: Runtime proof, authored requirement, mutable runner label

**VM-image producer form**:
The catalog-selected invocation contract within a VM-image component: either an exact command argument vector or
a direct-Packer project, build name, and public variable bindings. Material bindings are shared authoring data,
but only direct Packer currently names an explicit manifest handoff; command/Make lowering remains unavailable
until it gains a typed handoff instead of an inferred argument or environment variable.
_Avoid_: Builder type, output format, evidence lane

**Build-material manifest**:
A deterministic description of one producer's declared component inputs: binding name, observed component name,
output kind, exact artifact digest, and a slash-relative path beneath a private staging root. It is not an
authenticated observation or a host-path authority.
_Avoid_: Node-SKU staging manifest, producer receipt, absolute staging path

**VM-image builder**:
A from-source builder capability for a disk-image output. The catalog binds one VM-image producer form and its
artifact path. Generic build requirements use the shared execution contract; typed VM lowering and execution
remain separately admitted capabilities.
_Avoid_: Disk format, public output format, virtual machine artifact
