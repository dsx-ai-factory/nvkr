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
size through the exact held input descriptor. The native QEMU branch leaves the
extent's explicit format string empty; NKD accepts that native label or `SPARSE`,
not an absent field or another extent type.

## Exact tested source and runtime

The replay used base candidate `43359788201e44fd585e9cb131d2960dc3bf7792`
with these Git blob identities. They identify source bytes, not NKD Build identities.

| File | Tested Git blob |
| --- | --- |
| `pkg/nkd/assemble/codec.go` | `9c93d117064d844d66d4a7d5359595110e62aabe` |
| `pkg/nkd/assemble/codec_test.go` | `8971f69d08b39cce6a1f01d62b1ef5ae7954e134` |
| `pkg/nkd/assemble/vmdk_admission.go` | `16c0902d4d0ca2102a4948c479508b0104d388e5` |
| `pkg/nkd/assemble/vmdk_admission_test.go` | `7f4d745a05781d5b1548ba91ddc7ddb5bfacbe4e` |
| `pkg/nkd/assemble/vmdk_real_test.go` | `28df930943e9811d3f372a1e98c5734232b323e2` |

The Linux ARM64 test binary SHA-256 was
`f0c075b103296008e912235511940e0ea4664a74621a1ba0b92cffc855411503`.
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
so the negatives were not merely malformed images. Controlled probe/conversion markers additionally
proved invalid prefixes never launch QEMU and invalid extent metadata never
starts conversion. Legacy VMDK3, truncated headers and empty inputs fail closed.

The complete terminal output is retained locally as `nkd-pr75-vmdk-final-replay.log`,
SHA-256 `9df008be546a3f12f4e3bf62a2b7a4cfb768e8fcc73c25fb39b2e12fa946133c`.
This small ARM64 fixture replay does not establish cross-version decode
determinism, an AMD64 runtime result or qualified reproduction of an NKE image.
The lifecycle's separate QCOW2 header/feature admission gap remains tracked in
ADR-14; it is not repaired by this VMDK change.
