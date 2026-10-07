# Native disk inspection and codec observations

These October 1–5, 2026 diagnostics retain distinct source and runtime subjects. They inspect
installed package metadata in retained NKE disks or exercise QEMU format admission. They do not
establish an original live build/material/inspection join, full-disk CycloneDX, native AMD64
inspection, publication, license/security approval or qualified reproduction. Current contracts and
operator preparation belong in [live inspection](../nke-os-live-inspection.md) and the
[inspector toolchain](../../toolchains/nkd-disk-inspect/README.md).

## Draft inspection

The October 1 draft used base `91567c2717dd195829d6a88a19a6d6e4baec3dd9` plus uncommitted
changes; its exact tested tree was not retained. Its ARM64 input from attempt
`20260923-arm64-source-owned-02` was QCOW2 SHA-256
`8150fa1bc3606c54ba1bbe73573d3bdfd357467ffbd2c685ae160125220302e0`,
2,853,175,296 bytes, mode `0444`. Local image was
`sha256:6d678a5fb86066540003ad51e577d734fcb9418a87c70c3f44261a54c624cf81`.
The [full log](2026-10-01-nke-os-disk-inspection.log) records 730 packages in 30.48 seconds,
encrypted-whole-device rejection in 19.46 seconds, external-reference rejection and cleanup.
The [focused cancellation capture](2026-10-01-nke-os-disk-cancellation.log) observed the actual
`-kernel` appliance and required its exact process group to be absent after joined cancellation.

Earlier Ubuntu 26.04 tools rejected `--key all:error`; empty `--keys-from-stdin` was supported,
but that appliance failed to boot within the diagnostic deadlines. Ubuntu 24.04 initially
misdetected KVM and failed with `gic-version=host requires KVM`; explicit `force_tcg` corrected
the selection. An EFI-ZBOOT theory was not established. The whole-device encrypted fixture proved
unsupported-layout rejection, not the separate partition-key EOF branch. These root-user draft
results do not validate the later non-root source or image.

## Corrected inspection

The October 2 replay used `6e2b90b280846cf7ad21be987d645491bad6509e` with these replacements;
other compiled sources stayed at that candidate. Git blob identities are exact source descriptions,
not NKD Build identities.

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

The ARM64 binary was `sha256:7e434ec8dee5bac3514c1ca82341d32d94582b5ccbe10add0455a2e10ba0afcc`;
local image `sha256:2440b6a89c13b4ee3ce0e9a1fc173eb7ff0fb728e80488e8f92d51a0226f0cbd`.
It inspected the unchanged draft disk above as UID/GID 1000, network-none, capability-free,
read-only, with init and a private 12 GiB tmpfs. The preceding non-root failure identified a
mode-`0600` appliance kernel; the corrected image made only `/boot/vmlinuz-*` readable and
removed acquisition-only CA/source files.

The [complete log](2026-10-02-nke-os-disk-inspection.log), SHA-256
`339589ff4f64eb3744c84793da78da7c4c508b39501afcf56f0f6f03d9b8e4d6`, records 730 packages
in 35.95 seconds, unsupported encrypted-whole-device rejection, external-data/backing rejection
and observed-appliance cancellation in 0.43 seconds. No accepted inventory came from those negatives.

A separate checked-inventory follow-up at `462cced6dd079854cc622a7d49183eff8d663322` used:

| File | Tested Git blob |
| --- | --- |
| `pkg/nkd/sbom/disk_inspect.go` | `0846943246521cbf2cdb518a4214b387dcf7787a` |
| `pkg/nkd/sbom/disk_inspect_process_test.go` | `c6c714837c510d971537c2cc33aca6df55633697` |
| `pkg/nkd/assemble/codec.go` | `daff29aa2ffe01a4d1defd5ad009be48039f865a` |
| `pkg/nkd/assemble/codec_test.go` | `966268424fbb3406d58ed3c9eb5438940c50227e` |
| `pkg/nkd/assemble/vmdk_real_test.go` | `0f1dcb773327d87ec5fe45ce18473de57b5463b8` |
| `toolchains/nkd-disk-inspect/Dockerfile` | `1c7a3cdb9badef3357937d6552f80ba930b1c37d` |
| `toolchains/nkd-disk-inspect/build_test.go` | `0c71318d50bb63715d72658be66dee243dff4172` |

The rebuilt local image was
`sha256:f79899c06f1af1044a40ad996f2519f8a01789e82ee12d42b56e1bcb8541fcf6`;
the SBOM test binary was `sha256:f3cc083b82b0f345daa7a9bb5ce7b671cb31816721887093f2779e7eb712a1f1`.
The [combined appliance/VMDK follow-up log](2026-10-02-nke-os-disk-replay-follow-up.log),
SHA-256 `c50177e5ca038848c2d06da7b5fbb0ac1726d0004ae90262a66380a700c1b4d8`, retains separate
terminal replays. Image-build/inventory checks themselves were uncommitted operator captures.
The [root-admission capture](2026-10-02-nke-os-root-admission.log),
SHA-256 `e6dd80e88131c7824fe6d4d40ff02997de09c86411a8ffbe84a82083169c520a`,
records only a root guard, not an image scan.

## Runtime inspection

The October 2 concrete runtime replay used base
`d1a4ec9403a936b154b16f4c53e3d3f6daa4c739` with:

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

The separate retained disk came from the [September 23 build](2026-09-23-nke-arm64-local-os-build.md),
attempt `20260923-arm64-e2e-17`. The following closure was also reused in the later host-side replays:

| Subject | Exact SHA-256 / value |
|---|---|
| QCOW2 | `da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7`, 2,851,076,096 bytes |
| Local ARM64 image | `a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c` |
| Podman executable | `fea01b6be0bcc416eeb480073ea14c9a09c5f4557e1b4456957fefff2f982378` |
| Observed daemon | `7218aeb5d0da98276ca4c6783805c007ee52aa3d6a5b9a8a0c1b7a0c7f7b63df` |
| Unchanged ARM64 helper ELF | `fa56af977873a433c44fd2256513b3dc27a7f1a02d1befadc59c49a6765c2046` |
| Held virt-inspector | `e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b` |
| Held qemu-img | `f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e` |
| Closure package list | `eb5a0837e52afb66ed0234f5aacf3387a8fcaf6e8f437e99da7b93e7ff6fdd77`, 335 rows |
| Initial native test binary | `76e3a55d8e910c7856a8d4a642b321613add2ced5a091798dd3753e2b0d40a92` |

Sealed bytes flowed through stdin to the concrete runtime; the daemon and host kernel remained trusted.
The fixed inspection used UID/GID 65534, no network, read-only root, capability-drop, init and
`--pull=never`. It returned 730 installed packages; the real test passed in 45.17 seconds.
Its two-second cancellation result did not observe the running phase, so later tests strengthened
that assertion. Compiling an AMD64 helper was not native AMD64 runtime evidence.

## Review fixes

On October 3, a local patch on `ea6d3df94014d01a98757ddf2ee98dea6ead1e49` used:

| File | Git blob |
| --- | --- |
| `pkg/nkd/pipeline/disk_inspection_runtime.go` | `52334a7275503097f2364844a302b1c4b5816bbf` |
| `pkg/nkd/pipeline/disk_inspection_runtime_integration_test.go` | `f08af7f22df27646762340cd32ad7bca7ee1bbbe` |
| `pkg/nkd/pipeline/disk_inspection_runtime_test.go` | `d852e87460f508761618f397263ed417387c5212` |
| `pkg/nkd/sbom/disk_inspection_runtime.go` | `46f44574ae81362797910e791eee86213c3291f6` |
| `pkg/nkd/sbom/disk_inspection_runtime_test.go` | `bef9acd1e2cc944d79992279100abb3dd316b124` |
| `pkg/nkd/sbom/disk_inspect.go` | `7080ec6acb18f4d5aa9bb16eb0ff15dc7f739a93` |

The ARM64 pipeline test binary was
`sha256:ac94cb69374c417d3bfc5e31912987c30f271cb34f4a08206a7e5ed235628ca6`.
It reused the closure above, returned 730 packages, and passed in 37.98 seconds.
Cancellation first observed a running container, then joined cleanup and required no remaining
inspection container; that subtest passed in 1.33 seconds.

The host SBOM-runtime blob `46f44574ae81362797910e791eee86213c3291f6` was reported but was not
recoverable from the repository object store at correction. The image still contained the old
helper sources: SBOM runtime `689812ec087ed19a0faebdbf0811e68e6b6f6308`, inspector
`dbe44f59722af9ce1d867c0a9fa6781a6feed448`, command
`9a77cd9be52d61fe6b6ac158d6e0904452b01131`. Later in-image XML/package validation and timeout
classification did not execute in this replay.

## Mount suppression

The October 3 patch on `26093dfd47e2f0fe665adf5c4a2323051e7340d7` used:

| File | Git blob |
| --- | --- |
| `pkg/nkd/pipeline/disk_inspection_runtime.go` | `e21409058eb4b710c4689085b37d35ddd03f5956` |
| `pkg/nkd/pipeline/disk_inspection_runtime_test.go` | `f174737bbc15e47535a08f11fc63910cc84b6d7d` |
| `pkg/nkd/sbom/disk_inspection_runtime.go` | `5ca3504fda3dab6018e3ab84da5196ba02e542e1` |
| `pkg/nkd/sbom/disk_inspection_runtime_integration_test.go` | `5857692cebe94de81c570be8c3de6595aeefcc77` |

Podman 5.6.2 reported `Mounts=[]` after create, yet a synthetic nonsecret `mounts.conf` marker
appeared when selected at start. Selecting `/dev/null` at start suppressed it. Create-time mount
observation alone was therefore insufficient; overrides were needed at create, start and cleanup.
This did not suppress OCI hooks/FIPS mounts or authenticate a malicious daemon.

The unchanged runtime/helper/disk closure above returned a bound 730-package inventory in 43.22 seconds.
The pipeline test passed in 41.29 seconds, with observed-running cancellation/cleanup in 1.45 seconds.
Binary SHA-256 values were SBOM `0a2f56178a4ade20e0199bc86c7895fa686133b0251bc297bfa2e6ef7a5726c6`
and pipeline `13e2f73d9e4fd2972d36852be9f94b058e36ce0c56f05c92a2b7afd7a5d0d3db`.
An image/inspector-binding swap mutation failed after a successful scan. The later helper
subject-holding/timeout changes still did not run inside this image.

## Launcher replay

The October 5 local patch on `57b03cd24efdb0eb0d4f683bc42d4032b16745fc` used:

| File | Git blob |
| --- | --- |
| `pkg/nkd/pipeline/disk_inspection_runtime.go` | `ac330c1c8ac6fd9a6c0983615f872e3dbd562a18` |
| `pkg/nkd/pipeline/disk_inspection_runtime_integration_test.go` | `fc97ba6c73c24aee9c0f9798bc2fd2aa0a488a58` |
| `pkg/nkd/pipeline/packer_container.go` | `ed0ef03d8b5a34bdb8be96af006308b846a2d89b` |
| `pkg/nkd/sbom/disk_inspection_runtime.go` | `6e8288f11a0780ab14573fe120804f285b09f778` |
| `pkg/nkd/sbom/disk_inspection_runtime_integration_test.go` | `b45e3b6d496e2b87c8d2598be18eda5ac6b00178` |

The runtime selected `--log-driver=none` and attached start by validated create ID. It reused the
same exact closure above, not a rebuilt helper. Pipeline/SBOM test binaries were respectively
`sha256:d249377d6d3f4b08ca5af84e3dc73f98a8051fa2cfc1c47d5d4a44713ef44efa` and
`sha256:6a9c12159e4fa69ab5cba9ad56697588ee3a6a4e9625d98534f533f2d81ec562`.
The host test ran as root; inspection remained fixed at UID/GID 65534.

The pipeline test passed in 41.62 seconds, observed-running cancellation in 1.49 seconds, and
the bound 730-package inventory in 41.12 seconds. No warning/no-ID late-registration cleanup
failure was reproduced; that branch had controlled regressions only. This is native ARM64
Podman retained-disk evidence, not Docker, a new helper closure, or the original live NKE bake join.

## VMDK codec admission

The October 2 separate QEMU replay used base `84898d837da11ee273407dfac851a30499927005` with:

| File | Tested Git blob |
| --- | --- |
| `pkg/nkd/assemble/codec.go` | `9c93d117064d844d66d4a7d5359595110e62aabe` |
| `pkg/nkd/assemble/codec_test.go` | `8971f69d08b39cce6a1f01d62b1ef5ae7954e134` |
| `pkg/nkd/assemble/vmdk_admission.go` | `16c0902d4d0ca2102a4948c479508b0104d388e5` |
| `pkg/nkd/assemble/vmdk_admission_test.go` | `6041c49cb3320792495dfa50c120f77f4bfc7e74` |
| `pkg/nkd/assemble/vmdk_real_test.go` | `3ce65c15cd8b55bf8dfa3bfba989cf4935257bb2` |

Its ARM64 binary was `sha256:2c8f674762dc583d66c71ef8834db4f3b8c29a5ba2fc88a9aabc64c900d686a5`;
image `sha256:2440b6a89c13b4ee3ce0e9a1fc173eb7ff0fb728e80488e8f92d51a0226f0cbd`, and QEMU
pin as listed above. UID/GID 1000, init, no network/capabilities, read-only root and private 2 GiB
tmpfs enclosed the caller-owned runtime. The [full log](2026-10-02-vmdk-codec-admission.log),
SHA-256 `260cc1e380270411f284f9c5897f67030cdb4788a870f05ce26f4d2a11d29090`, records success.

Real sparse and stream-optimized fixtures decoded to 1,048,576 RAW bytes with SHA-256
`f32c846a3dd3cfaa09dbd7f75cb9d33d46129721912f7336955f219287eb65e4`.
Ungated QEMU accepted valid external-descriptor fixtures; NKD rejected them with
`CodeEnvelopeDecode`. Controlled markers distinguished rejection before probe from rejection before
conversion. The [checked-inventory replay](#corrected-inspection) separately used later codec blob
`daff29aa2ffe01a4d1defd5ad009be48039f865a`, real-test blob
`0f1dcb773327d87ec5fe45ce18473de57b5463b8` and binary
`d6a995807548e3d2b2fee605a5c09d44440247377e6cb5f1e91722e704adf133`.

This records narrow pinned-QEMU interoperability, not general VMDK support, cross-version decode
determinism, native AMD64 execution or qualified NKE reproduction. The separate lifecycle QCOW2
header/feature admission gap was not repaired by this VMDK change.
