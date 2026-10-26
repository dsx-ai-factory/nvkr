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
Each invocation retains a fresh attempt and prints its output path.
The launcher runs in a child shell, so failure does not change the caller's SSH shell settings.

## Repository CI tests

`ci-test-shard.py` discovers the ordinary Go suite and partitions it across the Merge Gate's eleven
test jobs. It includes examples and fuzz seed tests, and requires exact completion of the discovered
selection. New packages and tests need no allowlist update. `make test-shard` reproduces one matrix
entry; `make test` still runs the full suite. Each child command may retain at most 32 MiB of stdout;
exceeding that limit fails the shard and stops its process group while preserving diagnostics already
read. `ci-test-summary.py` summarizes the retained Go JSON events. Partition, completeness, parser and
small offline Go composition contracts run with `make test-ci-tools`; the Go fixture exercises every
matrix entry without rebuilding the product suite.

## CLI artifact test baseline

```sh
GOTOOLCHAIN=local go build -o bin/nvkr ./cmd/nvkr
bin/nvkr run artifact-test --artifact /path/to/nvkectl --plan /path/to/matching/plan.json
```

Imports the matching pinned NVKE tests and invokes their `test-artifact` target
against a supplied NVKR-built binary, without rebuilding it. See
[artifact testing](../docs/artifact-testing.md) for prerequisites, results, and scope.
This trusted-input diagnostic is implemented in Go. The guide separates prior
Python-adapter runtime results from validation of the Go replacement.
The current production pin lacks the upstream entrypoint; upstream adoption and
explicit maintained-input refresh remain follow-ups. The guide distinguishes prior results from committed regressions.

## Definition → Run

For repository pipeline changes, `make pipeline-dev-run PIPELINE_DEV_REPO=owner/repo` checks the generated
development bundle and calls `pipeline-dev.py`. The helper requires a clean named branch matching the
remote commit and an active registered `nvkr-dev.yml`, then dispatches that branch explicitly. It performs
no commit, push, environment-policy update or registry cleanup. `--check` runs only its provider preflight.
See [pipeline development](../DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch) for full M2 branch
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
one measured Packer toolchain, including Helm 4.2.4 and ORAS 1.3.3. Profiles are
data bundles, not a separate container image. No publication/signing is enabled.
The launcher prints its exact shell-escaped `nvkr` invocations to stderr before execution, without printing environment values.

Contract tests: `go test ./internal/nvkedemo ./cmd/nvke-demo -count=1`.
For qualification, add `--qualify-local` to `nvke-demo build` or `nvke-demo e2e`.
It prepares fresh local keys, a Plan-bound demo policy, pinned cosign, scratch and an owned disk-backed
loopback registry, then stops that registry after qualification while retaining data and evidence.
See [local qualification setup](../docs/local-development.md#rehearse-local-m3-qualification)
for prerequisites and the advanced explicit setup/stop commands.
See [reconciliation](../docs/adr/reconciliation.md) for recorded runtime results;
the reduced flow has not passed a new clean-machine end-to-end run. This is not
a [full source-built distribution](../docs/full-nvke-source-build.md).

```sh
# Plan only: validates and creates no attempt storage.
nvkr run plan --definition docs/examples/nvke-tutorial/definition.yaml --environment docs/examples/nvke-tutorial/environment-local.yaml

# Local execution; Docker, Go and acquisition/build network access are required.
# Despite its historical name, the M1 script executes CLI + CWE + Helm.
NVKE_LOCAL_CLONE=/path/to/nvke scripts/demo-m1-local.sh
```

`demo-m1-local.sh` generates and checks its three-output GitHub bundle in a fresh temporary directory,
whose path it prints for inspection. The repository's committed bundle selects the 34-output M2 candidate.
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

`nightly-build.sh` dispatches one maintained generated producer and joins its
exact GitHub run. The separately authored nightly coordinator invokes the Go
integration test and NVKE's existing runtime suite after authenticated publication.
See the [nightly workflow and runtime bindings](../docs/nvke-nightly.md).
The initial profiles select kind plus native ARM64 KVM guests with explicit OVN
variants. The special TestGrid integration is deferred.

- `demo-catalog-change.sh` exercises previewed and machine-managed source pin
  changes against an isolated catalog copy.
- `demo-tamper-detection.sh` demonstrates canonical identity changes, corrupt
  transport rejection, and path-containment failures.

Package representation details remain covered
by contract tests rather than parallel public workflows. Workspaces live below
`.nvkr/`; scripts either create immutable attempts or require their own workspace
marker before resetting derived output.
