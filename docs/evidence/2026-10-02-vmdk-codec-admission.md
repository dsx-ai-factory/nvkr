# Shared VMDK codec admission replay

This fixes the shared codec's external-extent admission boundary. It is not a
new OS-image build, full-disk SBOM, qualified runtime admission or general VMDK
support. The installed-package diagnostic remains QCOW2-only.

## Owning implementation

[QEMU v8.2.2's VMDK implementation](https://github.com/qemu/qemu/blob/v8.2.2/block/vmdk.c)
opens descriptor extents while opening an image, before producing `info` JSON.
`BDRV_O_NO_IO` therefore cannot make a post-JSON descriptor check sufficient.
The native VMDK4 sparse branch instead uses the already-open input file when
header capacity is nonzero. Its stream-optimized footer replacement does not
re-enter descriptor parsing. NKD checks this binary subset before launching
the probe, without implementing the descriptor grammar. The pinned QEMU still
owns full format validation.

The subsequent [QAPI extent metadata](https://github.com/qemu/qemu/blob/v8.2.2/qapi/block-core.json)
must identify one admitted create type and one extent covering the full logical
size through the exact held input file (`/proc/self/fd/N`). The native QEMU branch leaves the
extent's explicit format string empty; NKD accepts that native label or `SPARSE`,
not an absent field or another extent type.

## Exact tested source and runtime

The replay used base candidate `84898d837da11ee273407dfac851a30499927005`
with these Git blob identities. They identify source bytes, not NKD Build identities.
This is the earlier replay, not proof that later revisions used identical test bytes.

| File | Tested Git blob |
| --- | --- |
| `pkg/nkd/assemble/codec.go` | `9c93d117064d844d66d4a7d5359595110e62aabe` |
| `pkg/nkd/assemble/codec_test.go` | `8971f69d08b39cce6a1f01d62b1ef5ae7954e134` |
| `pkg/nkd/assemble/vmdk_admission.go` | `16c0902d4d0ca2102a4948c479508b0104d388e5` |
| `pkg/nkd/assemble/vmdk_admission_test.go` | `6041c49cb3320792495dfa50c120f77f4bfc7e74` |
| `pkg/nkd/assemble/vmdk_real_test.go` | `3ce65c15cd8b55bf8dfa3bfba989cf4935257bb2` |

The Linux ARM64 test binary SHA-256 was
`2c8f674762dc583d66c71ef8834db4f3b8c29a5ba2fc88a9aabc64c900d686a5`.
The cached local runtime image was
`sha256:2440b6a89c13b4ee3ce0e9a1fc173eb7ff0fb728e80488e8f92d51a0226f0cbd`;
its exact `qemu-img` executable pin was
`sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e`.
Execution used UID/GID `1000:1000`, `--init`, no network, all capabilities dropped,
no-new-privileges, a read-only container root and a private 2 GiB `/tmp` tmpfs.
The runtime closure remains caller-owned, not NKD-authenticated execution authority.

## Recorded result

`/test -test.run TestQualifiedVMDK -test.v -test.timeout 2m` completed with exit 0.
Real `monolithicSparse` and `streamOptimized` fixtures both decoded to the known
RAW SHA-256 `f32c846a3dd3cfaa09dbd7f75cb9d33d46129721912f7336955f219287eb65e4`,
`1048576` bytes. Text and zero-capacity binary descriptors referencing an existing
external file were rejected. Ungated real QEMU first accepted both descriptor
fixtures and reported that exact private external extent and its complete size,
so the negatives were not merely malformed images. NKD returned
`CodeEnvelopeDecode` for each fixture. Controlled probe/conversion markers
proved invalid prefixes never launch QEMU and invalid extent metadata never
starts conversion.
Legacy VMDK3, truncated headers and empty inputs fail closed.

The [checked inventory follow-up](2026-10-02-nke-os-disk-inspection.md#checked-inventory-follow-up)
records the later `codec.go` blob `daff29aa2ffe01a4d1defd5ad009be48039f865a`
and `vmdk_real_test.go` blob `0f1dcb773327d87ec5fe45ce18473de57b5463b8`,
fixture binary `d6a995807548e3d2b2fee605a5c09d44440247377e6cb5f1e91722e704adf133`
and rebuilt runtime image. That replay adds specific pre-probe diagnostic assertions
and admitted-prefix/extent marker positive controls; its complete capture is
[the follow-up log](2026-10-02-nke-os-disk-replay-follow-up.log).

The complete terminal output is committed as
[2026-10-02-vmdk-codec-admission.log](2026-10-02-vmdk-codec-admission.log),
SHA-256 `260cc1e380270411f284f9c5897f67030cdb4788a870f05ce26f4d2a11d29090`.
This small ARM64 fixture replay does not establish cross-version decode
determinism, an AMD64 runtime result or qualified reproduction of an NKE image.
The lifecycle's separate QCOW2 header/feature admission gap remains tracked in
ADR-14; it is not repaired by this VMDK change.
