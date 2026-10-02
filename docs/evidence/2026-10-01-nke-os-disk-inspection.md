# Read-only NKE disk inspection, local diagnostic evidence

This is installed-package diagnostic evidence, not full-disk CycloneDX,
publication, reproduction or hosted-runner acceptance. Code is based on NKD
`91567c2717dd195829d6a88a19a6d6e4baec3dd9` plus the Stream C working diff.

## Exact inputs and runtime

The retained ARM64 non-FDE image from local attempt
`20260923-arm64-source-owned-02` was read-only bound into an ARM64 Linux Podman
container. Original-file SHA-256 before the final-runtime attempt was
`8150fa1bc3606c54ba1bbe73573d3bdfd357467ffbd2c685ae160125220302e0`,
size `2853175296`, mode `0444`.

The exact local runtime image ID was
`sha256:6d678a5fb86066540003ad51e577d734fcb9418a87c70c3f44261a54c624cf81`.
Its official Ubuntu 24.04 base index was
`sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3`.
Packages came from the signature-checked Ubuntu snapshot `20260920T000000Z`,
`noble`, `noble-updates`, `noble-security`, main and universe:

| Package | Exact version |
| --- | --- |
| guestfs-tools | 1.52.0-2ubuntu5 |
| libguestfs0t64 | 1:1.52.0-5ubuntu3 |
| linux-image-virtual | 6.8.0-139.139 |
| qemu-system-arm and qemu-utils | 1:8.2.2+ds-0ubuntu1.18 |

The image records 335 installed packages in
`/usr/share/nkd-disk-inspect/packages.tsv`, SHA-256
`eb5a0837e52afb66ed0234f5aacf3387a8fcaf6e8f437e99da7b93e7ff6fdd77`.
Held executable pins measured from that exact image were:

- `/usr/bin/virt-inspector`, SHA-256
  `e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b`.
- `/usr/bin/qemu-img`, SHA-256
  `f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e`.

This pins the diagnostic executables, not an authenticated production runtime
closure. Root-owned loader, libraries, QEMU appliance and kernel remain its
explicit runtime TCB. No NVIDIA license/security approval is asserted. The Go
vulnerability gate is not an OS-package vulnerability clearance.

## Recorded real inspection

Replay from the Stream C worktree after building the Dockerfile. The image ID
below is local-only, not a published registry manifest. Compilation produces the
Git-ignored `bin/nkd-disk-sbom-linux.test`; the source is the acceptance tests in
`pkg/nkd/sbom/disk_inspect_test.go`.

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go test -c ./pkg/nkd/sbom -o bin/nkd-disk-sbom-linux.test
podman run --rm --init --network=none \
  -v "$PWD/bin/nkd-disk-sbom-linux.test:/test:ro" \
  -v /Users/ikislenko/Workspace/github/nke-recipes/.git/checkouts/codex/nke-os-integration/.nkd/runs/20260923-arm64-source-owned-02/outputs/nke-node-image-arm64.qcow2:/input/subject.qcow2:ro \
  -e NKD_DISK_INSPECT_SUBJECT=/input/subject.qcow2 \
  -e NKD_DISK_QEMU_PATH=/usr/bin/qemu-img \
  -e NKD_DISK_QEMU_DIGEST=sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e \
  -e NKD_DISK_INSPECTOR_PATH=/usr/bin/virt-inspector \
  -e NKD_DISK_INSPECTOR_DIGEST=sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b \
  sha256:6d678a5fb86066540003ad51e577d734fcb9418a87c70c3f44261a54c624cf81 \
  /test -test.run TestDiskInspectionReal -test.v -test.timeout 15m
```

The final runtime used `--network=none`, a read-only input bind, held exact
executables, a sealed input snapshot and a private read-only libguestfs copy.
The implementation explicitly selects `LIBGUESTFS_BACKEND=direct` and the
upstream `LIBGUESTFS_BACKEND_SETTINGS=force_tcg`; it does not admit a KVM device.
The replay uses the engine's standard init process to forward signals and reap
orphaned descendants; this is part of the enclosing diagnostic runtime TCB.

The recorded terminal output was:

```text
=== RUN   TestDiskInspectionRealQCOW2
    disk_inspect_test.go:130: diagnostic only: QCOW2 sha256:8150fa1bc3606c54ba1bbe73573d3bdfd357467ffbd2c685ae160125220302e0 bytes=2853175296 installed-packages=730
--- PASS: TestDiskInspectionRealQCOW2 (30.53s)
=== RUN   TestDiskInspectionRealExternalReferences
=== RUN   TestDiskInspectionRealExternalReferences/data
=== RUN   TestDiskInspectionRealExternalReferences/backing
--- PASS: TestDiskInspectionRealExternalReferences (0.02s)
    --- PASS: TestDiskInspectionRealExternalReferences/data (0.01s)
    --- PASS: TestDiskInspectionRealExternalReferences/backing (0.01s)
```

The external-reference fixtures name deliberately missing files. The held QEMU
probe succeeds in reading metadata, then rejects each reference as
`NKD-E-ENVELOPE-DECODE`, `not self-contained`. Ubuntu's signed QEMU source
archive `qemu_8.2.2+ds-0ubuntu1.18.debian.tar.xz`, SHA-256
`7f1993808bbe15fee04c13aeb8ab5bc4f011b0b3b3a15d511c19d8956b34dc48`,
contains applied `CVE-2024-4467-1.patch`, which prevents `qemu-img info` from
opening external data files with `BDRV_O_NO_IO`.

The encrypted whole-device fixture returned no inventory and
`NKD-E-ENVELOPE-DECODE`, `disk inspection requires one operating system`, in
14.82 seconds. Its first test assertion incorrectly required the separate
partition key-reading EOF branch, so that combined test command exited 1.
The assertion is corrected to require coded rejection and no inventory. The
partition key-reading EOF path is source-verified, not exercised by that
whole-device fixture.

## Earlier unsuccessful runtime attempts

The initial Ubuntu 26.04 snapshot runtime used virt-inspector 1.54.0 and QEMU
10.2.1. The current manual's `--key all:error` was rejected by this exact older
CLI, so it was replaced with supported `--keys-from-stdin` and explicitly empty
stdin. Its appliance then did not boot during a three-minute encrypted-fixture
deadline and a separate 45-second blank-appliance diagnostic. The stalled
retained-image attempt was stopped. No accepted inventory came from it.

The alternative Ubuntu 24.04 runtime initially auto-detected KVM incorrectly
inside Podman, selected `gic-version=host` and `cpu=host`, then failed when QEMU
fell back to TCG: `gic-version=host requires KVM`. Selecting upstream `force_tcg`
corrected this runtime misselection. An earlier EFI-ZBOOT incompatibility theory
was not established and is not the reported cause.

## Remaining scope

The observed packages are installed package-manager metadata. They are not
staged `.deb` payloads, OCI archives or loose-file inventory. The factory-owned
material trace bridge, guest acquisition closure and semantic reconciliation
remain incomplete, so full-disk CycloneDX and publication admission stay closed.
AMD64 runtime inspection, FDE and qualified reproduction were not accepted here.

## Final acceptance and cancellation

The corrected full native suite exited 0. Its copied terminal output is in
[the full native log](2026-10-01-nke-os-disk-inspection.log): 730 installed
packages in 30.48 seconds, coded encrypted-whole-device rejection in 19.46
seconds, both external-reference negatives and real QEMU process-group cleanup.
The cancellation selector was then tightened to wait for the actual `-kernel`
appliance, excluding preliminary QMP probes. That focused final-code test exited
0 in 0.55 seconds; see [the actual-appliance cancellation log](2026-10-01-nke-os-disk-cancellation.log).
Inspector PID18, appliance PID297 and recovery child PID298 shared PGID18.
The final assertion checks every process row against that exact captured group,
including exited zombies, not only executable names. After joined cancellation,
only the init process, test process and observation `ps` remained.

The coordinator independently replayed the full final-code suite against the
same exact runtime and read-only subject. It exited 0: 730 installed packages
in 39.21 seconds, encrypted-whole-device rejection in 16.74 seconds, external
reference rejection in 0.02 seconds and actual-appliance cancellation in 0.50
seconds. Inspector PID1246, QEMU PID1522 and recovery PID1523 shared PGID1246;
the joined cancellation left no row in that group. No test container remained.

A diagnostic run without the engine init process confirmed `Z` exited zombies,
not live children, under a Go test acting as PID1. The standard `--init` reaper
removed them. No custom reaper or process-group implementation was added.

After the final accepted disk scan, the original-file SHA-256 remained
`8150fa1bc3606c54ba1bbe73573d3bdfd357467ffbd2c685ae160125220302e0`,
size `2853175296`, mode `0444`. The held source and private snapshot were also
checked by the API. This proves no change to this retained input, not all runtime
providers or future toolchains.

The final exact-file macOS `make check build workflows-check notices-check`
exited 0: formatting, vet, lint (zero issues), shellcheck, full race/coverage
suite, Go vulnerability check, both builds, generated-bundle drift and notices.
Coverage was 71.2%; the Go vulnerability check found no vulnerabilities.
The final ARM64 Linux `go test -race ./pkg/nkd/sbom ./pkg/nkd/assemble -count=1`
also exited 0, using the existing exact local Go image
`sha256:266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`
with no network. `git diff --check` passed. These are local contract gates,
separate from the real appliance evidence and not full-disk SBOM acceptance.
