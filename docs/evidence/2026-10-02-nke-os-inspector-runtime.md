# Native ARM64 diagnostic inspection runtime

## Scope

This record exercises the concrete admitted OCI runtime against a retained,
read-only NKE QCOW2. It does **not** recreate the original factory Run or its
private material trace, issue a live factory receipt, or establish a complete
disk SBOM, publication acceptance, reproducibility, or qualification.

The implementation admits native Linux AMD64 and ARM64. Both architecture
selectors have contract tests and both helper binaries compile. This record has
native **ARM64** runtime evidence only. Native AMD64 image assembly and inspection,
and original live build-plus-inspection on either architecture, remain unexercised.
No registry publication or NVIDIA license/security clearance is asserted.

## Exact subjects and closure

Base source: `d1a4ec9403a936b154b16f4c53e3d3f6daa4c739`, with the tested runtime
source blobs identified below. Image assembly used Go `go1.27.1`, the committed
signed Ubuntu archive snapshot and package versions in
`toolchains/nkd-disk-inspect/toolchain.lock.json`. No image tag is an authority pin.

| Measurement | Exact value |
| --- | --- |
| Native runtime platform | `linux/arm64`, existing Podman VM |
| Local complete image config | `sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c` |
| Held Podman executable | `sha256:fea01b6be0bcc416eeb480073ea14c9a09c5f4557e1b4456957fefff2f982378` |
| Observed daemon identity | `sha256:7218aeb5d0da98276ca4c6783805c007ee52aa3d6a5b9a8a0c1b7a0c7f7b63df` |
| ARM64 helper ELF | `sha256:fa56af977873a433c44fd2256513b3dc27a7f1a02d1befadc59c49a6765c2046` |
| AMD64 helper ELF, compile only | `sha256:78947513ef52bbaadafa0bc9a3d5e8b2d901d4205177d406289ab0d1f8b69f4d` |
| Held inspector ELF | `sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b` |
| Held QEMU codec ELF | `sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e` |
| Inspector closure package-list bytes | `sha256:eb5a0837e52afb66ed0234f5aacf3387a8fcaf6e8f437e99da7b93e7ff6fdd77`, 335 rows |
| Retained QCOW2 | `sha256:da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7` |
| QCOW2 exact size | `2851076096` bytes |
| Native test executable | `sha256:76e3a55d8e910c7856a8d4a642b321613add2ced5a091798dd3753e2b0d40a92` |

The disk was independently rehashed before this exercise. Its original location
was `<nke-os-integration-worktree>/.nkd/runs/20260923-arm64-e2e-17/outputs/nke-node-image-arm64.qcow2`.
The runtime receives sealed bytes through stdin, not this host pathname. The
original disk is not modified. The local image config identifies immutable
rootfs diff IDs, including the helper, loaders, libraries, QEMU, supermin and
packaged appliance kernel. The local daemon and host kernel remain trusted.

Git blob identities for the tested source:

| File | Git blob |
| --- | --- |
| `pkg/nkd/pipeline/disk_inspection_runtime.go` | `cdb69ac38d72cded57579e9ccf9aea29a4f026f2` |
| `pkg/nkd/pipeline/disk_inspection_runtime_test.go` | `49a15ffe0335149ef0b255437a10be7858baea83` |
| `pkg/nkd/pipeline/disk_inspection_runtime_integration_test.go` | `e59247397c4a03924d891ef4513ef91b4c7ad449` |
| `pkg/nkd/sbom/disk_inspection_runtime.go` | `689812ec087ed19a0faebdbf0811e68e6b6f6308` |
| `pkg/nkd/sbom/disk_inspection_runtime_test.go` | `fc99bf153a751104aa55350e7702fa5239a45d74` |
| `pkg/nkd/sbom/disk_inspect.go` | `dbe44f59722af9ce1d867c0a9fa6781a6feed448` |
| `cmd/nkd-disk-inspect/main.go` | `9a77cd9be52d61fe6b6ac158d6e0904452b01131` |
| `toolchains/nkd-disk-inspect/Dockerfile` | `2fb1f111a32de26dd53d1d282c7cab18d9ce15ae` |

## Commands and containment

Run from the runtime worktree using `GOTOOLCHAIN=local` and the repository's
`.go-version`. Host-specific worktree and module-cache roots below are represented
by placeholders; the retained attempt-relative path and measured identities are
unchanged:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false \
  -o bin/nkd-disk-inspect-arm64 ./cmd/nkd-disk-inspect
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -o bin/nkd-disk-inspect-amd64 ./cmd/nkd-disk-inspect
podman build --build-arg TARGETARCH=arm64 \
  -f toolchains/nkd-disk-inspect/Dockerfile \
  -t localhost/nkd-disk-inspect:runtime-arm64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c \
  -o bin/disk-inspection-runtime-test-arm64 ./pkg/nkd/pipeline
```

The resulting test binary was executed on the native Linux VM with
`podman machine ssh`, these explicitly named environment variables, and
`-test.run TestDiskInspectionRuntimeReal -test.v -test.timeout=12m`:

```sh
NKD_TEST_DISK_INSPECT_IMAGE=sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c
NKD_TEST_DISK_INSPECTOR_SHA256=sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b
NKD_TEST_DISK_CODEC_SHA256=sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e
NKD_TEST_DISK_SHA256=sha256:da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7
NKD_TEST_DISK_QCOW2='<nke-os-integration-worktree>/.nkd/runs/20260923-arm64-e2e-17/outputs/nke-node-image-arm64.qcow2'
```

The adapter uses only its fixed command: UID/GID 65534, network-none, read-only
root, all capabilities removed, no-new-privileges, init reaping, no caller
mounts/devices/credentials, and `--pull=never`. The test declares 1000 CPU millis,
10240 MiB RAM, 512 MiB workspace availability and 600 seconds. RAM-backed `/tmp`
and equal memory/swap limits bound helper work. Pre-copy admission additionally
budgets the external factory snapshot; resource availability is not a reservation.
Operation deadlines include the mutex queue and work. Inspection cancellation
joins the stdin writer/runtime command and removes the adapter-owned container
before returning.

## Terminal results

Final exact-closure terminal output:

```text
=== RUN   TestDiskInspectionRuntimeReal
held runtime: executable=sha256:fea01b6be0bcc416eeb480073ea14c9a09c5f4557e1b4456957fefff2f982378 daemon=sha256:7218aeb5d0da98276ca4c6783805c007ee52aa3d6a5b9a8a0c1b7a0c7f7b63df
runtime-only: image=sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c subject=sha256:da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7 bytes=2851076096 packages=730 inspector=sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b codec=sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e
=== RUN   TestDiskInspectionRuntimeReal/canceled_inspection_joins_and_removes_container
--- PASS: TestDiskInspectionRuntimeReal (45.17s)
    --- PASS: TestDiskInspectionRuntimeReal/canceled_inspection_joins_and_removes_container (2.17s)
PASS
```

The cancellation case uses a two-second caller deadline, requires a timeout-coded
error and no observation, then queries the daemon and requires no adapter-owned
container to remain. The final source also contract-tests a queued inspection
with no caller deadline: its own one-second configured timeout bounds lock wait.

`make check` passed on the frozen functional code: formatting, vet, lint (zero
issues), shellcheck, race/coverage (70.8%) and Go vulnerability scanning (none
found). `go test ./... -count=1`, `go test -race ./... -count=1`,
`gofmt -l cmd client internal pkg` and `git diff --check` passed. Native ARM64
Linux ran the exact worktree's complete pipeline and SBOM race suites, including
the new regression tests, through this pinned Go test container:

```sh
podman run --rm --name=nkd-b-inspector-runtime-linux-gate --init \
  --user=65534:65534 --network=none --cpus=2 --memory=4g \
  --volume '<os-inspector-runtime-worktree>:/src:ro' \
  --volume '<go-module-cache>:/go/pkg/mod:ro' --workdir=/src \
  --env GOTOOLCHAIN=local --env GOFLAGS=-mod=readonly \
  --env GOCACHE=/tmp/nkd-go-cache --entrypoint go \
  sha256:266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6 \
  test -race ./pkg/nkd/pipeline ./pkg/nkd/sbom -count=1
```

Terminal exit zero: pipeline `9.708s`, SBOM `8.208s`. A separate execution of the
same native test binary ran all `TestDiskInspection` contract cases successfully,
including the one-second queue timeout and both architecture argument fixtures;
that command omitted the real-test environment, so its integration case skipped
as designed. The independently listed real runtime exercise above did not skip.

## Local critique

An independent local source review identified two defects before handoff:

- Pre-copy memory admission must cover the entire requested container grant plus
  the additional external held snapshot, not the maximum of independent checks.
- The operation's own timeout must begin before waiting for its mutex, otherwise
  a caller without a deadline can queue outside the documented time budget.

Both were corrected and have explicit resource-boundary and queued-deadline
regressions. An independent final
`go test -race ./pkg/nkd/pipeline -run 'TestDiskInspection(DeadlineIncludesQueuedWait|ResourceBudgetIncludesEntireContainerGrant)' -count=1`
passed (`4.282s`), and the final source was reread with no remaining confirmed
defect. This is local critique and contract evidence, not an external model panel,
security clearance, or operational acceptance of the missing cases below.

## Remaining acceptance

Native AMD64 closure/runtime evidence, original live factory build-plus-inspection
on both architectures, OS-package vulnerability/security and license approval,
registry publication and authenticated acquisition/installed-content
reconciliation remain outstanding. Installed package metadata alone is not a
full disk CycloneDX SBOM. Runtime selection is an optional diagnostic SDK
capability, not an authored Definition or compiler Plan input.
