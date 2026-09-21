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
Existing Helm image-output dependencies remain explicit packaging edges. AICR renderer settings belong to the
individual output and the generated IR v4 activation. The effective deployer and selectors participate in its
BuildDigest. Run-plan and artifact-graph v4 contain no cluster-composition nodes or records. Compiler handoff documents
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

### 3. Helper-owned isolation on generic hosted runners

Every job uses a generic hosted runner. The verified `nkd-exec` helper launches each pinned toolchain container
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
Missing prerequisites fail capability admission. No bespoke/self-hosted runner labels are emitted.

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
acquisition dependencies. The Plan selects an execution class, uses its host architecture to select the pinned VM
toolchain, and projects the raw disk producer artifact into the named lifecycle output without ordinary binary
packaging. A material-bearing command producer remains unsupported because it has no typed manifest handoff.
Every local and hosted execution or generation adapter still fails with `CodeEngineCapability` before source,
subprocess, or output work. Runner requirements are not VM-image-specific
metadata. The generic `execution.Requirements` shape carries minimum CPU, memory and ephemeral-storage capacity,
an optional sorted OR-set of eligible runner-host platforms, typed capability policies, and an exact
executor-owned wall-time budget. `kvm` is the first admitted capability with `required`, `preferred`, and
`forbidden` policies. Host platform is distinct from the IR target/toolchain platform. Network mode remains the
existing T1 IR field and is not duplicated in execution requirements.

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

Execution-class declarations are planning input, not runtime proof. Every current local or hosted execution
adapter still rejects a selected build with nonempty execution requirements before execution or emitted output.
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
no source, subprocess, output, or workspace mutation during admission, and no execution driver consumes it yet,
so existing adapter gates remain closed.

This is current-availability authority for a future local VM adapter, not a resource reservation, authenticated
provider allocation, cross-process receipt, or qualification evidence. Holding the validated KVM descriptor
proves availability through the callback lifetime; it does not prove Packer or QEMU consumed that exact
descriptor. Driver dispatch must enforce the selected device mode and use the supplied deadline context. A hosted
or qualified adapter must independently authenticate its effective provider or cgroup allocation and outer
deadline before it can admit work.

Future `vm-image` execution dispatches the typed producer driver owned by ADR-10. The `command` driver executes the
exact authored argument vector and can therefore invoke Make. The `packer` driver invokes the pinned Packer
executable against the selected source-owned HCL project without parsing or generating HCL. Both receive only
authenticated staged materials and declared variable bindings, use the same admitted execution class and network
policy, write into a private output location, and return the declared disk through the same collection boundary.
The shared runner owns this driver lowering. A CI adapter may transport its typed inputs and outputs, but it may
not render an independent Make or Packer command.

VM topology beyond the required-KVM same-architecture invariant, arbitrary devices, privilege, nested
virtualization, NUMA, accelerators, requirements on non-build nodes, adapter integration, qualified runtime
authentication, and provider-specific lowering remain deferred. Produced-artifact size remains an artifact and
transport budget. An adapter may not reconstruct producer commands, invoke an upstream workflow, or weaken a
request; a grouped provider job must satisfy every member node.

Generated Docker utility containers run with the host runner's UID/GID and a temporary home, matching the
local runner's ownership contract. Private package layouts must remain reopenable by later host transport
steps. Rootless Podman's existing user mapping is retained; the rootless BuildKit build container keeps its
separate fixed UID and private output-promotion contract.

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
through `pipeline.NodeSession`, and cancels and joins pending operations on failure. Hosted jobs enter through
`factory.ExecuteNode`, which dispatches producers to that same operation. Neither path converts a whole producer
report into a second scheduling result model.
Source receipts remain reusable; result slots and completeness still belong to each exact named output.
The generated Plan is the single executable input document; separate per-build graph files are retired.

Maintained lifecycle targets are local and GitHub. GitLab, Tekton and Argo selection returns
`CodeEngineCapability` before reading authored inputs or producing output. No fallback renderer exists.

| Event | Admitted work and credentials |
|---|---|
| Fork pull request | Regeneration/schema checks; no credentials |
| In-repository pull request | Build/package; read-only mirror credential on fetch steps only; no OIDC, push or environment access |
| Default-branch push or manual dispatch on that ref | Publish/sign/attest/verify inside a protected environment; phase-scoped registry and OIDC credentials |
| Git tag | No signing trigger |

The current generated Run workflow is manual-only, and execution requires dispatch on the default branch.
Pull-request regeneration checks run in the separate repository Merge Gate workflow when generator inputs
or outputs change. Default-branch push
publication and in-repository PR builds remain deferred. Live publication/signing/verification jobs name the
`nkd-release` GitHub deployment environment; operators must configure its deployment policy to admit only
the default branch. Only artifact signing and the pre-publication SBOM-attestation job receive OIDC minting permission.
The SBOM job receives no registry credentials or package permissions. The verifier needs registry read
access and obtains no identity token. Job timeouts are
bounded, and workflow concurrency does not cancel an in-progress publication. Terminal observation and Run
finalization still run after failures within an admitted execution. The generated Run workflow does not
start on pull requests.

Identity is one committed workflow path and default ref; verification rejects PR refs. Protect workflow,
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

GitHub job and step labels describe the operation and selected component or output. Grouped jobs disclose Helm
image binding when present; stable execution step IDs and exact commands retain the graph binding independently
of display names. Each execution step logs its start, node ID, successful outputs, elapsed seconds and exit
status. Failure preserves the command's exit code and cannot print a successful completion. Packaging reports
the actual bound image digest, and publication reports its persisted immutable reference. Logs are diagnostic,
not authority receipts, and generated progress messages never dump credentials or environment variables.

The compiler creates a private release preparation, a production SBOM-attestation stage producing the
final release package, and one publish/sign/verify route. The attestation stage binds and verifies each
CycloneDX document before final release identity is derived. It adds one internal handoff. GitHub handoff artifacts remain
internal job transfers, not additional deliverables. Product names do not cause duplicate copies of the full
closure to be uploaded or published.

The finalizer batches downloads with an exact set of planned carrier names. It still restores every carrier
and records its producer and restore outcome; a successful batch download alone proves no completeness.

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
- The runner is a mode/subset of `nkdctl`. Maintained hosted jobs use `nkd-exec factory execute-node` and
  share producer execution with local Runs. Qualified graph protocols retain the separate `exec-node` entry point.

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
