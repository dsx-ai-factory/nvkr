# Direct-Packer toolchain image

This directory defines the OCI toolchain consumed by NKD's local direct-Packer
executor. Each host-platform image contains Packer 1.14.2, the QEMU plugin 1.1.6,
QEMU for both supported guest architectures, AMD64 and ARM64 UEFI firmware, `qemu-img`,
`ssh-keygen`, Python, `tar`, a shell and the core utilities used by the NKE
Packer project.

`toolchain.lock.json` binds the Ubuntu and download-helper multi-platform base
indexes, one immutable Ubuntu archive snapshot, and the official Packer and
QEMU-plugin release archive and extracted executable digests for both host
architectures. The download
helper supplies the CA bundle needed to bootstrap the signed Ubuntu snapshot
from a minimal Ubuntu base. The Dockerfile reproduces Packer 1.14.2's documented
namespaced plugin layout and checksum file from the separately verified plugin
executable. It never executes a target-architecture Go binary while assembling
an image. No plugin discovery or download is performed by NKD at runtime.

Build and validate one local host architecture:

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
fixed Ubuntu snapshot and verifies both downloaded release archives before
extracting them. The command does not push, sign, or write a transport digest.
After both platform images are published through an approved registry workflow,
record their immutable repository digests in the lock and only then select them
from a maintained catalog.

The Packer executable remains an external subprocess under the repository's
license policy; it is not linked into `nkdctl`. The image preserves the release
archives' Packer BUSL-1.1 and QEMU-plugin MPL-2.0 license texts. Ubuntu package
copyright records remain under `/usr/share/doc`.

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
