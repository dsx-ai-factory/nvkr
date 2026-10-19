# Ubuntu 24 NVKE node images

These opt-in inputs select Ubuntu **24.04.5** installers for native AMD64 and ARM64 NVKE
QCOW2 builds. Ubuntu 26.04 remains the existing default. The source is M2's pinned NVKE
`ff21ba8c07cafff74b68e238a8876af11e5beded`, which contains upstream's
[Ubuntu 24 support](https://github.com/dsx-ai-factory/nvke/commit/96ccc50cb7f8ed8bb08f7cf8655906982774f809).
The installer material selects the guest OS; this source has no Ubuntu-version Packer variable.

| Selection | Definition | Output |
|---|---|---|
| Local plain AMD64 | `definition-amd64.yaml` | `nvke-node-image-ubuntu24-amd64` |
| Local plain ARM64 | `definition-arm64.yaml` | `nvke-node-image-ubuntu24-arm64` |
| GitHub plain AMD64 | `definition-github-image-amd64.yaml` | `nvke-node-image-ubuntu24-amd64` |
| GitHub plain ARM64 | `definition-github-image-arm64.yaml` | `nvke-node-image-ubuntu24-arm64` |
| GitHub FDE AMD64 | `definition-github-fde-amd64.yaml` | `nvke-node-fde-ubuntu24-amd64` |
| GitHub FDE ARM64 | `definition-github-fde-arm64.yaml` | `nvke-node-fde-ubuntu24-arm64` |

Each selects the complete eleven-producer closure. The ordered stack is the existing M2 OS
catalog, its native-toolchain overlay, then `ubuntu24.json`. The last overlay replaces the two
installer suppliers and the agent producer as complete declarations, bound to the predecessor
catalog digest. No existing lock, M2 output, or committed workflow is changed.

## Agent compatibility and pins

Changing only the installer is insufficient: an agent compiled against Ubuntu 26's libc can
install successfully on Ubuntu 24 and then fail to start. The Ubuntu 24 agent uses native,
digest-pinned **Go 1.27.2 Bookworm** images with glibc 2.36. Both preparation and compilation
reject a different libc. Preparation downloads nFPM 2.47.0 with the archive and executable
digests retained in the Packer toolchain lock, then captures it with the vendored source.
Offline compilation uses NVKE's existing Debian packaging target and that exact executable.
The pinned source's `check-ubuntu-elf.sh` must accept the agent binary: the native Ubuntu
loader, a libc dependency, no RPATH or path-qualified dependency, and required GLIBC versions
no newer than 2.39. Its updater must remain static, with no ELF program interpreter,
matching the pinned source's own package checks. A failed check prevents acceptance of
the package output.

The remaining materials and Packer toolchains retain their M2 definitions. Packer's host OS
does not select the guest OS. Network images retain their own container filesystems; package
producers continue to package their source-selected upstream payloads. This change does not
claim independent ABI inspection of every acquired payload.

The installer pins match Ubuntu's published
[AMD64 checksums](https://releases.ubuntu.com/24.04/SHA256SUMS) and
[ARM64 checksums](https://cdimage.ubuntu.com/ubuntu/releases/24.04.5/release/SHA256SUMS):

| Architecture | Installer SHA-256 |
|---|---|
| AMD64 | `97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8` |
| ARM64 | `f12484464c7d4d73e2167e0a7dbda1e669f8fae9bcf778dc0944937034b320cb` |

These are exact-byte acquisition pins, not upstream signature verification. Updating the
installer release or agent toolchain changes the logical Build identity. The usual overlay
predecessor check rejects silently applying these replacements to a changed M2 catalog.

## Build locally

Use a native Linux host with Docker, KVM, 8 CPUs, 10 GiB memory, at least 64 GiB free
workspace storage, and access to the pinned sources. Run from the repository root:

```sh
NVKE_OS_UBUNTU_VERSION=24.04 NVKE_OS_ARCH=arm64 \
  scripts/nvke-os-local.sh ubuntu24-arm64-001 1
```

Use `NVKE_OS_ARCH=amd64` and a fresh attempt name on an AMD64 host. The wrapper freezes
the selected inputs and its locally built Packer image ID before compiling the attempt.
The agent retains its separate Bookworm toolchain. Optional `NVKE_LOCAL_CLONE` supplies a
Git transport override; it does not replace the selected source commits.

The retained disk is `.nvkr/runs/<attempt>/outputs/nvke-node-image-ubuntu24-<arch>.qcow2`.
Successful local execution remains unqualified. These native KVM inputs do not change the
separate ARM64 TCG laptop-demo profiles or their Ubuntu 26 image URLs.

## Generate a native GitHub build

The `environment-github-<arch>.yaml` files admit the existing native ARC KVM route and
select a utility runner matching the disk architecture. Build-only
disk finalization admits **one disk per Run**, so select one hosted Definition at a time.
With `bin/nvkr` built, run from the repository root:

```sh
NVKR_UBUNTU24_BUNDLE=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-ubuntu24.XXXXXX")
bin/nvkr run generate \
  --definition definitions/nvke-os-ubuntu24/definition-github-image-arm64.yaml \
  --environment definitions/nvke-os-ubuntu24/environment-github-arm64.yaml \
  --engine github --root "$NVKR_UBUNTU24_BUNDLE"
bin/nvkr run generate \
  --definition definitions/nvke-os-ubuntu24/definition-github-image-arm64.yaml \
  --environment definitions/nvke-os-ubuntu24/environment-github-arm64.yaml \
  --engine github --root "$NVKR_UBUNTU24_BUNDLE" --check
```

Choose another hosted Definition from the table to change architecture or encryption,
and use its matching architecture Environment.
All use the `nvke-os-ubuntu24` bundle name, so generate alternatives into separate scratch
roots. Runner access, private NVKE source access, and read access to the retained native
Packer toolchain packages are prerequisites. The generated workflow uses manual dispatch
under the existing branch policy. It retains raw QCOW2 and a `static-unqualified` Run;
it does not publish or sign a Release. These inputs are separate from maintained M2
release membership.

FDE uses the existing public bootstrap passphrase `changeit`. It provides no deployed
confidentiality claim; private-passphrase FDE and strict FDE inspection remain unavailable.

## Validation scope

Contract tests compile all four hosted selections and both local selections, check native
KVM admission and exact material membership, compare Build identities, and generate/check
each hosted bundle. These tests do not build or boot an Ubuntu 24 disk. A full native bake,
guest boot and agent-start smoke test remain external acceptance work for each selected
architecture and encryption mode. A separate native ARM64 container exercise built the
agent package offline and passed its ELF checks; it did not execute the disk build.
Current evidence belongs in
[reconciliation](../../docs/adr/reconciliation.md#ubuntu-24-node-image-inputs).
