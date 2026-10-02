# Corrected local disk-inspection replay

This ARM64 Linux replay exercised the corrected diagnostic API, not lifecycle
SBOM generation, publication, qualified reproduction or AMD64 appliance inspection.
The [older draft evidence](2026-10-01-nke-os-disk-inspection.md) did not retain an
exact tested tree and must not be used as committed-candidate acceptance.

## Exact tested source and runtime

The compiled package used candidate `6e2b90b280846cf7ad21be987d645491bad6509e`
with these exact source replacements. Values are Git blob identities from
`git hash-object`, not NKD logical Build identities. Other compiled sources stayed
at that candidate. The table records the tested bytes independently of the later
documentation and commit message.

| File | Tested Git blob |
| --- | --- |
| `pkg/nkd/sbom/disk_inspect.go` | `c62d22c2d6c5b1e8709d44dea4d144bbc904f554` |
| `pkg/nkd/sbom/disk_inspect_test.go` | `8b167c8814e59aaf0c7cec345d1fe9272c51f172` |
| `pkg/nkd/sbom/disk_inspect_process_test.go` | `b503964e5d9f945c41905bf3ec7384612e075acb` |
| `pkg/nkd/assemble/codec.go` | `6fdee53fa17dac4427694b394de37c01bb8c708d` |
| `pkg/nkd/assemble/codec_test.go` | `84a4dc0196c0459c540781314ac10522af756e23` |
| `go.mod` | `330df898c1808f4433f1db219f7b01f0d58c1a11` |
| `go.sum` | `ce35604b5704fbffdb958aa84460a8a84a82dc5f` |
| `toolchains/nkd-disk-inspect/Dockerfile` | `8cc7c3717f2b813f8c1ceaa8192b4225dd50ab43` |
| `toolchains/nkd-disk-inspect/toolchain.lock.json` | `4b65ada6474f5d7b4654bd4bd9c56c5764645c6e` |

The Linux ARM64 test binary, compiled with `CGO_ENABLED=0`, had SHA-256
`7e434ec8dee5bac3514c1ca82341d32d94582b5ccbe10add0455a2e10ba0afcc`.
The newly built local runtime image was
`sha256:2440b6a89c13b4ee3ce0e9a1fc173eb7ff0fb728e80488e8f92d51a0226f0cbd`,
not a published registry manifest. Its measured `virt-inspector` and `qemu-img`
pins were respectively
`sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b`
and `sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e`.
The installed-package index SHA-256 was
`eb5a0837e52afb66ed0234f5aacf3387a8fcaf6e8f437e99da7b93e7ff6fdd77`.
These bind the inspector and probe bytes, not the separately launched
`qemu-system-aarch64`, appliance `qemu-img`, supermin, loader, libraries or kernel.
Those remain the enclosing runtime TCB; no NVIDIA license or security clearance
is asserted.

An initial non-root replay against the older runtime failed during supermin
construction. Direct inspection established that its packaged kernel was
root-owned mode `0600`. The corrected Dockerfile makes only `/boot/vmlinuz-*`
readable (`0644`) and removes the acquisition-only CA bundle and APT source file.
The new image passed direct non-root checks for kernel readability and removal
of those acquisition files. No root, device or extra capability access was added.

## Actual appliance replay

The retained local image was bound read-only: QCOW2
`sha256:8150fa1bc3606c54ba1bbe73573d3bdfd357467ffbd2c685ae160125220302e0`,
`2853175296` bytes. Execution used UID/GID `1000:1000`, `--init`,
`--network=none`, `--cap-drop=ALL`, `--security-opt=no-new-privileges`,
`--read-only` and a private 12 GiB `/tmp` tmpfs. No host credentials, devices or
guest keys were supplied. The API rejects root and places sockets, cache,
appliance temporary files and its read-only disk copy beneath one short private
workspace. Its enclosing sandbox is caller-owned, not authenticated NKD runtime
authority.

The command `/test -test.run TestDiskInspectionReal -test.v -test.timeout 15m`
completed with exit 0 and produced the following excerpts. Complete terminal
output is committed as
[2026-10-02-nke-os-disk-inspection.log](2026-10-02-nke-os-disk-inspection.log), SHA-256
`339589ff4f64eb3744c84793da78da7c4c508b39501afcf56f0f6f03d9b8e4d6`.
The raw output ends with `PASS`; these excerpts omit the full process listings:

```text
=== RUN   TestDiskInspectionRealQCOW2
disk_inspect_test.go:164: diagnostic only: QCOW2 sha256:8150fa1bc3606c54ba1bbe73573d3bdfd357467ffbd2c685ae160125220302e0 bytes=2853175296 installed-packages=730
--- PASS: TestDiskInspectionRealQCOW2 (35.95s)
=== RUN   TestDiskInspectionRealEncryptedWholeDevice
disk_inspect_test.go:216: encrypted whole-device rejection, not a partition-key EOF proof: [NKD-E-ENGINE-CAPABILITY] disk inspection requires one operating system
--- PASS: TestDiskInspectionRealEncryptedWholeDevice (15.56s)
=== RUN   TestDiskInspectionRealExternalReferences
--- PASS: TestDiskInspectionRealExternalReferences (0.02s)
=== RUN   TestDiskInspectionRealCancellation
--- PASS: TestDiskInspectionRealCancellation (0.43s)
PASS
```

Both missing external data-file and backing-file fixtures were rejected before
appliance execution. Cancellation observed the actual `-kernel` QEMU process,
not just a QMP capability probe: inspector PID1274, emulator PID1547 and recovery
child PID1548 shared PGID1274. After joined cancellation only init, the test
process and `ps` remained. The encrypted whole-device fixture produced no
inventory; the error denotes the unsupported no-OS layout, not proof of a
partition key-reading EOF branch.

## Contract validation and remaining scope

The frozen source passed native macOS `make check build workflows-check
notices-check`: formatting, vet, lint (zero issues), shellcheck, full race/coverage
suite (71.1%), Go vulnerability check (none found), both builds, generated-bundle
drift and notices. Linux ARM64 `go test -race ./pkg/nkd/sbom ./pkg/nkd/assemble
./toolchains/nkd-disk-inspect -count=1` also passed in a non-root, network-less
container using the existing local Go image
`sha256:266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`.
That contract suite uses its actual ELF test binary to verify exact argv,
environment, cwd, empty key input, all four inventory bindings, pin mismatch,
envelope rejection, close and cancellation. `git diff --check` passed.

Installed-package metadata excludes uninstalled `.deb` archives, staged OCI
images and loose files. Full material closure, acquisition/consumption
reconciliation, lifecycle inspection, full CycloneDX, FDE and qualified
reproduction remain absent. The new diagnostic envelope gate is QCOW2-only. This
replay predates the later shared VMDK codec and ELF test-helper corrections; its
source table remains the identity of what was exercised. The separate
[VMDK codec admission replay](2026-10-02-vmdk-codec-admission.md) records that
subsequent correction. The pre-existing lifecycle QCOW2 incompatible-feature/header
admission gap remains separately tracked in ADR-14.

## Checked inventory follow-up

A later replay used candidate `462cced6dd079854cc622a7d49183eff8d663322`
with the following source replacements; other compiled sources stayed at that
candidate. The earlier replay above retains its original source identity.

| File | Tested Git blob |
| --- | --- |
| `pkg/nkd/sbom/disk_inspect.go` | `0846943246521cbf2cdb518a4214b387dcf7787a` |
| `pkg/nkd/sbom/disk_inspect_process_test.go` | `c6c714837c510d971537c2cc33aca6df55633697` |
| `pkg/nkd/assemble/codec.go` | `daff29aa2ffe01a4d1defd5ad009be48039f865a` |
| `pkg/nkd/assemble/codec_test.go` | `966268424fbb3406d58ed3c9eb5438940c50227e` |
| `pkg/nkd/assemble/vmdk_real_test.go` | `0f1dcb773327d87ec5fe45ce18473de57b5463b8` |
| `toolchains/nkd-disk-inspect/Dockerfile` | `1c7a3cdb9badef3357937d6552f80ba930b1c37d` |
| `toolchains/nkd-disk-inspect/build_test.go` | `0c71318d50bb63715d72658be66dee243dff4172` |

The Dockerfile now checks `dpkg-query` before sorting its output, then removes the
temporary unsorted inventory. The actual ARM64 rebuild completed from the pinned
inputs as local image
`sha256:f79899c06f1af1044a40ad996f2519f8a01789e82ee12d42b56e1bcb8541fcf6`.
Its measured inspector, probe and installed-package index hashes equal those
listed above. Non-root checks verified sorted inventory, temporary-file removal,
kernel readability and acquisition-file removal. A controlled invalid-option
`dpkg-query` call under the same checked shell sequence exited 2 before sorting
and produced no final inventory. This exercises the failure mechanism, not an
actual failure of the successful image build.

The new ARM64 SBOM test binary SHA-256 was
`f3cc083b82b0f345daa7a9bb5ce7b671cb31816721887093f2779e7eb712a1f1`.
The same four real appliance tests passed under the original non-root,
network-less, capability-free, read-only settings and private 12 GiB tmpfs:
the unchanged retained QCOW2 yielded 730 packages, the encrypted whole-device
fixture remained unsupported, external references were rejected, and the actual
QEMU process group was absent after joined cancellation. The subject digest
matched the earlier record both before and after the replay. Complete terminal
captures from this appliance replay and the following VMDK replay are concatenated
without edits in [2026-10-02-nke-os-disk-replay-follow-up.log](2026-10-02-nke-os-disk-replay-follow-up.log).
That committed capture's SHA-256 is
`c50177e5ca038848c2d06da7b5fbb0ac1726d0004ae90262a66380a700c1b4d8`.

The updated VMDK fixture binary, SHA-256
`d6a995807548e3d2b2fee605a5c09d44440247377e6cb5f1e91722e704adf133`,
also passed against the rebuilt image with its unchanged exact QEMU pin.
Native `make check build workflows-check notices-check` and non-root Linux
race tests with atomic coverage passed. The strengthened process fixture now
distinguishes envelope rejection from an inspector failure and bounds cancellation
to less than ten seconds; the source-lock
test checks the exact AMD64 and ARM64 case mappings. These are diagnostic
regressions and ARM64 runtime evidence, not an AMD64 appliance result, a new
OS-image build, full CycloneDX or enclosing-runtime authentication.

## Root admission negative

The retained SBOM fixture binary `f3cc083b82b0f345daa7a9bb5ce7b671cb31816721887093f2779e7eb712a1f1`
also ran the existing `TestDiskInspectorRejectsRootBeforeWork` under UID/GID
`0:0` in the rebuilt image `sha256:f79899c06f1af1044a40ad996f2519f8a01789e82ee12d42b56e1bcb8541fcf6`.
Its root guard and test retain the production/source identities from the checked
inventory follow-up: `disk_inspect.go` blob `0846943246521cbf2cdb518a4214b387dcf7787a`
and process-test blob `c6c714837c510d971537c2cc33aca6df55633697`. This separate
negative does not claim that later test-fixture edits were in that binary.

The invocation used `podman run --rm --init --network=none --cap-drop=ALL
--security-opt=no-new-privileges --user=0:0 --read-only --tmpfs=/tmp:rw,size=256m
-v <retained-binary>:/test:ro <exact-image> /test
-test.run '^TestDiskInspectorRejectsRootBeforeWork$' -test.v -test.timeout 30s`.
It passed without a skip and with exit 0. The test requires the root-specific
diagnostic before a nil subject can reach filesystem or child work.
[The complete capture](2026-10-02-nke-os-root-admission.log), SHA-256
`e6dd80e88131c7824fe6d4d40ff02997de09c86411a8ffbe84a82083169c520a`,
records only that admission negative, not an image scan or authenticated sandbox capability.
