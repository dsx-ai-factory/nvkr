## Support by owner

ADR-18's 2026-11-20 revision accepts destination staging by digest, with repository-local GHCR
and platform job tokens as the new-adopter default. It is design only: the current emitter still
transfers output payloads through private CI storage and excludes destination writers from producer jobs.
No destination-staging contract test or hosted Run is recorded. The job-boundary evidence below covers
the current carrier path, not the revised producer stage-writer exception. OIDC and the release environment
remain confined to release jobs in both contracts.

Migration requires separate changes: development disks first, comparing release-root bytes with the current
path for identical prepared bytes and frozen identity inputs; all output kinds and aggregate indexes next;
then helper/source/intermediate/diagnostic edges, followed by deletion of the CI-storage implementation and
all `NVKR_CI_STORAGE_*` secrets/variables. Build-only residual edges need explicit transport admission before
that deletion. No generated workflow or credential configuration changes with this decision. The platform-token profile accepts
repository linkage and fork-package exposure; the opt-in independent writer needs a distinct stage-only
credential, never the `nvkr-release` writer. The signed GitLab platform-token profile remains design only; the ordinary
unsigned GitLab implementation and its proposed contract below are distinct. Tekton's cluster-key contract
is unchanged.

GitHub per-output recovery preserves an original failed job's ID and, for execution stages, Plan-derived
display label for the finalizer's completeness error, prioritizing actual failure over cancellation/skips.
Within each outcome, ancestors precede dependents, and independent roots use job-ID order. Local contracts cover
direct and transitive failure selection, recovery-to-finalizer propagation, bootstrap labels,
legacy/malformed/foreign diagnostics and unchanged
failure codes; partitioned recovery still expands original provider results before selecting diagnostics.
`TestGitHubRecoveryPreservesUpstreamFailureForFinalizer` uses controlled provider descriptions and no live
GitHub jobs. The provider data has no timestamps or original error text, so chronological first failure and
upstream error-code reproduction are not claimed. No hosted exercise of the revised finalizer is recorded.

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
The bootstrap stages them separately from the committed Plan tree.
The v3 tree derives its artifact graph from authenticated stored inputs through the same compiler as
Plan validation, checks `graphDigest`, and retains the full canonical Plan identity. Graph omission does
not reduce the packed carrier or change Build Digests. Legacy v1/v2 trees remain readable.
The helper invokes the installer to admit and regenerate that tree. Bootstrap installation renders
the baseline and candidate once each. Stage installation verifies the saved baseline Plan and fixed
checkout members, then renders only the candidate. Secondary multi-source bootstraps combine the
runtime-input check and carrier staging in one render. Frozen Plan and step record addresses remain stable during a source-pin refresh; catalog variants
retain content addresses. Target membership, script bytes, consumer cohorts and non-Plan members remain fixed.
Runtime-plan Environments have bundle-scoped stable records. `TestRuntimePlanRefreshBesideProduction`
covers dispatch refresh and both carrier installers beside an unchanged production bundle.
A vendor cohort split requires normal regeneration even when its script bytes and addresses are unchanged.
Packed-carrier and cohort-split refusal fixtures exercise these boundaries locally. The retained
`nvke-nightly` selection passes synthetic source-pin refresh with unchanged scripts and exact reconstructed
Plan bytes. The local source fixture restores and replays the frozen commit through the packed carrier. No hosted carrier
transfer with the tree layout has been recorded.

## GitLab ordinary unsigned profile

This GitLab extension remains proposed for maintainer review. The provider runs below cover only their
exact recorded source snapshots; they do not establish acceptance for the integration with upstream
`12e0badb` (shared Plan storage and catalog consolidation) or later source changes.

The current implementation admits the authenticated project default branch plus exact authored
`additionalRefs`, with `push`/`web` defaults or explicit `pipelineSources`. It denies tags, requires
authenticated branch protection for every publication, and binds ref/source into operation outcomes and
recorded complete or failed Runs. Refreshes retain the original invocation and default branch. API, carriers
and isolated GitLab registry access share direct TLS trust: authored bounded PEM CA replaces system
roots; omitted CA refuses ambient certificate overrides, and proxies are not taken from the environment.
The token-free checkout flag is required; a bounded raw/effective local Git-config check refuses the actual
job/reader credential bytes before provider/build work. These later changes are outside the historical
real-server snapshots below and require fresh affected acceptance.

The bounded adapter implements native runner/bootstrap policy, complete-Plan projection,
provider-authenticated current jobs, original-job outcomes/carriers and shared lifecycle finalization.
Producer placement and budgets use the executable producer view. Generated jobs select the native
Docker-helper umask switch and fresh-clone strategy before checkout; shared exact `0644` checks remain.
Pinned Runner source and local Git reproductions establish why both settings are needed: default helper
`umask 0000` creates `0666` files, and fetch/forced checkout retain those non-executable permissions.
A fresh normal-umask clone passes with identical bytes. Nonstandard helper/repository policies still fail closed.

On November 18, 2026, `make test-gitlab-docker` passed against real GitLab CE 19.4.8 and Runner 19.4.0
on native Linux ARM64. All eight generated jobs succeeded without retry: source-built bootstrap, real
NVKE source fetch and `nvkectl` build, Syft acquisition, observation, SBOM, packaging and finalization.
The test downloaded and inspected the executable bundle and exact complete `static-unqualified` Run,
matched its original invocation and Plan, captured generator/bootstrap blob identities, exited zero and
joined cleanup with no test-owned resources remaining. NVKE was `v0.23.0` at
`2cfd00081b468d85a4894ec17e9deac13c9d8169`. The complete 3,055-entry tested source snapshot had SHA-256
`88cc238746c2ff7abf75d3ed2ec895c97b212692ff2b5497156a3927d22d51c8`; all source bytes and modes
were independently rechecked unchanged. This local acceptance has no public hosted-Run URL. Test code,
pinned infrastructure and the manual hosted workflow are committed; private receipts retain the original
failed attempts and successful Run. The hosted workflow has not been exercised.

A separate Go 1.27.4 Linux ARM64 run passed both controlled lifecycle targets, all 26 subcases with no
failures or skips. It covers production-client publication/refusal and finalization against controlled HTTPS
fixtures, separately from the real server/Runner acceptance above. It does not establish a live project-registry push.

The real exercise used a scoped project `read_api` private-token reader, separate from native current-job
authentication and registry-write authority. This describes the fixture's provisioning, not scope enforcement:
NVKR checks the credential kind and endpoint access but does not introspect token grants. Least-privilege
reader provisioning remains the operator's responsibility. The optional job-token API-reader route remains unverified,
especially project metadata, job-list permissions and the branch-read permission needed by the later
protected-publication check. No job-token access proof is inferred for that added endpoint. See GitLab's
[job-token access table](https://docs.gitlab.com/ci/jobs/ci_job_token/#job-token-access) and
[fine-grained permissions](https://docs.gitlab.com/ci/jobs/fine_grained_permissions/).
Native AMD64 execution, module-installed bootstrap, live registry publication and actual retry/cancellation
recovery remain outside this acceptance. Signing, qualification, disk and private-source execution remain
refused. Full repository checks and maintainer review are separate gates; this exercise grants no production acceptance.

Native metadata GETs retry HTTP 429 at most twice inside the existing lookup and caller deadlines.
They honor a valid `Retry-After` of at most 30 seconds, use one- and two-second delays when absent,
and refuse ambiguous, invalid or excessive delays without exposing provider text. Failed bodies close
before waiting; cancellation and client Close stop the wait. Contract tests cover these bounds, credential
destinations, exhaustion and joined cancellation. Other statuses, identity changes and failed validation
are not retried. This is controlled-client coverage, not live provider-throttling acceptance.

Each consumer authenticates every ancestor outcome against one provider snapshot and retains the original
attempt bindings; only selected payload carriers are downloaded. Publication revalidates that acquired
closure after inspection and before the first write, then again on completion. The finalizer uses the last
stage as its completion barrier and downloads only its utility bootstrap. Raw checkout input bytes are
compared without Git worktree conversion against the runner clone's blobs for the provider-authenticated
commit identifier. This establishes consistency with that clone, whose object database is trusted; it does
not independently rehash the commit/tree/blob closure or protect against a compromised runner.

A fresh native Linux ARM64 server/Runner exercise passed on the complete 3,067-entry source archive
`ce0a8f34a90e99b659d0871bf528f56a1385cab796bf899f295faa740ced0f2c`, based on `1f156647` plus these
review fixes. All eight generated jobs succeeded with zero retries; the test verified the executable and
complete exact Run, exited zero in 295.101 seconds, and joined cleanup with no owned resources remaining.
Source bytes and modes remained unchanged. The finalizer remained unstarted while the build was active;
this success case does not establish mixed-platform skipped-branch failure scheduling. The retained Run
contains only the unsigned executable output, with no registry publication. Later publication-error
presentation distinguishes observed write attempts from pre-write failures and reports a completed push
whose readback remains unverified; controlled delivery and HTTPS-fixture regressions cover those cases.
Separate cancellation-test regressions require retry-wait entry and prove that Close joins the held lookup.
These later source and test changes do not inherit live publication or error-path acceptance from this Run.
The current manual GitHub workflow still selects a standard runner whose guaranteed disk is below the
35 GiB test prerequisite. An adequately sized hosted route and hosted execution remain unverified.

## GitLab SDK reuse assessment

The current follow-up assessment identifies the official `gitlab.com/gitlab-org/api/client-go/v3` SDK
`v3.52.0` at `c65d798cad511d8a1ee8d3e274eea4b0fa188fe2` as a reuse candidate for the required API
methods and generated `testing.TestClient`. Its [pinned module](https://gitlab.com/gitlab-org/api/client-go/-/blob/c65d798cad511d8a1ee8d3e274eea4b0fa188fe2/go.mod)
requires `go-cleanhttp v0.5.2` and `go-retryablehttp v0.7.8`; both carry MPL-2.0
([cleanhttp license](https://github.com/hashicorp/go-cleanhttp/blob/v0.5.2/LICENSE),
[retryablehttp license](https://github.com/hashicorp/go-retryablehttp/blob/v0.7.8/LICENSE)). The
[SDK runtime client](https://gitlab.com/gitlab-org/api/client-go/-/blob/c65d798cad511d8a1ee8d3e274eea4b0fa188fe2/gitlab.go#L462-L466)
constructs and uses these dependencies even when a caller supplies an HTTP client. Its Apache-2.0 top-level
license therefore does not satisfy the repository's [Apache-clean linked-dependency policy](../../../CONTRIBUTING.md#the-invariants-your-change-must-respect).
The [Merge Gate note](../../../.github/workflows/merge-gate.yaml) records automated license enforcement as
planned; a green gate supplies no policy exception. This SDK closure must not enter NVKR's production import
graph under the current policy. Its [generated test client](https://gitlab.com/gitlab-org/api/client-go/-/blob/c65d798cad511d8a1ee8d3e274eea4b0fa188fe2/testing/client.go)
also imports the SDK; it is not a dependency-free alternative.

SDK adoption remains recommended for evaluation after an upstream policy-compatible closure or explicit
license-policy decision, followed by pinned dependency, notices, security and provider-boundary review.
No dependency or policy exception is admitted here. External official `glab` provisioning is available at the
permitted subprocess boundary without linking its dependency graph into NVKR; it grants no runtime provider
or publication authority. SDK test doubles remain controlled fixtures and cannot replace real GitLab plus
Runner acceptance.
