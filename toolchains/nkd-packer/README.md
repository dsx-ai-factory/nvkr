# Direct-Packer toolchain image

This directory defines the OCI toolchain consumed by NKD's local direct-Packer
executor. Each host-platform image contains Packer 1.15.4, the QEMU plugin 1.1.6,
QEMU for both supported guest architectures, AMD64 and ARM64 UEFI firmware, `qemu-img`,
`ssh-keygen`, Python, `tar`, a shell and the core utilities used by the NKE
Packer project. It also contains Go 1.27.1, crane 0.21.9, nfpm 2.47.0,
`dpkg-scanpackages`, `cloud-localds`, a native C compiler, Git, GnuPG, 7-Zip
and xorriso. Those are the preparation and ISO-composition tools required by the
exact NKE source at `726f76944404e1106626b8333d3576bdf67b575b` for non-FDE
builds on the supported host architectures; the locked ARM64 local image has
native validation. Go 1.27.1 matches that source's workspace version and the NKD
module used to build the source-bound local bootstrap runner.

`toolchain.lock.json` binds the Ubuntu and download-helper multi-platform base
indexes, one immutable Ubuntu archive snapshot, and the official Packer and
QEMU-plugin release archive and extracted executable digests for both host
architectures. It also pins the official Go, crane and nfpm archives and the
extracted crane and nfpm executables. The download
helper supplies the CA bundle needed to bootstrap the signed Ubuntu snapshot
from a minimal Ubuntu base. The Dockerfile reproduces Packer 1.15.4's documented
namespaced plugin layout and checksum file from the separately verified plugin
executable. It never executes a target-architecture Go binary while assembling
an image. No plugin discovery or download is performed by NKD at runtime.

Build and validate one local host architecture. The runtime check proves the
exact Packer, plugin, Go, crane and nfpm versions; QEMU/QCOW2 and firmware
availability; the SSH-key, Debian-package and ISO-composition tools; and a real
`packer validate` call through the preinstalled QEMU plugin:

```sh
toolchains/nkd-packer/build.sh --arch arm64
toolchains/nkd-packer/build.sh --arch amd64
```

Native builds run the toolchain under NKD's read-only, network-disabled,
capability-dropped container profile. A non-native image may be assembled with
`--cross-build-only`, but that mode is structural evidence only and must be
followed by the same validation and a real bake on a native host before
publication.

The [October 6 native preparation record](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-native-arc.md#native-amd64-assembly-and-validation)
records a successful AMD64 Docker assembly and this script's native validation.
It includes the retained image archive and exact identities; it grants no
publication or redistribution approval.

Use `--check` for the network-free source-lock validation. Building uses the
fixed Ubuntu snapshot and verifies every release archive before
extracting them. The command does not push, sign, or write a transport digest.

For Docker builds behind a TLS proxy, `NKD_BUILD_CA_BUNDLE` supplies a trusted
public CA bundle as an ephemeral, read-only BuildKit secret. Its mode permits
APT's unprivileged downloader to read it; it is never copied into the image.
`NKD_BUILD_ARCHIVES` selects a local directory through the `nkd_archives`
[named build context](https://docs.docker.com/reference/cli/docker/buildx/build/#build-context).
Place the official Packer, QEMU plugin, Go, crane and nfpm release archives there
under their upstream filenames for the selected architecture. Local mode copies
them through read-only build mounts and applies the same archive and executable
checksum checks. Missing or corrupt inputs fail; local mode never falls back to
downloading release archives. The pinned base images and signed Ubuntu snapshot
still require network access. Both optional inputs require Docker Buildx.

```sh
CONTAINER_RUNTIME=docker NKD_BUILD_ARCHIVES=/absolute/path/to/archives \
  NKD_BUILD_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt \
  toolchains/nkd-packer/build.sh --arch amd64
```

Docker builds use Buildx with `SOURCE_DATE_EPOCH=0` and the Docker exporter's
`rewrite-timestamp=true` option for deterministic image creation and layer-file
timestamps. Docker explicitly streams the image archive to `docker image load`,
separating timestamp rewriting from image-store unpacking; failure in either
stage stops validation. The existing Podman ARM64 local pin describes the earlier
measured image. These Dockerfile changes need a new native Podman measurement
before that pin can be replaced; a different rebuilt ID still fails closed.
The fixed epoch does not itself prove identical image
bytes. Repeat a native Docker build on fresh runners and compare the resulting
image IDs before adding a Docker pin.
`localPodmanImages` and `localDockerImages` keep the two runtime identities
separate. A null Docker entry permits a diagnostic build but is not itself an
execution pin. The local Docker wrapper freezes the exact observed image ID
into an attempt-specific catalog overlay before compiling the Plan; NKD
rechecks that ID before execution. This binds loaded image bytes to the attempt Plan, not toolchain provenance,
and does not make the image an approved
maintained toolchain. There is no toolchain publication workflow in this repository.
`images` remains `null` because a local image ID is not an OCI repository
digest. The build command fails if a rebuilt image differs from its recorded
runtime-specific local ID.
After license approval and a vulnerability scan, the exact external publication
sequence is:

```sh
IMAGE_REPOSITORY='<approved-registry>/nkd-packer-toolchain'
IMAGE_TAG=nke-726f7694-amd64
toolchains/nkd-packer/build.sh --arch amd64 --tag "${IMAGE_REPOSITORY}:${IMAGE_TAG}"
docker push "${IMAGE_REPOSITORY}:${IMAGE_TAG}"
crane digest "${IMAGE_REPOSITORY}:${IMAGE_TAG}"
```

Record the resulting `${IMAGE_REPOSITORY}@sha256:...` under `images.amd64`,
then sign, attach the SBOM and read the digest back through the approved
registry workflow before selecting it from a maintained catalog. Do not use the
mutable tag as the catalog pin.

The Packer executable remains an external subprocess under the repository's
license policy; it is not linked into `nkdctl`. The image preserves the release
archives' Packer BUSL-1.1 and QEMU-plugin MPL-2.0 license texts. Ubuntu package
copyright records remain under `/usr/share/doc`; the Go BSD-3-Clause, crane
Apache-2.0 and nfpm MIT texts are retained with the toolchain notices.

This repository does not record NVIDIA approval to redistribute Packer under
BUSL-1.1 or a completed vulnerability review of this image. The source may be
reviewed and built locally, but the image must not be published, selected by a
maintained catalog, or represented as an approved NVIDIA toolchain until both
reviews are recorded by the owning process. Keeping the image digests null in
the lock records that the source is not ready for maintained catalog use; the
required review remains an organizational publication gate, not a technical
control implemented by this source lock.

The [October 6 candidate record](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-native-arc.md#candidate-publication-exception)
records the user's explicit exception for four measured native images used only by M2.
That exception permits the candidate overlay; it does not mark these general review gates approved.

This source and a successful container build prove only the toolchain layout.
They do not prove an NKE QCOW2 bake, bootability, publication, pipeline
attestation or qualified reproduction.

## Native preparation and catalog selection

Run preparation from the final composed NKD checkout with its pinned Go toolchain. Check the source lock
without network, then build and inspect only the matching architecture on each native Linux host:

```sh
toolchains/nkd-packer/build.sh --check
packer_arch=arm64
CONTAINER_RUNTIME=podman toolchains/nkd-packer/build.sh --arch "$packer_arch"
podman image inspect --format '{{.Os}}/{{.Architecture}} {{.Id}}' \
  "localhost/nkd-packer-toolchain:dev-${packer_arch}"
```

Use `packer_arch=amd64` on the separate native AMD64 host. Docker is an explicit alternative supported
by the build script. A cross-build does not supply native execution proof. A rebuilt image differing from
a recorded runtime-specific local pin must be investigated; do not overwrite or bypass the check.
Preparation neither pushes nor signs an image, writes a source lock, or supplies an approved repository digest.
The native preparation script/workflow can retain exact image archives and measurements for a separately
authorized publication owner.

Consume a reviewed published native image through the catalog's existing `toolchainSubstitutions` contract:
bind the exact predecessor catalog digest, native `arch`, existing `from` pin, measured `to` pin and sorted
explicit component set. For each M2 disk, the nine source-map consumers are `nke-agent`, `nke-cni-calico`,
`nke-cni-ovn`, `nke-image-inputs`, `nke-kubeadm-distro`, `nke-node-image`, `nke-patched-installer-iso`,
`nke-spire-agent` and `ovn-kubernetes-chart`. Preserve `ovn-kube-ubuntu`'s independent BuildKit override.
An upstream index tag or inherited local ID cannot replace the selected native publication pin.
After publication, read back the exact repository reference's native descriptor, for example
`crane digest --platform linux/arm64 '<repository>@sha256:<digest>'`, with the matching platform.

The source lock's historical `validatedNKE` commit is not validation of the capture-enabled M2 source.
The [M2 README](../../definitions/nke-m2/README.md) owns current source selections, candidate exceptions
and generated execution requirements; the machine-managed locks and overlay own their exact values.
Native preparation and registry read-back do not establish source-owner release acceptance, runtime
isolation, a full image bake, organizational approval or independent consumer acceptance.
