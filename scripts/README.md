# Operational scripts

The maintained scripts use the public lifecycle or demonstrate one focused
supporting contract. They never turn local diagnostics into release authority.
The [demo index](../demos/README.md) collects the presentation and guided journeys.

## Definition → Run

For first-time ARM64 NKE development, use the [local build guide](../docs/local-development.md)
and `bash scripts/nke-full-arm64-local.sh ATTEMPT 1 components`. `all` also selects the
OS disk; read its toolchain/capacity prerequisites before launching that larger Run.
`providers` selects only the source-built CAPNICo and provisioned-provider images.
`simulator` separately builds a Linux ARM64 NICoSim server/CLI bundle from pinned
source; it is not part of `all` yet and does not start a simulator or deploy a site.
This incremental lane bootstraps pinned Go/GCC toolchain sources on Docker or
when the recorded Podman image is missing, then binds the measured native ID
through an exact attempt overlay. `--check simulator` performs no pull/build and
does not require a previously cached toolchain; Docker bootstrap requires Buildx.
Start with `bash scripts/nke-full-arm64-local.sh --check providers` (or `--check os`).
The launcher bootstraps NKD inside the pinned utility container: no host Go/Make.
`CONTAINER_RUNTIME=docker` selects native Linux ARM64 Docker, or explicitly
selected `COLIMA_PROFILE=NAME` on Apple Silicon; not Docker Desktop on macOS.
The guide covers clean Podman/Colima VM setup and attempt-pinned fresh Packer
bootstrap. Check-only diagnostics never pull/build or create attempts.
The Colima guide exports the committed checkout to native ext4 to avoid large
source trees on the Mac shared mount. Run storage remains below that checkout's
`.nkd/`; an external absolute attempts root is not supported by the factory runner.
Native guest capacity does not provide extra physical Mac space.
Despite its historical filename, this launcher does not yet build a full
source-built NKE distribution; see the [remaining work](../docs/full-nke-source-build.md).

```sh
# Plan only: validates and creates no attempt storage.
nkdctl run plan --definition definitions/nke-v1/definition.yaml --environment definitions/nke-v1/environment-local.yaml

# Local execution; Docker, Go and acquisition/build network access are required.
# Despite its historical name, the M1 script executes CLI + CWE + Helm.
NKE_LOCAL_CLONE=/path/to/nke scripts/demo-m1-local.sh
```

`demo-m1-local.sh` generates and checks its three-output GitHub bundle in a fresh temporary directory,
whose path it prints for inspection. The repository's committed bundle selects the 34-output M2 candidate.
The local walkthrough publishes to its loopback registry using dry-run signing. Local attempts are immutable and
`unqualified-local-execution`; plans remain `static-unqualified`. See the
[M1 walkthrough and acceptance limits](../demos/m1-local.md).

The demo uses the loopback registry on port 5000. It generates its own valid
immutable attempt names.

The non-FDE OS-image bootstrap uses one shared catalog. Its authored routes are ARM64 Podman/TCG and native
ARM64 and AMD64 Linux Docker/KVM:

```sh
CONTAINER_RUNTIME=podman scripts/nke-os-local.sh local-arm64-podman-001 1
CONTAINER_RUNTIME=docker NKE_OS_ARCH=arm64 scripts/nke-os-local.sh local-arm64-docker-001 1
CONTAINER_RUNTIME=docker NKE_OS_ARCH=amd64 scripts/nke-os-local.sh local-amd64-docker-001 1
```

Use a different immutable attempt name for each run. The Podman path has a recorded
[full build and boot smoke](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-09-23-nke-arm64-local-os-build.md).
The [ARC evidence](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-01-nke-arc-os-builds.md) records successful ARM64 and AMD64
Docker/KVM builds and boot smoke at an earlier exact commit, not validation of the latest changes.
See the [Definition guide](../definitions/nke-os/README.md) for route requirements and limitations.
Set `NKE_LOCAL_CLONE` to reuse an exact local NKE checkout. On macOS the same
path must be visible inside the Podman machine; the wrapper forwards it and
fails before the build if the checkout is unavailable there.

Local attempts deliberately retain their frozen inputs, bootstrap runner and Run outputs. After reviewing an
attempt, archive those three attempt-scoped directories together instead of reusing its name:

```sh
attempt=local-arm64-podman-001
archive_root=/absolute/path/to/nkd-attempt-archive
for area in inputs bootstrap runs; do
  if [[ -e ".nkd/${area}/${attempt}" ]]; then
    mkdir -p "${archive_root}/${area}"
    mv -- ".nkd/${area}/${attempt}" "${archive_root}/${area}/"
  fi
done
```

The toolchain uses `localhost/nkd-packer-toolchain:dev-arm64` or `localhost/nkd-packer-toolchain:dev-amd64`,
according to the selected architecture, not a per-attempt tag. Once no
build is running and no retained workflow needs the local image, remove that optional cache with
`podman image rm localhost/nkd-packer-toolchain:dev-arm64` or the equivalent `docker image rm` command. Archiving or
removing storage does not make an attempt name reusable; choose a new name for every Run.

## Release consumption

```sh
scripts/demo-release-consumer.sh --help
```

The consumer takes one digest-pinned reference and one explicit Verification
Policy with `artifact-publication` scope. This does not establish M3 qualification or M4 Release-manifest
acceptance. It requires a real statically linked, digest-pinned Linux cosign executable
and a signed current v4 GitHub publication; local dry-run output is insufficient.
On macOS the script automatically builds a Linux amd64 NKD helper and verifies inside
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
`.nkd/`; scripts either create immutable attempts or require their own workspace
marker before resetting derived output.
