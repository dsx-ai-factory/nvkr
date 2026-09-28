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

Use `--check` for the network-free source-lock validation. Building uses the
fixed Ubuntu snapshot and verifies every downloaded release archive before
extracting them. The command does not push, sign, or write a transport digest.
Docker builds use Buildx with `SOURCE_DATE_EPOCH=0` for deterministic image
creation metadata; Podman retains its existing build path and independent
ARM64 local image pin. The fixed epoch does not itself prove identical image
bytes. Repeat a native Docker build on fresh runners and compare the resulting
image IDs before adding a Docker pin.
There is no toolchain publication workflow in this repository. `localImages`
records a validated content-addressed local image ID when one has been built
and reproduced on its native architecture; `images` remains `null` because a
local image ID is not an OCI repository digest. The build command fails if a
rebuilt local image differs from a recorded local ID.
After license approval and a vulnerability scan, the exact external publication
sequence is:

```sh
IMAGE_REPOSITORY=<approved-registry>/nkd-packer-toolchain
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

This source and a successful container build prove only the toolchain layout.
They do not prove an NKE QCOW2 bake, bootability, publication, pipeline
attestation or qualified reproduction.
