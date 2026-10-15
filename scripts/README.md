# Operational scripts

The maintained scripts use the public lifecycle or demonstrate one focused
supporting contract. They never turn local diagnostics into release authority.
The [demo index](../demos/README.md) collects the presentation and guided journeys.

## CLI artifact test baseline

```sh
GOTOOLCHAIN=local go build -o bin/nvkr ./cmd/nvkr
python3 scripts/test-artifacts.py --artifact /path/to/nvkectl --plan /path/to/matching/plan.json
```

Imports the matching pinned NVKE tests and invokes their `test-artifact` target
against a supplied NVKR-built binary, without rebuilding it. See
[artifact testing](../docs/artifact-testing.md) for prerequisites, results, and scope.
This is a trusted-input diagnostic with independently reproduced fresh recipe-build
and cold-cache suite execution on native Linux AMD64/ARM64, including the local
macOS ARM64/Docker VM path.
The current production pin lacks the upstream entrypoint; upstream adoption and
explicit maintained-input refresh remain follow-ups. The guide distinguishes prior results from committed regressions.

## Definition → Run

Follow the [Ubuntu ARM64 build-and-demo guide](../docs/local-nvke-demo.md) for
`nvke-demo e2e`: build selected artifacts through `nvkr`, then deploy those
completed Run to a local site and QCOW2-backed tenant. macOS supplies the Linux
host through Colima; this is not a native Darwin build.

`nvke-full-arm64-local.sh ATTEMPT JOBS LANE` also supports
[focused selections](../definitions/nvke-local-dev-arm64/README.md);
`--check LANE` performs diagnostics only. The full demo compiles binaries once and shares
one measured Packer toolchain, including Helm 4.2.4 and ORAS 1.3.3. Profiles are
data bundles, not a separate container image. No publication/signing is enabled.
The launcher prints its exact shell-escaped `nvkr` invocations to stderr before execution, without printing environment values.

Contract tests: `go test ./internal/nvkedemo ./cmd/nvke-demo -count=1`.
See [reconciliation](../docs/adr/reconciliation.md) for recorded runtime results;
the reduced flow has not passed a new clean-machine end-to-end run. This is not
a [full source-built distribution](../docs/full-nvke-source-build.md).

```sh
# Plan only: validates and creates no attempt storage.
nvkr run plan --definition definitions/nvke-v1/definition.yaml --environment definitions/nvke-v1/environment-local.yaml

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

The non-FDE OS-image bootstrap uses one shared catalog. Its maintained routes
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

Use a different immutable attempt name for each run. The historical Podman
[full build and boot smoke](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-09-23-nke-arm64-local-os-build.md)
does not validate a current maintained route.
The [ARC evidence](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-01-nke-arc-os-builds.md) records successful ARM64 and AMD64
Docker/KVM builds and boot smoke at an earlier exact commit, not validation of the latest changes.
See the [Definition guide](../definitions/nvke-os/README.md) for route requirements and limitations.
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

- `demo-catalog-change.sh` exercises previewed and machine-managed source pin
  changes against an isolated catalog copy.
- `demo-tamper-detection.sh` demonstrates canonical identity changes, corrupt
  transport rejection, and path-containment failures.

Package representation details remain covered
by contract tests rather than parallel public workflows. Workspaces live below
`.nvkr/`; scripts either create immutable attempts or require their own workspace
marker before resetting derived output.
