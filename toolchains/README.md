# Execution helper

`nvkr-exec/` owns the execution helper and its pins. Maintained NVKE Definitions select their own
pinned utility images. The old worker-specific Go/CGO builders, image lock, and rootfs/musl/QEMU contexts
have been removed; the NVKE OS-image build owns Kubernetes, containerd, runc and crictl.

`nvkr-packer/` owns the source and acquisition lock for the multi-architecture
Packer/QEMU toolchain used by the local direct-Packer executor. Its image is not
catalog-eligible until both platform variants are published and pinned by their
immutable repository digests.

`nvkr-disk-inspect/` supplies the staged installed-package diagnostic API's
libguestfs runtime. Repository tests compare declared pin and architecture-mapping
strings, not the Dockerfile's complete grammar or runtime closure. The image is
unpublished, unqualified, and not a maintained lifecycle toolchain. See its README
for the mandatory non-root, credential-free and network-less execution boundary.

Use `definitions/` and the demo index for current workflows. Generated hosted OS-image integration remains deferred.

`nvsentinel-debian/` owns the candidate Ubuntu 24.04/26.04 Go and dpkg build images for the NVSentinel catalog. Its lock records both architectures and local image IDs; these are not published hosted pins. See its README for exact inputs and reconstruction.

`nvsentinel-ko/` owns the local supporting ko toolchain and complete native base-image cache for the
NVSentinel image producer. Its README discloses the selected development tool source and dependency
security patch, independently of NVSentinel's source compiler version. It provides no signing or
publication authority.
