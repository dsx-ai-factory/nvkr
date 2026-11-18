## Support by owner

Container runtime selection defaults to Docker; local Runs inherit their Environment and reject conflicting executable overrides before work, including dry runs.

The Plan-produced VM runtime extension has manager-neutral package inputs, exact mkosi installed-package
closure checks, source-closure acquisition, and immutable single-layer OCI capture. The native namespace
[VM proof](https://github.com/njhensley/nvkr/actions/runs/44194123590) at
`0dbb69bb55dc190884ef3ca2b9d673a6c365284d` exercised both assembly and consumer launchers on stock
Ubuntu 24.04 AMD64 with kernel `7.0.0-1014-aws`. The separately owned executable tmpfs retained its
inherited mount restrictions. Private proc/root mounts, assembly user-ID mapping, host-file protection,
consumer capability dropping, private loopback, output projection and cancellation of descendants in
separate process sessions passed. The test service had one CPU quota, 256 MiB memory, 96 tasks and a
60-second lifetime limit. Its fixture does not build a VM runtime or launch QEMU, Packer, KVM or libguestfs;
the revised launcher has no corresponding ARM64 VM proof. The production assembly backend now stages
the source-pinned module, performs two fresh offline builds, checks original package reports and measured
files in each packaged layer, and requires identical complete OCI closures before issuing a production
receipt. Its focused tests cover staging, substitution rejection, closure checking and receipt revocation.
The [source-pinned production proof](https://github.com/njhensley/nvkr/actions/runs/44220466951) at
`3a50d00c43abadfff8f959a06439662d6b01d0a9` passed on native AMD64 with kernel `7.0.0-1017-azure`.
Both fresh assemblies passed exact installed-package closure, embedded original-report/recipe and measured
file checks, then produced the same complete OCI closure. Mkosi emitted each tar; NVKR wrapped its bytes
unchanged as one uncompressed layer. Literal backslashes in pinned systemd unit filenames were preserved
without treating them as separators. The resulting manifest subject was
`sha256:f6388e80bc29b72a368e8fd1001a6f208ee59b6dc82e9750e291f85866d6e40d`, layer digest
`sha256:5929cc0ad843c685e250b92ad1ff76d5721f9b161874eb0ba99e2dc2bbc8355a`, and complete layout digest
`sha256:8be63fb227252b4d15e90e6c170057289b045d412eb1383488077dca37f7c776` (1,942,375,343 layout bytes).
The test authenticated the concrete ProducedRuntime receipt against its returned result. Namespace
assertions also passed, including assembly-only `/dev/full` and `/dev/tty`, full-device `ENOSPC` behavior
and absence of a controlling terminal. The assembly service used a two-CPU quota, 5 GiB memory limit,
512-task limit and a separately owned 20 GiB scratch filesystem. The workflow retained text diagnostics,
not the OCI payload. Host bootstrap utilities were recorded but not snapshot-pinned.

The Run dispatcher can retain and revoke concrete runtime production and join recorded terminal/SBOM
evidence. Runtime inventory reads original reports from the authenticated layer and projects a subject-bound
SBOM; focused fixtures cover these boundaries. The compiler resolves the Environment's named amd64
runtime producer into each selected Packer Build and adds the runtime result, terminal observation and
SBOM as required consumer inputs. Complete Plan fixtures exercise catalog revalidation, runtime repins,
removal of the unused image dependency and exact license companion names. These are compiler contracts,
not executed disk builds. Consumer-use receipts, namespace consumer execution and hosted scheduling
remain incomplete, so maintained Run execution stays gated. No real Run has exercised
the new runtime SBOM and dispatcher together. This proof does not establish disk-output
equivalence, guest boot, ARM64 operation or a stronger evidence lane. The earlier isolated amd64
[assembly Run](https://github.com/njhensley/nvkr/actions/runs/44145459516) built two byte-identical runtime
archives from fresh roots and the same revalidated snapshot inputs. It used Ubuntu's mkosi package,
not the source-commit bootstrap now required by the contract. That Run establishes neither production
Plan integration nor disk-output equivalence, guest boot, ARM64 operation or a stronger evidence lane.

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
