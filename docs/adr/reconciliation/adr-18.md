## Support by owner

Container runtime selection defaults to Docker; local Runs inherit their Environment and reject conflicting executable overrides before work, including dry runs.

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
| 18 GitHub job boundaries | Jobs are grouped by authority class and runner, normally for one Build target or publication route; bounded single-platform image publication batches may span compatible routes. Registry credentials are scoped to their operation's step, a job's token scope is the exact union of its operations' scopes, and bundle validation rejects a release credential at job scope and a producer beside release authority. An Environment map of exact AMD64 and ARM64 BusyBox pins enables native observation, SBOM, payload packaging and publication-package preparation, with platform-specific Syft pins under one catalog tool name; release authority retains separate jobs and the default utility platform. All three maintained bundles regenerate as one flat workflow each. Native utilities previously reduced gpu-site from 234 to 197 operation jobs and from 259 to 222 total jobs; the recorded same-Run source-sharing and bounded image-publication revision reduced that layout to 184 operation jobs and 209 total jobs without changing Plan or Build identity. For the earlier native utility change, all 87 Build Digests and producer graphs remain unchanged, while Plan and Environment identities change intentionally and 11 Build catalog snapshots gain the two Syft pins. Partitions engage only when the flat workflow fails provider size admission; contracts preserve exact membership and reduce the largest parser-event count across entry and workers | Hosted [Run 44270895414](https://github.com/dsx-ai-factory/nvkr/actions/runs/44270895414), at `8fdf3737`, completed on attempt 2 with all 209 final jobs successful, all 83 named artifacts/evidence verified and the finalizer reporting 83 observed outputs. One isolated disk blob-readback HTTP 403 required a failed-job-only retry; disk credentials, destination construction and publication actions were unchanged. This exercises shared-source execution, publication batches, step-scoped credentials, keyless signing and same-job remote verification for this exact development selection. The measurements and limits below include retry cost. The 580-job flat [Run 39673173322](https://github.com/dsx-ai-factory/nvkr/actions/runs/39673173322) predates this grouping and shows only that an explicit job list above GitHub's 256-job matrix limit runs. Helper-preparation savings have not been isolated from other job work. No maintained bundle exercises the partition fallback; when one depth layer dominates, partitioning does not relieve the byte ceiling, although it reduces parser events. Native ARM64 and emulated AMD64 Syft 1.51.1 scans of the same pinned executable yielded the same 275 normalized dependencies. Cross-compiled delivery writers produced byte-identical binary-bundle OCI files, with the corresponding NVKR SBOM differing only in scanner executable digest. These fixed-input probes do not establish full NVKE payload reproduction, hosted execution or publication. Native Debian inspection on a non-default utility platform remains gated pending a matching reader profile |

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

Same-Run source sharing and single-platform image publication batching are implemented in the GitHub
projection. The logical Plan and producer instructions remain intact. Source owners are keyed by exact
repository/commit/tree, with separate prepared closures for distinct vendor specifications and native platforms. Every consumer
admits its own source environment and native vendor runtime, verifies the restored seed, and rehashes its
private checkout. A differing vendor specification reconstructs the pristine pinned tree with a fresh
Git repository and full object-integrity check before vendoring. Seed configuration, attributes,
refs and index state are not imported; alternate object sources and links in the copied object store fail closed. Missing or changed seeds fail closed. Source-only owner jobs release their
carriers before producer work begins. A consumer depends on its owner's vendor success and downloads
that closure even when it only needs Git metadata. Same-target source fetches share acyclic seed
prerequisites; credentialed fetches stay separate. Tekton retains independent fetching. This does not
implement ADR-30 cross-Run output reuse.

Image publication batches preserve each operation's timeout, command, evidence and result slot, signing
subject, and step-scoped credential. They retain the 360-minute job ceiling and may split an output chain
across jobs. Each batch spans at most two output routes, ordered by output to limit payload fan-in. New dependencies must be acyclic; cross-output waiting and failure coupling are explicit.
`TestGitHubSharedSourcesKeepConsumerAdmission` and
`TestGitHubImagePublicationBatchesPreserveOperations` check the maintained development Plan's exact node
coverage and unchanged canonical Plan bytes. Seed-boundary and image-batch refusal contracts accompany
those projection tests. Linux source-seed tests cover closure drift, pin mismatches, missing receipts,
destination substitution, Git object corruption, attributes/sparse-checkout isolation and separate vendor
preparation. Linux CI passed these contracts at the hosted revision, including carrier round trips.

The successful development Run used 1,043.22 elapsed runner-minutes across both attempts, compared with
1,200.92 in [baseline Run 44138140727](https://github.com/dsx-ai-factory/nvkr/actions/runs/44138140727).
Its first-job-start to final-job-completion span was 117.52 minutes versus 135.23, including a 10.28-minute
manual/queue gap before retry. Durations include setup and transfers, exclude pre-start queue time, and
are not billed minutes. GitHub copied 204 earlier successful check records into attempt 2; those copies
are counted once, while all five jobs actually executed in the retry are included. There were 213 actual
job executions versus 222 in the baseline; the candidate's final logical job set contains 209 successes.

| Elapsed runner-minutes | Baseline | Successful candidate, including retry |
|---|---:|---:|
| Matching 16 node-source jobs | 91.88 | 75.70 |
| All standalone source jobs (16 baseline, 21 candidate) | 91.88 | 85.00 |
| Output publication (83 baseline executions, 66 candidate) | 469.40 | 389.47 |
| Release job | 36.75 | 15.35 |

The release job took 921 seconds versus 2,205 in the baseline. This is one recovered Run, with no
variance estimate or controlled causal attribution. Intervening main changes include the separate
[release-verification change](https://github.com/dsx-ai-factory/nvkr/pull/530); runner and registry variation
also affect the observation. At the hosted revision, source sharing/batching preserved all 11 then-maintained Plans and 481 Build Digests
relative to its main base. The current three-bundle Plan-tree layout has separate local contracts; this
historical Run does not exercise that layout. Relative to the older timing baseline, main changed the development Plan and five Build
Digests (`nktp`, `nvkectl`, `nvkectl-linux-arm64`, `operator-tools`, `operator-tools-linux-arm64`).
The same 83 named outputs verified in both Runs; 68 artifact digests match and 15 differ. These observations
do not establish byte-identical release reproduction or identical invocation-bound evidence across Runs.

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
