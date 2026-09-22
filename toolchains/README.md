# Execution helper

`nkd-exec/` owns the execution helper and its pins. Maintained NKE and AICR Definitions select their own
pinned utility images. The old worker-specific Go/CGO builders, image lock, and rootfs/musl/QEMU contexts
have been removed; the NKE OS-image build owns Kubernetes, containerd, runc and crictl.

`nkd-packer/` owns the source and acquisition lock for the multi-architecture
Packer/QEMU toolchain used by the local direct-Packer executor. Its image is not
catalog-eligible until both platform variants are published and pinned by their
immutable repository digests.

Use `definitions/` and the demo index for current workflows. Wrapped OS-image integration remains deferred.
