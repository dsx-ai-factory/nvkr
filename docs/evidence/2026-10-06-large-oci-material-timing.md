# Dense OCI material timing, 2026-10-06

At 22:46:44 UTC, a controlled **2,147,657,216-byte plain OCI carrier** passed the unchanged public
`RunDiskContentHelper` on native Linux ARM64 in **33.824104435 seconds**, within its existing 600-second
deadline. The complete 869-byte report contained the expected Debian package with container observation
class, exact original-carrier/tool/platform/budget bindings, and no invented root-file inventory.
The native process exited 0; the entire fixture test took 49.31 seconds. This addresses the large-material
timing probe requested in PR103 comment `4199393255`, not complete NKE disk acceptance.

## Source and reproducibility

The executable source was commit `b7605d2cd4b20c3c70d18606d628ad3304632d21`. The aggregate SHA-256 of
all **797 tracked Go/module/version file hashes** was
`3decca033c54ab6032814f438794d12151ab7ceaa0b82b29a4550c04980d3a3d` before and after execution.
The private overlay added only the retained [measurement harness](2026-10-06-large-oci-material-timing.go.txt)
to the test build; no production or ordinary test source changed. Its SHA-256 was
`7c1dcdeb5773954f0497e170501beaa48d9928a069af8cc29d8706aa712737df`, unchanged before and after.

Go 1.27.1 compiled the Linux ARM64 binary with `CGO_ENABLED=0` and `-trimpath`, without race instrumentation.
The binary SHA-256 was `435df02de80dea9093a9bc042ddc6f06273d5a75ead64cd7b42d190b6f7a8625`.
The [exact commands](2026-10-06-large-oci-material-timing.commands.txt),
[relative reproduction overlay](2026-10-06-large-oci-material-timing.overlay.json), and
[verbatim native output](2026-10-06-large-oci-material-timing.log) are retained.
The native output SHA-256 is `66856f8559410d8e1792d247e06ba35232a7beef090357d94c76818a8b9b5452`.
Building from the retained relative overlay reproduced the measured binary byte-for-byte with the same hash.

The carrier was created with standard Go tar, the existing pinned go-containerregistry v0.22.1 layer/layout
writers, and the existing transport archive owner. One layer contains the exact synthetic Debian status entry
`nkd-scanner-fixture`, version `2:1.0-1ubuntu4`, architecture `all`, plus **2,147,483,648 dense bytes** generated
by Go's seeded pseudorandom reader with seed 103. Gzip level `NoCompression` preserves the multi-GiB carrier;
compressed caching was not enabled. This is a byte-I/O scaling fixture, not an NKE package/content distribution.

| Fixture identity | Exact SHA-256 |
|---|---|
| Original plain OCI carrier | `3731f2babaef118d72d3f091a54a7098554177e3f7e24076e4765f22e81958ab` |
| Compressed layer | `1164f05ea7585149dbddab341c62897ecba67ea71d573168e909dea2f11ab927` |
| Uncompressed layer (DiffID) | `73dce11c5d74c4567af64cf823ae3bf9096ae9f6f348acbf02f7ccb016d32c55` |
| Native image manifest | `80ba26e349cc4f31d1eada8c31c3a33b50271b9680d77c75ce542b0c3e58a280` |
| Native image config | `d7e17016f70b37f8f29adf9369e3fae1f78123ef77906b6555aaa568fb1ce32b` |
| Layout index.json | `5514f572729ac06e590103371bc73745b619f4ff12e5455061be97d0de3258bb` |
| Complete report | `99be071cf6986785cf3cd99ac011580b6837989c0a1d2a2a0c4f0c8d02f886bf` |

Preparation took 12.305059569 seconds. Independently rehashing the entire original generated-file owner took
0.766779835 seconds before and 2.381146145 seconds after the helper. Both checks matched the original digest
and size. These three durations are outside the reported helper time; the helper's own capture, closure and
transport revalidation remain inside its 600-second deadline. The harness feeds the original descriptor's exact
section reader, rather than a caller pathname, into the public helper. It requires a complete subject-bound
report on success and an empty report on failure; no controlled scanner or inventory seam replaces Syft here.

## Runtime and tool observations

Only the already-local Linux ARM64 image
`6fe5b341957b02b266610fdec2b7fdb11403a8f0160d7b5b03fcda8c76d8e15d` was executed, with no image pull,
network, or package installation. Its older bundled helper was not invoked: the freshly compiled test binary
called the current public helper. Actual startup reported `Linux aarch64`, UID/GID `502:20`, CPU quota
`200000/100000` (2 CPUs), memory limit **10,737,418,240 bytes**, swap limit 0 and PIDs limit 256.
Private anonymous scratch was disk-backed **XFS**, block size 4096, with 62,784,689 available blocks at startup
(about 239.5 GiB). Capacity was observed, not reserved; the owned container and its anonymous volume were removed.

The helper authenticated these exact native executables before reading input:

| Tool | SHA-256 |
|---|---|
| Syft | `617cef943234e0cae184b4901414ca4059ad6c6923a9c9decb46f49f6a2c6573` |
| dpkg-deb | `3cf93403af757e938bda51322774697b8ff1e1d030b333008b2308e0b79a8dd6` |
| virt-inspector | `e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b` |
| qemu-img | `f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e` |
| guestfish | `db1f245febce9e588902858806fc63b7878cc2c02ad9651e9a87102bbebe2feb` |

Cgroup CPU usage advanced from 10.559449 to 40.696351 seconds around the helper and immediate diagnostic
printing: **30.136902 CPU seconds**, not scanner-only CPU time.
Whole-process memory peak reached the 10 GiB limit, with `memory.events max=1714`, `oom=0`, `oom_kill=0` and
PIDs peak 12. That peak includes preparation and disk page cache; it is not isolated Syft RSS or a production
profile requirement. No OOM kill was recorded; these counters do not isolate allocation or cache-reclaim causes.
Cache was not cleared between preparation
and inspection, so this is not a cold-cache benchmark. The helper's filesystem byte bound was 3 GiB, not a
cumulative I/O allowance; no package, closure, member, image or report gate was relaxed.

## Observed helper phases

The table subtracts paired cumulative milliseconds from the retained fixed diagnostic records.
It does not attribute uninstrumented gaps to a guessed cause.

| Phase | Start–complete (ms) | Duration (s) |
|---|---:|---:|
| Tool authentication | 0–74 | 0.074 |
| Original held capture | 74–2030 | 1.956 |
| Private OCI carrier copy | 2030–5326 | 3.296 |
| OCI marker admission | 5326–5457 | 0.131 |
| Complete OCI extraction | 5458–18505 | 13.047 |
| Pre-scan closure/platform validation | 18506–20525 | 2.019 |
| Image 1 scanner transport | 20525–23077 | 2.552 |
| Real Syft scan | 24051–28834 | 4.783 |
| Post-scan closure/original-subject validation | 28878–33709 | 4.831 |
| Report completion | 33761–33824 | 0.063 |

## Acceptance limits

This is one synthetic, dense, single-image material on native ARM64, measured through the complete existing
OCI owner and pinned real scanner. It does not establish real NKE material complexity, cold-cache performance,
multi-image shared-base inventory fit, a production inspection profile, native AMD64 execution, QCOW2/libguestfs
discovery, reconciliation/export receipts or full retained-material matrix acceptance. The additional 20-minute
Go test timeout covers fixture preparation and cleanup; it does not extend the helper's existing 600-second
operation limit. Full NKE disk inspection remains separately unverified.
