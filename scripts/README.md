# Operational scripts

The maintained scripts use the public lifecycle or demonstrate one focused
supporting contract. They never turn local diagnostics into release authority.
The [demo index](../demos/README.md) collects the presentation and guided journeys.

## First NVKE build

`bash scripts/nvkr-quickstart.sh` checks Git, Go and Docker before starting. Make is not required.
A missing or mismatched Go compiler can be installed inside the checkout after confirmation, or with explicit
`--install-go` consent. Matching installed Go is reused; implicit toolchain downloads are disabled.
The SDK is verified against [official download checksums](../toolchains/quickstart/README.md).
Git must already be installed. Supported Docker package installs are shown for approval; they are never performed without consent.
It selects the checkout's required Go compiler, compiles a native NVKR CLI, then builds
the three [quickstart](../docs/quickstart.md) artifacts and dependency SBOMs.
Each invocation prints the Definition and Plan, retains a fresh attempt, and prints
its output path plus inspection commands. `--build-only` stops after compiling NVKR;
the [hello demo](../demos/hello/README.md) uses this setup and reuses its Go SDK.
An inherited non-redirect Git configuration is refused before compilation with
instructions to unset `GIT_CONFIG_COUNT`.
The launcher runs in a child shell, so failure does not change the caller's SSH shell settings.

## Repository CI tests

`test-tekton-kind.sh` runs the native AMD64 core Definition in an owned kind cluster with pinned Tekton
and an in-cluster registry. The [Tekton guide](../docs/generate-pipeline.md#tekton-cluster-execution)
lists prerequisites, trust scope and retained evidence. The GitHub acceptance workflow runs this same
command; it needs no publishing secrets. This explicit integration test fails when prerequisites are absent.

`ci-test-shard.py` discovers the ordinary Go suite and partitions it across the Merge Gate's fifteen
test jobs. It includes examples and fuzz seed tests, and requires exact completion of the discovered
selection. New packages and tests need no allowlist update. `make test-shard` reproduces one matrix
entry; `make test` still runs the full suite. Each child command may retain at most 32 MiB of stdout;
exceeding that limit fails the shard and stops its process group while preserving diagnostics already
read. `ci-test-summary.py` summarizes the retained Go JSON events. Partition, completeness, parser and
small offline Go composition contracts run with `make test-ci-tools`; the Go fixture exercises every
matrix entry without rebuilding the product suite.

`check-status-restated.py BASE [HEAD]` backs the rule that validation status lives in the
[reconciliation ledger](../docs/adr/reconciliation/README.md). It fails when a Markdown line added since the
merge base, outside the ledger directory and `docs/evidence/`, matches one of a fixed list of status phrases
taken from fixed review findings. Other phrasings pass, it cannot judge whether a sentence is true, and it
ignores text already in the tree. Each finding names the owner's `adr-NN.md` ledger file when the file, or the
matched paragraph or list item, cites an ADR, and the ledger index otherwise. The Merge Gate runs it on every
pull request against the PR's base and head. `make status-restated-check` runs it locally against
`origin/main` and the working tree, including uncommitted and untracked files; set `STATUS_BASE` when
`origin/main` is not your base, and `STATUS_HEAD` to check a commit instead. For a deliberate exception, add
`<!-- status-ok: reason -->` to the matched line; an escape without a reason is itself a finding.

## NVKE hardware E2E

The [NVKE hardware E2E](../.github/workflows/nvkr-nvke-hardware-e2e.yml) workflow calls these scripts and the
`internal/hwe2e` command. Their contract tests (the Go tests in `internal/cicontract` and `internal/hwe2e`, which run in
`make test`) replace every external dependency (the hardware site, a registry, an object store, the container engine)
with a stand-in, so they show what the scripts and checks do with the inputs the stand-ins give them. Whether the real
services behave as the stand-ins do is a question only a recorded run answers;
the [ADR-18 reconciliation row](../docs/adr/reconciliation/adr-18.md) says which runs exist.

`internal/hwe2e` is one Go command with three subcommands, `check-junit`, `check-node-image` and
`publish-node-image`. Both workflows build it from the checkout with the repository's pinned Go toolchain
(`go build -trimpath -o hwe2e ./internal/hwe2e`) and run the binary; no interpreter is involved. `hwe2e SUBCOMMAND -h`
prints a subcommand's arguments, and each source file's header comment states its exit codes.

`hardware-e2e-substitute-plan.sh PLAN PROVENANCE OUTPUTS OUT` checks the committed plan fixture's bytes against its
provenance record, then replaces the slots NVKR builds with the references in an outputs manifest and writes
`plan.json` and `substitutions.json` into a fresh `OUT`. OCI references must be pinned by digest, node images must be
plain HTTPS `.qcow2` URLs with a SHA-256 and a named verification (`http-get`, or `s3-readback` citing the
publication run and what it read back), every slot is required, and a result that still names an `nvcr.io`
artifact is refused. Each replaced node image carries its verification and its source into `substitutions.json`; the source
is `nvkr-release` (an NVKR release digest) or `ci-run` (another CI run's HTTPS URL, such as the NVKE run that published a
release image), and only the first is NVKR-built. A node image built at another NVKE version than the plan's is refused unless
`--allow-node-image-version-skew` is given. `hardware-e2e-substitute-plan.sh --build-subject PLAN PROVENANCE OUT` keeps the
plan byte for byte and records that nothing was replaced. Failures carry the stable `NVKR-E-*` codes and leave no
partial output.

- `hardware-e2e-matrix.sh HARNESS_SCRIPT PLAN` builds the stream matrix with NVKE's own `matrix` step and admits only
  streams of the harness's `forge` provider, amd64 before arm64.
- `hwe2e check-node-image` checks a node image by the method its output names. With `--url` and `--sha256` it
  fetches the URL the way the hardware site's imager will (anonymous GET, no redirects, streamed SHA-256, qcow2 header) and
  records what the URL served. With `--substitutions` it takes the URL, SHA-256 and `s3-readback` verification from
  `substitutions.json`, fetches nothing and records the read-back as not observed by this run.
- `hwe2e publish-node-image` pulls one disk output of an NVKR release by exact digest, checks it is a plain
  qcow2, copies it under a key holding its SHA-256, reads it back from the object store with credentials and writes
  the record, with the outputs-manifest entry, that the
  [publication workflow](../.github/workflows/nvkr-nvke-node-image-publish.yml) uploads. The declared architecture is
  required and the image must declare a positive virtual size. Its tests replace the pull and the object store with
  fake executables, run as real child processes, so the streaming, the deadlines and the exit handling are the code that
  ships.
- `hardware-e2e-stage-inputs.sh` pulls the NVKR-built controller, capi-provisioned and nvkectl by digest, retags the
  images under a `localhost/` name and saves the archives the suite loads onto the node. It records the node image as
  not NVKR-built unless the substitution record shows an NVKR release as its source for that architecture.
- `hardware-e2e-build-subject.sh` builds the same inputs from the subject commit with NVKE's own `make site-inputs`,
  after checking the checkout is exactly that commit. Nothing it produces is NVKR-built.
- `hwe2e check-junit` compares a run's JUnit with the specs a Ginkgo dry run selected. The dry run must carry
  the leg's label filter and select a spec from each group the stage registers (site checks from `deployment`, tenant
  specs at `full`); `--dry-only` checks that before any machine is taken. At `full` every selected spec must pass; below
  it the check requires no failure and at least one pass and records that it is not complete.
- `hardware-e2e-summary.sh` stamps each leg, writes the run summary and asserts its contract, including
  `qualificationAuthority: "none"`, that a node image is named as not NVKR-built unless it is an NVKR release, that the
  matrix, plan, staged record and node-image check name the same streams, digests and URLs, and that each leg's
  coverage counts agree with its stage.

Each script's header states its arguments and exit codes. The `hwe2e` tests are ordinary Go tests and run in
`make test`.

## CLI artifact validation baseline

```sh
GOTOOLCHAIN=local go build -o bin/nvkr ./cmd/nvkr
bin/nvkr validators run --profile /path/to/validation.yaml --keep
```

Imports the matching pinned NVKE tests and invokes their `test-artifact` target
against a supplied NVKR-built binary, without rebuilding it. See
[artifact testing](../docs/artifact-testing.md) for prerequisites and a profile example.
This trusted-input diagnostic is implemented in Go. The guide separates prior
Python-adapter runtime results from validation of the Go replacement.
The current production pin lacks the upstream entrypoint; upstream adoption and
explicit maintained-input refresh remain follow-ups. The guide distinguishes prior
results from committed regressions.

## Definition → Run

`generate-maintained.py` drives `make workflows-generate` and `make workflows-check` from
`inputs/policies/maintained-generation.json`. That authored list selects every maintained Plan bundle and
the default qualifier policy. Contract tests compare the list with committed Plans and manifests, so a
new bundle must enter the same image-access contract. Group-specific Make targets use this list too.

For repository pipeline changes, `make pipeline-dev-run PIPELINE_DEV_REPO=owner/repo` checks the generated
development bundle and calls `pipeline-dev.py`. The helper requires a clean named branch matching the
remote commit and an active registered `nvkr-dev.yml`, then dispatches that branch explicitly. It performs
no commit, push, environment-policy update or registry cleanup. `--check` runs only its provider preflight.
See [pipeline development](../DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch) for full NVKE branch
execution, separate development signing/publication credentials, and attempt-scoped registry cleanup.
Controlled dispatch-adapter tests run with `make test-ci-tools`; registry cleanup is owned by the Go
development module and the separate default-branch cleanup workflow.

The Go CLI is the entry point for the
[shared native Linux/Mac path](../docs/local-nvke-demo.md#native-linux-and-mac-path):
`nvke-demo build`, `run` and `e2e`; `tools` and `native` expose focused operations. The native site runs in QEMU; Kind is
temporary bootstrap infrastructure. NVKR owns artifact builds, including QCOW2
outputs; native NVKE commands own bootstrap and deployment. Native runtime
acceptance against the selected source remains pending.

Follow the [native build-and-deploy guide](../docs/local-nvke-demo.md#native-linux-and-mac-path).
The standard `e2e` command builds through NVKR, then invokes the same three-VM
deployment owner as `run`. The default Mac BUILD prepares native inputs from the
documented installed prerequisites before invoking NVKR. No Colima execution host is substituted.

`nvke-full-arm64-local.sh ATTEMPT JOBS LANE` also supports
[focused selections](../inputs/definitions/demo/README.md);
`--check LANE` performs diagnostics only. The full demo compiles binaries once and shares
one measured Packer toolchain, including Helm 4.3.1 and ORAS 1.3.6. Profiles are
data bundles, not a separate container image. No publication/signing is enabled.
The launcher prints its exact shell-escaped `nvkr` invocations to stderr before execution, without printing environment values.

Contract tests: `go -C demos/nvke test ./internal/nvkedemo ./cmd/nvke-demo -count=1`.
For qualification, add `--qualify-local` to `nvke-demo build` or `nvke-demo e2e`.
It prepares fresh local keys, a Plan-bound demo policy, pinned cosign, scratch and an owned disk-backed
loopback registry, then stops that registry after qualification while retaining data and evidence.
See [local qualification setup](../docs/local-development.md#qualify-local-artifacts)
for prerequisites and the advanced explicit setup/stop commands.
See [reconciliation](../docs/adr/reconciliation/README.md) for recorded runtime results;
the reduced flow has not passed a new clean-machine end-to-end run. This is not
a [full source-built distribution](../docs/full-nvke-source-build.md).

```sh
# Plan only: validates and creates no attempt storage.
nvkr run plan --definition docs/examples/nvke-module-tags/definition.yaml --environment docs/examples/nvke-module-tags/environment-local.yaml

# Local execution; Docker, Go and acquisition/build network access are required.
# Despite its historical name, the M1 script executes CLI + nvke-controller + Helm.
NVKE_LOCAL_CLONE=/path/to/nvke scripts/demo-m1-local.sh
```

`demo-m1-local.sh` generates and checks its three-output GitHub bundle in a fresh temporary directory,
whose path it prints for inspection. The repository's committed bundle selects the 34-output NVKE candidate.
The local walkthrough publishes to its loopback registry using dry-run signing. Local attempts are immutable and
`unqualified-local-execution`; plans remain `static-unqualified`. See the
[M1 walkthrough and acceptance limits](../demos/m1-local.md).

The demo uses the loopback registry on port 5000. It generates its own valid
immutable attempt names.

The non-FDE OS-image bootstrap selects explicit catalog inputs. Its maintained routes
are native ARM64 and AMD64 Linux Docker/KVM. The older Podman/TCG route and its
stale toolchain pin are removed; generic NVKR Podman execution support is unchanged.

These OS-only commands require KVM. For Docker/Colima TCG and attempt-pinned
fresh toolchain bytes, follow the
[laptop OS route](../docs/local-development.md#build-the-complete-selection-including-the-os)
instead; its validation does not change the older wrapper's pin contract.

```sh
CONTAINER_RUNTIME=docker NVKE_OS_ARCH=arm64 scripts/nvke-os-local.sh local-arm64-docker-001 1
CONTAINER_RUNTIME=docker NVKE_OS_ARCH=amd64 scripts/nvke-os-local.sh local-amd64-docker-001 1
```

The default guest is Ubuntu 26.04. Set `NVKE_OS_UBUNTU_VERSION=24.04` to select the
[Ubuntu 24.04.5 inputs](../inputs/definitions/diagnostics/ubuntu24/README.md), including their
separate compatible agent compiler. The architecture and native KVM requirements stay the same.

Use a different immutable attempt name for each run. The historical Podman
[full build and boot smoke](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-09-23-nke-arm64-local-os-build.md)
does not validate a current maintained route.
The [ARC evidence](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-01-nke-arc-os-builds.md) records successful ARM64 and AMD64
Docker/KVM builds and boot smoke at an earlier exact commit, not validation of the latest changes.
See the [Definition guide](../inputs/definitions/diagnostics/README.md) for route requirements and limitations.
Set `NVKE_LOCAL_CLONE` to reuse an exact local NVKE checkout, accessible at the
same absolute path to the native Linux runner and Docker daemon.

Local attempts deliberately retain their frozen inputs, bootstrap runner and Run outputs. After reviewing an
attempt, archive those three attempt-scoped directories together instead of reusing its name:

```sh
attempt=local-arm64-docker-001
archive_root=/absolute/path/to/nvkr-attempt-archive
for area in inputs bootstrap runs; do
  if [[ -e ".nvkr/${area}/${attempt}" ]]; then
    mkdir -p "${archive_root}/${area}"
    mv -- ".nvkr/${area}/${attempt}" "${archive_root}/${area}/"
  fi
done
```

The toolchain uses `localhost/nvkr-packer-toolchain:dev-arm64` or `localhost/nvkr-packer-toolchain:dev-amd64`,
according to the selected architecture, not a per-attempt tag. Once no
build is running and no retained workflow needs the local image, remove that optional cache with
`docker image rm localhost/nvkr-packer-toolchain:dev-arm64`. Archiving or
removing storage does not make an attempt name reusable; choose a new name for every Run.

## Release consumption

```sh
scripts/demo-release-consumer.sh --help
```

The consumer takes one digest-pinned reference and one explicit Verification
Policy with `artifact-publication` scope. This does not establish M3 qualification or M4 Release-manifest
acceptance. It requires a real statically linked, digest-pinned Linux cosign executable
and a signed current v4 GitHub publication; local dry-run output is insufficient.
On macOS the script automatically builds a Linux amd64 NVKR helper and verifies inside
the pinned Docker toolchain image. Linux defaults to native execution; `--runtime docker`
selects the container route explicitly, and `--runtime native` selects the host CLI.
Docker mode needs the pinned Go toolchain and a Linux amd64 cosign executable.
Use `--anonymous` for public registry access, or `--registry-config /path/to/auth-dir`
with inline credentials in `config.json` for private access. Host credential helpers
are rejected in container mode. Neither route relaxes executable pins, signer policy,
release membership, or payload verification.
Supplying `--definition`, `--environment`, and `--plan` together additionally
requires the verified publication to bind those exact authored and compiled
identities and realize their complete output set. Partial or empty exact-input
selection fails before registry access. Relative input paths resolve from the
caller's working directory. A successful exact-input check emits the verified
Release and a defensive exact-input description. The opaque receipt remains
process-local; its description grants no stronger evidence lane or Release scope.
Signer-policy rejection is covered by the owning consumer contract tests.

## Focused supporting contracts

`prepare-native-toolchains.sh` retains native Packer/inspector archives and measurements;
`verify-native-toolchains.sh` shares the committed-pin and candidate native checks.
The [toolchain maintenance workflow](../docs/toolchain-maintenance.md) delegates subject admission,
publication, read-back and authored pin changes to `pkg/nvkr/toolchainmaintenance`, then regenerates
through the existing owners. `propose-toolchain-refresh.py` publishes only the checked review diff with
the repository App. `report-native-toolchains.py` compares measured manifests with
committed pins and routes actionable reports to one labeled tracking issue; both
scripts' offline behavior tests run with `make test-ci-tools`.

The ARM64 build/publish/consume nightly cycle is retired. `nightly-build.sh` and
`TestNightlyPublishedCandidate` remain as uninvoked legacy code pending removal;
no maintained nightly supplies their dispatch or consumer inputs. The current
[nightly](../docs/nvke-nightly.md) builds and tests locally on public hosted AMD64.

- `demo-catalog-change.sh` exercises previewed and machine-managed source pin
  changes against an isolated catalog copy.
- `demo-tamper-detection.sh` demonstrates rejection of changed OCI payload bytes before
  extraction, corrupt transport rejection, and path-containment failures.

Package representation details remain covered
by contract tests rather than parallel public workflows. Workspaces live below
`.nvkr/`; scripts either create immutable attempts or require their own workspace
marker before resetting derived output.
