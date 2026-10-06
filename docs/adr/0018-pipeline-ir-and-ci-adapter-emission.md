# ADR-18 — Pipeline IR & CI-adapter emission (hermetic execution)

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-18 in the consolidated NKD ADR set (added to close the Definition→realized-pipeline gap).
- **Relates to:** ADR-02 (IR), ADR-10 (builder types), ADR-07 (builder profile / runner-digest pin), ADR-04
  (verify), ADR-19 (deployment ownership boundary).
- **Satisfies:** FR-05 (CI portability), FR-06 (hermetic build), SEC-06 (terminal verify), SEC-11 (build
  isolation).

- **Scope:** Sections 1–7 define shared execution requirements and the deferred qualified provider protocol.
  Qualified CI renderers and their TransportPlan implementation are removed; shared local authority primitives
  remain. Section 8 defines the implemented GitHub Run emitter and event policy.

## Context

The IR must become a **concrete, realized pipeline** on whichever maintained execution target an operator runs.
For an exact subject in the qualified lane, each admitted environment must build hermetically and the independent
outputs must compare byte-identically. Pipeline-attested producers instead retain their stated evidence contract;
matching their inputs or content does not turn their output into a reproduction. The open question is what
executes each build node, and how execution semantics stay aligned across environments without a fragile parallel
implementation of rendered strings that must be kept in sync.

## Decision

### 1. One IR, many adapters — topology/transport only

The maintained GitHub adapter and local runner use the same producer IR. GitLab, Tekton and Argo generation
are unavailable; their unused adapters are removed, as are the separate qualified GitHub/GitLab renderers.
The former GitLab-only `runtime.builderImage` schema field has no retained adapter and any nonempty value
fails capability admission. GitHub uses its pinned setup-go action for source bootstrap.
A future provider must satisfy its evidence admission before receiving an implementation. Adapters map **job/step
topology and artifact transport only**, never build semantics. Adding an engine is one thin adapter; adding a
component is one catalog entry and no code. Definition v4 names each output, its exact catalog producers,
and its concrete platform. Compilation derives one internal build target per output; no authored reusable
producer group, recipe selection, criteria matcher, mixin or recipe layer participates. The BuildDigest
identifies the selected executable producer closure and excludes the output name, packaging and transport.
Two named outputs with equal BuildDigest retain independent executions, results and evidence obligations.
Existing Helm image-output dependencies remain explicit packaging edges. Run-plan and artifact-graph v4 contain no cluster-composition nodes or records. Compiler handoff documents
are removed; the complete Plan retains the exact authored Definition and Environment.

The Plan retains its base catalog snapshot and each output's selected catalog snapshot, checked against their
catalog identities before deriving executable tools. The Plan's action pins must exactly match the base
snapshot. Each output's complete executable build graph is recompiled from its selected snapshot, with only
Environment-authorized toolchain registry relocation and deterministic execution binding applied. Admission
compares canonical full-graph bytes; the transport-independent Build Digest cannot authenticate repository,
artifact or flake locators, image registry prefixes, or execution metadata.
Compilation and validation also share one complete artifact graph derivation.
Validation compares every node and artifact, including exact input/output sets, transport
URL, format, archive and executable digests, executable member and output path. Producer source and receipt
edges, package membership and terminal-observation gates, and publication/signing routes must match the
admitted inputs. Missing, extra or substituted operations, artifacts or edges fail before execution or emission.
Only the local runner may relocate admitted artifact paths into its selected workspace; a serialized path does
not grant relocation authority.

#### Required engine capability and transport contract

A future qualified adapter must admit an explicit capability contract before lowering. At minimum the engine or its configuration
must provide:

1. a digest-pinned toolchain container launched by the admitted helper/runtime per execution node;
2. an enforceable network-deny mode for build nodes — not merely a convention that the command will avoid the
   network — with fetch and build split into different jobs or pods when isolation cannot change within a job;
3. read-only logical inputs, a private output directory, a fixed environment allowlist, and no ambient secret or
   service-account credentials in build/verify nodes;
4. content-verified artifact/workspace transport between jobs and pods;
5. an always-run finalizer capable of receiving all successful node reports and detecting missing or failed
   nodes; and
6. for a from-source graph, a typed provider execution-attestation adapter that measures the runner, the
   provider-selected container/toolchain identity, sandbox, and any realized closure after materialization, and
   produces ADR-07's stage-two receipt before artifact work.


The deferred qualified protocol requires a canonical transport plan. It must bind each logical item to its producer, exhaustive
consumer set, allow-listed workspace paths, media type, and authentication method: verified runner binary,
canonical node bundle and bundle manifest, fetched input blobs and bindings, component outputs, per-node result
receipts, ValidationVector, and terminal verification result. An implicit shared path is not a transport
contract.

Such a plan **cannot claim the future content digest** of a runner, source closure, component output, or result
that does not exist at emission time; such a digest is unknowable then. The runtime producer emits a content
receipt after creating the item, transport carries the item plus receipt, and every consumer re-hashes the
received bytes before use. Exact digests of already-existing static bundle members stay in the execution-bundle
identity and must never be fabricated as future transport-plan values. The former unused TransportPlan and
qualified bundle writer are removed. Maintained Runs use their complete Plan and generated ownership manifest.

Emission validates both the adapter's declared capabilities and the requested policy. If the target engine cannot
express offline isolation, immutable container selection, required transport, or unconditional finalization, the
emitter fails with the coded `engine-capability` error. A from-source emitter also fails with that code when no
provider execution-attestation adapter is configured; runner, toolchain, and sandbox declarations are not
observations. The emitter MUST NOT emit a best-effort pipeline or silently weaken policy. Capabilities supplied by
external infrastructure — a Kubernetes NetworkPolicy, for example — are named as immutable configuration inputs
and checked by an emitted preflight rather than assumed.

The maintained renderer is context-first and returns one finite bundle value. Publication to a terminal,
file, repository, or provider is a separate owning boundary. Writer-callback emission is rejected before graph
inspection or output, so a blocking caller-controlled writer cannot defeat cancellation.

### 2. A shared, from-source runner owns execution semantics

Build execution is a single **static, from-source runner** (`nkd-exec`, a mode/subset of `nkdctl`) that executes
one IR node (`fetch → build → collect`). Every job uses the same execution code as the local runner, avoiding duplicated build semantics. Reproduction still requires independently checked bytes and execution evidence.
The IR is pinned data. Qualified graph diagnostics execute the exact admitted node bundle; maintained Runs
execute a producer reference from the complete Plan through `factory execute-node`.

### 3. Helper-owned isolation and narrowly admitted native ARC disks

Jobs use generic hosted runners except the explicit native ARC disk policy below. The verified `nkd-exec` helper launches each pinned toolchain container
through the shared `pipeline.Local.ExecNode` launcher used by local and hosted Run operations. Adapters render job topology, dependencies
and declared transport; they do not reproduce isolation as provider-specific build syntax. Fetch/pull nodes
materialize pinned inputs first; hermetic build and verification nodes enforce network denial, read-only inputs,
private output and a fixed credential-free environment. Shared code prevents semantic drift; it does not alone
prove byte identity across hosts.

Landlock/seccomp application and process replacement run on one dedicated locked OS thread. Restrictions are
thread-local before exec; a Go scheduler handoff must not change the thread that enters the build command.
If the transition fails, its goroutine exits without unlocking so the runtime retires the possibly restricted
thread instead of returning it to the scheduler.

Environment selects the runtime and its pins. Rootless podman is the intended default, with docker an explicit
operator-selected alternative. Admit the configured runtime before work; do not silently choose another sandbox
or weaker isolation. Rootless operation still depends on supported user namespaces, UID mapping, storage,
cgroups and the provider's nesting policy. A mounted privileged Docker socket remains an explicit trust cost.
Missing prerequisites fail capability admission. Arbitrary self-hosted labels are not admitted by the native disk route.

The pipeline-attested `githubARC: native-kvm-v1` Environment policy admits only native Linux AMD64 and ARM64
unencrypted direct-Packer QCOW2 outputs with required KVM. Their execution classes select exactly
`arc-amd64-kvm` and `arc-arm64-kvm`. This is an explicit operator-trusted runner/daemon boundary: it does not
claim an authenticated provider allocation, subordinate identity, independent sandbox attestation or qualification.
The operator must provision ephemeral single-job runners and restrict pool access to the reviewed workflow on the default branch and any exact
branches explicitly admitted by `githubDispatchBranches`. The generated disk jobs use the `nkd-arc` protected Environment. Provider configuration and fresh
native interoperability are external acceptance requirements; emitting a label or naming an Environment does
not establish those facts.

The route reuses the exact `githubDispatchBranches` manual-dispatch policy described below. Both
`nkd-arc` and `nkd-release` deployment policies and the signing identity must admit each selected ref.
It permits no arbitrary setup, injected environment, job container or services.
Docker is the initial admitted runtime. Bootstrap, source fetch, publication, signing and finalization remain
on generic hosted runners. Each disk output contracts its acquisition/build, terminal observation, inspection
and packaging operations into one bounded helper invocation against the complete Plan. The original producer
session and held disk survive through packaging; strict mode additionally requires reconciliation. The raw disk has no independent
carrier; its standard OCI wrapper carries its exact bytes. All component evidence and producer-result slots
remain required. Only fixed source-fetch steps receive private-source credentials; ARC disk jobs receive no
source token, registry-write credential or OIDC signing token.

Internal native toolchains beneath the candidate publication prefix's `toolchains/`
repositories use a separate preparation step with `contents: read` and `packages: read`.
Only that step receives the ephemeral workflow token. The helper admits the complete Plan,
native platform and disk host requirements before pulling; it uses a private temporary Docker
configuration and deletes it on success or failure before source or producer execution.
Held runtime observation accepts an already-local exact repository digest, rejects a present
but mismatched record, and pulls only after a failed local lookup. Image/platform/tool-byte
admission remains required. No registry credential is forwarded into producer containers.
The package owner must grant the workflow repository Actions read access separately.

Before toolchain preparation or producer work the helper binds the native host and observes cgroup-capped
CPU/memory, held-workspace capacity and KVM through the existing single-use VM admission. It repeats admission
inside the producer deadline, uses the same guarded Docker device projection, and closes/joins all held owners.
Published native Packer pins remain required. The strict disk-evidence mode additionally requires the full
inspector image and measured executable pins. Explicit M2 `diskEvidence: build-provenance-v1` omits inspector
preparation and execution; it authenticates live producer/material evidence under ADR-13/20 instead.
The whole ARC group has a bounded sum of serial operation budgets, capped at 24 hours; generic hosted jobs
retain their six-hour cap. Current availability is not a capacity reservation. The local runtime observations
remain unqualified descriptions; the complete hosted finalizer, signing and consumer checks own the production claim.

Native preparation is a separate read-only-permission workflow that builds both images and records exact local
image/tool measurements on matching native hosts. Measurements do not supply publication approval or registry
pins. Toolchain publication gates and full generated M2 operational acceptance remain unresolved until separately
recorded; the generation contract must not fill those inputs with placeholders.

Held Linux Podman execution uses a package-owned, initially empty `XDG_CONFIG_HOME`, removed by the same
runtime owner on close. It does not inherit caller Podman configuration or ambient storage-driver overrides;
this is configuration isolation, not a new user-namespace or provider boundary. An explicit typed
`podmanStorageDriver` is supported by the held inspector and Environment-bound direct-Packer admission,
where command arguments and observed driver must agree. Complete-Plan local, GitHub and execute-node
adapters reject nonempty overrides before work because other runner, utility and producer routes do not
yet project that setting. Empty preserves the runtime's existing store selection.

The observed Podman identity includes UID/GID mappings, remote-service status and OCI runtime name in
addition to the existing host, storage, security and version fields. Adding these fields changes the
`nkd.runtime-identity/v1` preimage; this pre-ratification implementation does not translate older identities
or prove compatibility with previously recorded qualification profiles.

The repository-owned NKE OS integration workflow is a narrow validation exception, not generated Run CI and not
a hosted VM adapter. Each push to `main` selects that exact commit for full AMD64 and ARM64 builds, boot smoke,
and short-retention QCOW2 uploads. Manual dispatch from the default-branch workflow remains available for
toolchain-only or full validation of either architecture or both, after an exact main commit or copy-pr-bot
mirror is authenticated. Runs are independent; newer pushes do not cancel earlier attempts. Both routes may
select only the declared `arc-amd64-kvm` or `arc-arm64-kvm` KVM scale sets for full validation. Main pushes use
the NKE pin from the merged catalog and validated Plans; manual full dispatch additionally checks the entered
NKE commit against that pin. The cross-repository source credential must not share a job with candidate code; the
trusted source-fetch job alone receives it. That job verifies the selected commit and tree, makes a fresh
credential-free Git checkout, and uploads it as a one-day internal artifact with an exact SHA-256 passed as a
job output. The build job verifies the archive digest and Git commit/tree before candidate code runs. Candidate
code receives no authority-bearing evidence role. The build job's logs, image observations, QCOW2 digest and boot
result are explicitly unqualified diagnostic output. The [2026-10-01 runs](../evidence/2026-10-01-nke-arc-os-builds.md) exercise both
ARC pools from `nke-recipes`; they do not establish lasting availability or provider isolation. Shared Actions
caches are disabled for the trusted bootstrap and generated helper so candidate cache writes are not restored
into those trusted binaries. Mirror selection checks an open, non-draft same-repository PR head, not a human
approval. `/ok to test` is procedural vetting; branch equality does not authenticate the mirror's writer. The
repository-scoped source secret remains a separate policy gap. This exception grants no pipeline-attested or
qualified claim and changes no generated adapter rule.

A secret-free manual `preflight` mode may execute a branch workflow at its exact dispatched commit to test
the same no-chmod KVM-access step used by full validation on either ARC pool. It has no source checkout on
the KVM runner, private-source fetch, image build or source credential. This branch exception is diagnostic
only; this workflow's selection step requires toolchain and full validation to run from the default branch.
That self-check is not GitHub-enforced authorization: the recorded branch probe reached both ARC KVM pools,
and a different branch-authored workflow can omit the check. The repository-scoped source secret is likewise
not restricted to this workflow. Restricting runner-group access to selected workflows/refs and moving the
secret into a main-restricted environment remain separate provider and repository configuration follow-ups.

Helper delivery can use pinned source bootstrap or a pinned helper image. Delivery identity is not qualified
execution authority: a qualified claim still needs exact helper bytes and the ADR-07 runtime observation.
Prebuilt helper-image qualification stays disabled until that admission contract exists. Runtime/helper changes
belong to Environment/profile configuration; component toolchains remain producer inputs.

### 4. Runner provenance and conditional self-hosting bootstrap

- The exact runner bytes that execute from-source nodes are always measured and verified against the
  builder-profile runner pin.
- A self-hosting execution builds the runner **from pinned source** — its own pinned producer definition, because the tool is not a
  trusted pre-built binary — under a pinned bootstrap toolchain, producing a digest-pinned runner. Its stage-two
  observation records `bootstrapped`, measures that actual bootstrap toolchain, and verifies both identities.
- An execution that uses a preexisting runner records `preexisting`, hashes those exact bytes, and MUST NOT claim
  or require a bootstrap toolchain that did not participate. This prevents a profile declaration from becoming
  fabricated evidence.
- The runner is **output-affecting** (it executes the build), so its digest is a **fail-closed builder-identity attribute in the builder
  profile** (ADR-07), not part of `buildDigest`: a runner bump is an environment change, not a definition
  change.

The profile-free prebuilt artifact lane still executes a runner to acquire bytes, enforce the transport
allow-list, and perform terminal verification. That runner therefore needs authority even though the artifact
does not need a build-environment profile. Before a future qualified provider bundle reads a catalog or graph, its generator must
authenticate the exact canonical runner profile under `RoleBuilderProfile` with explicit signer, issuer,
trusted-root, and verifier pins, then derive its helper digest only from that opaque receipt. The emitted digest
authenticates the interpreter. It does **not** attach the profile to a prebuilt artifact, create a stage-two
build observation, or change the artifact's profile-free identity.

### 5. Hermetic execution

Every qualified build job runs under an **enforced network-deny capability** in its helper-launched pinned container, and
**least-privileged** (ADR-10's rootless-userns for `nix`). Anything needing the network — source fetch,
dependency vendoring, the `nix` locked-closure realize — runs in a **separate, explicit fetch job or pod before**
the offline build and crosses the `TransportPlan` with a runtime producer receipt plus consumer re-hash. Putting
fetch and compile in one network-enabled job and asking the compile command not to connect is nonconforming.
Isolation and the fixed environment/mount contract are identical across all engines and the local runner.

### 6. Lane lowering

- `oci-hermetic` and proposed `nix` build nodes use helper-launched pinned toolchains; unsupported execution adapters fail admission.
- `buildkit-rootless` is the networked wrapped OCI-export mode for generated lifecycle Runs (ADR-10).
  The shared Docker launcher owns its fixed non-root isolation settings and child-process cleanup. Run
  scheduler records reference the exact embedded producer instruction; network, container and platform are
  resolved from that instruction at execution or rendering, never serialized a second time. A future qualified
  pipeline emitter must reject this mode rather than silently render it as a network-denied build.
- `prebuilt` nodes lower to acquire-and-digest-verify.
- `exclusion` emits no node.

`vm-image` and `disk-image` have a compiler-only lowering contract. IR v4 records the disk contract, exactly one
command or Packer producer form, public Packer variables, and typed material bindings backed by exact build or
acquisition dependencies. An optional producer-owned material-manifest projection records its exact schema and
per-binding kind vocabulary in the Packer contract and logical Build identity; it cannot change the authenticated
material bytes, digest, component, architecture or deterministic staging path. The Plan selects an execution
class, uses its host architecture to select the pinned VM
toolchain, and projects the raw disk producer artifact into the named lifecycle output without ordinary binary
packaging. A material-bearing command producer remains unsupported because it has no typed manifest handoff.
The local unencrypted direct-Packer adapter is connected; command, encrypted and qualified VM adapters
still fail before source, subprocess, or output work. Runner requirements are not VM-image-specific
metadata. The generic `execution.Requirements` shape carries minimum CPU, memory and ephemeral-storage capacity,
an optional sorted OR-set of eligible runner-host platforms, typed capability policies, and an exact
executor-owned wall-time budget. `kvm` is the first admitted capability with `required`, `preferred`, and
`forbidden` policies. Host platform is distinct from the IR target/toolchain platform. Network mode remains the
existing T1 IR field and is not duplicated in execution requirements.

The producer may additionally author `buildNetwork: none|default`; omission is effective `none`. Only a direct
Packer VM producer may request `default`, and compilation admits it only when the exact Environment independently
authors `runtime.buildNetworkPolicy: producer-declared`. Omission or `offline-only` denies the request. The
resolved mode is carried once in the identity-bearing IR node and is not duplicated in execution requirements.
Before runtime preparation, the local adapter issues a process-local receipt binding that validated Environment
authorization to the exact canonical bytes of every networked Packer node. A raw IR node cannot grant itself
network access at the exported pipeline boundary.

The currently admitted authoring path is deliberately narrower: a from-source catalog producer authors
`buildRequirements`, and compilation copies it unchanged into the corresponding build node in the full IR embedded in the
Plan. The scheduler holds only its exact producer reference. It changes catalog and Plan identity but is structurally excluded from logical build identity. Other node kinds
reject the field until their compiler projection and admission semantics exist. The request is optional for
ordinary from-source builders. A `vm-image` producer requires positive values for all three resources, at least
one eligible host platform, an explicit KVM policy, and a positive timeout. Host-platform and capability sets are
sorted and duplicate-free. Unknown platforms, capability names, and policies fail closed.
For a `vm-image` build, required KVM also requires an eligible host platform with the resolved guest
architecture. Preferred or forbidden KVM may retain a cross-architecture TCG route. Per-architecture VM
toolchain pins follow those eligible execution-host platforms, not the guest architecture. VM compilation
requires an exact pin for the selected host and binds that host platform into Build identity; it does not use
the scalar source toolchain as a cross-host fallback.

An Environment may advertise a canonical, nonempty inventory of named execution classes. Each class binds one
unique route label to a host platform, allocatable resources, exposed capabilities, and a maximum executor
timeout. Omission preserves the existing `runsOn` path for unconstrained nodes; an explicitly empty inventory is
invalid. Class names and capability sets are sorted and duplicate-free, route labels are unique, and unknown or
unrepresentable values fail validation.

Compilation filters by every hard request, prefers classes exposing each `preferred` capability, and breaks equal
scores by canonical class name. It records the selected class and resolved capability choice once in the full IR embedded in the
Plan, resolves scheduling through that selected class, and does not infer an ordinary build host from target/toolchain
architecture. Plan admission recomputes the deterministic selection from the exact Environment inventory and
rejects a substituted class, route, request, or binding. The class inventory affects Environment and Plan identities; the selected binding affects full-IR bytes and Plan identity. Neither
enters logical build identity.

Execution-class declarations are planning input, not runtime proof. Ordinary adapters reject requirements;
local direct-Packer execution and the explicit native ARC disk route perform the VM admission described here.
A runtime adapter must authenticate the realized host platform, capacity, capability exposure, and enforced
deadline before work; a provider's outer job timeout must permit the authored executor budget.

The execution owner provides a narrower diagnostic local Linux observation helper. The caller supplies an
already-open workspace-directory capability. The helper duplicates that descriptor while its lifetime is
protected by the runtime, checks effective write and traversal access, observes host platform, process CPU
affinity capped by the complete cgroup-v2 quota ancestry, host-available memory capped by complete cgroup-v2
headroom, available workspace capacity and KVM API exposure, then closes every retained
workspace and KVM handle before returning. It never resolves a replaceable workspace pathname. Non-Linux hosts
reject the observation.

This helper accepts neither an execution request nor a selected binding. It does not authenticate Environment
membership or deterministic selection, own or enforce an execution deadline, reserve observed resources, prove a
provider allocation, or authorize execution.

The local VM admission primitive builds on the same observer without promoting diagnostic values into durable
evidence. The factory owner validates the complete Plan and identifies one exact `vm-image` build. An opaque,
same-process capability then binds both logical identity and the exact current Plan bytes, the selected node,
compiler-selected request and binding, and a current Linux observation. CPU capacity is the smaller of process
affinity and every cgroup-v2 ancestor quota. Memory capacity is the smaller of host-available memory and headroom
under every cgroup-v2 ancestor limit. The observer resolves a unique cgroup2 mount root and mountpoint from the
process mount table, maps the process membership beneath that root, and rejects absent, ambiguous, or escaping
mappings. Admission fails closed where that effective capacity cannot be established.
It rejects a host-platform mismatch, insufficient CPU, memory or held-workspace capacity, and an enabled KVM
resolution when KVM is unavailable. The capability retains its duplicated workspace directory and any selected,
opened, API-validated KVM device until one callback completes or the unused capability is closed. A disabled
preferred or forbidden resolution exposes no KVM handle to the callback even when the host has KVM; driver
isolation must withhold the device from the subprocess.

The callback is single-use and receives the authored executor deadline plus descriptor capabilities, never the
caller workspace or KVM pathname. CPU, memory, held-workspace capacity and KVM availability are re-observed inside
that deadline immediately before work. Request, binding, any current Plan-byte mutation, or node substitution
fails before the callback. Every retained and callback-borrowed handle closes on return. This primitive performs
no source, subprocess, output, or workspace mutation during admission. The local runner has a private typed
direct-Packer dispatch boundary that requires this exact admission before Packer selection. A separate run-owned
resolver contract binds the exact Plan, producer session, runtime, daemon and digest-pinned OCI toolchain images
after pure Plan/workspace-shape validation but before workspace mutation. The resulting executor may perform source,
material and output work only inside the single-use VM callback. The local production resolver is connected for
the direct-Packer, unencrypted form only. The exact full-Plan subject is checked independently by resolver
admission, local VM admission and dispatch; a valid Plan-only change with unchanged producer IR fails before the
driver. Command/Make `vm-image` authoring still lowers into the Plan, but local execution rejects it before resolver
or workspace mutation until a held-output adapter exists. Every encrypted form is rejected before source,
material, toolchain or VM work until a typed non-public secret channel exists.

Direct-Packer execution authenticates one already-local digest-pinned OCI toolchain for the selected host platform;
it never pulls an image or invokes `packer init`. The image may declare at most one inherited `PATH`, which NKD
overrides with a fixed value; declared image volumes and every other inherited environment entry are rejected.
Every consumer container after input extraction has a fixed hostname, the host UID/GID, disabled health check, read-only
root, no new privileges and all capabilities dropped. OpenSSH must be able to resolve that numeric identity.
Podman therefore generates exact passwd/group rows for the invoking UID/GID. Docker receives read-only mounts of
attempt-owned public passwd/group projections containing only that numeric identity, so it does not depend on the
host's NSS or account-file configuration. Shadow databases are never mounted. These public identity projections do
not change the process's numeric authority. Preparation, extraction, version checking, SSH-key
generation and `packer validate` always use network-none. Only `packer build` receives the exact admitted node
mode. `default` grants egress to the whole build container, including plugins and local provisioners, as well as
the QEMU guest through NAT. It selects the container runtime's default bridge, which also permits addressing the
host gateway and unrelated containers on that bridge; it is not an outbound-only firewall, guest-only networking,
or authenticated dependency acquisition. No Docker
socket or ambient credentials are injected. Source, configuration, manifest and material inputs are packed into one
deterministic bounded tar preserving directories, modes and symlink targets. Docker transports only a one-file
outer carrier. The pinned toolchain verifies the held inner-tar digest before extracting that same opened inode;
Packer then sees the input volume read-only. Before transport, the runner derives the material manifest only from
the authenticated Plan-selected observations. A producer-owned projection may relabel its schema and per-binding
kind, but the runner retains the observed component, architecture, digest and canonical staged path. The work
volume remains private to the attempt.

The local unqualified adapter does not configure a subordinate container UID or Docker user-namespace remap.
An escape from its Packer/QEMU consumer would therefore act as the invoking host user against resources already
available to that user, despite the container's network, filesystem, capability and privilege restrictions. This
is an explicit limitation of the unqualified local lane, not an accepted hosted or qualified isolation boundary.
Qualified adapters require an authenticated subordinate identity, user namespace or stronger provider sandbox.
The pipeline-attested native ARC exception instead explicitly trusts the operator-managed ephemeral runner and
daemon under §3; interoperability evidence remains required before claiming operational acceptance.

Fresh Docker volumes and `docker cp` content are root-owned. One fixed input-extraction container runs as root
with only `CAP_CHOWN` and `CAP_DAC_OVERRIDE`, mounts only the attempt's input and work volumes, and has no network
or KVM device. It authenticates the held inner archive before extraction, preserves sealed directory modes, then
assigns both volumes to the observed host UID/GID. DAC override permits traversal and population of sealed
directories; it is not passed to consumers. This container executes no source-owned command or Packer operation
and is removed before consumer execution. Version checking, SSH-key generation, validation and build all run as
the host UID/GID with all capabilities dropped. Only the build receives the admitted network mode, KVM device and
its observed supplemental group when the exact binding enables KVM.

The runtime sets a fixed `PACKER_PLUGIN_PATH`, requires the exact Plan-authored Packer version, generates and checks
an attempt-local SSH key pair when the source contract declares its variables, runs `packer validate` with the exact
selector and clean-output rule, then runs `packer build -on-error=cleanup` with that same selector. Validation is the
authoritative compatibility check for the exact source-declared plugin constraint against the authenticated
toolchain closure. Exactly one declared QCOW2 is collected into a held inode and re-hashed. All containers, volumes,
held inputs and private workspace state are reaped on success, returned failure or context cancellation. This
in-process cleanup cannot run after `SIGKILL`, a process crash or host loss. Cross-process orphan recovery remains
deferred until NKD has an authenticated owner-liveness protocol; a label-only startup sweep must not delete volumes
that may belong to another live attempt.

This is current-availability authority for the current local direct-Packer VM adapter, not a resource reservation, authenticated
provider allocation, cross-process receipt, or qualification evidence. Holding the validated KVM descriptor
proves availability through the callback lifetime; it does not prove Packer or QEMU consumed that exact
descriptor. Driver dispatch must enforce the selected device mode and use the supplied deadline context. The native ARC exception measures cgroup-capped availability and enforces the authored deadline without
claiming authenticated provider allocation. Qualified adapters still require independent allocation evidence.

For the local Docker adapter, the daemon may occupy a different PID/mount namespace from NKD. A client-side
`/proc/<pid>/fd/<fd>` therefore is not a portable daemon device path. The adapter retains and API-validates the
admitted KVM descriptor, observes its character-device major/minor numbers and the caller's procfs kernel boot ID,
and requests only the daemon's fixed `/dev/kvm` with read/write access. A fixed guard in the pinned build container
requires the same kernel boot ID and device numbers before it execs Packer. Missing or mismatched observations
fail closed; no source-owned command runs in that build container before the guard. This is a same-kernel device projection, not transfer
of the open descriptor or qualified consumption evidence. It adds no privilege, host PID namespace, arbitrary
device selection or network grant. The local Podman route retains its existing procfs descriptor path.

`vm-image` execution dispatches the typed producer driver owned by ADR-10. The connected `packer` driver invokes the
authenticated containerized Packer against the selected source-owned HCL project without parsing or generating
HCL. A future `command` driver will execute the exact authored argument vector and can therefore invoke Make after
it gains an equivalent held-output adapter. Both forms must receive only authenticated staged materials and
declared variable bindings, use the same admitted execution class and network policy, write into a private output
location, and return the declared disk through the same collection boundary. The shared runner owns driver
lowering. A CI adapter may transport typed inputs and outputs, but it may not render an independent Make or Packer
command.

VM topology beyond the required-KVM same-architecture invariant, arbitrary devices, nested virtualization, NUMA,
accelerators, requirements on non-build nodes, general hosted VM adapters, qualified runtime authentication, and
provider-specific lowering remain deferred. The narrowly scoped input-extraction stage above is the only local
direct-Packer root exception; it grants no general privileged producer lane. Produced-artifact size remains an
artifact and transport budget. An adapter may not reconstruct producer commands, invoke an upstream workflow, or
weaken a request; a grouped provider job must satisfy every member node.

Generated Docker utility containers run with the host runner's UID/GID and a temporary home, matching the local
runner's ownership contract. The direct-Packer input-extraction stage is the sole exception and is constrained as
specified above. Private package layouts must remain reopenable by later host transport steps. Rootless Podman's
existing user mapping is retained; the rootless BuildKit build container keeps its separate fixed UID and private
output-promotion contract.

Each fetched source transports its exact receipt file, using the pipeline module's destination-to-receipt
path mapping. A receipt carrier must not include the shared receipt directory: independent fetch jobs can
otherwise claim the same immutable destination during finalization. The path is a transport description;
receipt authentication and no-replace restoration remain mandatory.

### 7. Terminal verify + granularity

Every admitted Run retains component operations and an **unconditional terminal verify** (GitHub
`if: always()`), so verification runs even after a build failure (SEC-06). A future provider must enforce the
same failure behavior before it is admitted. Equal logical build identities do not collapse named output executions
or their evidence slots. The current Definition path performs no general reuse of prior builds or verification;
any future reuse needs an authenticated prior result bound to the exact output, members, policy, status and time.

The terminal signature verifier is itself an authenticated execution input. An emitted qualified pipeline must
name a committed workspace-relative `cosign` executable and carry its exact lowercase `sha256` byte digest. The
stock verifier privately snapshots and executes only bytes matching that pin. An adapter that omits either value,
or cannot make the executable available to the terminal job, fails emission before writing provider syntax.


An unintegrated `exec-node` process cannot turn its own declarations about a container into a provider-authenticated runtime observation. Its result is therefore explicitly **unqualified**. Only a provider attestation adapter may
attach a stage-two receipt and convert exact-node results into qualified terminal evidence; the finalizer rejects
missing, zero, changed-selection, or forged bindings.

### 8. Pipeline-attested generation and credential admission

The factory emitter shares the local node-execution primitive and renders from-source production for GitHub
at the pipeline-attested tier without a qualified provider receipt. The unused single-graph qualified emitters
are removed; future qualified provider work must satisfy §1 before reintroducing an adapter.
Self-reported isolation is recorded, never promoted to qualification. Wrapped production uses ADR-10's deferred
fused-job contract. Invoking an upstream reusable workflow needs a separate execution and evidence contract;
v1 invokes pinned source entrypoints instead.

Maintained local and hosted Runs share the factory producer operation and `pipeline.Local.ExecNode` launcher.
A producer reference resolves the exact embedded IR instruction; utility operations retain their pinned execution
containers. Local execution schedules the whole Run graph once in bounded ready batches, retains source receipts
through `pipeline.NodeSession`, and cancels and joins pending operations on failure. Hosted build and utility jobs enter through
`factory.ExecuteNode`; hosted source fetch delegates through a private factory source owner to the same producer primitive. Neither path converts a whole producer
report into a second scheduling result model.
Source receipts remain reusable; result slots and completeness still belong to each exact named output.
The generated Plan is the single executable input document; separate per-build graph files are retired.

The hosted source owner admits the complete Plan and exact selected Git helper/environment, then requests a
pipeline-issued source-workspace capability. Linux admission precedes opening the workspace; zero or closed
handles cannot launch work. Close cancels and joins registered fetch calls before releasing the directory.
Git receives retained checkout/metadata descriptors with explicit Git directory/work-tree selection, disabled
repository hooks/filesystem monitors and no submodule recursion, never an upward-discovered repository. A shared
closed child-environment owner admits the selected absolute Git search path and only the existing exact public
baseline or repository-specific credential-helper configuration. Only network fetch receives that helper/token;
init, checkout and status receive the public baseline. An empty descriptor-owned child HOME/XDG configuration
prevents ambient home or netrc lookup without changing the host process environment. The new held route admits
HTTPS and absolute local/file repositories; selected private credentials require the configured HTTPS mirror.
Git's package-owned protocol override refuses repository-configured external transports, and held fetch fixes
the upload-pack program, disables recursive fetching and requests the exact selected repository rather than a
retained origin. Before operational Git work, Git's own bounded local-configuration query rejects filter,
include/includeIf, local URL rewriting and delegated worktree/promisor/callback configuration. The query has no
source token, reads no included configuration, and cancellation remains a timeout. Directory identity rechecks
do not claim immutable configuration bytes against a concurrent hostile host writer. Unsupported held Git/SSH transports fail capability
admission before work; the general catalog and ordinary Local route retain their existing Git/SSH behavior.
Original checkout and receipt directories are rechecked
through the held workspace before receipt reuse or commit. This capability grants filesystem access only, not
producer observation, installed-content, publication or qualification authority. Ordinary Local fetch remains
the existing developer route without these additional guarantees.

Dependency vendoring authenticates the selected runtime/daemon and exact native pinned image before source work.
A bounded, Git-metadata-free archive enters a fresh private volume; the pinned entrypoint, exact vendor arguments
and declared fetch network remain. Neither source credentials nor host bind mounts enter that container. Its
bounded return is decoded by the existing archive owner, rejects Git metadata at every depth and unsafe members
before copy-back, and must preserve the original source binding. Container/transport cleanup and the admitted
runtime's closure finish before source receipt issuance; cleanup failures remain coded errors. Docker and Podman
use their respective existing private-volume no-copy syntax. Local rootless Podman networking uses a source-only
trusted-system helper admission: the runtime selects pasta
or slirp4netns, and no-follow descriptor checks require `/`, `/usr`, `/usr/bin` and that executable to be root-owned,
executable and not group/world writable before selecting that helper directory. Shared closed PATH/config
defaults and qualified runtime admission remain unchanged. This is host-system trust for the unqualified source
lane, not a digest-pinned helper closure or protection against the host administrator. Fetch-stage deadlines, declared networking and
observed return-carrier headroom are separate from container capacity enforcement: fetch IR currently declares
no CPU, memory or PIDs budget, so the vendor container has no such caps or tmpfs byte-size cap. This capacity
enhancement remains deferred, not a provider reservation or a claim made by the timeout. Local fixtures
model the daemon; cross-compilation is not native descriptor execution, and real pinned Docker/Helm execution
requires separate runtime evidence. An opt-in native smoke test requires an explicitly selected installed runtime
and already-local exact native image ID; absence of those inputs skips it, not proves the backend works.
No generated disk executor or stronger evidence lane is implied.

Maintained lifecycle targets are local and GitHub. GitLab, Tekton and Argo selection returns
`CodeEngineCapability` before reading authored inputs or producing output. No fallback renderer exists.

| Event | Admitted work and credentials |
|---|---|
| Fork pull request | Regeneration/schema checks; no credentials |
| In-repository pull request | Build/package; read-only mirror credential on fetch steps only; no OIDC, push or environment access |
| Default-branch push or manual dispatch on that ref | Publish/sign/attest/verify inside a protected environment; phase-scoped registry and OIDC credentials |
| Explicitly allowed branch manual dispatch | Same publication lane; exact Environment allowlist, deployment policy and signing identity must admit the branch |
| Git tag | No signing trigger |

The current generated Run workflow is manual-only. Execution admits the default branch plus exact branch
names explicitly listed in `Environment.githubDispatchBranches`. The optional list is sorted and unique;
wildcards, tag/PR refs and expression syntax are rejected. It is bound into Environment and Plan identity,
cloned with its owner, and enforced in source access, execution and finalization guards. Local execution
rejects this GitHub-only option. Omitting it preserves default-branch-only execution.
Pull-request regeneration checks run in the separate repository Merge Gate workflow when generator inputs
or outputs change. Default-branch push
publication and in-repository PR builds remain deferred. Live publication/signing/verification jobs name the
`nkd-release` GitHub deployment environment; operators must configure its deployment policy to admit only
the default branch and any explicitly authorized dispatch branches. The signing identity policy must also
recognize those exact workflow refs. Only artifact signing and the pre-publication SBOM-attestation job receive OIDC minting permission.
The SBOM job receives no registry credentials or package permissions. The verifier needs registry read
access and obtains no identity token. Job timeouts are
bounded, and workflow concurrency does not cancel an in-progress publication. Terminal observation and Run
finalization still run after failures within an admitted execution. The generated Run workflow does not
start on pull requests.

Identity is one committed workflow path and explicitly admitted branch refs; verification rejects PR refs. Protect workflow,
generator, helper and signing code with branch protection and code ownership. Hosted source access uses a mirror
with vendored inputs. The renderer must reject guards it cannot express, including OIDC outside the signing job
family or credentials in build phases. These are testable contracts; current rendering is not proof that a
provider or protected environment has exercised them.

The v1 scheduler may group consecutive offline observation, SBOM and packaging nodes with identical runner,
container and platform into one bounded job. Every logical node retains its exact execution command and
container isolation; fetch, build, publication and signing remain separate jobs. Internal dependencies use the
same workspace bytes and consumer checks. Each logical output retains its carrier for finalization. A failed
shared job makes its outputs unsuccessful for finalization, even if an earlier step uploaded a carrier.
This provider scheduling choice does not change the logical graph or logical build identity.

GitHub job and step labels describe the operation and selected component or output. Grouped jobs name their
packaged outputs and disclose Helm image binding when present; stable execution step IDs and exact commands
retain the graph binding independently of display names. Each execution step logs its operation, node and
producer instruction, component and selected output where applicable, platform/network, declared inputs and
expected outputs. The host helper reports successful outputs, elapsed seconds, primary exit
status and the last phase; failed execution cannot print a successful completion. Job summaries show the same diagnostic context and
outcome without requiring every transfer step to be expanded. Closed log streams and an absent or unwritable summary cannot change the
operation's exit status. Descriptions are emitted as one-line runtime data and escaped for control lines and Markdown;
arbitrary recipe arguments, source/destination URLs, file contents and environment values are not dumped.
The exact producer argv remains in the canonical Plan for deliberate local inspection. Packaging reports the
actual bound image digest, and publication reports its persisted immutable reference. Logs and summaries remain
diagnostics, not authority receipts, completion evidence or qualification.

Strict native disk inspection retains the sanitized bounded progress log in one exclusive regular file beneath
the original held workspace. The helper drains progress into a package-owned memory collector and writes to
caller log sinks only after the owned operation stops. The generated one-day, always-run diagnostic upload
is optional and failure-tolerant; only its exact output-bound descriptor may tolerate a missing file. Required
evidence and payload uploads remain mandatory. Retention failure cannot replace the inspection verdict,
and graceful-timeout retention does not promise recovery after runner loss or `SIGKILL`. The explicit M2
build-provenance mode emits neither inspection work nor its diagnostic upload. Changing the helper requires
rebuilding and repinning its complete inspection image before claiming hosted operation.
Retention uses a separate ten-second cleanup context with cancellation checks between filesystem operations;
an already-entered regular-file syscall cannot be interrupted, so this is not a hard filesystem deadline.

The compiler creates a private release preparation, a production SBOM-attestation stage producing the
final release package, and one publish/sign/verify route. The attestation stage binds and verifies each
CycloneDX document before final release identity is derived. It adds one internal handoff. GitHub handoff artifacts remain
internal job transfers, not additional deliverables. Product names do not cause duplicate copies of the full
closure to be uploaded or published.

Generated steps invoke `factory github run-node`, `restore-inputs`, `pack-outputs`, or `recover` against
the complete canonical Plan and an exact node or derived stage selector. The factory module admits the
complete graph and capabilities before work, derives carrier membership and paths, and owns progress,
optional summaries and utility-container launch. Utility nodes share a launcher with local execution; the
inner helper checks the outer helper's exact Plan digest before executing. Provider YAML retains topology,
permissions, source credential scope, pinned actions and explicit operator setup. Packing remains a separate
step from source fetch so it receives no source credential. No authored transport manifest or arbitrary
command argument is added.

Each stage downloads one exact set of external input carriers, then restores each required carrier in one
helper step. Missing or invalid carriers fail before consumer execution, including when a provider batch matches only
part of the requested set. Each operation prepares its outputs in one step and retains an independent upload
for each exact carrier. Carrier-name/path collisions fail before rendering; no wildcard imports unrelated
artifacts. Logical commands, transport authentication and isolation remain unchanged.

The finalizer batches downloads with the exact set of all planned carrier names. One recovery helper visits
every carrier and records its producer and restore outcome separately; it continues after missing carriers,
failed producer jobs and returned restore failures. A diagnostic summary names every outcome. A successful batch
download or recovery step proves no completeness: the finalizer consumes those unchanged per-carrier records
and authenticates the complete output and evidence closure. The helper accepts bounded `toJSON(needs)` data
through a step environment value, with an exact stage/bootstrap job set, known statuses, duplicate rejection
and bounded parsing before filesystem work. Unknown, missing or malformed job results fail closed. Individual
carrier failure does not stop later recovery; cancellation or failed outcome persistence does.
Every generated run script remains checked against GitHub's 21,000-character ceiling, conservatively measured
as UTF-8 bytes, including explicit operator setup and bootstrap commands.
Provider outage or failed local outcome storage can
still prevent finalization.

Finalizers are scheduled unconditionally and fail on failed/missing producers, failed restore, omitted required
artifacts, malformed or substituted observations/SBOMs, and wrong production predicates. The platform may prevent
the finalizer itself from starting; inspect provider status when no Run record exists (ADR-17).

## Consequences

**Positive**
- Shared execution code removes parallel build implementations; independent observations and comparison still
  establish byte identity.
- Helper-owned isolation shares the local execution path across hosted providers.
- When self-hosting is selected, the tool itself is built from source and pinned and verified, so "trust nothing
  pre-built" extends to `nkd-exec` without pretending every execution performed a bootstrap.
- Adapters are genuinely thin (topology and transport); a new engine is a small adapter.

**Negative / costs**
- A self-hosting bootstrap adds startup time and a bootstrap-toolchain dependency.
- The runner must be a static binary that runs in any toolchain image and needs no network during a build.
- Passing the runner, the node file, and inputs/outputs between jobs is the adapter's transport responsibility.
- Some nominal CI configurations cannot meet the capability contract without operator-provided network policy or
  artifact storage; emission correctly fails for those configurations.

**Neutral**
- The runner is a mode/subset of `nkdctl`. Maintained hosted steps use `nkd-exec factory github`; its utility-container
  child uses `factory execute-node` bound to the expected Plan digest. Producer execution is shared with local Runs. Qualified graph protocols retain the separate `exec-node` entry point.

## Alternatives considered

- **Engine-owned isolation per provider.** Rejected as the shared execution model because provider-specific network control duplicates the local launcher and can require bespoke executors.
- **Emitter renders the build command per node.** Rejected — byte-identity would be *asserted*, since rendered
  strings must match the local runner; that is the fragile parallel implementation this ADR avoids.
- **A fat runner image bundling every toolchain.** Rejected — loses per-component toolchain pinning and bloats the
  image; keep toolchains pinned per component and launched by the helper.

## De-risking spikes

1. **Helper isolation** — exercise the same local and hosted launch path with real pinned containers, network-egress probes, read-only inputs, UID ownership, cancellation and nested-runtime capability failures.
2. **Runner provenance** — prove both allowed origins: a self-hosted runner is reproduced as its own exact
   qualified subject and binds its actual bootstrap toolchain, while a preexisting runner binds its exact bytes
   and no bootstrap identity. Neither statement reproduces the producer output that the runner later executes.
3. **Transport/failure matrix** — for every engine, prove the runner, inputs, outputs, node receipts, and the
   ValidationVector reach their consumers, and that an intentionally omitted or corrupted item fails closed.

## Follow-ups

- **ADR-03:** owns the stable coded `engine-capability` failure used by this contract.
- **ADR-07:** the runner digest is a fail-closed builder-identity attribute in the builder profile.
- **ADR-19:** downstream deployment owns topology and placement; generated Run pipelines build and publish named outputs.

## Acceptance evidence

Current contract evidence in `pkg/nkd/pipeline/execnode_test.go` and `pkg/nkd/pipeline/results_test.go`
retains exact-node execution, explicit unqualified results and terminal receipt enforcement. Factory generation
and lifecycle tests cover GitHub semantic validation and fail-before-input/output rejection of retired engines.
The qualified renderers, their bundle/transport-plan models, and self-referential renderer tests are removed.
The shared archive transport, source receipts and local execution contracts remain. These tests do not establish
new provider execution or qualified acceptance.

Qualified external acceptance requires each implemented provider independently; adding another provider requires
the same evidence before its capability can be admitted:

1. schema/provider validation of the emitted document and a fixture run that transports the runner, fetched
   source, outputs, node receipts, and the ValidationVector through separate jobs or pods;
2. an egress probe proving the build node cannot connect while the fetch node can, plus assertions for read-only
   source, private output, sanitized environment, and absent ambient credentials;
3. corruption/omission tests at every transport edge and a failed-build test proving the terminal verifier still
   runs and reports every missing node;
4. negative capability fixtures proving emission itself fails when offline isolation, finalization, or actual
   stage-two observation cannot be supplied; and
5. an adversarial provider-receipt fixture proving a zero receipt, changed profile/node set, missing observation,
   or result from outside the selected build set cannot reach a qualified terminal report.
