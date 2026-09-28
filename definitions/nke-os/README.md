# NKE node image

This development input owns one NKE non-FDE QCOW2 component graph. NKD builds the NKE agent, Calico, OVN,
kubeadm distribution, SPIRE package, patched installer ISO and aggregate image inputs before running the
source-owned Packer project. Architecture-dependent build and vendor argv use the compiler-owned `{{.Arch}}`
substitution; the graph is not copied per architecture.

Two thin ARM64 Definitions select execution behavior from that shared graph:

- `definition-arm64-kvm.yaml` requires native Linux ARM64 KVM and uses source-owned Packer defaults for the host CPU.
- `definition-arm64-tcg.yaml` preserves the macOS Apple M4 Pro Podman path and its proven `cortex-a57` TCG setting.

The AMD64 graph is deliberately not authored yet. `toolchains/nkd-packer/toolchain.lock.json` has no native-validated
AMD64 Docker image ID, and an exact toolchain identity is required before the catalog can select that execution
architecture. The repository workflow provides the credential-free first pass that observes the ID. A repeat native
build must agree before the Docker pin is recorded and the thin AMD64 Definition and installer supplier selection
are added. Do not copy a Podman ID or patch frozen inputs at runtime.

The architecture-generic wrapper defaults to the existing ARM64 compatibility behavior:

```sh
CONTAINER_RUNTIME=podman scripts/nke-os-local.sh local-arm64-podman-001 1
```

On native Linux ARM64, Docker selects the KVM Definition:

```sh
CONTAINER_RUNTIME=docker NKE_OS_ARCH=arm64 scripts/nke-os-local.sh local-arm64-docker-001 1
```

`scripts/nke-os-arm64-local.sh` remains a compatibility shim. The Podman Environment describes the Linux ARM64
machine behind the local macOS host. The Docker Environment requires at least 8 CPUs, 10 GiB memory, 64 GiB free
workspace storage, KVM and producer-declared network access. NKD authenticates those facts before starting QEMU.

The wrapper rebuilds the locked native toolchain and rejects any result whose content-addressed local image ID differs
from the catalog and toolchain lock. It freezes authored inputs below `.nkd/inputs/<attempt>` before execution. Attempt
names are immutable. A successful ARM64 attempt retains:

```text
.nkd/runs/<attempt>/outputs/nke-node-image-arm64.qcow2
.nkd/runs/<attempt>/lifecycle.json
```

This remains `unqualified-local-execution`. The recorded evidence proves one earlier ARM64 Podman/TCG bake only.
The new Docker/KVM workflow is an integration validation path, not generated hosted execution, publication, pipeline
attestation, FDE support or qualified reproduction.
