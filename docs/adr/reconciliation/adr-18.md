## Support by owner

Container runtime selection defaults to Docker; local Runs inherit their Environment and reject conflicting executable overrides before work, including dry runs.

Plan serialization shares exact package-input documents across all catalog, resolved-component and
fetch/build occurrences. The v8/v9/v10 wire codec restores complete recipes before ordinary Plan admission;
Build identities and separately named runtime requirements retain their original descriptions. Focused
contracts in `plan_package_snapshots_test.go` cover two/four requirements, byte-identical restoration,
digest/path/reference rejection, substitution of valid snapshots into the wrong producer, expansion limits
and the unchanged 8 MiB generation/read boundary.
This representation change supplies no new native assembly or hosted execution evidence.

The Plan-produced VM runtime extension has manager-neutral snapshot/package inputs, source-pinned mkosi
assembly, exact original installed-package report checks, measured projected files and subject-bound SBOMs.
It produces one uncompressed OCI layer and requires two fresh assemblies to produce the same complete OCI
closure before issuing a runtime production receipt. Epoch zero fixes generated dates and mtimes. A read-only
GNU tar adapter fixes traversal, numeric ownership and PAX header names; both the outer Python search path
and mkosi's nested search path select it. Focused tests include a real Python/GNU tar regression that reproduces
the adapter bypass when only the nested path is set.

Runtime production, evidence and use remain with the original Run owner. The use receipt binds the original
and relocated Plan, exact consumer instruction, produced runtime and completed terminal/SBOM evidence.
Private consumer staging rejects reconstruction, substitution, replay and unowned use. The owner revokes uses
before closing runtime resources, with cancellation/join and cleanup covered by focused race tests. The
original disk-backed layer remains held read-only until cleanup; no second retained tar or memory-backed
copy is admitted. Cgroup admission excludes only inactive file pages from usage and preserves host/ancestor
limits. Generated native disk jobs use an unprivileged preflight, a fixed scrubbed sudo coordinator and an
owned watchdog. Narrow input/work projections preserve plugin and firmware paths. Packer preparation stays
offline; the build uses the snapshot-pinned, unprivileged `slirp4netns` helper with fixed arguments, numeric
DNS projection and paired process lifetime. Shell and namespace fixtures cover these boundaries.
The launcher re-arms its parent-death kill after dropping credentials and checks a held original-parent
descriptor. A local native ARM64 Linux test under Docker killed the launcher in both offline and outbound
modes; both namespace children exited and a detached descendant stopped writing. This is process-lifetime
evidence, not ARM64 runtime assembly or disk-build evidence. Runtime SBOM extraction uses the original
coordinator's private scratch. After all execution owners close, selected carrier outputs and sealed diagnostics
return to the held workspace's owner; contract tests reject symlink escapes and shared root-owned files.

The four maintained amd64 disk consumers select separately named runtime requirements. Each native group
retains its own production, observation and SBOM; equal Build Digests do not permit cross-job receipt
restoration. Run completion requires matching manifest and complete-layout digests and sizes, with every
requirement's own evidence. Each disk result must also match its exact named runtime's observation, SBOM,
manifest, layout, layer, size and terminal record. Finalizers transport those consumer result records,
and focused substitution tests reject changed bindings. These descriptions grant no execution authority. The compiler removes
the unused container-image dependency from selected consumers and binds runtime production into their Builds.
Composite admission checks the declaring member before catalog work. Each disk publication requires its
contributing runtime's producer records and complete SBOM, while runtime payloads remain absent from
Release membership. Aggregate and per-output tests reject missing runtime evidence and check that a
per-output publication excludes its sibling's runtime evidence.
Maintained ARM64 disk consumers retain the existing image route.

The amd64 Packer producers use a separate source declaration without agent vendoring, pinned to the same
NVKE commit `2cfd00081b468d85a4894ec17e9deac13c9d8169` and tree
`a377abf5f0d584db9c86cc9981b2fdaa8bb4ac53`. Agent producers keep their original vendoring. The pinned
[Packer project](https://github.com/dsx-ai-factory/nvke/tree/2cfd00081b468d85a4894ec17e9deac13c9d8169/os-image/packer)
uploads the authenticated image-inputs archive; its guest provisioner installs `nvke-agent.deb` from that
material. Six source tests passed with Git-blob-verified scripts and no agent source tree. They cover exact
supplied agent-byte preservation through composition and both variants' staging, deterministic archives,
missing inputs, digest rejection and the templates' material-only host input path. Package bytes and metadata
tools were synthetic fixtures; these tests do not prove guest package installation or maintained disk equivalence.
The catalog contract separately checks all four Packer selections, matching source pins and retained agent
vendoring. The shared catalog change also changes the legacy combined Definition's amd64 disk Build identities.

Two independent native AMD64 proofs passed:
[first run](https://github.com/njhensley/nvkr/actions/runs/44303358758) at
`e575f0bd8879fed7f997987b60debe4dbe73c5d0` and
[second run](https://github.com/njhensley/nvkr/actions/runs/44305879063) at
`8e6e9863628becd283296ebdde1d1dab8a757580`. Both exercised namespace/device/resolver checks, original-owner
production, two fresh assembly closures, terminal observation, SBOM generation, Packer validation, QEMU guest
write and ACPI shutdown, consumer UID/capability checks, and an exact snapshot download over HTTPS through the
produced helper. The consumer service used a two-CPU quota, 5 GiB memory limit, 512-task limit and a separately
owned 20 GiB scratch filesystem. Host bootstrap utilities were recorded but are not snapshot-pinned.

Both runs produced runtime Build
`sha256:b146cd100a8abb76b2f48e15ef04231a026bb9879e003592f13d23c7d8cda5f7`, layer
`sha256:50b86b3c794640beeec4dfd90564fa65fceadb48621dae2e895cce6a6951753d`, manifest
`sha256:bbc6fe52b2697fae5af9b2031051aab1e3dace6b7287fea248beb30cb305eb3b` and complete layout
`sha256:6d01dd1f479d1031dc1789b99cff8d6b995b14144524f7c5b93aee5d1c483ed1` (1,942,467,503 bytes).
The retained inventories match all 39,422 entries, including order, metadata, content hashes and exact header
segment hashes. All mtimes are zero, user/group names are empty, numeric ownership is preserved and directory
entries are sorted. The complete three-document SBOM set also matches:
`sha256:2897fe011c1cc7381b1da91ebfa84a992c9291b933e1b514dafd1febd063bb9e`. Each original-owner export checks
its full evidence set against the completed-use digest. Runtime observations and terminal digests differ
between executions; they are bound separately and are not runtime payload identity.

The bounded QCOW2 is byte-identical in both runs:
`sha256:890f00ca2521cc18f880f75e9051799a46db8accc533bde052d2aa0243f20cd7` (393,216 file bytes, 8 MiB virtual
size).

The runtime layer comparison with the earlier epoch-zero
[diagnostic baseline](https://github.com/njhensley/nvkr/actions/runs/44290280188) matches resolved hard-link
payloads, modes, numeric ownership, symlink targets and retained extended attributes;
only the checked configuration and its recipe evidence change content. Archive ordering, owner-name encoding
and hard-link representation intentionally change. Secret-file comparison used whole-file hashes only.

These proofs establish independent runtime reproduction and a bounded BIOS guest build, not a completed public
Run or maintained NVKE old-image/new-runtime disk equivalence. The latter remains a required hosted development
gate using its existing registry-read boundary. Publication, signing, libguestfs inspection, EFI, ARM64 runtime
execution and any stronger evidence lane remain unproven by these fixtures. The recorded Runs retained
diagnostics and complete SBOM documents, not the OCI payload. Their fork-only proof workflow is removed
from the upstream change; hosted consumer proof uses the maintained development workflow.

The quick machinery E2E [fork Run](https://github.com/njhensley/nvkr/actions/runs/44043009475)
at `ee807779f8122a0cce1105853a0a82c88dbb4c56` passed on stock Ubuntu in 91 seconds (71 seconds
for the shared Make target): real hello build, registry round-trip, unprivileged smoke and harness
rejection of a self-consistent wrong-Plan predicate, with signing and verification in dry-run mode.
Its unsigned diagnostics do not exercise generated-job dependencies, carrier transport, real signing,
saved compiler-input reverification, disk/KVM, private-storage authentication or ARC; Podman remains unexercised.

Tekton implements ADR-18's explicit cluster trust contract. The provider authenticates
complete PipelineRun, TaskRun and PVC bindings and binds carrier bytes to successful task results. Controlled
TLS and carrier fixtures pass; a local kind v0.31.0 / Kubernetes v1.35.0 / Tekton v1.18.0 installation has
admitted diagnostic TaskSpecs. Those probes did not run a core Definition or authenticate published artifacts.
Generation, task execution, cluster-key signing, shared lifecycle smoke/qualification, manifest issuance and
verification, and the shared local/CI kind harness are connected in source. On 2026-11-09 the maintained
core Definition's complete native AMD64 selection (13 outputs) compiled and regenerated without drift.
The local Tekton v1.18.0 API accepted its 109-task PipelineRun in a server-side dry run and preserved the
complete execution specification. [Real keyed Cosign interoperability](adr-04.md) passed separately.
These checks do not establish an end-to-end Tekton build, qualification or release acceptance. The full
kind harness requires a native AMD64 Docker host with at least four CPUs and 6 GiB. Its local invocation
failed closed at the native-platform preflight (`got aarch64; emulation is not admitted`); the selected
daemon also reports two CPUs and 2,054,246,400 bytes of memory. The harness preserves that selected Unix
socket while isolating registry credentials. The same harness is wired into the AMD64 CI workflow.
The [2026-11-10 AMD64 run, attempt 2](https://github.com/dsx-ai-factory/nvkr/actions/runs/42759693766/attempts/2)
at `0f617667aa7b472a99e324ce398ca2ea424f20e1` completed all 110 tasks in the main PipelineRun, including
all 13 output publications, signatures and verifications, release assembly/publication/verification, native
smoke, independent qualification and finalization. The harness authenticated and extracted the complete
release manifest; complete carrier restoration, omission, corruption, substituted-manifest rejection and
Run cancellation checks passed. This is native AMD64 core acceptance under fresh test-only cluster keys,
not OS-image support, production cluster acceptance or exact-byte qualification.
That attempt's overall harness result was failure: the failed-prepare fixture retained an empty `args`
field that the API omitted, so exact specification authentication rejected it before authenticating the
failed task. The fixture now omits that field too; provider comparison remains exact. The recorded attempt
does not establish successful failed-task receipt authentication. Its first attempt failed on upstream Quay
HTTP 502/504 responses and was rerun on the unchanged head.

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 18 Execution and GitHub | Shared local/hosted ordinary-operation session (routing, direct-node admission and kind dispatch) and producer launcher, held source/vendor lifetime, capability admission, manual-only GitHub producer generation, exact optional publication-branch allowlist, separate full-development branch workflow, mode-specific concurrency and artifact retention, scoped credentials and unconditional finalization; inline named jobs with ancestor-preserving dependency reduction and bounded exact-result collectors with diagnostic hosted evidence; at most two optional summaries per attempt with operation details in logs; explicit native ARC disk grouping, raw build-only disk transport and optional native hosted platform maps (including separate CLI output slots); authenticated independent qualification invocation/source-event binding; generated fixed native worker and qualifier with separate credentials; authored public-hosted AMD64 NVKE build/Go E2E adapter outside product execution (the ARM64 published-artifact nightly adapter is retired); producer bootstrap and private-transfer helpers build the runner from checkout source for checkout-built generators, or install the generator's own exact module version (a release or a commit's pseudo-version) and require its recorded module version and checksum; an unstamped released generator labels its Plans and `--version` with that build-information release version, and an unstamped commit install with the revision its pseudo-version encodes | Production hosted qualification, retry qualification of the qualifier itself and independent-consumer acceptance remain unproved. The [development record](feature-branch-pipeline-development.md#feature-branch-pipeline-development) documents successful qualification of the earlier main-source candidate; hosted validation of the current v0.23.0 selection remains pending. The downstream adapter trusts privileged NVKE source frozen once from main and yields unsigned diagnostics; [completed hosted cycle evidence](consumer-acceptance-scopes.md#consumer-acceptance-scopes) records its exact NVKR/NVKE revisions and does not cover later edits or qualification. Default-branch push publication and in-repository PR build triggers remain deferred; arbitrary self-hosted/other-provider routes and qualified emitters fail closed. Provider configuration and fresh runtime evidence remain separate. The module-pinned bootstrap and build-information generator label have contract tests with injected build information; a local `go install …@<commit>` of the change rendered the commit-pinned bootstrap, but no tagged NVKR release contains it yet and no hosted Run from a repository without NVKR source is recorded. The release form's Go version is the toolchain that built the generator, so installs of one release built with different Go patch releases render different bundles |
| 18 GitHub job boundaries | Jobs are grouped by authority class and runner for one Build target or publication route. Registry credentials are scoped to their operation's step, a job's token scope is the exact union of its operations' scopes, and bundle validation rejects a release credential at job scope and a producer beside release authority. An Environment map of exact AMD64 and ARM64 BusyBox pins enables native observation, SBOM, payload packaging and publication-package preparation, with platform-specific Syft pins under one catalog tool name; release authority retains separate jobs and the default utility platform. All 11 maintained bundles regenerate as one flat workflow. Native utilities reduce gpu-site from 234 to 197 operation jobs and from 259 to 222 total jobs; all 87 Build Digests and producer graphs remain unchanged, while Plan and Environment identities change intentionally and 11 Build catalog snapshots gain the two Syft pins. Partitions engage only when the flat workflow fails provider size admission; contracts preserve exact membership and reduce the largest parser-event count across entry and workers | Hosted execution of the authority-grouped layout is unrecorded, including step-scoped release credentials, keyless signing and same-job remote verification. The 580-job flat [Run 39673173322](https://github.com/dsx-ai-factory/nvkr/actions/runs/39673173322) predates this grouping and shows only that an explicit job list above GitHub's 256-job matrix limit runs. Hosted wall time and helper-preparation savings are unmeasured. No maintained bundle exercises the partition fallback; when one depth layer dominates, partitioning does not relieve the byte ceiling, although it reduces parser events. Native ARM64 and emulated AMD64 Syft 1.51.1 scans of the same pinned executable yielded the same 275 normalized dependencies. Cross-compiled delivery writers produced byte-identical binary-bundle OCI files, with the corresponding NVKR SBOM differing only in scanner executable digest. These fixed-input probes do not establish full NVKE payload reproduction, hosted execution or publication. Native Debian inspection on a non-default utility platform remains gated pending a matching reader profile |

Image admission uses the admitted image-use route for factory utility, direct producer and source/vendor
consumers. The shared pipeline owner issues process-local receipts over subject, platform, config ID and
observed runtime; private misses fail without pulling, anonymous misses admit bounded materialization,
and factory launches reauthenticate with implicit pulls disabled. Contract fixtures cover prepared private
consumption, substitutions, eviction and runtime changes. This is not a reproduced maintained utility
outage, a live private-registry/runner exercise, qualification or production acceptance. Per-runner private
preparation and credential destruction retain their existing owner.

The dispatch-frozen multi-source route records exact commit/tree/timestamp selections and authored
input bytes in the Plan, preserves the executable bundle, restores the original selection on retries,
and replays exact commits through complete-composite qualification without re-reading main. The
isolated GPU-site candidate has its own generated producer/qualifier/smoke policy and 14-day uploads,
including runtime inputs, handoffs, outcomes and diagnostics. Contract coverage
is local only; full-candidate live-source freeze/replay remains unverified, and no hosted
floating-composite Run or publication is recorded. Registry access, protected
environments, signer admission and source/material retention remain maintainer configuration.

Stable worker naming and local finish-gap splitting have local contract coverage. Workers, caller jobs
and cross-worker exports use the lowest member job ID; capacity remains 64 jobs and 50 workers.
`TestGitHubPartitionRootAdditionChurn` renders the maintained Plan before and after inserting one root
and bounds the changed worker files to two. Split-boundary, namespace, exact bundle and inline-signer
contracts remain local evidence; the renamed worker topology has no recorded hosted execution.

GitHub runtime-input carriers contain exactly two files: a packed canonical Plan and the manifest.
The bootstrap stages them separately from the committed Plan tree;
the helper invokes the installer to admit and regenerate that tree. Bootstrap installation renders
the baseline and candidate once each. Stage installation verifies the saved baseline Plan and fixed
checkout members, then renders only the candidate. Secondary multi-source bootstraps combine the
runtime-input check and carrier staging in one render. Frozen JSON record addresses may change during a source-pin refresh,
while target membership, script bytes, consumer cohorts and non-Plan members remain fixed.
A vendor cohort split requires normal regeneration even when its script bytes and addresses are unchanged.
Packed-carrier and cohort-split refusal fixtures exercise these boundaries locally. The retained
`nvke-nightly` selection passes synthetic source-pin refresh with unchanged scripts and exact reconstructed
Plan bytes. The local source fixture restores and replays the frozen commit through the packed carrier. No hosted carrier
transfer with the tree layout has been recorded.
