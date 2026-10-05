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
unexercised. Full-disk SBOM generation, OCI or object-store publication, and
artifact-level disk compatibility are unimplemented. Deployment topology and cluster runtime qualification belong
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
CycloneDX: staged package/image archives and loose files, the factory-owned material-trace handoff, complete
acquisition records and semantic reconciliation remain absent. The enclosing loader, libraries, `qemu-system`
emulator, appliance `qemu-img`, supermin and kernel remain diagnostic runtime TCB, not an authenticated
production closure. A non-root caller is enforced; credential-free, network-less sandboxing remains a caller
requirement, not an authenticated NKD capability. The shared codec probe explicitly selects the driver and
rejects backing/data-file references; diagnostic admission is QCOW2-only. Shared VMDK decoding rejects
descriptor layouts before the probe and admits only single-file VMDK4 sparse inputs whose QAPI extent binds
the held input file (`/proc/self/fd/N`) and full virtual size, with an [actual ARM64 QEMU fixture replay](../evidence/2026-10-02-vmdk-codec-admission.md).
The shared probe pin must carry the CVE-2024-4467 QCOW2 data-file `NO_IO` fix; an arbitrary digest pin is not patch evidence.
The local lifecycle's bounded QCOW2 header handling still requires
separate external-data/feature admission.
Public lifecycle SBOM/publication and
qualified admission are not enabled by this API.
The compiled graph preserves exact material-producer relationships, but no current disk path issues a mixed
producer evidence verdict, performs content validation, or claims qualified disk reproduction. Reproduced or
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

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 01–03 Identity and errors | Canonical integer-only codec, frozen families, strict decoding, transport-free build identity, explicit execution-metadata exclusion, whole-graph admission, stable coded errors | Independent-language corpus for every signed family; external failures need continuing contract coverage |
| 04 Signing and consumption | Linux held-executable exact-byte signing/offline verification; OCI keyless adapter; scoped Release consumer with mandatory signature and production attestation, exact closure, identity/issuer and close lifecycle | Hosted keyless signature/attestation production and verification exercised; independent cosign-only and public Release consumption exercised; root rotation remains unexercised. Full v1 release and qualified consumer scopes fail before adapters |
| 05 Trust/membership | Subject-bound vectors, publisher evidence, required Inventory versus realized ReleaseSet; lifecycle freezes required slots before observation | Signed cross-process publication of all authority records; governed coverage profile is deferred |
| 06 Status | Status-owned diagnostic acceptance authenticates exact checkpoints/declarations, commits receipt-derived high-water state through protected reads and CAS, then interprets withdrawal | Unused release-specific trusted-time/clearance integration removed; production discovery/time adapters, rotation, v1 manifest tags, revocation and channel concurrency are unimplemented |
| 07–08 Reproduction | Linux stage-two receipts bind actual helper/runtime/image and terminal bytes; deterministic comparisons and profile matching | Other-platform authority, provider receipts, qualified helper-image admission, independent two-machine bake-off and causal profile graduation |
| 09 Provenance | Deterministic reproduction and distinct SLSA production predicates; exact plan/dependency/subject checks during hosted finalization | Real signed hosted provenance and independent consumption exercised; ongoing provider/service acceptance remains operational |
| 10 Builders | OCI source execution, digest-pinned acquisition and Docker/Podman rootless BuildKit wrapped OCI exports; complete upstream CWE Dockerfile builds and offline payload checks passed locally and from the generated hosted Run; `vm-image` command/direct-Packer structural validation and IR/Plan lowering; one-to-one component-material bindings, exact producer edges, public literal Packer variables, deterministic build-material manifest model with source-owned schema/kind projection, host-selected VM toolchain pins, and generic producer runner-requirement authoring; connected unqualified local direct-Packer OCI driver with exact full-Plan admission, authenticated source/material carrier, fixed Packer/plugin environment, validate-before-build and declared held-QCOW2 collection; draft Podman ARM64 and committed-head Docker/Packer/NKE ARM64 and AMD64 execution and boot smoke are recorded; dormant held-ELF Packer boundary remains contract-tested with a fake executable | Generated hosted VM and qualified execution; maintained release acceptance; material-bearing command/Make handoff and driver; disk inspection/SBOM and measured NKE host requirements; qualified Nix closure observation, other wrapped adapters, fused large-output credential phases and `embeds` remain |
| 11–12 Resolution | Direct output-selected producers and concrete platforms; no Recipe authoring, matching, inheritance, mixins or deployment classification; concrete target admission | Embedded-version finite-set compatibility; accelerated-runtime rendering (formerly `pkg/nkd/aicr`) was removed and is deferred pending a concrete use case |
| 13 Evidence | Per-observed-artifact CycloneDX SBOM and per-build index; pinned offline Syft dependency discovery for binaries/images and Helm chart metadata discovery, including acquired Helm `raw` outputs through the bounded chart metadata inventory reader (referenced container images are neither scanned nor mirrored); exact payload/scanner inputs and shared packaging/finalization checks; one release index referencing required stage-bound CycloneDX attestation bundles beside their outputs and signed sibling output repository/digest bindings; internal indexes are not published; [all three real NKE artifacts inspected offline](../dependency-sboms.md); typed evidence and mirror-invariant qualified publisher evidence; process-local disk staged-output tracing retains successful live producer/material handoffs, independently observes the exact held QCOW2, and checks direct/transitive edges and shared producers; missing/substituted consumption observations, result-file replacement, serialization exclusion and sibling-output substitution are contract-tested | The internal trace API has no lifecycle/CLI consumer and no exercised NKE trace; its tests use fake producers. The staged-output trace does not inventory private source acquisitions or guest downloads, does not inspect installed content and cannot admit a full-disk SBOM or publication. Complete acquisition tracing, inspection handoff, reconciliation and CycloneDX disk evidence remain unimplemented. NVIDIA Run 34540582090 independently verified signatures and provenance, but its SBOMs were outside the signed root. Run 34631274098 published and signed the new layout but failed verification when cosign selected an SBOM bundle instead of legacy SLSA provenance. Cosign 3.1.3 with consistently published Sigstore bundles has pinned-binary local interoperability coverage; a successful new hosted Run remains required. Qualified signed evidence-receipt publication, vulnerability scanner execution and advisory generation remain outside v1; acquired upstream signatures/provenance are not verified |
| 14 Delivery | Typed executable/APT/Helm layouts, including acquired Helm archives wrapped without changing their pinned bytes (exact producer/result checks and local four-chart NKE execution), and observed source Helm archives with exact producer checks and explicit image-package dependencies; Helm packages as a structurally standards-conformant Helm OCI chart artifact (real Chart.yaml-derived config, `application/vnd.cncf.helm.chart.content.v1.tar+gzip` layer, deterministic manifest annotations, chart-root name discovered from the source archive rather than assumed), contract-tested via generic OCI-layout readers (go-containerregistry, oras-go), with evidence subject bound to the manifest digest and inspection input to the chart-tarball layer digest; resource-bounded RAW/ISO measurement; Linux held-qemu existing-envelope verification; process-local held-QCOW2 exact-byte lifecycle observation bound to the revalidated Build/Plan, root producer and successful outcome; local direct-Packer result population feeds that observation under the same full-Plan subject; lifecycle-owned retention at a deterministic immutable-attempt path with held-source digest recheck, durable file/mode sync, explicit disk-plus-record transaction marker and rollback; draft Podman ARM64 and committed-head Docker ARM64 and AMD64 bakes produced, retained and boot-smoked QCOW2s | Source quickstart archive, produced-image binding, checksums and all three local chart renders exercised from independently verified hosted publications; unmodified `helm pull`/`helm show`/`oras pull` interop against the produced OCI artifact is not yet externally exercised (`helm.sh/helm` is not imported and no test drives a real helm/oras binary against packaged output). Image-bound chart packaging now fails closed on any sibling content under the wrapper root (correct, contract-tested), the maintained chart producer selects only the source controller chart at controller/v0.1.1-rc.9. Definitions map image slots to outputs; catalog producers map digest values using shared document traversal, with no template rewriting or deployment-override enforcement; contract tests cover registry-independent package bytes and preservation of chart-owned checksum files without injecting binder bookkeeping into root charts, and real Helm rendered the pinned controller chart for two repositories. A new combined build/publication remains unexercised. RAW reproduction, disk evidence, compatibility and publication remain outstanding. Encryption and qualified packaging fail admission; role-scoped staging and deployment-role disk fields are removed; no masked-region support |
| 15 Air gap | Outside current scope; former package, CLI and tests removed | No mirror or zero-egress lifecycle implementation |
| 16 Release gates | Strict Inventory/ReleaseSet/PromotionPolicy documents, required membership completeness and authenticated ReleaseSet/policy diagnostics | Unused qualified authorization, gate receipts, waivers and publication clearance implementation removed; qualified release acceptance and reuse are deferred. V1 M3 qualification/M4 manifests are unimplemented |
| 17 Public lifecycle | Six objects; compiler-owned output joins, immutable attempts, failed requirements retained; context-first SDK; descriptions cannot restore executable/verification authority; disk output selection admits exactly one root and its complete explicit material closure without category/role labels; compiler projects the root's raw disk artifact as the required lifecycle output; exact held QCOW2 observation contract and local direct-Packer population path are implemented and contract-tested with producer, component, declared format, encryption, byte digest/size, Build/Plan identities and outcome; public local execution reaches the held VM path, closes the result, atomically retains the disk in its immutable attempt and reports that path; draft Podman ARM64 and committed-head Docker ARM64 and AMD64 Runs completed and their retained disks boot-smoked | Disk SBOM and publication; encrypted disks remain closed; superseded authoring and diagnostic APIs are removed; no backward-compatibility requirement or general resume protocol |
| 18 CI/execution | Shared helper launch; generic resource, host-platform, typed KVM-policy, and timeout requests propagated through full IR and Plan; Environment execution-class validation, deterministic hard-filter/preferred-capability matching, exact full-Plan binding and route, substitution/drift rejection, and fail-before-output adapter gates; compiler-only VM Plan with typed command/Packer inputs and material producer edges; point-in-time process-local Linux diagnostic observation plus exact-Plan local VM admission with current CPU, memory, held-workspace capacity and resolved KVM checks, retained descriptor capabilities, single-use semantics and the authored callback deadline; connected local direct-Packer OCI resolver bound to the full Plan subject, held runtime/daemon and already-local digest-pinned toolchain; authenticated bounded tar transport, read-only inputs, private work volume, fixed environment/hostname/host user with public passwd/group resolution, narrowly scoped authenticated extraction and volume-ownership stage, fixed plugin path, exact Packer-version preflight, validate-before-build, no init/pull, network-none preparation/validation, separately authorized build networking and held QCOW2 collection; lifecycle-issued attempt-owned retention of the exact material manifest and bounded source-owned regular build logs before cleanup, closed before Run recording with primary-error preservation; private run-owned Packer dispatch and local-scheduler disk-result population; draft Podman ARM64 and committed-head Docker/Packer/NKE ARM64 and AMD64 execution and boot smoke are recorded; hosted GitHub Run generation with separate Merge Gate PR drift checks, manual-only triggers and default-branch execution with explicit, identity-bound `githubDispatchBranches` exceptions, bounded timeouts, non-canceling concurrency, branch-restricted deployment environment, named operations with compiled component/output/input context, failure-preserving phase logs and diagnostic job summaries, grouped offline processing, exact batched input transfer and per-carrier finalizer recovery accounting ([local contracts and step counts](../evidence/2026-10-03-ci-log-readability.md); changed rendering not yet provider-exercised); a separate hand-authored NKE OS workflow declares main-push dual-architecture validation and manual dispatch (base push run `36944892985` passed both legs; candidate run `36958996866` failed before Packer; corrected candidate `d1e28ce` passed both full bake/boot legs in `37043068395`; changed workflow steps await post-merge validation) and uses a separate trusted source-fetch job and one-day digest-checked source handoff, declares two KVM pool labels and only unqualified diagnostic outputs; GitHub is the sole hosted renderer; unused GitLab/qualified renderers and their hidden bundle workflow are removed. Finalization checks job/restore success, terminal/SBOM/payload closure and production predicate | Both full jobs in the 2026-10-01 manual run `36812260448` succeeded, including boot smoke, exact digest/size observation and QCOW2 upload. Run `37043068395` exercised `d1e28ce`'s acquisition inputs only. The subsequent `GOENV=off` acquisition change and catalog digest still require fresh full builds and boot smoke on both architectures; candidate workflow edits await post-merge validation. These diagnostic runs do not establish provider isolation or capacity guarantees. Local direct-Packer consumers use the invoking host UID/GID without subordinate-ID or user-namespace remapping. A stronger hosted identity boundary, provider allocation proof, qualified runtime attestation, command/Make held-output adapter, encrypted-disk secret channel, hosted VM adapter and disk-specific SBOM/publication proof remain absent. Held KVM availability is not consumption proof. Authenticated GitHub fork fetch, combined CLI/CWE/Helm builds, SBOM, packaging, GHCR publication, real keyless signing/verification and complete pipeline-attested Run finalization exercised. Qualified provider emission has no retained implementation; GitLab/Tekton/Argo generation and non-GitHub signing fail before output. Provider outage can prevent any Run record |
| 19 Artifact membership and deployment boundary | Exact required/observed output membership, build/component evidence joins and canonical release content subject; no authored cluster views, generated compositions/realizations, node roles, categories or provisioning interface | Downstream systems own deployment topology, placement, provisioning and runtime qualification; historical topology-bearing records grant no current authority |
| 20 Publication | Environment publication prefix; one compiler-derived release signing route and distinct output repositories; catalog-owned tools; digest-only recursive OCI push/pull, except a Helm chart deliverable also advances two non-authoritative discovery tags (`<version>` and a deterministic `<version>-<digest>`) after its manifest is durably published at its digest; binary/APT/Helm and native-image consumer binding; held OCI-export import and unchanged payload identity from Run to Release | No disk OCI control root, object-store/CDN schema, publisher, read-back proof or consumer. Local registry and combined CLI/CWE/Helm GHCR publication exercised; independent private-package cosign and public consumer acceptance passed for all three published references. Unused qualified transport/root constructors removed; direct package/object-store publishers and qualified commit adapters unavailable; segment registration is deferred |
| 21 Generation | Exact Definition/Environment inputs recorded; deterministic manifest-last writes and byte-exact check rejecting unexpected generated files; SDK/CLI share generation owner; current lifecycle regeneration tests | Scheduled bump/release automation, supported-provider transactions and complete same-checkout crash matrix remain |
| 22 Catalog pins | Strict catalog v3/lock v1, component target-derived artifact pin sets, per-module `tagPrefix`, bounded no-follow reads, catalog-owned source/artifact maintenance with no-op/check semantics and cancel/CAS-safe pin writes, chained whole-producer catalog overlays v3 and exact-pin toolchain substitutions for explicit producer sets; retired Recipe layouts rejected; explicit `pinSet`/`pinnedBy`, bounded YAML/JSON extraction, kernel-limited packed Git acquisition on Linux/macOS, shared library-backed version syntax, pinned-tree re-derivation, atomic source/acquired member updates and `derivedFrom`; explicit `nke-deploy-v1` membership and chart identity checks; bounded HTTPS/anonymous OCI Helm package pinning and byte-preserving OCI blob acquisition, covered by contract tests | `curatedBy` graph binding and aggregate SBOM curation edges remain deferred; pin-set loading requires pinned Git object access; the NKE v0.21.0 six-output Definition and generated workflow are wired, including scoped bootstrap/finalization Git access; external pin-set production acceptance remains unexercised |

The common positive consumer scope is `pipeline-attested` / `artifact-publication`. It establishes exact
published bytes and build evidence, not M3/M4 full-release acceptance. A stronger unavailable scope never falls
back. Static/dry-run evidence cannot satisfy either production or qualified policy.

Qualified interfaces require package-issued opaque receipts. Caller structs, paths, digest strings, parsed
records and injected success booleans remain descriptions. Authority crossing a process boundary requires the
owning signed protocol, never serialization of an in-process capability.

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
