# NKE ARM64 node image

This development Definition builds the NKE ARM64 non-FDE QCOW2 from its pinned component graph. NKD builds the
NKE agent, Calico, OVN, kubeadm distribution, SPIRE package, patched installer ISO and aggregate image inputs before
running the source-owned Packer project. The default path builds OVN from its pinned source. The optional overlay
selects a digest-pinned prebuilt OVN image while leaving the remaining graph unchanged.

The wrapper supports Podman and Docker. The selected runtime is recorded in the frozen Environment for the attempt.
Podman is the default:

```sh
CONTAINER_RUNTIME=podman scripts/nke-os-arm64-local.sh local-arm64-podman-001 1
```

To use Docker:

```sh
CONTAINER_RUNTIME=docker scripts/nke-os-arm64-local.sh local-arm64-docker-001 1
```

These Environments describe the local macOS Apple M4 Pro setup used for development. Run the command inside its
Linux ARM64 Docker or Podman machine, with Python 3 and the workspace mounted. The admitted VM profile requires at
least 8 CPUs, 10 GiB memory, 64 GiB free workspace storage and producer-declared network access. KVM is preferred,
with QEMU TCG fallback.

The wrapper builds the pinned toolchain locally unless `NKE_OS_TOOLCHAIN_IMAGE_ARM64` names an already-local exact
image ID or repository digest. It freezes all authored inputs below `.nkd/inputs/<attempt>` before execution. Attempt
names are immutable. The resulting disk and lifecycle record are:

```text
.nkd/runs/<attempt>/outputs/nke-node-image-arm64.qcow2
.nkd/runs/<attempt>/lifecycle.json
```

Select the explicit prebuilt OVN supplier with:

```sh
NKE_OVN_SUPPLIER=prebuilt CONTAINER_RUNTIME=podman \
  scripts/nke-os-arm64-local.sh local-arm64-prebuilt-001 1
```

This is an `unqualified-local-execution` bootstrap path. The toolchain images are not published, GitHub workflow
generation for requirement-bearing VM builds remains fail-closed, Docker has contract coverage but no recorded full
image build, and FDE remains deferred.
