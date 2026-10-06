# Architecture implementation and evidence

Reviewed 2026-09-21. ADR-01–22 are the current engineering proposals, pending named-decider ratification.
Each ADR contains its current lane-specific decision; no amendment chain is needed. Code/tests establish
implementation. Contract tests, recorded external runs, and organizational acceptance are separate facts.

## Current boundary

Public lifecycle disk-output support now includes Plan lowering and a process-local exact-QCOW2 observation
contract. A public `disk-image`
output explicitly names one QCOW2 root plus its complete transitive material closure. The build-graph resolver
has contract coverage rejecting missing or multiple roots, incomplete material closures and unrelated selected
components. IR v4 binds the root's disk and command/direct-Packer form, public variables, and exact source and
material producer dependencies into Build identity. A direct-Packer producer may also bind an exact source-owned
manifest schema and per-binding kind vocabulary into that identity. Catalog, IR and runtime contract tests reject
incomplete or extra mappings and preserve authenticated component, architecture, digest and deterministic staged
path fields while projecting those producer-visible labels. Plan compilation selects the execution class and matching
host-architecture VM toolchain, projects the raw QCOW2 artifact as the lifecycle output, and deliberately emits
neither binary-bundle packaging nor an ordinary per-component SBOM for the disk. Catalog disk/`vm-image`
producer metadata is accepted. The observer consumes one producer-owned held regular-file capability, rechecks
the exact successful producer result against the revalidated Plan, independently streams SHA-256 and size,
checks a bounded QCOW2 v2/v3 header, and records Build/Plan, producer, component, declared format and encryption
bindings in the Run's delivery observation. The header check is not a full decoder or compatibility verdict. It
neither decodes RAW nor grants evidence, compatibility, publication or qualification.
Local unencrypted direct-Packer execution and population of its held result are connected and contract-tested.
The public local lifecycle now reaches that held-workspace execution path, routes the disk root through held
terminal observation, and retains the exact observed QCOW2 at a deterministic attempt-owned path. The held
Packer capability rechecks its collection digest during the single export stream; file and mode metadata are
synced before binding and result close. A durable attempt marker covers disk and record commit, rollback, and
interrupted-attempt detection. The CLI reports the retained path.
One unqualified local Podman/Packer ARM64 NKE bake and boot smoke exercised that path using attempt-frozen draft
bytes. Committed-head Docker/Packer ARM64 and AMD64 bakes and source-owned boot smoke subsequently passed on ARC;
the exact inputs, digest, size and artifact links are in the [2026-10-01 evidence](../evidence/2026-10-01-nke-arc-os-builds.md).
Both jobs and the full workflow succeeded. Qualified execution remains
unexercised. A separate frozen native-inspection profile and original-live-owner reconciliation path has
implemented, guard-tested CycloneDX export and receipt-gated streamed OCI wrapper packaging. Synthetic
transport tests cover both native platforms; no successful original-live-Run receipt-to-package-to-hosted-finalization
test or production invocation is claimed. Local admission rejects the unwired publication graph before build work. The raw
diagnostic observation alone grants none of that evidence authority. Real full-disk NKE inspection, OCI signing
and publication remain unexercised; object-store publication and artifact-level disk compatibility remain
unimplemented. Deployment topology and cluster runtime qualification belong
to downstream owners. No existing NKE OS publication is claimed.
Bounded in-process cleanup covers success, returned failure and context cancellation. Cross-process recovery of
labeled Docker resources after `SIGKILL`, process crash or host loss remains deferred because no authenticated
owner-liveness protocol exists; the runtime does not perform an unsafe label-only sweep across concurrent attempts.
The same local lifecycle now issues an opaque attempt-directory diagnostic sink to the private direct-Packer
dispatch. It retains and digest-rechecks the exact material manifest before external runtime execution, then, when
the source contract declares a build-log directory, retains bounded regular files from only that directory on
success or returned build failure before cleanup. Unsafe paths, links, devices, duplicate entries and
immutable-path violations fail closed; operational log loss or count/size overflow records an incomplete marker
without discarding a separately verified successful disk. A failure before the build starts leaves the manifest
only. The sink closes before the immutable Run record, retention failure cannot mask the primary build error,
and no FDE secret channel is admitted.
These outputs are non-authoritative diagnostics. The boundary and lifecycle ordering are contract-tested.
[ARC runs on both architectures](../evidence/2026-10-01-nke-arc-os-builds.md) uploaded failed-run diagnostics,
including the material manifest after the corrected KVM check. The subsequent ARM64 and AMD64 runs also completed
QCOW2 collection, boot smoke and image upload. Earlier failures remain scoped to their exact candidates.
A staged diagnostic installed-package inspector binds held QCOW2 and exact `virt-inspector` and `qemu-img` probe executable
bytes, rejects encrypted or externally dependent envelopes, and uses a private read-only libguestfs copy with
explicit TCG execution. It rechecks the copy and held subject before returning an immutable installed-metadata
inventory. One retained ARM64 non-FDE disk yielded 730 installed packages in the
[non-root local replays of their recorded source blobs](../evidence/2026-10-02-nke-os-disk-inspection.md),
including the later checked-inventory rebuild and ELF test-helper corrections. This is not full-disk inspection or
CycloneDX: staged package/image archives and loose files, complete
acquisition records and semantic reconciliation remain absent. The original executable-only API enforces a
non-root caller but leaves the enclosing loader, libraries, emulator, appliance and kernel as caller-owned
runtime TCB. An optional concrete diagnostic runtime now admits an already-local exact OCI image, its held
Linux Docker/Podman executable and observed daemon identity. Its fixed launcher enforces non-root execution,
network-none, a read-only root, dropped capabilities, absent caller mounts/devices/credentials, bounded
resources and cancellation/cleanup. Native Podman commands disable automatic `mounts.conf` inputs on create,
start and owned cleanup;
daemon configuration, OCI hooks and FIPS mounts remain inside the trusted runtime boundary.
The opaque installed inventory binds the exact subject, tools and complete
runtime-reported image identity, not a repository-approved image pin or separately pinned runtime executable or daemon; the caller-selected runtime,
daemon and host kernel remain trusted. Docker is admitted by argument contracts but no Docker inspection
daemon run is recorded, and the adapter does not establish Docker `.Id` as a config digest.
The native Linux AMD64 and ARM64 contracts
are tested, and the [native ARM64 runtime exercise](../evidence/2026-10-02-nke-os-inspector-runtime.md)
inspected the retained disk's 730 packages and checked timeout/cleanup. That historical cancellation test did
not observe a running container first, so it does not prove the interruption phase.
The [later observed-running cancellation exercise](../evidence/2026-10-03-nke-os-inspector-review-fixes.md)
applies to its host-side test binary and unchanged inspector image, not all later helper changes. A subsequent
[mount-suppression and issued-inventory replay](../evidence/2026-10-03-nke-os-inspector-mount-suppression.md)
records separate host-side source and unchanged in-image helper identities, native ARM64 runtime,
successful SBOM-wrapper bindings and observed-running cancellation. Neither replay rebuilt the helper;
later helper XML/package validation and timeout-code changes are contract-tested, not exercised in that image.
The [Oct 5 native ARM64 launcher replay](../evidence/2026-10-05-nke-os-inspector-launcher.md)
exercised `--log-driver=none` and attached start by validated create ID, again using the unchanged helper.
Those launcher changes were not part of the earlier Oct 2/3 executions; Docker and native AMD64 remain
unexercised. Warning/no-ID late-registration cleanup is bounded contract-test evidence only.
These are retained-disk exercises, not acceptance of a later committed head. Native AMD64 execution, original
live-Run integration, registry publication and license/security clearance remain unproved. This is an optional
diagnostic capability, not a compiler Plan input or an authenticated production closure. The shared codec probe
explicitly selects the driver and rejects external references. The local lifecycle now optionally
joins the original still-open factory disk/material trace to an opaque installed-inventory receipt through a
concrete admitted inspection runtime, before export and Run close. The receipt binds the original attempt owner,
output, Plan, Build, exact QCOW2 digest/size and
runtime/tool identities. Binding, description isolation, serialization exclusion and post-close rejection
are supplemented by a process-wide capacity-admission permit held before factory snapshotting until
inspection, container cleanup and snapshot close complete. Separate runtime instances serialize their
admitted snapshots; this does not reserve kernel resources against unrelated processes or authenticate the
daemon's enclosing cgroup. The Packer handoff owns admission, original-inode snapshot creation, scanning,
snapshot closure and permit release as one registered operation; neither its snapshot nor its private
reservation escapes. Runtime close cancels and joins that complete lifetime before releasing capacity.
Capacity queueing uses the declared inspection
deadline, with host observation separately bounded to thirty seconds. Held-child, blocked-cleanup,
registration-race and an observed queue-deadline regression cover these contracts, not a real daemon scan.
The queue test checks the actual declared deadline and shorter caller deadline, then cancels and joins;
it does not impose a thirty-one-second wait on the regular test gate.
The held-runtime Linux regression exercises snapshot allocation, scan cleanup and subsequent admission,
but its controlled runtime executable is not an actual inspection daemon or original NKE bake.
The receipt bindings
are contract-tested with AMD64 and ARM64 subjects. Separate negative tests cover substitution, sibling replay,
changed bytes and cancellation using the AMD64 fixture. Default builds remain build-only. The opt-in SDK acceptance test exists, but an
original live NKE bake followed by this inspection has not been exercised on either architecture.
When opted in, inspection failure fails the local Run and discards uncommitted disk delivery bytes; it does not
leave a successful build without the requested inventory. Later failure also clears diagnostic inventories.
The observation join checks inventory output, Plan, Build, digest, size, format and encryption against delivered
disk observations. Tests cover commit failure after a successful scan and missing, duplicate or substituted
inventory subjects, ordered mixed-output inspection, second-disk failure without partial inventories,
persisted failure codes and joined scan/Close errors. Snapshot tests cancel during copying and confirm that
the original artifact remains usable. The acceptance harness uses the lifecycle package directly, not only `client/v1`.
The earlier aggregate Linux race run disclosed in PR #79 failed in the unchanged finalizer fixture's
read-only-source storage setup; its separate rerun passed. After the review fixes, the native Linux ARM64
race run of pipeline, factory, lifecycle and SBOM passed together with read-only source and private writable
attempt tmpfs. That run used the source at `0bd280dd6b5009761d226f241a3f80f83b95136e`
and `go test -race ./pkg/nkd/pipeline ./pkg/nkd/sbom ./pkg/nkd/factory ./pkg/nkd/lifecycle -count=1`.
The [hosted merge gate for that exact head](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37328812530)
also passed. These are contract checks, not a real NKE bake or a newly built inspector image.
Separate controlled tests exercise the concrete Packer assertion, capacity-before-snapshot operation and
private disk-only output loop. The `inspectLocalDisks` Observe/Validate/Description closure, exported factory
Observe/Validate wrappers and successful runtime-inventory binding projection remain unexercised pending
the real SDK acceptance run.
Diagnostic admission is QCOW2-only, and the shared codec rejects backing/data-file references. Shared VMDK decoding rejects
descriptor layouts before the probe and admits only single-file VMDK4 sparse inputs whose QAPI extent binds
the held input file (`/proc/self/fd/N`) and full virtual size, with an [actual ARM64 QEMU fixture replay](../evidence/2026-10-02-vmdk-codec-admission.md).
The shared probe pin must carry the CVE-2024-4467 QCOW2 data-file `NO_IO` fix; an arbitrary digest pin is not patch evidence.
The local lifecycle's bounded QCOW2 header handling still requires
separate external-data/feature admission.
Public lifecycle SBOM/publication and
qualified admission are not enabled by this API.
The diagnostic APT acquisition archive verifier checks canonical capture metadata
and the exact digest/size of every declared held package member without extraction.
The [dated ARM64/native and AMD64/emulated byte-correspondence record](../evidence/2026-10-03-apt-capture-correspondence.md)
is not acquisition-origin authentication, installed inventory, a live-Run binding
or a complete closure. The original live NKE installer handoff remains unexercised. The separate full-content
path retains same-attempt acquisition bytes and reconciles installed, staged and acquired identities under an
original-owner receipt in controlled contract tests; that path has no full native NKE runtime evidence.
The compiled graph preserves exact material-producer relationships. Diagnostic traces alone issue no mixed
producer evidence verdict, and neither the full-content nor transport path claims qualified disk reproduction. Reproduced or
digest-verified materials cannot upgrade a future wrapped aggregate disk; its exact QCOW2 digest would identify
one delivered Artifact until ADR-14's canonical disk comparison is independently demonstrated.
Generic from-source `buildRequirements` are schema-validated, bound catalog → build IR → Plan, and excluded from
build identity. Other node kinds reject the field until they have an explicit compiler projection. Environment
execution classes now declare routes, host platforms, resource capacity, capabilities, and timeout ceilings.
Compilation deterministically matches each selected requested build, records the exact binding and route in the
full IR and Plan, and Plan validation recomputes the match to reject substitution. These declarations are not
runtime proof. A process-local Linux diagnostic helper now observes cgroup-v2-capped CPU and memory, available
workspace capacity, host platform and KVM exposure relative to the executor's held workspace-directory
capability. Descriptor duplication is protected against concurrent close/reuse, write and traversal access is
checked, and every duplicate workspace or KVM handle is closed before return. The helper accepts no request or
binding and therefore does not authenticate Environment membership or deterministic selection, own an execution
deadline, reserve resources, prove a provider allocation, or authorize execution. A separate local VM admission
primitive validates and binds the exact current Plan bytes plus one VM node/request/execution binding to current
Linux facts capped by process affinity and complete cgroup-v2 CPU/memory limits. Cgroup membership is mapped
through one authenticated mount root and mountpoint; missing, ambiguous, or escaping mappings fail closed. It retains the held workspace and
selected KVM device and grants one deadline-scoped callback through an opaque process-local capability, with a
fresh capacity/KVM observation immediately before work. It rejects substitution, resource/platform mismatch, an
unavailable selected KVM mode, closure and reuse; a disabled resolution yields no KVM handle even on a KVM-capable host.
It remains unqualified current-availability authority: it reserves nothing, proves no provider allocation, and a
held KVM descriptor does not prove Packer/QEMU consumption. The local runner now has a private, contract-tested
direct-Packer dispatch boundary that requires the exact single-use capability before selection. Its run-owned
resolver binds the exact full-Plan subject, producer session, held local Docker executable, daemon identity and
already-local digest-pinned OCI toolchain after pure validation and before workspace mutation. The production local
path is connected only for unencrypted direct-Packer VM producers. It transports one authenticated bounded inner
tar through a one-file Docker carrier, verifies that tar inside the pinned toolchain before extraction, mounts inputs
read-only, fixes hostname/environment/user, denies image pulls, and never invokes `packer init`. Preparation and
validation always deny network. An Environment may independently admit a direct-Packer producer's explicit
`buildNetwork: default`; only its build container receives that mode. The request grants egress to the complete
build container and QEMU guest. It uses the runtime's default bridge, which also permits addressing the host
gateway and unrelated containers on that bridge, so it is not an outbound-only firewall and remains wrapped,
unqualified execution. An opaque process-local receipt binds the validated Environment authorization to the exact
networked node bytes before runtime preparation. The stage container runs as root with `CAP_CHOWN` and
`CAP_DAC_OVERRIDE` to authenticate and extract the input archive into sealed directories, then assign both
attempt volumes to the observed host UID/GID. There is no separate ownership bootstrap. Version, SSH,
validation and build operations then run as the observed host UID/GID. Other
current adapters still reject selected builds carrying requirements before execution or generated output. The generic
request covers resources, eligible host platforms, typed KVM policy, and executor timeout. VM-image authoring
requires all of those constraint classes. Required KVM is resolution-checked against a same-architecture eligible
host, while preferred and forbidden policies retain TCG routes. VM toolchain pins follow eligible host platforms,
require exact per-host entries, and bind the selected host platform into Build identity.
The repository does not yet supply production NKE numeric values.
ADR-10 permits exact-command, including Make, and direct-Packer producer drivers behind the same generic
`vm-image` contract. A dormant host-executable Packer boundary holds an exactly pinned Linux ELF executable, snapshots an
exact plugin-file closure, consumes an authenticated materialized source, stages only receipt-derived held
component bytes at their manifest-relative paths, runs Packer validation before one exact build selection, and
safely collects only the declared relative artifact. Its subprocess contract is tested with a fake executable
on Linux only. Its private state and environment are not a kernel sandbox; network denial, local-exec/filesystem
confinement, credentials, the dynamic-loader/library closure, and VM host admission remain outer-adapter gates.
Production instead uses the connected OCI runtime described above. It requires the exact Plan-authored Packer
version, sets a fixed plugin path, relies on source HCL plus `packer validate` to reject a missing or incompatible
preinstalled plugin before QEMU, then builds one exact selector and holds one declared QCOW2. One real
Podman/Packer ARM64 NKE disk build and boot smoke is recorded in
[the 2026-09-23 local evidence](../evidence/2026-09-23-nke-arm64-local-os-build.md). The
[2026-10-01 ARC evidence](../evidence/2026-10-01-nke-arc-os-builds.md) records successful committed-head
Docker ARM64 and AMD64 builds and boot smoke through the local adapter. Generated hosted VM execution and qualified
execution remain unexercised. Encrypted direct-Packer execution
and every encrypted command form fail before resolver admission until a typed non-public secret channel exists.
A consumer currently runs as the invoking host UID/GID without subordinate-ID or user-namespace remapping. The
other container restrictions reduce exposure but do not prevent an escape from acting with that user's host
access. Hosted and qualified admission therefore remain closed until a stronger provider identity boundary is
authenticated and exercised.
A command producer without materials and a direct-Packer producer lower into IR/Plan, but command/Make VM execution
also remains disconnected until it has a held-output adapter. A material-bearing VM command producer remains unsupported
because the schema has no typed manifest handoff. Hosted and qualified VM execution remain fail-closed.

Ordinary `oci-hermetic` producers may consume explicitly selected component outputs through the native v2 material
manifest. Factory execution binds each exact Plan payload/result pair, requires canonical recorded prepared-source
and material-manifest digest identities, re-hashes and holds the exact regular-file payload bytes, and passes an
opaque closure to the pipeline. It does not independently authenticate those two recorded input identities because
the source and material receipts are not available at this boundary.
An execution-qualified result is rejected at this boundary because factory material observation does not own the
execution receipt needed to authenticate that stronger claim. The pipeline re-verifies and privately stages the closure at the fixed
`/nkd/inputs` ABI, mounts it read-only with the source while the command runs offline, and records the exact manifest
digest in the producer result. BuildKit, Nix, direct non-launch execution, and undeclared material edges fail closed.
One unqualified local Podman execution exercised this material path inside the complete NKE disk build using
attempt-frozen draft inputs. The newer source-owned NKE composer and committed NKD PR head were subsequently
exercised together in the successful Docker ARM64 and AMD64 ARC runs. Generated hosted VM and qualified evidence remain absent.

For the owner summary below, the network contract spans rows 10, 17 and 18: the producer request is a Builder
input, the exact Environment authorization is part of the public lifecycle identity, and compilation plus the
direct-Packer runtime enforce the admitted mode. A Linux ARM64 Podman execution produced and boot-tested one
non-FDE QCOW2 and recorded its digest and size. Docker ARM64 and AMD64 on ARC subsequently passed the same lifecycle
and source-owned boot smoke. Generated hosted VM execution remains unexercised, and FDE remains
unsupported.

Packer-only local Runs admit attempt-owned diagnostics; ordinary producers do not acquire that capability.
Missing, timed-out or oversized build logs record `logs-incomplete.json` without discarding a separately verified
successful QCOW2. Unsafe archive entries and immutable-path violations still fail closed. Manifest and nested
log directories are sealed before Run recording; partial extraction is rolled back. These failure contracts are
unit-tested; the existing ARC success evidence does not exercise each diagnostic failure mode.

The native ARC disk adapter contract now derives execution/job budgets from real producers, the selected VM
class and the strict inspection join, leaving separate cleanup headroom. Failure-only diagnostic transport
selects only one successfully sealed sink-issued route with bounded retention; early admission failures cannot
select workspace content. Both-native generation and tampering tests cover this projection, not a fresh
ephemeral-runner timeout or diagnostic-upload exercise.

The generated helper's shared-cache rejection is contract-tested. The trusted selector disables
shared Actions cache restoration and checks each selected Definition's Plan-derived source closure
with main's reader; future schemas must land in main first. Run `37043068395` exercised these selector controls,
Plan-derived boot-image selection and attempt-scoped uploads through trusted main workflow `91567c2`.
This does not validate later candidate workflow edits or restrict the
repository-scoped source secret. Main-scope shared-cache risk remains: candidate jobs dispatched through main
can populate caches subsequently restored by `merge-gate.yaml` and `vulnerability-scan.yaml`, whose
`actions/setup-go` steps still enable caching. Disabling those consumers' shared-cache restoration is a
separate main-workflow follow-up; the selector's disabled cache does not close that risk.
The mirror is required to equal an open, non-draft same-repository PR head. `/ok to test` is a maintainer convention,
not a workflow-authenticated approval, and mirror equality alone does not prove who wrote that branch.
Default-branch selection is a workflow-local self-check, not runner-group authorization. The recorded branch
probe reached both ARC KVM pools. Restricting those pools to selected workflows/refs and moving the source
secret into a main-restricted environment remain configuration follow-ups.

The hand-authored NKE OS validation workflow selects `arc-amd64-kvm` and `arc-arm64-kvm` for full validation.
At the 2026-09-30 review, NKE main used the ARM64 scale set and NKE PR #1100 selected the AMD64 set.
Those declarations alone did not prove access. The [2026-10-01 ARC evidence](../evidence/2026-10-01-nke-arc-os-builds.md)
records successful ARM64 and AMD64 builds and boot smoke in runs 36812260448 (`bb6e870`) and
36878232719 (`1f989e9`), and retains the earlier failure history. Both successful runs exercised the Docker
adapter's fixed daemon-side `/dev/kvm` projection with a same-kernel boot-ID and character-device-number guard
before Packer, retaining the admitted descriptor and API check.
The recorded workflow used a `chmod` fallback that changed a host-bind-mounted device beyond the runner pod.
The updated preflight instead requires existing read/write access and never changes device permissions.
KVM group access or a device plugin belongs in the scale-set configuration. The secret-free preflight in
[run 36929803967](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36929803967) executes the same
admission script at `336e6d7` and passes on both ARC architectures without changing permissions. Both
observed devices are mode 0666; this proves current access, not its configuration origin or a durable
permission baseline for newly provisioned hosts. It is not another image build or boot test.

The historical NKE direct-Packer source contract merged at commit
`5ac0ce2389c06d0291535fb6bf0f25be8a4ca0f2`, tree
`7d9b93c65c3cc8c2317b2953bca638c2203701e9`. The current development catalog instead pins NKE PR head
`726f76944404e1106626b8333d3576bdf67b575b`, tree `3d6ea77f85d5da2648038425f805a598e23464ba`, which owns the
composer, installer patcher and guest-memory input in NKE source. The recorded Podman bake used the older
`de0e46bb14cae2f892677bf9e21b91a7add2f9d9` source plus attempt-frozen draft NKD bytes, so it is not execution
evidence for the newer pin. The ARM64 and AMD64 ARC runs above exercise the newer pin with committed NKD bytes.
Approved published per-host toolchain image digests remain absent; the development pin is not a maintained
release or production-acceptance claim.

The repository-owned NKE OS validation workflow in row 18 is separate from generated hosted Run CI. Each main
push selects its exact commit for full AMD64 and ARM64 KVM builds, boot smoke and QCOW2 uploads. Automatic
run `36933187521` at `156d32c` passed both legs; run `36939569803` at `53ba88b` passed AMD64 but failed ARM64
during Go module acquisition, before Packer. The later push run `36944892985` at `91567c2` passed both full legs.
Candidate dispatch `36958996866` ran source `db4b8d9` through the trusted main workflow at `91567c2` and failed
both legs in the ownership-sensitive module-file Git guard. See the
[dated evidence](../evidence/2026-10-01-nke-arc-os-builds.md).
The corrected command uses exact module-file checksums instead of Git; Go-owned transport fallback, metadata
and ZIP faults, module-file drift, offline verification/vendoring, and failure-preserving workflow snippets have
contract coverage. Dispatch `37043068395` then passed both full builds, source-owned boot smoke and QCOW2
uploads at corrected candidate `d1e28ce` through trusted main workflow `91567c2`. Its downloaded evidence
records the exact image digest and size per architecture. This is unqualified diagnostic execution, not
validation of candidate workflow YAML or later execution inputs. The subsequent `GOENV=off` acquisition change
and its catalog digest have no recorded full bake; exact-candidate builds and boot smoke on both architectures
remain pending. Changed workflow steps still require
the first post-merge push and a clean rerun. Manual dispatch remains available for native
toolchain diagnostics or full image validation, on either architecture or both. The trusted main workflow defines
admission for an exact main commit or a mirror matching a current, open, non-draft same-repository PR head.
Candidate dispatches `36958996866` and `37043068395` exercised mirror admission. `/ok to test` is a maintainer convention,
not enforced approval or proof of who wrote
the mirror. Candidate code receives the pinned private NKE source contents and network-capable KVM execution,
but never the source credential; a separate fetch job alone holds it.
That credential is a repository secret, not bound to a branch-restricted deployment environment.
The local Docker authoring graph now includes AMD64 and ARM64 KVM Definitions and a source-locked toolchain
bootstrap that freezes the observed image ID into an attempt-local catalog overlay before Plan compilation. The
workflow's full-build selector checks the source-lock inputs rather than requiring a falsely stable Docker ID.
Both ARC jobs in run `37043068395` completed toolchain bootstrap, full QCOW2 build, boot smoke and image upload
at source candidate `d1e28ce` through trusted main workflow `91567c2`.
These establish diagnostic executions, not native toolchain byte reproduction or the generated hosted VM adapter.

The new held source-fetch path requests the exact selected repository rather than a retained origin. Git's own
bounded query refuses local callback/include/URL/worktree/promisor delegation before operational commands;
all held children restrict transports. Native ARM64 Git/descriptor contract tests executed these guards and
query cancellation. Local rootless Podman vendoring has a source-only root-protected system-helper admission;
it does not change shared closed PATH defaults or qualified runtime admission, and is not a digest-pinned helper
closure. Actual default-network vendor acceptance is tracked separately from modeled daemon tests.

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 01–03 Identity and errors | Canonical integer-only codec, frozen families, strict decoding, transport-free build identity, explicit execution-metadata exclusion, whole-graph admission, stable coded errors | Independent-language corpus for every signed family; external failures need continuing contract coverage |
| 04 Signing and consumption | Linux held-executable exact-byte signing/offline verification; OCI keyless adapter; scoped Release consumer with mandatory signature and production attestation, exact closure, identity/issuer and close lifecycle | Hosted keyless signature/attestation production and verification exercised; independent cosign-only and public Release consumption exercised; root rotation remains unexercised. Full v1 release and qualified consumer scopes fail before adapters |
| 05 Trust/membership | Subject-bound vectors, publisher evidence, required Inventory versus realized ReleaseSet; lifecycle freezes required slots before observation | Signed cross-process publication of all authority records; governed coverage profile is deferred |
| 06 Status | Status-owned diagnostic acceptance authenticates exact checkpoints/declarations, commits receipt-derived high-water state through protected reads and CAS, then interprets withdrawal | Unused release-specific trusted-time/clearance integration removed; production discovery/time adapters, rotation, v1 manifest tags, revocation and channel concurrency are unimplemented |
| 07–08 Reproduction | Linux stage-two receipts bind actual helper/runtime/image and terminal bytes; deterministic comparisons and profile matching | Other-platform authority, provider receipts, qualified helper-image admission, independent two-machine bake-off and causal profile graduation |
| 09 Provenance | Deterministic reproduction and distinct SLSA production predicates; exact plan/dependency/subject checks during hosted finalization; pipeline-attested acquired-image dependencies distinguish the authored parent transport locator and exact native artifact URI | Real signed hosted provenance and independent consumption exercised; ongoing provider/service acceptance remains operational; parent-bound qualified acquisition provenance is unsupported |
| 10 Builders | OCI source execution, digest-pinned acquisition and Docker/Podman rootless BuildKit wrapped OCI exports; complete upstream CWE Dockerfile builds and offline payload checks passed locally and from the generated hosted Run; `vm-image` command/direct-Packer structural validation and IR/Plan lowering; one-to-one component-material bindings, exact producer edges, public literal Packer variables, deterministic build-material manifest model with source-owned schema/kind projection, host-selected VM toolchain pins, and generic producer runner-requirement authoring; connected unqualified local direct-Packer OCI driver with exact full-Plan admission, authenticated source/material carrier, fixed Packer/plugin environment, validate-before-build and declared held-QCOW2 collection; optional producer `acquisitionArchive` schema/IR lowering and original-owner bounded carrier collection before cleanup (byte correspondence, not repository origin or installed-content authority); draft Podman ARM64 and committed-head Docker/Packer/NKE ARM64 and AMD64 execution and boot smoke are recorded; dormant held-ELF Packer boundary remains contract-tested with a fake executable | General hosted VM and qualified execution remain unavailable; the explicit native ARC grouped route executed both disk builds in run 37486058412; maintained release acceptance; material-bearing command/Make handoff and driver; successful full native disk inspection/SBOM evidence and complete measured NKE resource/capacity acceptance; qualified Nix closure observation, other wrapped adapters, fused large-output credential phases and `embeds` remain |
| 11–12 Resolution | Direct output-selected producers and concrete platforms; no Recipe authoring, matching, inheritance, mixins or deployment classification; concrete target admission | Embedded-version finite-set compatibility; accelerated-runtime rendering (formerly `pkg/nkd/aicr`) was removed and is deferred pending a concrete use case |
| 13 Evidence | The narrower native ARC build-provenance candidate retains exact live producer/material subjects and reports inspection as not performed; the proposed strict M2 target requires inspected evidence on both native architectures, pending named-decider ratification. Per-observed-artifact CycloneDX SBOM and per-build index; pinned offline Syft dependency discovery for binaries/images and Helm chart metadata discovery, including acquired Helm `raw` outputs through the bounded chart metadata inventory reader (referenced container images are neither scanned nor mirrored); exact payload/scanner inputs and shared packaging/finalization checks; one release index referencing required stage-bound CycloneDX attestation bundles beside their outputs and signed sibling output repository/digest bindings; internal indexes are not published; [all three real NKE artifacts inspected offline](../dependency-sboms.md); typed evidence and mirror-invariant qualified publisher evidence; process-local disk material-handoff retention retains successful live producer/material handoffs, independently observes the exact held QCOW2, and checks direct/transitive edges and shared producers; the optional local lifecycle inspection handoff uses a concrete admitted OCI runtime and issues an opaque original-owner/output/Plan/Build/bytes/tool-bound installed-inventory receipt before export/close; opted-in inspection failure fails the Run and discards uncommitted disk output; the diagnostic receipt has no packaging or publication consumer; binding, description isolation, serialization exclusion and post-close rejection are contract-tested with AMD64 and ARM64 subjects; separate AMD64-fixture negative tests cover missing/substituted consumption, sibling replay, changed bytes/runtime and cancellation; frozen native inspection profiles, original same-attempt acquisition-carrier retention, runtime-owned content snapshots and joined private scratch, pinned guestfish/Syft/dpkg-deb content-discovery paths, class-separated installer/staged/acquisition package correspondence, mounted-tree regular-file hashing, method-scoped per-artifact disk-content documents and operating-system disk-reconciled subjects, with contained components described by immediate held-carrier bindings rather than invented installed paths and every uncontained non-file component explicitly marked at its observation subject root, and original-owner exact-disk CycloneDX export receipts are implemented; modeled reconciliation, selected negative admission/decoder cases, private issuer-core success/rejection for both consumer architectures, queue/admission clock limits and export-owner revalidation are contract-tested; [native ARM64 controlled public issuer/export plus six negative issuer cases and concrete memory-profile rejection](../evidence/2026-10-06-controlled-public-disk-evidence-issuer.md) passed against original public owners (controlled ELF protocol, not Packer/Syft discovery) | Both native live NKE bake/material-trace/inspection attempts timed out in run 37445475840; no successful full inspection join is recorded on either architecture; contract tests use controlled producers/inspection seams and the opt-in SDK bake test has not run. The older staged-output diagnostic trace and installed-package metadata inspection do not inventory private source acquisitions, guest downloads, staged archives or loose files, and cannot admit a full-disk SBOM or publication; this limitation does not describe the separate full-content scan implemented above. Private issuer-core tests keep Plan/Run/payload/trace checks concrete but model stopped scan/profile observations; they do not establish full helper discovery; separate native ARM64 controlled public-owner coverage does not establish native AMD64 or actual NKE discovery. Mounted-tree export does not establish coverage of unmounted or unrecognized filesystem extents; nested gzip/plain-tar discovery accepts unambiguous Docker archives and complete OCI layouts, but rejects modern mixed Docker-save layouts, ./-prefixed OCI members and unsupported/foreign-platform attestation descriptors; nested Debian package databases in filesystem and container scans fail with CodeEngineCapability and a specific diagnostic until separately held. The helper and receipt path have no full native NKE runtime proof; native expansion and complete real-image inventory/document size, both original live joins, published inspector closure/license/security clearance and production publication remain unverified. The shared all-subject SBOM bounds are 32 MiB documents / 64 MiB signed bundles, with 16 MiB composition reports (oversize/malformed SBOM: CodeVerifyFailed/exit22; independent provenance remains 4 MiB/CodeProvenanceMissing/exit45). A controlled 50,000-file projection fits, but the 200,000-file structural ceiling is not a byte-fit guarantee; evidence fails without truncation. Top-level material hashes alone participate in file correspondence; nested payload file digests are not projected as installed files or matched material subjects. Private-volume admission authenticates actual capacity/local options but not nosuid/nodev mount flags or quotas; conservative RAM/scratch admission can reject a runner. Pinned GCR layout accessors still reparse bounded indexes for unique child/image lookup; repeated OCI image digests are scanned once per layout, not across material carriers. Docker entries and unique per-layout OCI images charge one shared 256-image allowance. Ordinary non-image gzip/tar candidates do not consume image-expansion/member allowance; their streaming marker probe is deadline-bound without a separate probe byte limit. NVIDIA Run 34540582090 independently verified signatures and provenance, but its SBOMs were outside the signed root. Run 34631274098 published and signed the new layout but failed verification when cosign selected an SBOM bundle instead of legacy SLSA provenance. Cosign 3.1.3 with consistently published Sigstore bundles has pinned-binary local interoperability coverage; a successful new hosted Run remains required. Qualified signed evidence-receipt publication, vulnerability scanner execution and advisory generation remain outside v1; acquired upstream signatures/provenance are not verified |
| 14 Delivery | Typed executable/APT/Helm layouts, including acquired Helm archives wrapped without changing their pinned bytes (exact producer/result checks and local four-chart NKE execution), and observed source Helm archives with exact producer checks and explicit image-package dependencies; Helm packages as a structurally standards-conformant Helm OCI chart artifact (real Chart.yaml-derived config, `application/vnd.cncf.helm.chart.content.v1.tar+gzip` layer, deterministic manifest annotations, chart-root name discovered from the source archive rather than assumed), contract-tested via generic OCI-layout readers (go-containerregistry, oras-go), with evidence subject bound to the manifest digest and inspection input to the chart-tarball layer digest; resource-bounded RAW/ISO measurement; Linux held-qemu existing-envelope verification; process-local held-QCOW2 exact-byte lifecycle observation bound to the revalidated Build/Plan, root producer and successful outcome; local direct-Packer result population feeds that observation under the same full-Plan subject; lifecycle-owned retention at a deterministic immutable-attempt path with held-source digest recheck, durable file/mode sync, explicit disk-plus-record transaction marker and rollback; draft Podman ARM64 and committed-head Docker ARM64 and AMD64 bakes produced, retained and boot-smoked QCOW2s; streamed exact-QCOW2 OCI wrappers with distinct native-platform manifest and raw payload identities, guard-tested original-live-owner reconciliation receipt admission, complete component evidence collection and synthetic held consumer projection; the successful live receipt-to-package path is not exercised | Source quickstart archive, produced-image binding, checksums and all three local chart renders exercised from independently verified hosted publications; unmodified `helm pull`/`helm show`/`oras pull` interop against the produced OCI artifact is not yet externally exercised (`helm.sh/helm` is not imported and no test drives a real helm/oras binary against packaged output). Image-bound chart packaging now fails closed on any sibling content under the wrapper root (correct, contract-tested), the earlier nke-v1 chart-producer record selects the source controller chart at controller/v0.1.1-rc.9; the M2 controller/chart selection uses its independent owning catalog. Definitions map image slots to outputs; catalog producers map digest values using shared document traversal, with no template rewriting or deployment-override enforcement; contract tests cover registry-independent package bytes and preservation of chart-owned checksum files without injecting binder bookkeeping into root charts, and real Helm rendered the pinned controller chart for two repositories. A new combined build/publication remains unexercised. RAW reproduction and disk compatibility remain outstanding; full native NKE reconciliation, signing/publication and independent disk read-back remain unexercised. Encryption and qualified packaging fail admission; role-scoped staging and deployment-role disk fields are removed; no masked-region support |
| 15 Air gap | Outside current scope; former package, CLI and tests removed | No mirror or zero-egress lifecycle implementation |
| 16 Release gates | Strict Inventory/ReleaseSet/PromotionPolicy documents, required membership completeness and authenticated ReleaseSet/policy diagnostics | Unused qualified authorization, gate receipts, waivers and publication clearance implementation removed; qualified release acceptance and reuse are deferred. V1 M3 qualification/M4 manifests are unimplemented |
| 17 Public lifecycle | Six objects; compiler-owned output joins, immutable attempts, failed requirements retained; context-first SDK; descriptions cannot restore executable/verification authority; disk output selection admits exactly one root and its complete explicit material closure without category/role labels; compiler projects the root's raw disk artifact as the required lifecycle output; exact held QCOW2 observation contract and local direct-Packer population path are implemented and contract-tested with producer, component, declared format, encryption, byte digest/size, Build/Plan identities and outcome; public local execution reaches the held VM path, closes the result, atomically retains the disk in its immutable attempt and reports that path; draft Podman ARM64 and committed-head Docker ARM64 and AMD64 Runs completed and their retained disks boot-smoked; compiler-owned raw disk output versus publication wrapper separation, disk-specific hosted observation and guard-tested live-receipt packaging/finalization APIs; producer-provenance grouped disk execution passed both architectures in run 37486058412; successful strict receipt-gated packaging is not exercised and local publication admission fails before work | The narrow generated native ARC route is exercised, but complete full-reconciled NKE disk signing/publication remains unproven; encrypted disks remain closed; superseded authoring and diagnostic APIs are removed; no backward-compatibility requirement or general resume protocol |
| 18 CI/execution | Shared helper launch; scoped hosted source fetching uses a pipeline-issued Linux workspace owner, retained checkout/Git descriptors, shared closed selected child environment, fetch-only source token/helper, empty descriptor-owned child HOME/XDG, disabled repository hooks/filesystem monitors and submodule recursion; pre-source authenticated private-volume vendoring preserves the pinned entrypoint/argv/network, rejects unsafe return members and joins container/transport and admitted-runtime cleanup before source receipt issuance; fetch IR has no CPU/memory/PIDs budget and vendor tmpfs has no byte-size cap, so container-capacity limits remain deferred independently of stage deadlines and return-carrier headroom; ordinary Local fetching is unchanged; held Podman runtime owns private empty configuration cleanup and binds observed mapping/service/OCI-runtime identity fields; typed storage selection supported by held inspector/direct-Packer owners, while complete-Plan execution rejects nonempty overrides before work; generic resource, host-platform, typed KVM-policy, and timeout requests propagated through full IR and Plan; Environment execution-class validation, deterministic hard-filter/preferred-capability matching, exact full-Plan binding and route, substitution/drift rejection, and fail-before-output adapter gates; compiler-only VM Plan with typed command/Packer inputs and material producer edges; point-in-time process-local Linux diagnostic observation plus exact-Plan local VM admission with current CPU, memory, held-workspace capacity and resolved KVM checks, retained descriptor capabilities, single-use semantics and the authored callback deadline; connected local direct-Packer OCI resolver bound to the full Plan subject, held runtime/daemon and already-local digest-pinned toolchain; authenticated bounded tar transport, read-only inputs, private work volume, fixed environment/hostname/host user with public passwd/group resolution, narrowly scoped authenticated extraction and volume-ownership stage, fixed plugin path, exact Packer-version preflight, validate-before-build, no init/pull, network-none preparation/validation, separately authorized build networking and held QCOW2 collection; lifecycle-issued attempt-owned retention of the exact material manifest and bounded source-owned regular build logs before cleanup, closed before Run recording with primary-error preservation; private run-owned Packer dispatch and local-scheduler disk-result population; draft Podman ARM64 and committed-head Docker/Packer/NKE ARM64 and AMD64 execution and boot smoke are recorded; explicit native ARC Docker grouped execution and producer-provenance packaging passed both disk jobs in run 37486058412; strict inspection attempts timed out in run 37445475840. Hosted GitHub Run generation with separate Merge Gate PR drift checks, manual-only triggers and default-branch execution with explicit, identity-bound `githubDispatchBranches` exceptions, bounded timeouts, non-canceling concurrency, branch-restricted deployment environment, named operations with compiled component/output/input context, helper-owned Plan-bound execution, failure-preserving phase logs and diagnostic job summaries, shared local/hosted utility launch, grouped offline processing, exact batched input transfer and per-carrier finalizer recovery accounting ([initial shell contracts](../evidence/2026-10-03-ci-log-readability.md) and [compact helper validation](../evidence/2026-10-04-ci-runner-encapsulation.md); generated artifact workflow not yet dispatched with the compact helper); a separate hand-authored NKE OS workflow declares main-push dual-architecture validation and manual dispatch (base push run `36944892985` passed both legs; candidate run `36958996866` failed before Packer; corrected candidate `d1e28ce` passed both full bake/boot legs in `37043068395`; changed workflow steps await post-merge validation) and uses a separate trusted source-fetch job and one-day digest-checked source handoff, declares two KVM pool labels and only unqualified diagnostic outputs; GitHub is the sole hosted renderer; unused GitLab/qualified renderers and their hidden bundle workflow are removed. Finalization checks job/restore success, terminal/SBOM/payload closure and production predicate | Both full jobs in the 2026-10-01 manual run `36812260448` succeeded, including boot smoke, exact digest/size observation and QCOW2 upload. Run `37043068395` exercised `d1e28ce`'s acquisition inputs only. The subsequent `GOENV=off` acquisition change and catalog digest still require fresh full builds and boot smoke on both architectures; candidate workflow edits await post-merge validation. These diagnostic runs do not establish provider isolation or capacity guarantees. Local direct-Packer consumers use the invoking host UID/GID without subordinate-ID or user-namespace remapping. A stronger hosted identity boundary, provider allocation proof, qualified runtime attestation, command/Make held-output adapter, encrypted-disk secret channel, general hosted VM adapter and successful full disk-specific SBOM/publication proof remain absent. The narrow operator-trusted native ARC disk adapter is implemented; it is not a qualified provider-isolation boundary. Held KVM availability is not consumption proof. Authenticated GitHub fork fetch, combined CLI/CWE/Helm builds, SBOM, packaging, GHCR publication, real keyless signing/verification and complete pipeline-attested Run finalization exercised. Qualified provider emission has no retained implementation; GitLab/Tekton/Argo generation and non-GitHub signing fail before output. The held source/vendor route is contract-tested locally; native Linux descriptor/Git execution and real pinned Docker/Helm vendoring require fresh exact-head CI/runtime evidence. Provider outage can prevent any Run record |
| 19 Artifact membership and deployment boundary | Exact required/observed output membership, build/component evidence joins and canonical release content subject; no authored cluster views, generated compositions/realizations, node roles, categories or provisioning interface | Downstream systems own deployment topology, placement, provisioning and runtime qualification; historical topology-bearing records grant no current authority |
| 20 Publication | Registry/local release navigation opens the complete held closure, projects explicitly derived output/evidence locators and observed executable/image/disk platforms, and renders integrity-only text/JSON/Mermaid with post-validation output focus; detached descriptions make no registry-availability claim; anonymous/authenticated loopback, copy/close, reference admission, private temporary-workspace cleanup, empty Helm build-subject arrays and Unicode-preserving JSON escaping contracts pass; the [built CLI navigated a Docker registry containing both actual NKE CLI bundles and SBOMs](../evidence/2026-10-03-release-navigation.md), with byte-identical local/remote descriptions (unsigned diagnostic scope); Environment publication prefix; one compiler-derived release signing route and distinct output repositories; catalog-owned tools; digest-only recursive OCI push/pull, except a Helm chart deliverable also advances two non-authoritative discovery tags (`<version>` and a deterministic `<version>-<digest>`) after its manifest is durably published at its digest; binary/APT/Helm and native-image consumer binding; held OCI-export import and unchanged payload identity from Run to Release; exact acquired OCI and Docker-schema-2 manifest/config/layer import and offline scanner transport; streamed QCOW2 OCI control roots, guard-tested original-owner receipt-gated evidence packaging APIs without a successful live packaging/finalization test, disk-aware release metadata and held consumer/registry closure contracts | Generated native disk builds and terminal byte checks passed on both architectures; deep inspections timed out. Producer-provenance disk packaging passed in run 37486058412, but its aggregate release-carrier download failed with ENOSPC before publication ([dated evidence](../evidence/2026-10-06-m2-release-carrier-transport.md)); complete hosted delivery and independent signed disk read-back remain unproven; object-store/CDN schema, publication and consumption are unavailable. Local registry and combined CLI/CWE/Helm GHCR publication exercised; independent private-package cosign and public consumer acceptance passed for all three published references. Unused qualified transport/root constructors removed; direct package/object-store publishers and qualified commit adapters unavailable; segment registration is deferred |
| 21 Generation | Exact Definition/Environment inputs recorded; deterministic manifest-last writes and byte-exact check rejecting unexpected generated files; SDK/CLI share generation owner; current lifecycle regeneration tests; explicit catalog-maintenance config, current-bundle admission, atomic pin bump plus regeneration/check preserving the owner's coded failures, prefix-collision admission for private-source transport before workflow output or pin mutation, opt-in GitHub candidate scheduling, read-only patch handoff and optional complete local diagnostic execution gate | Automatic PR publication remains capability-gated; scheduled/private-source service acceptance is unexercised; lock and generated files remain separate transactions; supported-provider transactions and complete same-checkout crash matrix remain |
| 22 Catalog pins | Strict catalog v3/lock v1, component target-derived artifact pin sets, per-module `tagPrefix`, bounded no-follow reads, catalog-owned source/artifact maintenance with no-op/check semantics and cancel/CAS-safe pin writes, invocation-scoped same-release source-role groups and maintenance republished-version refusal checked before commit without inventing derived provenance, chained whole-producer catalog overlays v3 and exact-pin toolchain substitutions for explicit producer sets; retired Recipe layouts rejected; explicit `pinSet`/`pinnedBy`, bounded YAML/JSON extraction, kernel-limited packed Git acquisition on Linux/macOS, shared library-backed version syntax, pinned-tree re-derivation, atomic source/acquired member updates and `derivedFrom`; explicit `nke-deploy-v1` membership and chart identity checks; bounded HTTPS/anonymous OCI Helm package pinning and byte-preserving OCI blob acquisition, covered by contract tests; exact Git --commit maintenance; native OCI/Docker image pinning and authenticated index/native acquisition binding under owner/component contract tests; source-owned releaseExecutable archive/member acquisition and provenance, logical sparse and physical expansion bounds | Selected capture source approval/protected-main reachability is separately recorded; recorded upstream layer acquisition and production native closure acceptance remain absent; `curatedBy` graph binding and aggregate SBOM curation edges remain deferred; pin-set loading requires pinned Git object access; the NKE v0.21.0 six-output Definition and generated workflow are wired, including scoped bootstrap/finalization Git access; external pin-set production acceptance remains unexercised |

The common positive consumer scope is `pipeline-attested` / `artifact-publication`. It establishes exact
published bytes and build evidence, not M3/M4 full-release acceptance. A stronger unavailable scope never falls
back. Static/dry-run evidence cannot satisfy either production or qualified policy.

Qualified interfaces require package-issued opaque receipts. Caller structs, paths, digest strings, parsed
records and injected success booleans remain descriptions. Authority crossing a process boundary requires the
owning signed protocol, never serialization of an in-process capability.

## Native ARC disk route

The current Makefile drift gate and the single checked-in `nkd-run.yml` select the M2 candidate Definition
and Environment, replacing the earlier six-output deploy selection at that fixed generated path. Dispatch
does not choose among Definitions, and no parallel generated six-output deploy workflow is maintained there.
The retained deploy and three-output inputs can be generated in a separate checkout with their matching
byte-exact check. This records the current configuration, not a ratified canonical-product/path migration or
publication-namespace decision; that named-decider governance remains pending.

ADR-18 now permits the explicit `githubARC: native-kvm-v1` operator-trusted policy for the two native KVM pools.
The grouped helper retains original material and disk owners through reconciliation and packaging; generation
contracts cover both architectures, individual-node bypass rejection and unsupported policy rejection. Source
fetch, bootstrap and release credentials remain outside ARC jobs. Native preparation/measurement is provided
separately. M2 now generates all 34 outputs with two native disk jobs from the same Definition,
an exact catalog overlay and measured native inspection profiles. Four toolchain images were
published to internal GHCR packages under the user's scoped candidate exception, with exact
manifest/config read-back. This is not completed organizational license/security review.
Private pulls use a separate packages:read step whose temporary credentials are removed before
execution; cached exact image identities are revalidated by the held runtime.
Both native builds and terminal checks completed in run `37445475840`; both deep disk inspections timed out.
The user earlier moved deep disk/content inspection and dependent reconciliation to M3. The narrower candidate
`diskEvidence: build-provenance-v1` selects live producer/material evidence with signed uninspected scope,
normal packaging/signing/publication and unchanged subject/provenance/membership/completeness gates.
The [inspection deferral record](../evidence/2026-10-06-m2-inspection-deferral.md) records the scope and validation.
The strict inspection path remains available. The subsequent October 6 proposed strict M2 engineering target,
pending named-decider ratification, requires both native disks to pass full inspection, acquisition/material
reconciliation and receipt-gated packaging. The narrower candidate does not complete that proposed target.
Both candidate disk jobs succeeded in run `37486058412`, but the aggregate release download failed with ENOSPC
before publication. The [dated carrier evidence](../evidence/2026-10-06-m2-release-carrier-transport.md) records
that failed attempt and the residual capacity dependency; full delivery and consumer acceptance remain unproven.
The [October 6 preparation record](../evidence/2026-10-06-native-arc.md) retains final-source
ARM64 and AMD64 Packer/inspector assembly, executable measurements, native tool validation
and unprivileged libguestfs appliance filesystem probes. The earlier preparation Environment admitted
`codex/m2-arc-validation` through `githubDispatchBranches` and its signer identity. The maintained authored
Environment now selects only `main`; external deployment-policy alignment remains pending and is not established
by this authored change. The source secret name exists. Package Actions read access, native pool capacity
and generated M2 execution remain external acceptance requirements.

## Maintained M2 input candidate

The combined M2 Definition selects 34 named outputs: the existing ten CLI/controller/chart/operator/disk
outputs plus twelve third-party runtime images for each native architecture. The human-selected image mode
uses pinned NKE deployment values, not chart defaults; optional chart-test images remain separate. Native
AMD64 and ARM64 subjects are recorded in the artifact lock; external acquisition/media
read-back is not retained in this repository. The separate source-owner checkpoint for NKE images outside the
disk remains an explicit ownership question. Configurable charts do not imply image delivery or topology.

The refreshed native image pin owner uses the existing pinned OCI library and bounded anonymous HTTPS
transport. It authenticates native manifest/config bytes, rejects ambiguous or substituted descriptors and
preserves OCI/Docker schema-2 subjects under contract tests. No external acquisition diagnostic is recorded
for this candidate; layer scans and publication remain separate acceptance gates.
PostgreSQL and SPIRE Bash retain their source-owned index digests and independently locked native
children. The owning lock retains both exact parent bindings, and the full IR and logical content identity
freeze parent/native/platform constraints. Built-in acquisition authenticates actual membership before
output and consumes the original selected image. Static public catalog/Resolve/Compile tests retain all
24 image rows and explicit opt-in pinned-source Git integration covers all 34 output graphs;
ordinary tests still check declared 34/24 membership without private-source credentials.
Missing or substituted parent descriptions still fail closed.
Controlled TLS fixture materialization is not recorded upstream layer, scanner or production release proof.

The OS, Calico and kubeadm source entries retain the capture-capable NKE commit.
The explicitly authorized temporary branch test pins only SPIRE to `726f76944404e1106626b8333d3576bdf67b575b`,
omitting the DSX IMDS plugin after both hosted downloads returned HTTP 403. This mixed-source test
candidate does not establish complete M2 payload acceptance. Pins are machine-written without creating a release tag. The selected merge commit `ff21ba8c07cafff74b68e238a8876af11e5beded`
is separately recorded as approved NKE #1211 and protected-main reachable in the
[merged-source handoff](../evidence/2026-10-05-m2-input-acquisition.md#merged-capture-source-handoff).
The native declarations follow its required composition architecture and variant-relative capture path;
source approval does not supply missing runtime/profile/publication acceptance.
The generic commit-pin path shares Git acquisition and atomic lock writing;
GitHub actions and release-tool sources remain release-tag-only. Operator acquisition shares the source
tool lock's separate archive and executable pins and the standard-library archive selector. Acquisition is
contract-tested; external Helm/kubectl acquisition is not recorded here. Archive URL provenance binds the
archive digest separately from the selected executable/member digest in the pipeline-attested lane.
The qualified emitter retains its existing bare prebuilt URI and does not bind the release archive/member;
qualified release-executable provenance remains unsupported. Logical sparse-file sizes and physical
expansion are both bounded, with cancellation checks during synthesized-member reads.

The [input evidence map](../m2-input-evidence-map.md) records exact pins, media, matrix obligations,
the separate candidate publication prefix, existing signing identity and remaining gaps. Native capture graph contracts depend on the
separately reviewed acquisition-carrier schema and runtime implementation. The rebased maintained-input
slice has focused acquisition, pinning and image-membership contract tests; full integrated and Linux gates
remain separate. Published approved toolchain pins, native execution classes,
actual capture-enabled bakes and production/consumer evidence remain required for M2;
the proposed strict M2 inspection/reconciliation target remains pending named-decider ratification and successful
both-architecture execution. No placeholder class/image
pin or fictitious full M2 acceptance is substituted.

## Output-only implementation

The current implementation uses Definition v4: named outputs select components and concrete target platforms.
Catalog v3 supplies pinned producer instructions without categories or
role affinity. The compiler derives each output's IR v4 build graph in the `nkd.build` family. Required output
and evidence joins bind the output name together with its build digest; equal build content does not waive a
second output's execution or observation requirements. Cross-output build reuse is not implemented.

Cluster views, tenant/operator types, deployment context and validation declarations, generated compositions
and realizations, deployment-role fields, the unused provisioning interface, and role-scoped staging manifests
are removed. Generic output completeness, independently observed payload checks, signer/receipt roles, exact
evidence membership, and noncircular release content identity remain. Downstream consumers own topology and
runtime deployment validation. Qualified compatibility/conformance requirements remain proposed contracts;
their unused receipt and authorization implementation is removed rather than retained as a second lifecycle.

Accelerated-runtime rendering (`pkg/nkd/aicr`, `aicrProfiles`, output-owned `aicr.bundle` settings) has been
removed; `canonical.KindAICRBundle` remains a permanently reserved, retired digest domain (ADR-01 §3, ADR-12
§2). Reintroducing it is a new decision, not an implicit re-admission.

The output-only migration introduced v3 plans, artifact graphs and lifecycle bindings. The locality migration
below advances plans/artifact graphs to v4 and removes lifecycle bindings. Public Runs/Releases, publication
roots and validation vectors retain v3; Inventory/ReleaseSet use v4; disk manifests use v2. The former release
authorization v9, reproduction-result v3 and evidence-waiver v3 implementations are retired. Changed media types follow their owning schemas. Old schemas and
removed fields are rejected rather than reinterpreted, including empty legacy fields. Retired recipe,
cluster/composition/realization and staging digest families remain reserved and cannot mint new identities.
Historical signed bytes and dated evidence remain unchanged.

Whole-producer catalog replacement remains separate from the removed recipe and deployment models. The
generator retires obsolete members only when a prior manifest owns their unchanged bytes and mode; unknown or
edited files fail before replacement. No lock pins, producer commands or NKE checkout changes are required by
this simplification.

Focused catalog/buildgraph/AICR/IR, lifecycle/factory, and delivery/release/assemble/canonical contracts and
race checks pass; their focused lint checks pass. Tests retain exact output/evidence substitution negatives
and add strict retired-field rejection, per-output AICR defaults, identity, argument mapping and ownership
checks. Full uncached native tests and `make check` passed, including formatting, vet, lint, ShellCheck and
race/coverage tests. Linux sign/pipeline race tests, both maintained generation checks, catalog/tamper demos
and diff checks passed. A stale AICR test fixture was corrected before the successful full reruns.

## Code locality migration

Run Plan/artifact graph v4 remove independent compiler handoffs and copied producer instructions;
production predicates use v2 buildType URLs and original Definition/Environment
identities. Release-content canonical bytes are unchanged and now have one delivery owner. Required and observed
output membership remain independent; shared result validation checks each exact planned producer and its bytes.

Full uncached native unit tests and `make check` passed, including the full race/coverage suite. Affected Linux
race tests, NKE/AICR generation checks, Actionlint, reference checks and diff checks passed. This refactor has
no new hosted or qualified execution evidence. Earlier operational records retain their exact schema, code
and acceptance scope.

## Code organization

The [organization audit](../codebase-organization-audit.md) records nine implemented changes: factory-owned
typed payload observation, status-owned diagnostic acceptance and persistence, catalog-owned pin transactions,
and focused files for catalog admission, package formats, compiler contracts, local scheduling and binary
bundles. Shared process-group primitives retain the caller's cleanup policy and held-input lifetime. The CLI
keeps argument/policy mapping and presentation for the extracted status and catalog operations.

Binary lifecycle observation now requires the compiled Plan and retained raw producer bytes, including exact
producer metadata; package/result agreement alone is insufficient. Frozen requirements, outer publication
membership and immutable attempt records remain lifecycle-owned. Status remains diagnostic at a caller epoch,
and pin maintenance retains the same lock schema, no-op/check behavior and CAS. The organization refactor
preserves its base's wire versions, logical identities and generated NKE/AICR bundles. The report maps each finding to its implementation
and contract tests and records final validation separately from historical operational evidence.

The refreshed stack retains the process-local held-QCOW2 observation contract. Disk outputs require factory's
concrete execution result before any pathname projection; binary, APT, Helm and image outputs retain the shared
package observer. Lifecycle stages and rechecks held bytes, binds the observation, closes the held result, and
retains the synced read-only QCOW2 and Run record under one explicit attempt transaction marker. After successful
rollback, returned commit failures persist a failed Run. Marker removal plus attempt-directory sync is the durable
success commit point; later handle cleanup cannot reverse it. A reused interrupted attempt fails with a precise
incomplete-state error. Public descriptions deep-copy disk metadata and the CLI reports the lifecycle-derived
retained path. The production adapter is connected and contract-tested; one attempt-frozen draft Podman/Packer/NKE
bake is recorded, followed by committed-head Docker ARM64 and AMD64 ARC builds and boot smoke in runs
36812260448 (`bb6e870`) and 36878232719 (`1f989e9`).

The broader Linux race gate exposed a preexisting `NodeSession.Close` cancellation propagation race: an
asynchronous callback could let a resumed producer reach runtime lookup after session cancellation. Close now
cancels registered call contexts synchronously before exposing cancellation, then joins producers before
releasing snapshots. Caller values/deadlines and parent cancellation remain intact; repeated node-session
contracts cover the correction. Final uncached native and Linux race suites, native static gates,
NKE/AICR byte-exact generation, the binary-bundle byte comparison and documentation checks passed. Linux
validation used an init process to reap terminated descendants; external integration fixtures retained their
explicit skips. No new hosted, registry or qualified acceptance is claimed.

## Unused implementation removal

The usage audit covered production packages, pipeline renderers, maintained inputs/scripts, SDK entry points
and external dependencies. Unused pre-September-11 rendering and library surfaces are removed; additions from
September 11 onward are protected. The removal includes the
GitLab and qualified renderers, hidden bundle workflow, uncalled graph formatter, unreachable qualified
transport/root constructors, closed test-only qualified authorization and vulnerability-report subsystems,
and test-only forwarding/description APIs. GitHub generation, shared execution, live diagnostic authentication
and actual OCI publication remain. Retired bundle, gate-result, waiver, clearance, trusted-time, vulnerability
and qualified-publication allocations stay reserved and reject minting. Full uncached native unit tests and
`make check` passed, with 70.4% race-test statement coverage. Affected Linux/arm64 race checks, byte-exact
NKE/AICR generation, Actionlint, offline dependency/notice checks, local links and diff checks passed.

The Linux gate exposed an existing thread-affinity defect in sandbox setup. Policy application and exec now
share one dedicated locked OS thread; failures retire that thread. The scheduler-handoff regression failed
five times before the fix; both thread regressions and actual filesystem/network probes passed 100 repetitions
afterward. This repairs enforcement of the existing policy. No new artifact build, hosted execution or signing
acceptance is claimed; previous external evidence retains its own scope.

## Verification of the public model

The earlier public-lifecycle migration passed formatting, full native unit/race suites, vet, golangci-lint 2.13.1 built with
Go 1.27, ShellCheck, diff checks, deterministic generation, and Linux/arm64 sign/pipeline race tests with
container init and network disabled. Tests cover:

- Output derivation and Environment/build identity separation, including AICR tool changes.
- Frozen required membership, missing/extra/substituted observations, immutable failed attempts and opaque SDK preparation.
- Actual local executable/APT/Helm layouts, complete synthetic GPU closure on both architectures, relocation and stale-package rejection.
- Reference/policy-scoped consumer positives, missing signature/attestation, wrong issuer/subject/lane and forged cluster-binding negatives.
- Hosted producer/restore failures, omitted terminal/SBOM evidence, exact production semantics and deterministic provider generation.
- Typed canonical digest-family separation. Superseded plan/manifest compatibility fixtures are removed.

These tests use controlled producers and private verification seams. They do not establish real GPU builds,
AICR execution, provider jobs, signing services, full-release qualification or qualified operational acceptance.
Earlier verification does not transfer by renaming or reinterpreting records.

## Recorded external evidence

| Record | What actually ran | Limit |
|---|---|---|
| [Guest package capture byte correspondence, 2026-10-03](../evidence/2026-10-03-apt-capture-correspondence.md) | Held ARM64/native and AMD64/emulated APT capture archives rechecked with the recorded verifier blob; exact archive and manifest-blob SHA-256 values plus one annotated follow-up log retained | Local byte correspondence only; original capture logs and archive files unretained in Git, AMD64 emulated, no NKE installer bake, live Run or complete disk SBOM |
| [Concrete local inspection runtime, 2026-10-02](../evidence/2026-10-02-nke-os-inspector-runtime.md) | Exact local OCI closure, held Podman/runtime/tool identities, non-root networkless inspection of one retained ARM64 QCOW2, 730 installed packages, timeout/no-retained-container assertion and native Linux race suites | Historical cancellation test did not observe the running phase; native AMD64 runtime and original live-Run integration are unexercised; no registry publication, NVIDIA license/security clearance, full-disk SBOM or qualified claim |
| [Inspector review-fix replay, 2026-10-03](../evidence/2026-10-03-nke-os-inspector-review-fixes.md) | New host-side pipeline test binary used the unchanged Oct 2 ARM64 inspector image; retained-disk scan and observed-running cancellation/cleanup passed | No rebuilt helper or runtime exercise of later in-image XML/package validation and timeout classification; one historical host-side source blob is unavailable for independent recovery; no full-disk SBOM or production acceptance |
| [Mount-suppression and inventory replay, 2026-10-03](../evidence/2026-10-03-nke-os-inspector-mount-suppression.md) | New host-side runtime and SBOM test binaries used the unchanged Oct 2 ARM64 helper; issued-inventory bindings, mutation detection and observed-running cancellation passed | Later helper subject-holding and timeout classification did not execute inside this image; no rebuilt closure, native AMD64 runtime, original live-Run integration or production acceptance |
| [Launcher and inventory replay, 2026-10-05](../evidence/2026-10-05-nke-os-inspector-launcher.md) | Patched native ARM64 host launcher with log driver disabled and attached start by create ID delivered 730 packages; exact issued-inventory bindings and observed-running cancellation/cleanup passed | Unchanged Oct 2 helper, not a rebuilt closure or later helper validation proof; no real warning/no-ID cleanup failure, native AMD64/Docker, original live-Run integration or production acceptance |
| [Shared VMDK codec admission, 2026-10-02](../evidence/2026-10-02-vmdk-codec-admission.md) | Opt-in ARM64 real-QEMU sparse and stream-optimized decoding, valid external-descriptor rejection and controlled probe/conversion markers | Earlier and linked follow-up replays retain their own exact source, executable and committed log identities; not ordinary CI, an AMD64 runtime result, an NKE image build or qualified reproduction |
| [Corrected local installed-package inspection, 2026-10-02](../evidence/2026-10-02-nke-os-disk-inspection.md) | ARM64 ELF contract tests and non-root libguestfs scans, external-reference rejection and actual appliance cancellation at each replay's listed source blobs | Original and later checked-inventory rebuild retain separate source, test-binary and complete terminal-log identities. Diagnostic installed metadata only, not full SBOM, runtime authentication or qualified reproduction |
| [Local installed-package inspection, 2026-10-01](../evidence/2026-10-01-nke-os-disk-inspection.md) | Historical ARM64 libguestfs diagnostic scan of a retained non-FDE disk, external-reference negatives and cancellation | Exact draft tree was not retained; not committed-candidate acceptance, full-disk SBOM, publication or qualified reproduction |
| [Acquired NKE Helm charts, 2026-10-01](../evidence/2026-10-01-acquired-helm-packaging.md) | Local CLI acquired and packaged four real charts; each OCI chart layer equals the downloaded bytes and lock digest | Unqualified local execution; no publication, signing, installation or acquired-chart dependency scan |
| [Cosign discovery failure, 2026-09-11](../evidence/2026-09-11-cosign-discovery-fix.md) | Run 34631274098 created SBOM bundles, published and signed the single-entry layout, then failed provenance verification; cosign 2.6.3 reproduced the exact error locally; cosign 3.1.3 passed standard bundle verification | Local-key interoperability and repository contracts; updated hosted keyless verification/finalization and independent GHCR acceptance remain unexercised |
| [Separated repositories, 2026-09-10](../publication-architecture.md#local-validation) | Actual NVIDIA deliverables published once into distinct loopback repositories, metadata/evidence root published last, full cross-repository pull and exact artifact reference projection; absent copies in the release repository confirmed | Local and unsigned; GHCR permissions and hosted signing/finalization for the new locations remain unexercised |
| [Combined NKE hosted Run, 2026-09-10](../evidence/2026-09-10-combined-run.md) | All 20 jobs passed: CLI/CWE/Helm builds, observed artifacts, image-bound charts, GHCR publication, real signing, verification and complete Run; all three independent consumers and downloaded payload checks passed | Pipeline-attested artifact-publication scope; no qualified reproduction or cluster/service deployment |
| [Signed GitHub Run, 2026-09-10](../evidence/2026-09-10-github-signed-run.md) | All nine hosted jobs passed, real cosign signature/provenance verification, complete pipeline-attested Run and passing repository CI; 4m12s versus the 5m17s dry-run baseline | Independent cosign-only and public Release consumption passed after local GHCR login update; no qualified reproduction or full Release-scope acceptance |
| [Authenticated GitHub hosted Run, 2026-09-09](../evidence/2026-09-09-github-authenticated-run.md) | Private fork fetch, pinned CLI build, SBOM, packaging, GHCR publication, registry-digest verification and complete Run record; repository CI also passed | Static-unqualified with dry-run signing; no real signature/attestation verification or signed Release acceptance |
| [First GitHub hosted Run, 2026-09-09](../evidence/2026-09-09-github-run.md) | Actual hosted bootstrap, regeneration, helper transport and failed Run finalization | Internal NKE source fetch failed without cross-repository authentication; no hosted build, publication or signing success |
| [Maintained demo audit, 2026-09-09](../evidence/2026-09-09-demo-audit.md) | Fresh NKE build/publication/recovered CLI, focused CLI demos, provider generation; real AICR render, observation and Helm packaging; old worker-component attempts exposed stale demo scope | Local unqualified and generation evidence; the OS build owns embedded workers, its NKD wrapper is deferred, and at that time no signed publication existed for positive consumer acceptance |
| [Current local lifecycle, 2026-09-08](../evidence/2026-09-08-m1-public-lifecycle-local.md) | Current NKE Definition/Environment, immutable attempt, pinned source build, SBOM, OCI publication/pull-back and recovered CLI execution | LOCAL / UNQUALIFIED; dry-run signing; no authenticated Release consumer or hosted acceptance |
| [M1 local mechanics, 2026-09-04](../evidence/2026-09-04-m1-local.md) | Pinned NKE CLI build, loopback OCI publication/pull-back, dry-run signing | LOCAL / UNQUALIFIED; `signatureVerified: false`; earlier input model |

Dated evidence retains its original commands and identities. Earlier M1 demonstrations have their own recorded
runs. Historical success is never transferred by renaming inputs or inferred for other demos and providers.

## Remaining decisions

Named deciders and organizational ratification remain outstanding. Deployment owners must define service
timeouts/retries, registry retention, release-tag concurrency/collision behavior, source-mirror access, protected
environments and transparency disclosure. Qualified owners must supply runtime/provider observations,
authenticated discovery/trusted-time services, measured loader/runtime closure where required, signed external
evidence handoffs, independent reproduction and offline recovery exercises.

Helper delivery is resolved conservatively in ADR-07/18/21: source bootstrap and pinned-image transport are
distinct choices; image identity alone cannot satisfy qualified executable/runtime admission. The qualified
image route remains unavailable until its receipt protocol exists.

## Historical repository validation

The records below precede the output-only simplification and retain the schema, inputs and acceptance scope
exercised at the time. Current migration completion is tracked above; these passing checks do not establish its
final repository gate or fresh execution result.

The cosign 3.1.3 migration passed the full native gate, uncached unit tests, Linux sign/pipeline race tests,
Actionlint and byte-exact generation. The official pinned binary passed local-key signature/SLSA/SBOM bundle
interoperability, missing-evidence and wrong-subject negatives, and absence of legacy attachment tags.
Identity extraction, output bounds and fail-closed cleanup have always-run contracts. Default yamllint has
the same 187 formatting diagnostics as the baseline. The [migration evidence](../evidence/2026-09-11-cosign-discovery-fix.md)
records the new pin and limits; hosted keyless verification/finalization with this pin remains unexercised.

The preceding SBOM-attestation change passed the full native `make check` gate and uncached `go test ./... -count=1`,
with 70.0% race-test coverage, actionlint and byte-exact workflow regeneration. Linux sign/SBOM race tests passed
with networking disabled. An offline synthetic cosign 2.6.3 experiment used executable digest
`sha256:7c78a7f2efc00088bd788a758db6e0928e79f3e0eb83eb5d3c499ed98da4c4f4`, matching the catalog, to create a
standard bundle, recover its exact canonical document and verify an ephemeral local-key signature. That fixture
explicitly omitted transparency logging; production keeps transparency and identity checks enabled. Native
registry tests demonstrate attestation referrer discovery and absent copies in the release repository. Default
yamllint formatting findings remain separate from the passing release gate. No hosted attestation-only release
has been independently accepted yet. Run 34631274098 published and signed that layout but failed verification;
the [discovery fix](../evidence/2026-09-11-cosign-discovery-fix.md) records its cause and local validation.

On 2026-09-08 the consolidation/demo cleanup passed native `go test ./... -count=1`,
`go test -race ./... -count=1`, `go vet ./...`, formatting, golangci-lint 2.13.1 built with Go 1.27,
ShellCheck and `git diff --check`. Linux/arm64 sign/pipeline race tests also passed in Go 1.27 with container
init and network disabled. An initial restricted-sandbox `make check` could not bind three local test listeners.
The final complete `make check` passed with listener access and the compatible linter (63.3% total coverage).

The earlier NKE lifecycle generation check passed with manifest identity
`sha256:1b999d9b9dfe2c7a2688c4a82aafe3b20267cb6f9d13d74a554d0c9fee5192e3`. Both GPU architectures and both hosted
providers passed generation and byte-exact checks. NKE/GPU planning, the supported resolution matrix and its
unsupported-architecture negative, isolated pin-change/drift/malformed-lock demonstrations, and six tamper cases
passed. Release-consumer tests check path containment, hexadecimal tool pins and missing-policy refusal.
M1/consumer help and usage rejection also passed. Identity-rejection contracts remain in the verification
modules; the demo does not infer a specific rejection reason from cosign's generic subprocess-failure code.

Documentation checks preserve all 29 SADD template anchors and six diagrams, resolve local links in active
guides, and leave 22 primary ADRs without retired amendment references in active content.
Superseded plans, requirement exports and migration fixtures are removed; maintained Definitions and retained demo journeys use current lifecycle or focused catalog/identity
contracts. The [repository cleanup](../repository-cleanup.md) separates reference catalogs and test fixtures,
removes checked-in GitLab CI material, and records every domain package’s disposition.

The migrated M1 demo subsequently passed against the existing NKE clone and Docker loopback registry in 39
seconds; its exact identities and dry-run limitations are recorded above. The 2026-09-09 audit repeated that
journey and exercised real AICR rendering/Helm packaging. Attempts to run the old individual worker-component
demo exposed stale scope: the NKE OS build owns the whole embedded worker payload under the existing milestone
contract. Those component-build fixtures do not define the v1 delivery path. The NKD OS-image wrapper remains unexercised; positive signed artifact-publication consumption is recorded in the later evidence above. The later authenticated
GitHub Run recorded above establishes hosted CLI build/publication success with dry-run signing. Generation checks and
controlled contract producers do not establish those operational results.

The [GitHub configuration audit](../github-audit.md) replaces stale generated factory payloads with the current
NKE Run bundle, rejects extra generated files, and limits execution to default-branch manual dispatch. Its
local completion gate, uncached tests, Linux signing/pipeline/provenance race checks, workflow linters and
NKE/AICR regeneration checks passed. The subsequent first hosted dispatch passed bootstrap/regeneration and recorded an honest failed Run after internal source access failed; see the dated evidence above.

The subsequent authenticated fork dispatch at `c15e534` completed all generated jobs and published the CLI
to GHCR. Its repository CI passed. Source authentication is scoped to the matching fetch execution step and
retains canonical source/build identity; local and GitLab adapters reject this Environment option. See the
[successful hosted evidence](../evidence/2026-09-09-github-authenticated-run.md) for exact identities and limits.

Current authoring uses catalog source/lock entries for tool releases and Definition purpose selectors;
Environment publication is one untagged repository prefix with generated output routes. Tool asset pinning
was exercised against official Syft, cosign and AICR releases. Digest-only publication is covered by a local
registry round trip; the first NVIDIA hosted publication was independently verified, but the new single-entry SBOM-covered layout
requires a fresh hosted run. The current SBOM-attestation path has synthetic local-key format/verification
evidence with pinned cosign and Linux signing race coverage; these do not establish keyless/transparency acceptance.

GitHub action dependencies now use catalog source constraints and the same pin/bump lock writer. Run plans
carry resolved action commits; both GitHub renderers admit repository and major-version contracts before
output. The embedded action lock and separate action updater are removed. Migration retains the existing
four action releases, re-resolved from upstream; this is not a new hosted execution result.

The [demo language audit](../evidence/2026-09-10-demo-language-audit.md) exercised the current combined NKE
local build/publication/CLI-consumption path and AICR render/package/publication path successfully. Both Run
records are `unqualified-local-execution`; their plans are `static-unqualified`. Generation, catalog-change
and tamper demos also passed. M1 wording now distinguishes these local results, earlier real personal-namespace
signing, and the remaining current-namespace, trigger/helper-demonstration and governance acceptance gaps.
