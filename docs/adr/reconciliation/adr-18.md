## Support by owner

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
The [2026-11-10 AMD64 attempt](https://github.com/dsx-ai-factory/nvkr/actions/runs/42755804686)
at `9494d8fb64a97fb2f0b3c0a0721caff0b4ef7963` completed all 107 build and publication tasks, including
all 13 output publications, signatures and verifications, plus release assembly, publication, signing and
verification. Native smoke failed because its fresh private daemon lacked the policy's pinned image.
The Tekton smoke task now invokes the shared anonymous image-admission owner after smoke scope admission,
before opening the held runtime; the runtime independently reauthenticates local image bytes and does not
pull implicitly. Shared image-admission and smoke contract fixtures cover those owners. Successful complete
native smoke, independent qualification and finalization remain unrecorded.

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 18 Execution and GitHub | Shared local/hosted ordinary-operation session (routing, direct-node admission and kind dispatch) and producer launcher, held source/vendor lifetime, capability admission, manual-only GitHub producer generation, exact optional publication-branch allowlist, separate full-development branch workflow, mode-specific concurrency and artifact retention, scoped credentials and unconditional finalization; inline named jobs with ancestor-preserving dependency reduction and bounded exact-result collectors with diagnostic hosted evidence; at most two optional summaries per attempt with operation details in logs; explicit native ARC disk grouping, raw build-only disk transport and optional native hosted platform maps (including separate CLI output slots); authenticated independent qualification invocation/source-event binding; generated fixed native worker and qualifier with separate credentials; authored public-hosted AMD64 NVKE build/Go E2E adapter outside product execution (the ARM64 published-artifact nightly adapter is retired); producer bootstrap and private-transfer helpers build the runner from checkout source for checkout-built generators, or install the generator's own exact module version (a release or a commit's pseudo-version) and require its recorded module version and checksum; an unstamped released generator labels its Plans and `--version` with that build-information release version, and an unstamped commit install with the revision its pseudo-version encodes | Production hosted qualification, retry qualification of the qualifier itself and independent-consumer acceptance remain unproved. The [development record](feature-branch-pipeline-development.md#feature-branch-pipeline-development) documents successful qualification of the earlier main-source candidate; hosted validation of the current v0.23.0 selection remains pending. The downstream adapter trusts privileged NVKE source frozen once from main and yields unsigned diagnostics; [completed hosted cycle evidence](consumer-acceptance-scopes.md#consumer-acceptance-scopes) records its exact NVKR/NVKE revisions and does not cover later edits or qualification. Default-branch push publication and in-repository PR build triggers remain deferred; arbitrary self-hosted/other-provider routes and qualified emitters fail closed. Provider configuration and fresh runtime evidence remain separate. The module-pinned bootstrap and build-information generator label have contract tests with injected build information; a local `go install …@<commit>` of the change rendered the commit-pinned bootstrap, but no tagged NVKR release contains it yet and no hosted Run from a repository without NVKR source is recorded. The release form's Go version is the toolchain that built the generator, so installs of one release built with different Go patch releases render different bundles |

Image admission uses the admitted image-use route for factory utility, direct producer and source/vendor
consumers. The shared pipeline owner issues process-local receipts over subject, platform, config ID and
observed runtime; private misses fail without pulling, anonymous misses admit bounded materialization,
and factory launches reauthenticate with implicit pulls disabled. Contract fixtures cover prepared private
consumption, substitutions, eviction and runtime changes. This is not a reproduced maintained utility
outage, a live private-registry/runner exercise, qualification or production acceptance. Per-runner private
preparation and credential destruction retain their existing owner.
