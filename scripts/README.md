# Runnable journeys

The maintained scripts use the public lifecycle or demonstrate one focused
supporting contract. They never turn local diagnostics into release authority.

## Definition → Run

```sh
# Plan only: validates and creates no attempt storage.
nkdctl run plan --definition definitions/nke-v1/definition.yaml --environment definitions/nke-v1/environment-local.yaml

# Local execution; Docker, Go and acquisition/build network access are required.
# Despite its historical name, the M1 script executes CLI + CWE + Helm.
NKE_LOCAL_CLONE=/path/to/nke scripts/demo-m1-local.sh
scripts/demo-m1-local.sh --publish
```

`demo-m1-local.sh` also checks that the committed GitHub-hosted bundle
(`.github/nkd-run/`) matches the current Definition/Environment
(`nkdctl run generate --check`) before executing. Local attempts are immutable and
`unqualified-local-execution`; plans remain `static-unqualified`. See the
[M1 walkthrough and acceptance limits](../docs/demo-m1.md).

The demo uses the loopback registry on port 5000. It generates its own valid
immutable attempt names.

The ARM64 non-FDE OS-image bootstrap is available for Linux ARM64 with either Podman or Docker:

```sh
CONTAINER_RUNTIME=podman scripts/nke-os-arm64-local.sh local-arm64-podman-001 1
CONTAINER_RUNTIME=docker scripts/nke-os-arm64-local.sh local-arm64-docker-001 1
```

Use a different immutable attempt name for each run. The Podman path has one recorded full build and boot smoke;
Docker is contract-tested but has not completed a recorded full image build. See the
[Definition guide](../definitions/nke-os-arm64/README.md) and
[local execution evidence](../docs/evidence/2026-09-23-nke-arm64-local-os-build.md).

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

The toolchain uses the stable local tag `localhost/nkd-packer-toolchain:dev-arm64`, not a per-attempt tag. Once no
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
