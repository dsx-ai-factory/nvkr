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

The staged API has no lifecycle/CLI caller yet. It holds exact `virt-inspector`,
`qemu-img` probe and QCOW2 bytes, validates the
self-contained unencrypted envelope with held `qemu-img`, creates a private read-only
snapshot and checks it after inspection. The diagnostic capability deliberately
uses the upstream `force_tcg` backend setting and grants no KVM-device access.
Libguestfs uses its appliance, not a
host mount, and no guest executable, credentials or network are requested.
Dynamic loaders, libraries, `qemu-system-*`, appliance `qemu-img`, supermin and kernel remain the
explicit enclosing runtime TCB. See upstream
[security guidance](https://libguestfs.org/guestfs-security.1.html).

Callers MUST supply an unprivileged, non-root, network-less, credential-free
container or equivalent sandbox before calling the API. UID zero is rejected;
the API does not authenticate the enclosing sandbox. Use a standard container
init process to reap killed descendants, do not mount credentials or host
devices, and remove all capabilities. The current exact probe pin must include
the QEMU CVE-2024-4467 fix; an arbitrary caller pin is not security clearance.
The API places its private snapshot, temporary appliance files, cache and socket
directories beneath a short `/tmp` workspace. Budget up to 8 GiB for the held
input and an additional 8 GiB copy, plus appliance/cache/overlay storage; `/tmp`
may be memory-backed. Cancel inspection before `Close` when immediate shutdown
is needed. Installed-package inspection currently supports one Linux Debian
package-manager root; no-root or multi-root layouts return `engine-capability`.
An encrypted guest volume can therefore be rejected as an unsupported layout,
not necessarily a detected encryption marker.

Ubuntu installs the appliance kernel with root-only read permissions. The image
build makes only its packaged `/boot/vmlinuz-*` files readable (`0644`), so
unprivileged supermin can construct its appliance. This does not grant root,
device access, or write permission to the caller. The bootstrap CA bundle and
APT source file are removed after acquisition; runtime inspection does not
require network package access.

Source `toolchain.lock.json` pins the frozen acquisition inputs, not an image
digest or runtime closure. `go test ./toolchains/nkd-disk-inspect` checks the
declared pins and architecture mappings against the Dockerfile, not its complete
grammar or runtime closure. No AMD64 executable measurements are invented. Executable
hashes generated inside a local image are descriptive, not independent evidence.

Installed-package inventory is not a complete disk SBOM. It excludes uninstalled
Debian packages, staged OCI archives and loose files. Completing CycloneDX needs
the live authenticated acquisition/consumption closure and semantic reconciliation
of transformed content. A staged-output-only trace, including one missing guest
APT acquisition records, cannot admit full-disk SBOM or publication. FDE and
qualified reproduction remain unsupported by this diagnostic capability.
