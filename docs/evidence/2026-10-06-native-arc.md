# Native ARC implementation and preparation

## Scope

This records an uncommitted patch based on upstream main
`61ec66fa4ffaf75fee291450f7572d244f2e624f`. The patch admits the explicit
`native-kvm-v1` operator-trusted route under ADR-18. Contract tests render one
Definition with both native disk groups, keep credential-bearing source fetches
on native generic hosted runners, reject individual-node dispatch and reject
unsupported policy/profile selections. They do not exercise a complete ARC job.

The authored preparation workflow builds and measures on native AMD64/ARM64
GitHub-hosted runners and retains exact image archives for later publication.
It has not been dispatched. It requires a clean checkout, publishes nothing and
does not issue license/security approval or a production inspection profile.

## Candidate publication exception

On October 6 the user explicitly authorized a candidate-only exception to the
missing toolchain license/security review gate: "Consider this an exception."
It covers publishing the four final measured Packer/inspector images and using
them for the requested generated M2 candidate workflow. It does not record
completed organizational reviews, toolchain qualification or accepted M2 execution.
The source locks' review fields remain `not-recorded`; this scoped exception
does not change the general publication policy or authorize other images.
Candidate repositories are under
`ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/`.

## Native ARM64 preparation

The local Docker daemon reported Linux/aarch64. Image assembly and hash commands
executed natively there. The static helper was cross-compiled from the unchanged
helper source at the base commit using Go 1.27.1, `CGO_ENABLED=0`, `-trimpath` and
`-buildvcs=false`. Inspector assembly used the patched Dockerfile, epoch zero and
BuildKit timestamp rewriting. No CA override was needed for this build. These
ARM64 measurements precede the later archive-context and explicit helper-mode
changes; they do not claim a rebuild of the final Dockerfiles below.

| Input or observation | SHA-256 |
| --- | --- |
| Inspector Dockerfile bytes | `4f076a57c2a0b3b98c5123976dcc6cd15b57e5955a567a7c85d6a59a7f341cb2` |
| Inspector source lock bytes | `0d39d994eb5777c1d0f1d14dfb9035f6d7362d473868f41cfb0b4d0bca7e51b7` |
| Inspector exported manifest / observed Docker image ID | `b925fc0d49bbf8dcf2049fb6e17d0d1b6ff0ec3632fb466b8cead442109eef8a` |
| Inspector exported configuration | `ed1e97a50b7d391482f7f453b297b0eb159b4f2ebfee5ae743f2b09b5e2b919a` |
| Packer exported manifest / observed Docker image ID | `6ef3044d2652443ba5ee0f293b196412350b1811562c1659930b23cf8a41dee8` |
| Packer exported configuration | `0a98193fe906b43e32ae25912282b972f13f83a7211078f7927c01b5c180e2b7` |

The existing Packer build script completed its native validation. The final
inspector was loaded and selected by immutable image ID. Hash and inventory
commands ran as UID/GID 65534, network-none, read-only, with all capabilities
dropped and no-new-privileges. Its six [executable hashes](2026-10-06-native-arc/arm64-executables.sha256)
and [package inventory](2026-10-06-native-arc/arm64-packages.tsv) are retained.
These commands measure bytes; they do not run the full disk-content inspection,
measure production scratch requirements, authenticate a daemon or grant clearance.
At this preparation stage both images remained local and unpublished; the candidate publication below occurred later. The observed Docker ID is a manifest
digest in this store; it must not be described as the configuration digest.

## Final-source ARM64 replay

The final Dockerfiles were rebuilt on the local Linux/aarch64 Docker daemon.
`CONTAINER_RUNTIME=docker toolchains/nkd-packer/build.sh --arch arm64` completed
native tool/plugin checks and the real Packer validation. Inspector assembly used
`docker buildx build`, `linux/arm64`, `SOURCE_DATE_EPOCH=0` and timestamp rewriting,
followed by `docker load`. Downloads used the Dockerfiles' normal pinned archive
route; no CA or local archive override was needed. A fresh Go 1.27.1 static helper
build from the current source exactly matched the helper supplied to assembly.

| Final ARM64 artifact | SHA-256 |
| --- | --- |
| Packer exported manifest / observed Docker image ID | `ad467e43ae0ee7f3aaf5b646a117003623b9ea23de035630dc7f5c844758e0db` |
| Inspector exported manifest / observed Docker image ID | `3aed46be99e43aa273533808f3604e384e8c7f80c33ba7716bb382316ebf343e` |
| Retained image archive | `46b29b7d35636bdac1083b79a97636d36a72a8bc951bd4c3fa287a7a645fa98b` |

The archive's OCI index contains both exact native manifests. Retained
[source hashes](2026-10-06-native-arc/arm64-final/sources.sha256),
[inspector hashes](2026-10-06-native-arc/arm64-final/inspector-executables.sha256),
[Packer hashes](2026-10-06-native-arc/arm64-final/packer-executables.sha256),
[tool versions](2026-10-06-native-arc/arm64-final/inspector-versions.txt) and both
package inventories identify the final bytes. The helper executed unprivileged
and returned the expected missing-argument error with exit 2 and no stdout.
The [native appliance probe](2026-10-06-native-arc/arm64-final/appliance-probe.txt)
passed the same isolated TCG ext4 round trip as AMD64: network-none, read-only
root, UID/GID 65534, no capabilities/devices, two CPUs, 4 GiB memory and 2 GiB
temporary storage. This remains a 64 MiB diagnostic, not a full M2 inspection.

Exact images, build logs, the measurement driver and archive are retained in
the workspace's ignored `.nkd/native-toolchains/arm64-final/` directory.
The earlier ARM64 measurements above are superseded for publication preparation.
Both architectures now have final-source preparation evidence. Publication followed under
the separately recorded candidate exception; organizational review approvals remain absent.

## Native AMD64 assembly and validation

After the user authorized resuming assembly on the previously approved host,
both images were built and validated on Linux x86_64 with Docker 29.6.0 and
Buildx 0.34.1. The earlier GitHub HTTP 503 and secret-size failures were resolved
by transferring the six pinned public release archives through a declared,
read-only `nkd_archives` build context. Their digests were checked locally, after
transfer, and again inside the native builds before extraction. The
[release archive checksums](2026-10-06-native-arc/amd64/release-archives.sha256)
match the unchanged source locks. Local archive mode has no download fallback.
The real scanner stage passed with build networking disabled; separate missing
and corrupt archive probes failed at copy and checksum validation respectively.
APT still used the signed, frozen Ubuntu snapshot over verified TLS. The public
host CA bundle was mounted temporarily with mode 0444 so APT's unprivileged
downloader could read it; it was not copied into either image.

The transferred helper had mode 0644 even though the source binary was executable.
Native execution caught this, and the final Dockerfile now uses `COPY --chmod=0755`.
The final helper executed as UID/GID 65534 and rejected absent arguments with
`NKD-E-USAGE`, exit 2 and no stdout. Its measured bytes match the source helper.

| Input or observation | SHA-256 |
| --- | --- |
| Inspector Dockerfile bytes | `918a851278cb8a367ad2a05fb8c2eee3c9120e309157091e6eab72c2cfaeb5a6` |
| Packer Dockerfile bytes | `3e3cc8a808f63291bfd263ec1821d52378662e799d27acc79f5e14324f069f8f` |
| Inspector exported manifest / observed Docker image ID | `ff2a9dc76c7e6452c5b19416370f30a250b952b92af6296637978ec7abcebbc8` |
| Inspector exported configuration | `e73ac67cbf917a165a3dcd23a23f27707893d357f4fd6a204c244f898f010cc1` |
| Packer exported manifest / observed Docker image ID | `5bea678dc304d7d6814cd60646cc67b59194644335e83d7fe0a81920e8996013` |
| Packer exported configuration | `e19a2a08504cde137d260a5e5db676c95d7dc566d10da8be2b7da6d273d768ac` |
| Retained image archive bytes | `8b473db1daf37ce55b2a038861e37f878a6dce99ae18e55ced3d1fa36e2df38b` |

The [assembly results](2026-10-06-native-arc/amd64/assembly-results.log) record
successful Packer native validation, including the pinned tools, plugin and real
`packer validate` probe. The [inspector executable hashes](2026-10-06-native-arc/amd64/inspector-executables.sha256),
[Packer executable hashes](2026-10-06-native-arc/amd64/packer-executables.sha256),
[native versions](2026-10-06-native-arc/amd64/inspector-versions.txt) and both
[inspector](2026-10-06-native-arc/amd64/inspector-packages.tsv) and
[Packer](2026-10-06-native-arc/amd64/packer-packages.tsv) inventories are retained.

The [native appliance probe](2026-10-06-native-arc/amd64/appliance-probe.sh)
booted libguestfs under TCG, partitioned a disposable QCOW2, created and mounted
ext4, then wrote and read a marker successfully. The [result](2026-10-06-native-arc/amd64/appliance-probe.txt)
was obtained with network-none, read-only container root, UID/GID 65534, all
capabilities dropped, no-new-privileges, no devices, two CPUs, 4 GiB memory and
2 GiB temporary storage. The diagnostic initially selected exactly `/tmp` as
QEMU scratch, which [QEMU 8.2.2 redirects to `/var/tmp`](https://github.com/qemu/qemu/blob/v8.2.2/block.c#L794-L821).
The successful probe uses a private subdirectory, matching the existing NKD
inspector owner's layout. No production launcher or isolation restriction was
relaxed. This checks the native appliance, not a complete NKE disk inspection
or live factory evidence join.

Both exact images, build logs, source copies and measurements are retained on
`omnistation-omni-lsn-h712d` under
`/home/nhensley/.local/share/nkd/native-toolchains/2026-10-06-amd64/`.
The archive is `images.tar.gz`, with a companion checksum file. This assembly phase
issued no toolchain clearance or production profile. The later candidate publication is recorded below.

## Internal publication and generated M2 bundle

Under the recorded user exception, all four native images were published in
`ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer` and
`ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/inspector`.
The user explicitly selected "Keep internal; add read-only pulls." Both packages
retain internal visibility. Native architecture tags `native-20261006-amd64` and
`native-20261006-arm64` were transport handles; authored inputs select manifests
by digest. [Registry read-back](2026-10-06-native-arc/publication/registry-readback.json)
records all four references, verified SHA-256 over returned manifest bytes and
matching native configuration architecture. Image manifests match the assembly
measurements exactly; no image was rebuilt or modified during publication.

`definitions/nke-m2/native-toolchains.yaml` binds the exact predecessor catalog
snapshot and substitutes only the nine Packer consumers per architecture.
The two Environment profiles bind measured executable hashes and native inspector
manifests. They declare bounded capacity, not measured complete-M2 resource use:
28 GiB inspection memory, 224 GiB private scratch, a 16 GiB filesystem bound and
a 600-second timeout. The 36 GiB class memory includes the largest admitted held
subject snapshot. Native ARC hosts and the daemon must still satisfy admission.

The same Definition generates all 34 outputs, 97 jobs, two grouped native disk
helpers and twelve isolated private-image preparation steps. These steps alone
receive the ephemeral workflow token with packages:read. The owner pulls only
Plan-selected candidate toolchains through a private temporary Docker config,
removes that config on success/failure, and passes no token into runtime children.
Subsequent held runtime admission revalidates the exact locally prepared image.
Source fetch credentials remain on their separate generic hosted steps.
Package owners must grant the workflow repository Actions Read access to both
internal packages; this was requested and is not yet verified by a live workflow.
See [GitHub's package access instructions](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility).

The generated Plan digest is
`sha256:7e477f64adc6aa092ee644dad7594ba5522f0c6e374109a15ef057ddf5d21f63`.
The workflow, Plan and manifest were regenerated through the owning command;
`make workflows-check` now selects M2 and passes byte-exact regeneration.
The earlier [missing-profile diagnostic](2026-10-06-native-arc/m2-generation.log)
is historical and no longer the current generation result.

The [nkd-arc Environment](2026-10-06-native-arc/publication/arc-environment.json)
was created with custom branch restrictions and exactly one
[main branch policy](2026-10-06-native-arc/publication/arc-branches.json).
The user subsequently authorized allowing the validation branch. The authored Environment
now admits exactly `codex/m2-arc-validation` in addition to the default branch,
and the signer regex admits exactly `main` or that branch at the existing workflow path.
ARC reuses the existing identity-bound exact-branch mechanism; tag/PR refs and
wildcards remain rejected. Read-back confirms the branch was added to both
[nkd-arc](2026-10-06-native-arc/publication/arc-validation-branches.json) and
[nkd-release](2026-10-06-native-arc/publication/release-validation-branches.json).
Existing deployment policies were preserved. The checkout is named for that branch;
no commit or push was performed. Contract tests exercise both native ARC platforms,
all execution/finalization job guards and substituted-branch rejection. Byte-exact
regeneration, actionlint, yamllint, exact signer-ref checks and Linux-targeted lint
passed for the branch change. The repeated full gate reaches the same five macOS
staticcheck findings described below. The factory race suite found the prior main-only
signer expectation in the maintained-input test; it was updated to assert the exact
branch allowlist and positive/negative signing refs. The focused maintained-M2 and
native ARC race tests then passed.
The `NKE_SOURCE_TOKEN` repository secret name exists. Online ARC runner listings
do not prove the KVM scale sets' capacity or access restrictions. Full generated
M2 build, capture, inspection, reconciliation, publication, signing and consumer
verification remain unexercised. Repository changes remain uncommitted.

Validation used pinned Go 1.27.1. Fresh full uncached tests and the full race
suite passed after the published pins, generated M2 bundle and private-pull
changes. Native Linux ARM64 pipeline/factory race tests passed under a non-root
UID; the initial root-container attempt correctly failed the existing Packer
consumer identity contract, and was rerun without relaxing it. Credential
boundary tests cover read-only job scopes, exact step credentials, tampered
workflow rejection, cleanup after failed pulls and local digest substitution.
Logs are retained in the [publication evidence directory](2026-10-06-native-arc/publication/).

Formatting, vet, actionlint, yamllint, Linux-targeted golangci-lint v2.13.1,
shellcheck, vulnerability scan and diff whitespace checks passed. `make check`
remains blocked by five existing macOS staticcheck SA4023 reports in the non-Linux
executable-runtime paths of `disk_inspection_runtime.go` and `source_vendor.go`;
those functions were not changed. No repository-wide clean gate or production
acceptance is claimed. No Git commit, push, PR or workflow dispatch was performed.
