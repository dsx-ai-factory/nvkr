# ADR-18 — Pipeline IR & CI-adapter emission (hermetic execution)

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-18 in the consolidated NVKR ADR set (added to close the Definition→realized-pipeline gap).
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
component is one catalog entry and no code. Definition v5 names each output and its exact catalog producers.
A native output declares one concrete `platform` and derives one internal build target. An OCI aggregate declares a sorted, unique `platforms` set
and derives one native build target per member plus one image-index package output. Every native member
retains its own producer results and evidence obligations; the aggregate binds their complete exact closure.
No authored reusable producer group, recipe selection, criteria matcher, mixin or recipe layer participates. The BuildDigest
identifies the selected executable producer closure and excludes the output name, packaging and transport.
Two named outputs with equal BuildDigest retain independent executions, results and evidence obligations.
Existing Helm image-output dependencies remain explicit packaging edges. Run-plan and artifact-graph v5 contain no cluster-composition nodes or records. Compiler handoff documents
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

Build execution is a single **static, from-source runner** (`nvkr-exec`, a mode/subset of `nvkr`) that executes
one IR node (`fetch → build → collect`). Every job uses the same execution code as the local runner, avoiding duplicated build semantics. Reproduction still requires independently checked bytes and execution evidence.
The IR is pinned data. Qualified graph diagnostics execute the exact admitted node bundle; maintained Runs
execute a producer reference from the complete Plan through `factory execute-node`.

### 3. Helper-owned isolation and narrowly admitted native ARC disks

Jobs use generic hosted runners except the explicit native ARC disk policy below. With `runsOn`
unset and no ARC policy selected, ordinary native ARM64 producer fetch/acquisition/build operations select `ubuntu-24.04-arm`;
AMD64 producers and utility operations retain `ubuntu-latest`. An explicit `runsOn` overrides
this default; a VM execution binding and the complete native ARC routing projection keep their existing
precedence. This is deterministic hosted routing, not runtime platform/capacity proof or
qualified execution. The same-Run, source-bootstrapped `nvkr-exec` helper launches each pinned toolchain container
through the shared `pipeline.Local.ExecNode` launcher used by local and hosted Run operations. Adapters render job topology, dependencies
and declared transport; they do not reproduce isolation as provider-specific build syntax. Fetch/pull nodes
materialize pinned inputs first; hermetic build and verification nodes enforce network denial, read-only inputs,
private output and a fixed credential-free environment. The local byte-observation exception in §8 executes
no producer code and carries no container-isolation or qualification claim. Shared code prevents semantic
drift; it does not alone prove byte identity across hosts.
The maintained pipeline-attested workflow trusts the bootstrap job and same-Run Actions artifact delivery;
it does not independently checksum the downloaded helper before restoring its execute bit. This delivery
contract is not the qualified exact-helper-byte verification required below.

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
direct-Packer QCOW2 outputs with required KVM. Plain outputs retain their strict or build-provenance evidence
selection. ADR-10's explicit public-bootstrap LUKS2 outputs require `diskEvidence: build-provenance-v1` for
publication; secret-valued FDE, encrypted inspection and qualified execution fail admission before work.
Their execution classes select exactly
`arc-amd64-kvm` and `arc-arm64-kvm`. This is an explicit operator-trusted runner/daemon boundary: it does not
claim an authenticated provider allocation, subordinate identity, independent sandbox attestation or qualification.
Production and standalone diagnostic disk jobs use the `nkd-arc` protected Environment, with pool access
restricted to the reviewed workflow on the default branch and any exact branches explicitly admitted by
`githubDispatchBranches`. The separate signed-development policy is described below.

With publication and signing omitted, the same native policy also admits a **build-only diagnostic**
workflow at `static-unqualified`. It accepts plain disks and ADR-10's explicit public-bootstrap LUKS2
producer. Its grouped executor retains the live owner through exact QCOW2 export and producer-result
recording, then transports the raw disk as a same-Run Actions artifact. Finalization rechecks the selected
output's bytes and result membership without reconstructing runtime or publication authority. No disk
inspection, release wrapper, registry upload, signing or OIDC job is emitted. The workflow still requires
the exact dispatch branch and `nkd-arc` deployment policy. Generated build-only workflows admit exactly one
disk output. Their finalizer restores the complete source/material/raw-disk closure on that output's selected
native ARC pool, using its matching helper and `nkd-arc` Environment with only `contents: read`. Its two-hour
job bound covers transfer, recovery and exact-byte observation; it does not rebuild, inspect or use KVM.
If the catalog selects a private pin set, existing catalog-source credentials remain scoped to the
final catalog-recompilation step; they are not passed to recovery or disk execution.
The pool's configured scratch capacity is not a provider reservation or an observed finalizer capacity proof.
Private native toolchain preparation may read
the maintained `ghcr.io/dsx-ai-factory/{nvkr,nke-recipes}/m2-candidate/toolchains/` namespaces; its temporary
read-only credential remains outside producer execution. This mode does not use publication configuration
as a surrogate for build credentials. Real native FDE execution is acceptance evidence recorded separately.

The operator must provision ephemeral single-job runners and restrict pool access to reviewed workflows and
admitted refs. Production uses the default branch plus exact `githubDispatchBranches` entries and the
`nkd-arc` protected Environment. Signed development uses the separate `nvkr-dev-arc` Environment and its
reviewed branch policy described below, with the same two fixed runner labels and execution contracts.
Provider configuration and fresh native interoperability are external acceptance requirements; emitting a
label or naming an Environment does not establish those facts.

Production `nkd-arc`/`nkd-release` and development `nvkr-dev-arc`/`nvkr-dev-release` deployment policies
and the corresponding signing identity must admit each selected ref.
It permits no arbitrary setup, injected environment, job container or services.
Docker is the initial admitted runtime. For publication workflows, bootstrap, source fetch, registry publication,
signing and finalization remain on generic hosted runners. Each disk output contracts its acquisition/build, terminal observation, inspection
and packaging operations into one bounded helper invocation against the complete Plan. The original producer
session and held disk survive through packaging; strict mode additionally requires reconciliation. The raw disk has no independent
carrier; its standard OCI wrapper carries its exact bytes. All component evidence and producer-result slots
remain required. Only fixed source-fetch steps receive private-source credentials; ARC disk jobs receive no
source token, registry-write credential or OIDC signing token. With `publication.mode: per-output-v1`, the
same ARC group also prepares that disk's offline publication layout using its existing material files. The
exact derived OCI package node is admitted only with network disabled; SBOM attestation and all credentialed
publication operations remain on hosted runners. Those runners restore the prepared layout rather than the
disk producer's full material closure.

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
inspector image and measured executable pins. Explicit candidate `diskEvidence: build-provenance-v1` omits inspector
preparation and execution; it authenticates live producer/material evidence under ADR-13/20 instead.
This is the M2 and M3 MVP producer-provenance scope; full inspection and material reconciliation remain
deferred hardening (ADR-13 §6).
The ARC group sums only executed material-producer budgets, applying the selected VM class ceiling to its
root producer. Synthetic SBOM/package nodes do not each add an adapter-default hour. One shared adapter-default
hour covers public toolchain pulls (each bounded to fifteen minutes), terminal observation,
`CompleteHostedDiskPublication` and, in per-output mode, offline `executePackage` preparation. These operations
share the helper deadline; the hour is not a separate allowance for each operation. Strict inspection additionally
reuses the selected profile's per-member plus acquisition join budget. Beyond the helper deadline, the job
reserves fifteen minutes shared across provider pre-steps and successful carrier packing/uploads, and five
minutes each for cleanup and failed Packer diagnostic upload. Strict inspection reserves another five minutes
for its independently bounded, always-run diagnostic upload; build-provenance mode does not charge that absent
upload. Each producer is
independently bounded; earlier caller deadlines still win. GitHub permits at most six hours for an explicit
[step timeout](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstepstimeout-minutes),
including on self-hosted runners. The disk step emits that timeout only when its full helper deadline plus
cleanup headroom fits the limit; longer steps omit the provider step timeout and remain bounded by their
unchanged helper deadline and enclosing job. The complete job remains capped at twenty-four hours; generic hosted jobs retain their
six-hour cap. These configured allowances provide neither per-step transfer budgets, a maximum transferable-byte
proof, nor a measurement of remaining provider-job time. Near the ceiling, slow image pulls or successful
carrier transfers can exhaust the shared reserves; fifteen minutes is an allowance, not evidence that those
transfers fit. Runner capacity and transfer headroom require separate operational evidence.
On failure the sealed Packer sink can emit one exact diagnostic-route description. The generated job uploads
only that bounded route, with one-day retention and a five-minute upload limit; early admission or failed
sealing emits no route. Diagnostic transport grants no artifact, receipt or release authority.
Current availability is not a capacity reservation. The local runtime observations
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
`nvkr.runtime-identity/v1` preimage; this pre-ratification implementation does not translate older identities
or prove compatibility with previously recorded qualification profiles.

For native multi-architecture pipeline-attested Runs, `Environment.githubRunners` maps each explicit
`linux/amd64` or `linux/arm64` execution platform to a supported generic hosted Ubuntu runner. It cannot
be combined with `runsOn`; unknown platforms, unrecognized labels, mismatched labels and missing output or
utility routes fail before catalog loading or emitted output. Omission retains the default native ARM64 producer routing and any explicit `runsOn` selection. The hosted adapter rejects unrecognized legacy labels and foreign-platform
execution rather than assuming emulation. The initial supported routes are `ubuntu-latest`, `ubuntu-22.04`
and `ubuntu-24.04` for AMD64, and `ubuntu-22.04-arm` and `ubuntu-24.04-arm` for ARM64, as listed in the
[GitHub runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners).

The adapter resolves routes from the already admitted execution view without changing producer IR or logical
Build identity. Route selections remain bound to Environment and complete Plan identity. Producer jobs use
their target-platform route; utility, acquisition-tool, signing and publication operations use their utility
platform route. Each configured native job checks its realized kernel OS/architecture before checkout or
helper use. Bootstrap builds a separate static Linux helper with explicit `GOOS`/`GOARCH` for each route;
transport pairs each job with that route's helper. The execution entrypoint checks its own native helper
platform against the exact Plan before work. Before build execution, the pipeline owner checks the
actual Docker/Podman daemon platform, pulls the pinned toolchain for the selected platform, and verifies its
reported image platform and manifest identity. Held source vendoring separately checks its daemon against
the native helper platform before image materialization or Git checkout, using the source owner's isolated
runtime environment rather than its Git credentials. It retains the pinned native vendor-image checks.
Plain Git fetching without a vendor command launches no container and requires no daemon check.
A foreign daemon or image fails before the corresponding source or build output work. Utility operations perform the same factory-owned full-Plan/node and container checks on the host
before their helper-owned container launch; the helper inside the utility container revalidates the Plan and its
own native platform without needing a nested daemon. The compact hosted helper owns this host
preflight. The shared hosted execution projection also supplies readable Plan views with effective runner
routes; presentation does not reconstruct the map. These are capability checks, not qualified runtime
receipts or provider-allocation proof.

The map does not itself admit resource requirements, VM execution, arbitrary host labels or
foreign-platform emulation. With `githubARC: native-kvm-v1`, the separately admitted disk group retains
its exact ARC route and raw-disk carrier suppression; the map routes ordinary hosted work. Local Runs reject this GitHub-only field and continue using a separate local
Environment and their existing explicit container-platform path.

The repository-owned NVKE OS integration workflow is a narrow validation exception, not generated Run CI and not
a hosted VM adapter. Each push to `main` selects that exact commit for full AMD64 and ARM64 builds, boot smoke,
and short-retention QCOW2 uploads. Manual dispatch from the default-branch workflow remains available for
toolchain-only or full validation of either architecture or both, after an exact main commit or copy-pr-bot
mirror is authenticated. Runs are independent; newer pushes do not cancel earlier attempts. Both routes may
select only the declared `arc-amd64-kvm` or `arc-arm64-kvm` KVM scale sets for full validation. Main pushes use
the NVKE pin from the merged catalog and validated Plans; manual full dispatch additionally checks the entered
NVKE commit against that pin. The cross-repository source credential must not share a job with candidate code; the
trusted source-fetch job alone receives it. That job verifies the selected commit and tree, makes a fresh
credential-free Git checkout, and uploads it as a one-day internal artifact with an exact SHA-256 passed as a
job output. The build job verifies the archive digest and Git commit/tree before candidate code runs. Candidate
code receives no authority-bearing evidence role. The build job's logs, image observations, QCOW2 digest and boot
result are explicitly unqualified diagnostic output. The [2026-10-01 runs](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-01-nke-arc-os-builds.md) exercise both
ARC pools from `nvkr`; they do not establish lasting availability or provider isolation. Shared Actions
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

- Qualified execution requires the exact runner bytes that execute from-source nodes to be measured and
  verified against the builder-profile runner pin.
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
  The shared Docker/Podman launcher owns its fixed non-root isolation settings and child-process cleanup.
  Authenticated source and output use separate attempt-owned volumes, not host bind mounts whose ownership
  happens to match UID 1000. A fixed, offline preparation stage preserves source modes and changes ownership
  only inside those volumes. It runs no producer command, mounts no host source or runtime socket, and grants
  the build no root privilege. The producer mounts source read-only; declared output is returned through bounded
  archive transport before ordinary host collection. Preparation, transfer and failure paths own bounded cleanup. Run
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
The local direct-Packer adapter admits unencrypted disks and ADR-10's explicit public bootstrap; command,
secret-valued encrypted and qualified VM adapters
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
the direct-Packer unencrypted or explicitly public-bootstrap form only. The exact full-Plan subject is checked independently by resolver
admission, local VM admission and dispatch; a valid Plan-only change with unchanged producer IR fails before the
driver. Command/Make `vm-image` authoring still lowers into the Plan, but local execution rejects it before resolver
or workspace mutation until a held-output adapter exists. Encrypted forms other than ADR-10's explicit
public `luks_passphrase: changeit` diagnostic input are rejected before source, material, toolchain or VM
work until a typed non-public secret channel exists. Public bootstrap values grant no confidentiality claim.

Direct-Packer execution authenticates one already-local digest-pinned OCI toolchain for the selected host platform;
it never pulls an image or invokes `packer init`. The image may declare at most one inherited `PATH`, which NVKR
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
For network-enabled ARC builds, the pool operator must also document and enforce isolation from cloud/cluster
metadata, workload and credential services, permitting only the outbound acquisition traffic required by the
producer. Container bridge networking does not establish these restrictions. Network-isolation configuration
and a fresh native negative-reachability demonstration are external acceptance evidence, not implied by the
Environment declaration; that evidence is not recorded here.

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
deferred until NVKR has an authenticated owner-liveness protocol; a label-only startup sweep must not delete volumes
that may belong to another live attempt.

This is current-availability authority for the current local direct-Packer VM adapter, not a resource reservation, authenticated
provider allocation, cross-process receipt, or qualification evidence. Holding the validated KVM descriptor
proves availability through the callback lifetime; it does not prove Packer or QEMU consumed that exact
descriptor. Driver dispatch must enforce the selected device mode and use the supplied deadline context. The native ARC exception measures cgroup-capped availability and enforces the authored deadline without
claiming authenticated provider allocation. Qualified adapters still require independent allocation evidence.

For the local Docker adapter, the daemon may occupy a different PID/mount namespace from NVKR. A client-side
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
runner's ownership contract. Direct-Packer input extraction and BuildKit private-volume preparation are the
narrow exceptions described above; neither permits privileged producer execution. Private package layouts must
remain reopenable by later host transport steps. Rootless Podman's existing user mapping is retained; the
BuildKit producer keeps its fixed UID and volume-transport contract.

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
A producer reference resolves the exact embedded IR instruction. Offline utility operations retain their pinned
execution containers except ordinary local non-disk terminal observation: it reopens and hashes produced bytes
in the host process, sharing collection's filesystem view after bounded BuildKit output collection. That step
executes no producer code or signature verifier and retains a static-unqualified observation; it does not claim
container isolation or runtime authentication. Disk observation retains its original held-owner path, and
hosted utility jobs are unchanged.

Local execution schedules the whole Run graph once in bounded ready batches, retains source receipts
through `pipeline.NodeSession`, and cancels and joins pending operations on failure. Hosted build and utility jobs enter through
`factory.ExecuteNode`; hosted source fetch delegates through a private factory source owner to the same producer primitive. Neither path converts a whole producer
report into a second scheduling result model.
Source receipts remain reusable; result slots and completeness still belong to each exact named output.
The generated Plan is the single executable input document; separate per-build graph files are retired.

An unqualified local Plan may select both direct-Packer disks and ordinary outputs.
Its offline utility child admits only non-disk observation, SBOM and ordinary packaging;
the complete Plan is checked and the local launcher binds its child invocation to the
original Plan digest. This flag is not mandatory on a manually invoked helper.
It cannot execute a producer,
consume a held disk, publish or sign. Ordinary requirement-bearing producers and unsupported
VM drivers remain rejected before work; the original Run owner retains VM admission.
Local static-unqualified and restored pipeline-attested lifecycle image observation
relocate the validated Plan and descriptive paths together,
preserving compiled identity and exact acquired-payload/result comparisons. Other ordinary
formats retain their existing descriptive workspace-path support. Relocation is not authority.

For the unqualified local diagnostic lane only, the maintained developer launcher
may bind a freshly measured Packer toolchain image ID through an attempt-scoped
catalog substitution after native tool/binary/platform validation. The substitution
is not a maintained pin, publication input or qualified execution claim. Native
Docker TCG is an explicit local diagnostic execution class; contract tests do not
establish a successful complete OS smoke or tenant deployment.

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
admission before work. Ordinary Local retains HTTPS/SSH and local/file transport under the closed configuration
and bounded credential projection described below, not unrestricted ambient Git behavior.
Original checkout and receipt directories are rechecked
through the held workspace before receipt reuse or commit. This capability grants filesystem access only, not
producer observation, installed-content, publication or qualification authority. Ordinary Local fetch retains
developer HTTPS/SSH transport without the held workspace guarantees. Cleartext HTTP and Git transports are
not admitted. Clone and ref discovery fix the upload-pack program; clone disables local-path optimization
with `--no-local` so transferred-object checks also apply to local installer copies, and
disables recursive submodules; neither receives developer credentials. Its shared Git-command owner excludes
ambient global callbacks, templates and line-ending conversion, projects selected developer credential and
proxy/TLS trust settings into network fetch, and admits explicit absolute local-repository redirects. Arbitrary
SSH command overrides remain excluded. Checkout/status receive no
developer HOME or helper. The projected global credential keys are helper, username and useHttpPath;
system helpers, GH/GCM/keyring environment, arbitrary SSH commands and global SSH/URL configuration are
not inherited. This is bounded helper compatibility, not a claim that every developer login works.
Explicit redirects admit canonical absolute filesystem paths (including one terminal slash), not file URIs.
Repository-local filters, includes, HTTP/SSH callbacks, delegated worktrees and object sources fail admission.
Ordinary local source roots refuse nonempty, untracked, missing, symlinked or mismatched `.gitmodules`
descriptors and indexed gitlinks. A regular tracked empty descriptor is admitted only when bounded Git
queries confirm its empty HEAD blob and matching index and worktree; it grants no nested-source acceptance.
Git owns the bounded nonrecursive index-mode query, whose `ls-files --format` capability requires Git 2.38+;
a failed capability query names that floor without interpreting private Git output. Disabling
submodule recursion or ignoring submodules during status does not authenticate nested source cleanliness.
Real Git fixtures exercise exact checkout bytes, callback exclusion, authenticated fetch and local redirects;
they do not establish current private-source access or a held workspace receipt.

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
| Development Environment branch manual dispatch | Unsigned hosted builds, or full signed publication using development environments/credentials, the admitted native ARC route, and disposable attempt namespaces |
| Git tag | No signing trigger |

Generated Run workflows are manual-only. Production execution admits the default branch plus exact branch
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

`Environment.githubDevelopment: true` selects the separate `nvkr-dev.yml` workflow and admits manual
branch refs only. Tags and pull requests remain closed. It shares the compiler, complete graph, helper,
output requirements and finalizer with production; no selected outputs are filtered out. Local execution
rejects this GitHub-only setting. Arbitrary runtime setup/variables and a second dispatch-branch allowlist
are rejected. Legacy runner overrides must name an admitted generic hosted label; disks use only the
explicit native ARC policy and fixed execution-class labels above.

Unsigned development omits both publication and signing, remains `static-unqualified`, and cannot use
ARC or disk evidence. Signed development requires real cosign, the GitHub issuer, `per-output-v1`, the
reserved development namespace and exact development workflow identity in ADR-20. It retains the
`pipeline-attested` lane and all output-local signing, evidence, verification and completeness gates.
The Environment cannot select a production signer, anonymous/plain-HTTP registry, or arbitrary publication
credential. Development verification is a separate consumer trust decision, not production acceptance or
qualified reproduction. The current hosted repository/workflow identity is admitted before execution.
Development finalization emits its Run record without a production qualification handoff; explicit handoff
admission rejects development before qualification evidence collection.

Development publication and signing jobs use `nvkr-dev-release`; native disk jobs use `nvkr-dev-arc`.
They never inherit the production deployment environments. Dedicated write and read registry credentials
are delivered only to their corresponding phases. Development jobs receive no repository-token package-write
permission; only signing and SBOM attestation receive OIDC. Read-only toolchain preparation can still use
the repository token for exact retained private M2 toolchain roots; credentials are removed before producer
execution. ARC jobs never receive source, publication-write or OIDC credentials. Source-read credentials
remain confined to the existing fixed source-fetch and catalog-validation steps. Branch code must be
reviewed before dispatch. Provider ACLs must independently exclude production write access from development
principals; selecting a namespace or protected environment alone cannot enforce token scope.

Development and production have separate generated layouts and concurrency groups (ADR-21). Unsigned
development cancels superseded same-branch Runs and retains helpers/carriers for one day. Signed development,
like production, is non-canceling and retains helpers/carriers for seven days to cover native disk budgets.
Final Run records and production qualification handoffs retain seven days and disk diagnostics one day. Provider cancellation or runner loss can
prevent finalization. Expired carriers require a complete new Run. Signed development uses per-attempt
registry namespaces and full-attempt retries; production's authenticated earlier-attempt reuse is unchanged.
ADR-20 owns the separate default-branch registry cleanup protocol. Actual dispatch, credential/pool
isolation, provider expiry and remote deletion require external evidence.

Production identity is one committed workflow path and explicitly admitted branch refs; verification rejects PR refs. Protect workflow,
generator, helper and signing code with branch protection and code ownership. Hosted source access uses a mirror
with vendored inputs. The renderer must reject guards it cannot express, including OIDC outside the signing job
family or credentials in build phases. These are testable contracts; current rendering is not proof that a
provider or protected environment has exercised them.

The M3 qualification contract admits only the exact successful originating build from the intended repository
and default branch. Source Run, attempt, commit, workflow identity, Definition, Environment and Plan bindings
are authenticated separately from the qualification invocation. A manually dispatched qualification workflow
does not itself prove that its source build succeeded or came from the approved branch. The M2 validation-branch
exception cannot authorize qualification. Any later workflow adapter must reject unrelated or unsuccessful
Runs before downloading executable subjects or requesting qualification signing credentials.

Delivered-tool smoke execution receives the exact authenticated payloads, has networking disabled and has no
qualification OIDC or attachment authority. The qualification publisher consumes authenticated results only
after the complete required set passes, and uses a committed workflow identity distinct from the build
workflow. Missing required smoke or external-suite results fail closed; declared suites outside MVP scope are
reported as not evaluated. Source/build authentication and all verdict collection precede signing. A signing
or attachment failure can leave earlier attachments but never produces successful qualification. ADR-16 owns
the evidence and publication contract; reconciliation separately records implementation and hosted acceptance.

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
status and the last phase; failed execution cannot print a successful completion. Per-operation details stay in
job logs. Closed log streams cannot change the operation's exit status. Descriptions are emitted as one-line
runtime data and escaped for control lines and Markdown;
arbitrary recipe arguments, source/destination URLs, file contents and environment values are not dumped.
The exact producer argv remains in the canonical Plan for deliberate local inspection. Packaging reports the
actual bound image digest, and publication reports its persisted immutable reference. Logs and summaries remain
diagnostics, not authority receipts, completion evidence or qualification.

Generated workflows write at most two optional summaries per Run attempt. One deterministic runner-bootstrap
job writes a brief opening after native admission and before checkout. The existing always-run lifecycle
finalizer writes the closing summary as its last step, after the final Run record upload. Its standalone shell
uses provider job and step outcomes without requiring a checkout or helper. It counts required jobs by
`success`, `failure`, `cancelled` and `skipped`, and reports the finalization outcome without claiming an
authenticated Run or Release verdict. Missing or unwritable summary files cannot change the result. Runner
loss, force cancellation or a pending deployment environment may prevent the final summary from running;
two summaries are a ceiling, not an availability guarantee.

Strict native disk inspection retains the sanitized bounded progress log in one atomically published, no-replace regular file beneath
the original held workspace. The helper drains progress into a package-owned memory collector and writes to
caller log sinks only after the owned operation stops. The generated one-day, always-run diagnostic upload
has a five-minute timeout and is optional and failure-tolerant; only its exact output-bound descriptor may tolerate a missing file. Required
evidence and payload uploads remain mandatory. Retention failure is reported with a bounded, quoted cause and cannot replace the inspection verdict.
Temporary siblings are not upload targets; publication happens only after complete writing and file sync.
An existing destination is rejected, not overwritten or authenticated as this attempt's log; operators must
retain the ephemeral, single-job workspace policy. Failure snapshots are folded in job logs with the same
truncation marker as retained files, and logs show the last accepted inspection phase. Successful operations
do not dump the snapshot. Graceful-timeout retention does not promise recovery after runner loss or `SIGKILL`. The explicit M2
build-provenance mode emits neither inspection work nor its diagnostic upload. Changing the helper requires
rebuilding and repinning its complete inspection image before claiming hosted operation.
Retention uses a separate ten-second cleanup context with cancellation checks between filesystem operations;
an already-entered regular-file syscall cannot be interrupted, so this is not a hard filesystem deadline.

The compiler creates a private release preparation, a production SBOM-attestation stage producing the
final release package, and one publish/sign/verify route. The attestation stage binds and verifies each
CycloneDX document before final release identity is derived. The private preparation is disposable: delivery
consumes its unchanged payload files into the final layout after preparing the signed evidence, with the
ownership and retry contract in ADR-20 §6. It adds one internal handoff. GitHub handoff artifacts remain
internal job transfers, not additional deliverables. Product names do not cause duplicate copies of the full
closure to be uploaded or published.

Generated steps invoke `factory github run-node`, `restore-inputs`, `pack-outputs`, or `recover` against
the complete canonical Plan and an exact node or derived stage selector. The factory module admits the
complete graph and capabilities before work, derives carrier membership and paths, and owns progress,
optional per-operation summaries for explicit API callers and utility-container launch. Generated CLI
operations do not write per-operation summaries. Utility nodes share a launcher with local execution; the
inner helper checks the outer helper's exact Plan digest before executing. Provider YAML retains topology,
permissions, source credential scope, pinned actions and explicit operator setup. Packing remains a separate
step from source fetch so it receives no source credential. No authored transport manifest or arbitrary
command argument is added.

Each stage downloads one exact set of external input carriers, then restores each required carrier in one
helper step. Missing or invalid carriers fail before consumer execution, including when a provider batch matches only
part of the requested set. Restoration holds the opened carrier inode and streams extraction into a private
staging tree while hashing every carrier byte. It then rehashes that same held inode and checks unchanged
metadata before publishing any extracted root. This uses no additional full-archive snapshot; the retained
carrier and extracted tree coexist until validation completes. Archive traversal, entry and byte limits,
no-follow access and atomic no-replace publication remain mandatory. Carrier consistency is transport
validation, not artifact or release authority; consumers still authenticate their own exact closures.
After a successful restore, the helper deletes that exact regular carrier file to
avoid retaining both the archive and its unpacked contents. Failed and unrequested carriers remain untouched.
Each operation prepares its outputs in one step and retains an independent upload
for each exact carrier. Carrier-name/path collisions fail before rendering; no wildcard imports unrelated
artifacts. Logical commands, transport authentication and isolation remain unchanged.

The legacy finalizer batches downloads with the exact set of all planned carrier names. One recovery helper visits
every carrier and records its producer and restore outcome separately; it continues after missing carriers,
failed producer jobs and returned restore failures. The recovery log names every outcome. A successful batch
download or recovery step proves no completeness: the finalizer consumes those unchanged per-carrier records
and authenticates the complete output and evidence closure. The helper accepts bounded `toJSON(needs)` data
through a step environment value, with an exact stage/bootstrap job set, known statuses, duplicate rejection
and bounded parsing before filesystem work. Unknown, missing or malformed job results fail closed. Individual
carrier failure does not stop later recovery; cancellation or failed outcome persistence does.
With `publication.mode: per-output-v1`, the compiler emits independent output publication routes and a
metadata-only aggregate operation (ADR-20 §6). Output publication packages cannot be grouped with other
utility nodes, so grouping cannot recreate an all-output payload download. Offline packaging receives only
the public GitHub workflow/run identity variables, never registry, source or OIDC credentials. Each output's
verification exports a bounded metadata carrier. The finalizer downloads the aggregate metadata,
published reference, signing record, predicate, verification record and pinned cosign tool. For the complete
qualification handoff it also restores each output's original publication reference, signing record and
predicate; these bounded metadata carriers do not download output payloads. Provider-result
admission still covers every stage and bootstrap job; any failed, skipped or missing required job prevents
completion. Assembly and finalization have registry-read permission and no OIDC minting or package-write
permission. All credentials and signing remain outside ARC producer jobs.

The top-level workflow keeps each named job, exact dependency set, runner, timeout, permission, protected
Environment and execution condition visible. Native-platform preflight remains before checkout. After
checkout, shared local composite actions expose fixed semantic interfaces for node execution, source
execution, input preparation, image preparation, output collection and individual carrier transfer.
`execute-node`, `execute-source`, `prepare-inputs`, `prepare-images` and `collect-node-outputs` describe
operations owned by the factory. The generator selects these interfaces directly; it does not create a new
template for each observed step shape, group size or output count. A shared preparation action downloads the
selected same-Run helper and immediately restores its execute bit. The production templates live under
`.github/nvkr-run/actions/`; one canonical
`.github/nvkr-run/action-data.json` stores invocation values keyed by stable job/segment selectors.
The development layout uses the corresponding paths under `.github/nvkr-dev/`. Both are
exact manifest-owned bundle members. Templates retain fixed helper commands and literal catalog-pinned
provider actions; invocation data contains selected node IDs, carrier names, paths and other parameters,
never executable scripts or credentials. Parameters use named operation roles such as `execute_selector`
and `upload_with_name` rather than numbered step positions. They add no authored operation, new scheduling
authority or independently selectable execution input.

The internal `factory github describe-action` operation admits the complete Plan and recomputes the selected
invocation from the renderer before emitting bounded canonical JSON to the provider's step-output channel.
Each job's output is limited to 200,000 UTF-8 bytes, including `data=` and the terminating newline; this is
not a limit on the complete `action-data.json` file. Generation and runtime reject oversized output before
opening the provider output file.
Shared preparation selects the stable provider job through the exact `github.job` context binding.
The checked-in data file is an inspectable generated projection, not runtime execution authority. Templates
map its recomputed parameters through explicit environment values or action inputs; fixed shell commands
quote their parameter variables instead of evaluating generated command text. Caller-only expressions and
credentials retain explicit bindings outside this data. Bootstrap uses a shared family that does not depend
on the helper it is building. Host and job-container shells and distinct credential roles have explicit
interfaces. Grouped jobs invoke the same node interface repeatedly in execution order. Contiguous input
download/restore and image preparation occupy declared optional node slots; preparation separated from
execution retains its own interface. Folding cannot cross inline setup or provider-control boundaries.
Execution and output collection provide two optional carrier slots;
additional carriers use the same individual upload interface before the next node executes. Every planned
output retains its own ordered upload. Neither group size nor output cardinality produces another action
file. Absent optional work does not execute; supported transport defaults preserve the pinned provider
interface, while static materialization restores original field presence. Missing required work,
unsupported bindings or reordered operations fail closed through expanded-graph admission.

The workflow header points to fixed Plan and manifest paths instead of embedding a changing Plan digest.
Source-fetch scheduling keys and ordering derive from the build target and its actual consuming component
cohort. The exact Plan, producer IR, source identities and execution selectors retain their pin-bound IDs;
the node-to-job mapping connects them to the provider projection. Missing consumers, collisions and
incomplete projected graphs fail closed. Updating a source pin cannot reorder otherwise unchanged jobs or
regroup utilities merely because its content-derived fetch ID sorts differently.

Routine source, artifact, toolchain and generator-version updates preserve top-level
workflow bytes when the scheduling projection, labels, action interface and visible policy are unchanged.
Transport-action pin changes preserve workflow bytes only when every affected transport is extracted.
The closing summary reads the finalization outcome and final Run upload's outcome/artifact URL, so those
steps remain inline; changing that upload action's pin changes the maintained workflow.
Plan and manifest identities still change, with invocation data or shared action bodies changing when their
exact parameters, commands or pins change. Template selection does not depend on changing node/carrier IDs
or transport-action commits. Topology changes, including source-cohort splits/merges, and changes to the
checkout pin, runtime job containers/services, runner/capability routes, permissions, secret references,
timeouts, qualification or
inline operator setup may require a workflow diff. The full bundle must still regenerate and pass its
byte-exact check after every input update; workflow stability grants no exemption from admission or drift.

Native preflight, pinned checkout, operator-authored environment setup, timed producer execution and steps
whose outcomes or outputs are referenced by the caller remain inline. Keeping setup inline preserves its caller contexts and action-path environment; arbitrary setup
expressions do not become composite inputs. A controlled upload/download may use a local wrapper containing
exactly one pinned transport step. Its ID, condition, timeout and continue-on-error policy remain on the
caller, while carrier selections come from generated invocation values and transport pins live in the
shared action. Transfer parameters are resolved before the caller's controlled boundary; descriptor
preparation cannot disappear after a producer failure and silently suppress its diagnostic transfer.
A caller step-output expression, such as the sealed disk diagnostic path, is passed explicitly into that
transport's original input.
An always-run transport also retains `always()` on its inner step because GitHub re-evaluates embedded
conditions during cancellation; other wrappers keep the default inner success condition and remain
cancellable. Transfers whose own outcomes or outputs are referenced remain inline. Recovery uses the
standalone `recover-inputs` interface before referenced inline finalization; unreferenced adjacent recovery
and finalization can use `finalize-run`. This retains the provider boundary
for bounded disk execution and failure/always-run diagnostic transport; controls cannot hide a multi-step producer
sequence behind a different outcome. Remaining sequences preserve logical operation order and fail on the
same mandatory execution or transfer errors. Composite shell steps explicitly preserve the original Linux
default (`bash -e {0}` on the host, `sh -e {0}` in a job container), without
introducing `pipefail`. Job policies and the source checkout are not hidden inside generated actions.

The caller passes secrets and provider-result expressions as explicit action inputs. Each input is mapped
only to the original inner step field that consumes it: source credentials remain on source execution,
private-image credentials remain on preparation, and catalog credentials remain on catalog validation or
finalization. Packing and uploads gain no credential environment through extraction. Recovery receives the
caller's exact `toJSON(needs)` value; no matrix result or action output replaces the required provider-job set.
Actions are statically selected from the generated bundle, with no arbitrary command or action-path input.

Validation checks the closed shared-template and invocation-data set and expands its admitted references
and input bindings before applying the existing graph, transport, permission, credential and execution-order
checks. Exact regeneration additionally compares every generated member's bytes, including templates and
invocation data. An unknown, missing or changed template or data member cannot be accepted merely because
the top-level workflow is unchanged. This lowering replaces numbered YAML anchors and inline-map compaction;
top-level policy and dependencies remain
ordinary readable YAML. Complete, identical native-platform preflight, pinned checkout and shared job
preparation steps may use descriptive aliases: `check-native-linux-amd64`, `check-native-linux-arm64`,
`checkout-workflow-source` and `prepare-stage-<runner-artifact>`. Preparation retains the same caller step ID
and binds its job selector through `github.job` in each job. Scalar values, permissions, dependency sets and
execution/transport action calls are not aliased.

Producer matrix scheduling and general reusable `workflow_call` pipelines remain deferred. The separately
owned fixed native qualification worker and its exact selected-platform matrix are admitted by ADR-21 §4;
that exception does not alter producer scheduling. A producer matrix changes dependency
barriers and the provider-result shape used by finalization; a callable workflow adds execution-context,
credential and identity contracts. Neither is a presentation-only substitution for the current exact named
jobs. Future admission must preserve each named output's execution/evidence slots, failure behavior and
complete result accounting before either form is emitted.

Every generated run script, including scripts inside composite actions, remains checked against GitHub's
21,000-character ceiling, conservatively measured as UTF-8 bytes, including explicit operator setup and
bootstrap commands. The generator also rejects a workflow file over 500,000 UTF-8 bytes before returning a
bundle or writing generated files; validation and bundle persistence enforce the same bound. The limit follows GitHub's
[500 KB workflow-file ceiling](https://docs.github.com/en/actions/reference/limits#workflow-file-size);
files that remain oversized fail closed instead of generating an unstartable workflow.
Local rendering and expanded-graph validation do not establish provider execution of the composite layout;
reconciliation records which regression checks have actually run.
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
  pre-built" extends to `nvkr-exec` without pretending every execution performed a bootstrap.
- Adapters are genuinely thin (topology and transport); a new engine is a small adapter.

**Negative / costs**
- A self-hosting bootstrap adds startup time and a bootstrap-toolchain dependency.
- The runner must be a static binary that runs in any toolchain image and needs no network during a build.
- Passing the runner, the node file, and inputs/outputs between jobs is the adapter's transport responsibility.
- Some nominal CI configurations cannot meet the capability contract without operator-provided network policy or
  artifact storage; emission correctly fails for those configurations.

**Neutral**
- The runner is a mode/subset of `nvkr`. Maintained hosted steps use `nvkr-exec factory github`; its utility-container
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

Current contract evidence in `pkg/nvkr/pipeline/execnode_test.go` and `pkg/nvkr/pipeline/results_test.go`
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
