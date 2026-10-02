# Diagnostic disk inspection runtime

This image supplies upstream `virt-inspector`, libguestfs, a Linux appliance
kernel and QEMU. It is not a published or qualified NKD toolchain. The Dockerfile
uses an exact official Ubuntu 24.04 base, the existing Packer toolchain's
certificate image and signed archive snapshot. The named package versions were resolved from that snapshot
for both AMD64 and ARM64. Transitive packages remain inside the same frozen,
signature-checked archive; the build records the complete installed package list
and executable hashes. Distribution copyright files remain installed.

Build a local native image, then record its actual digest and package inventory:

```sh
podman build --build-arg TARGETARCH=arm64 \
  -t nkd-disk-inspect:local toolchains/nkd-disk-inspect
```

Use `TARGETARCH=amd64` on an AMD64 host. Do not treat a local image tag, package
version, or executable digest as runtime-closure authority. No registry digest,
NVIDIA security clearance or license-allowlist approval is asserted here.

The libguestfs CLI is GPL-2.0-or-later, invoked out of process; it is not linked
into NKD. The library is LGPL-2.1-or-later. Repository subprocess-license guidance
is in `CONTRIBUTING.md`; source licenses are in the upstream
[libguestfs COPYING](https://github.com/libguestfs/libguestfs/blob/master/COPYING).
Installed-package output follows
[virt-inspector](https://libguestfs.org/virt-inspector.1.html). Its
[v1.52.0 application projection](https://github.com/libguestfs/guestfs-tools/blob/v1.52.0/inspector/inspector.c)
omits empty optional version, release and architecture fields and zero epochs.
NKD keeps those fields separate and does not guess missing versions.

The diagnostic API holds exact executable and QCOW2 bytes, validates the
self-contained unencrypted envelope with held QEMU, creates a private read-only
snapshot and checks it after inspection. The diagnostic capability deliberately
uses the upstream `force_tcg` backend setting and grants no KVM-device access.
Libguestfs uses its appliance, not a
host mount, and no guest executable, credentials or network are requested.
Root-owned dynamic loaders, libraries, appliance and QEMU subprocesses remain the
explicit enclosing runtime TCB. See upstream
[security guidance](https://libguestfs.org/guestfs-security.1.html).

Installed-package inventory is not a complete disk SBOM. It excludes uninstalled
Debian packages, staged OCI archives and loose files. Completing CycloneDX needs
the live authenticated acquisition/consumption closure and semantic reconciliation
of transformed content. A staged-output-only trace, including one missing guest
APT acquisition records, cannot admit full-disk SBOM or publication. FDE and
qualified reproduction remain unsupported by this diagnostic capability.
