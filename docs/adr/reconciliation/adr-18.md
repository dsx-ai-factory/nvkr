## Support by owner

Container runtime selection defaults to Docker; local Runs inherit their Environment and reject conflicting executable overrides before work, including dry runs.

The Plan-produced VM runtime extension has manager-neutral package inputs, exact mkosi installed-package
closure checks, source-closure acquisition, and digest-bound single-layer OCI capture. The layer is
retained through a read-only descriptor to the original disk-backed OCI tar, without a second retained
archive. Extraction rejects memory-backed storage and verifies the entire stream before releasing its
private root; that root is removed after inspection or consumption joins. Focused tests cover original
inode retention, ownership, read-only descriptors, tampering, tmpfs rejection and cleanup. Cgroup memory
observation excludes only inactive file pages from usage, preserves host and ancestor limits, and rejects
missing or malformed statistics. Native consumer execution with these changes remains unexercised. The native namespace
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
SBOM as required consumer inputs. Each disk output selects a separately named runtime requirement;
four-output fixtures retain equal runtime Build Digests with distinct artifacts and evidence. Native
group routing keeps each requirement with its own disk owner. Run completion compares independently
recorded manifest and complete-layout digests and sizes for equal producer selections, requiring each
requirement's own inventory and producer records. This comparison consumes descriptions from same-Run
transport, not restorable execution authority. Complete Plan fixtures exercise catalog revalidation, runtime repins,
removal of the unused image dependency and exact license companion names. These are compiler contracts,
not executed disk builds. The internal consumer-use receipt now binds the original and relocated Plan,
exact consumer instruction, runtime production and completed evidence. Contract tests cover single use,
substitution rejection, revocation and cancellation with joining; the owning Run retains private production
Plan bindings and revokes all its issued uses before closing runtime resources. Private consumer staging
requires that receipt, retains the original layer through cleanup, and rejects replay and unowned use.
Its cancellation and cleanup contracts have race tests. Narrow input/work mount points preserve the
runtime's plugin and firmware paths; filesystem tests reject symlink parents. Mkosi material observation
now uses the same authenticated producer-result boundary as other material consumers. Consumer dispatch
passes the original production and use receipt to the namespace Packer adapter. Version checking,
SSH-key generation and validation stay offline; the build uses the fixed outbound helper. The factory
compares the returned runtime-use record with the original receipt's completed binding before retaining
the disk. Bounded disk/log collection and descriptive binding checks have focused tests. The maintained
Environment has not switched to this route, and no native VM has exercised this adapter. A native factory proof has completed runtime production, terminal observation and SBOM through the
original owner, as scoped below; no completed public Run has exercised the consumer route. This proof does not establish disk-output
equivalence, guest boot, ARM64 operation or a stronger evidence lane. The earlier isolated amd64
[assembly Run](https://github.com/njhensley/nvkr/actions/runs/44145459516) built two byte-identical runtime
archives from fresh roots and the same revalidated snapshot inputs. It used Ubuntu's mkosi package,
not the source-commit bootstrap now required by the contract. That Run establishes neither production
Plan integration nor disk-output equivalence, guest boot, ARM64 operation or a stronger evidence lane.

The generated produced-runtime disk bootstrap has an unprivileged preflight, a fixed scrubbed sudo
invocation and a root-owned watchdog. Scratch admission checks the actual protected executable filesystem
and its capacity. Shell contract tests cover credential removal, exact operation selection and preflight
failure. The outbound launcher uses the snapshot's `slirp4netns` 1.3.3-1 TAP-descriptor interface, with
fixed arguments, numeric DNS projection, unprivileged helper execution and paired process lifetime.
The runtime package closure adds that exact package; all four library dependencies were already pinned.
The first [consumer proof attempt](https://github.com/njhensley/nvkr/actions/runs/44258176521) at
`2689862288223c4cc09bd58eab785810a68a1f3f` passed the assembly and consumer namespace tests but failed
the outbound resolver's protected-file admission before runtime production or disk execution. It does
not prove the outbound helper, SBOM or consumer path. Resolver admission now also checks the root's
delegation to protected systemd-resolved service-owned files; Linux ownership fixtures cover accepted
targets and rejection of foreign inodes, owners, writable parents/files and unlisted targets. The second
[consumer proof attempt](https://github.com/njhensley/nvkr/actions/runs/44259377400) at
`ba4c9f333dbcc57292915045e3dfeb470db587e5` passed all three native namespace tests, including outbound
handoff with its fixture helper. The host resolver file had UID 991, mode 0644 and 945 bytes. The newly
included ownership fixtures then failed because the service's `/tmp` was read-only, so production and
disk execution still did not run. Both diagnostic services now direct temporary files into their owned
scratch directories. The ownership fixtures pass in a bounded Linux container with read-only `/tmp`
and an explicit writable scratch directory; the complete native consumer proof remains pending.
The [factory production and SBOM attempt](https://github.com/njhensley/nvkr/actions/runs/44261280531)
at `5d1012c7bc79b085e28b0da9b8b2f68bee8f22bb` passed initial admission, both fresh mkosi assemblies,
the exact OCI reproduction gate, terminal observation and SBOM generation through the original owner.
The repeated consumer admission then failed with 351/4096 MiB memory and 16499/16384 MiB workspace
available/required inside the unchanged 5 GiB service. Packer and QEMU did not run. The retained log
records successful production and SBOM operations, but the failed attempt did not export standalone
OCI subject or SBOM documents. It establishes no completed runtime use, public Run or publication.
The [disk-retention attempt](https://github.com/njhensley/nvkr/actions/runs/44266926355) at
`a0e6c3122ca7f346c426478d923baeadde3d52e3` also completed production, terminal observation and SBOM.
Its consumer admission failed with 3/4096 MiB memory and 14647/16384 MiB workspace available/required.
The service reported 5057 MiB file cache (5054 MiB inactive), 20 MiB anonymous memory and 45 MiB process
RSS; the retained layer was a 1852 MiB disk snapshot. No OOM kill or Packer execution occurred. The
current storage path removes that extra retained snapshot and retains the original OCI tar. Budgets
remain unchanged; the working-set measurement and resulting workspace headroom need a fresh native proof.
The earlier assembly digest above describes the closure before the outbound helper was added and does
not evidence the changed runtime.

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
