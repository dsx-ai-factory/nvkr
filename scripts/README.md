# Operational scripts

The maintained scripts use the public lifecycle or demonstrate one focused
supporting contract. They never turn local diagnostics into release authority.
The [demo index](../demos/README.md) collects the presentation and guided journeys.

## Definition → Run

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
acceptance. Run it on Linux: it requires a real statically linked, digest-pinned Linux
cosign executable and a signed GitHub publication; local dry-run output is
deliberately insufficient.
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
