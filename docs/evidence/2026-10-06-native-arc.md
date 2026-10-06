# Native ARC toolchain measurements

Final native AMD64 and ARM64 Packer and inspector images were built, measured and published
internally on October 6, 2026. The maintained M2 inputs select their exact manifest digests.
[Registry read-back](2026-10-06-native-arc/publication/registry-readback.json) records all four
references, configuration digests and native architectures.

## Candidate publication exception

The user authorized publishing and using these four images for the M2 candidate despite pending
organizational toolchain license/security review. Source-lock review fields remain `not-recorded`.
The exception is limited to `ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/`;
it does not establish qualification or approve other images. Packages remain internal, with
read-only workflow pulls isolated from ARC execution.

## Native AMD64 assembly and validation

Assembly ran on Linux x86_64 with Docker 29.6.0 and Buildx 0.34.1. Pinned release archives
were supplied through a read-only build context and checked before extraction. A temporary
public CA bundle enabled verified APT snapshot access; it was not copied into either image.
Native Packer tool/plugin checks and validation passed. The inspector helper ran unprivileged
and rejected missing arguments with exit 2. Its Dockerfile explicitly preserves executable mode.

Retained measurements: [source hashes](2026-10-06-native-arc/amd64/sources.sha256),
[inspector executables](2026-10-06-native-arc/amd64/inspector-executables.sha256),
[Packer executables](2026-10-06-native-arc/amd64/packer-executables.sha256), and
[appliance probe](2026-10-06-native-arc/amd64/appliance-probe.txt).

## Final-source ARM64 replay

The final Dockerfiles were rebuilt on a native Linux/aarch64 Docker daemon using the pinned
archive download route. Native Packer tool/plugin checks and validation passed. A fresh Go 1.27.1
static inspector helper matched the assembly input and passed missing-argument admission.

Retained measurements: [source hashes](2026-10-06-native-arc/arm64-final/sources.sha256),
[inspector executables](2026-10-06-native-arc/arm64-final/inspector-executables.sha256),
[Packer executables](2026-10-06-native-arc/arm64-final/packer-executables.sha256), and
[appliance probe](2026-10-06-native-arc/arm64-final/appliance-probe.txt).

Both appliance probes used an isolated 64 MiB ext4 round trip with TCG, network disabled,
a read-only root, UID/GID 65534, no capabilities or devices, two CPUs, 4 GiB memory and
2 GiB temporary storage. They establish appliance operation, not complete NKE disk inspection
or production resource bounds. Deep inspection remains deferred to M3.

The [successful hosted run](2026-10-06-m2-metadata-publication.md#successful-hosted-validation)
records subsequent candidate execution, publication and signing for both architectures.
