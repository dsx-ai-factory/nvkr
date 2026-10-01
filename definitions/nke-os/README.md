# NKE node image

These development inputs select non-FDE NKE QCOW2 roots with shared material producers. NKD builds the NKE agent, Calico, OVN,
kubeadm distribution, SPIRE package, patched installer ISO and aggregate image inputs before running the
source-owned Packer project. Architecture-dependent build and vendor argv use the compiler-owned `{{.Arch}}`
substitution. The AMD64 disk root and ARM64 execution overlay are explicit producer declarations, not one
architecture-neutral root.

Thin Definitions select execution behavior from the shared graph:

- `definition-arm64-kvm.yaml` requires native Linux ARM64 KVM and uses source-owned Packer defaults for the host CPU.
- `definition-arm64-tcg.yaml` preserves the macOS Apple M4 Pro Podman path and its proven `cortex-a57` TCG setting.
- `definition-amd64-kvm.yaml` selects the native Linux AMD64 KVM route and its separate, digest-pinned Ubuntu installer ISO.

The AMD64 graph is authored, but neither Docker architecture has a stable maintained image ID in
`toolchains/nkd-packer/toolchain.lock.json`. The catalog's AMD64 ID is a substitution-only baseline, not a
maintained pin or a claim that a subsequent build will reproduce it. For a local Docker attempt, the wrapper builds the
source-locked toolchain and freezes its observed exact ID into an attempt-local catalog overlay before compiling the
executable Plan. The runtime independently checks that ID. This does not authorize publication or qualification.

The architecture-generic wrapper defaults to the existing ARM64 compatibility behavior:

```sh
CONTAINER_RUNTIME=podman scripts/nke-os-local.sh local-arm64-podman-001 1
```

On native Linux ARM64, Docker selects the KVM Definition:

```sh
CONTAINER_RUNTIME=docker NKE_OS_ARCH=arm64 scripts/nke-os-local.sh local-arm64-docker-001 1
```

Native Linux AMD64 uses the same wrapper with `NKE_OS_ARCH=amd64` and its AMD64 KVM Definition.

`scripts/nke-os-arm64-local.sh` remains a compatibility shim. The Podman Environment describes the Linux ARM64
machine behind the local macOS host. The Docker Environment requires at least 8 CPUs, 10 GiB memory, 64 GiB free
workspace storage, KVM and producer-declared network access. NKD authenticates those facts before starting QEMU.
ARM64 Docker does not fall back to TCG when KVM is missing. The compatibility shim preserves the macOS/Podman
TCG route, not a Docker TCG route.

The wrapper rebuilds the source-locked native toolchain and freezes the exact selected image ID with the authored
inputs below `.nkd/inputs/<attempt>` before execution. Podman additionally requires its existing local lock ID.
Attempt names are immutable.

For Docker, the checkout and `.nkd` workspace must be visible to the daemon at
the same absolute paths. The wrapper sets `TMPDIR` to a private attempt directory
under `.nkd/bootstrap/<attempt>/tmp`, so temporary bind sources do not depend on
the runner and daemon sharing `/tmp`.

A successful attempt retains:

```text
.nkd/runs/<attempt>/outputs/nke-node-image-<arch>.qcow2
.nkd/runs/<attempt>/lifecycle.json
```

This remains `unqualified-local-execution`. The [ARC build evidence](../../docs/evidence/2026-10-01-nke-arc-os-builds.md)
records successful committed-code ARM64 and AMD64 Docker/KVM builds, boot smoke and QCOW2 uploads, alongside the
earlier ARM64 Podman/TCG bake.
The Docker/KVM workflow is an integration validation path, not generated hosted execution, publication, pipeline
attestation, FDE support or qualified reproduction. The image-ID check binds loaded bytes to the attempt's
Plan; it does not authenticate toolchain provenance.
