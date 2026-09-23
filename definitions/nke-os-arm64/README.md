# NKE ARM64 node image

This development Definition builds the NKE ARM64 non-FDE QCOW2 from its pinned component graph. NKD builds the
NKE agent, Calico, OVN, kubeadm distribution, SPIRE package, patched installer ISO and aggregate image inputs before
running the source-owned Packer project. OVN is built from its pinned source into the regular-file OCI archive consumed
by the image-inputs producer.

The wrapper supports Podman and Docker on Linux ARM64. On the documented macOS Apple M4 Pro development host, the
wrapper delegates the command to its existing Linux ARM64 Podman machine. The selected runtime is recorded in the
frozen Environment for the attempt. Podman is the default:

```sh
CONTAINER_RUNTIME=podman scripts/nke-os-arm64-local.sh local-arm64-podman-001 1
```

To use Docker:

```sh
CONTAINER_RUNTIME=docker scripts/nke-os-arm64-local.sh local-arm64-docker-001 1
```

The Podman Environment describes the Linux ARM64 machine behind the local macOS Apple M4 Pro development host. The
Docker Environment describes a native Linux ARM64 development host. Both admitted VM profiles require at least 8
CPUs, 10 GiB memory, 64 GiB free workspace storage and producer-declared network access. KVM is preferred, with QEMU
TCG fallback.

The wrapper rebuilds the locked toolchain and rejects any result whose content-addressed local image ID differs from
the catalog and toolchain lock. It freezes all authored inputs below `.nkd/inputs/<attempt>` before execution.
Attempt names are immutable. The resulting disk and lifecycle record are:

```text
.nkd/runs/<attempt>/outputs/nke-node-image-arm64.qcow2
.nkd/runs/<attempt>/lifecycle.json
```

Frozen inputs, the attempt-scoped bootstrap runner and Run outputs remain on disk for review. Archive all three
together and never reuse the attempt name; the exact recoverable archive procedure and optional stable-toolchain
image cleanup are documented in the [script guide](../../scripts/README.md).

This is an `unqualified-local-execution` bootstrap path. The toolchain images are not published, GitHub workflow
generation for requirement-bearing VM builds remains fail-closed, Docker has contract coverage but no recorded full
image build, and FDE remains deferred.
