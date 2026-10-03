# Diagnostic disk inspection runtime

This image supplies upstream `virt-inspector`, libguestfs, a Linux appliance
kernel and QEMU. It is not a published or qualified NKD toolchain. The Dockerfile
uses an exact official Ubuntu 24.04 base, the existing Packer toolchain's
certificate image and signed archive snapshot. The named package versions were resolved from that snapshot
for both AMD64 and ARM64. Transitive packages remain inside the same frozen,
signature-checked archive; the build records the complete installed package list
and executable hashes. Distribution copyright files remain installed.

Build the static helper from the exact NKD source using `.go-version`, then
assemble a local native image from the repository root. Record its actual image
identity, executable hashes and package inventory; no tag is an authority pin:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOTOOLCHAIN=local \
  go build -trimpath -buildvcs=false \
  -o bin/nkd-disk-inspect-arm64 ./cmd/nkd-disk-inspect
podman build --build-arg TARGETARCH=arm64 \
  -f toolchains/nkd-disk-inspect/Dockerfile \
  -t nkd-disk-inspect:local .
```

Use `GOARCH=amd64`, `bin/nkd-disk-inspect-amd64` and `TARGETARCH=amd64` on an
AMD64 host. Docker is also supported. Do not treat a local image tag, package
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

The original executable-only diagnostic API holds exact `virt-inspector`,
`qemu-img` probe and QCOW2 bytes, validates the
self-contained unencrypted envelope with held `qemu-img`, creates a private read-only
snapshot and checks it after inspection. The diagnostic capability deliberately
uses the upstream `force_tcg` backend setting and grants no KVM-device access.
Libguestfs uses its appliance, not a
host mount, and no guest executable, credentials or network are requested.
Dynamic loaders, libraries, `qemu-system-*`, appliance `qemu-img`, supermin and kernel remain the
explicit enclosing runtime TCB. See upstream
[security guidance](https://libguestfs.org/guestfs-security.1.html).

Callers of that original API MUST supply an unprivileged, non-root, network-less,
credential-free container or equivalent sandbox. UID zero is rejected; that
API does not authenticate the enclosing sandbox. The concrete
`sbom.DiskInspectionRuntime` instead admits an already-local complete image and
the held Linux Docker/Podman executable and daemon identity. It revalidates
image identity before and after inspection and uses a fixed launcher: UID/GID
65534, network-none, read-only root, all capabilities removed, no devices or
credential mounts, no-new-privileges, an init reaper and bounded process count.
The helper is part of the selected image closure and receives only authenticated
stdin bytes, not a host pathname. No runtime download or image pull is performed.
The daemon and host kernel remain trusted in this local development route; it
does not authenticate provider isolation or grant qualification.

The exact image config digest binds its immutable rootfs content, including the
helper, dynamic loaders, libraries, QEMU, supermin and packaged kernel. Inspector
and codec byte pins are checked again inside that closure. The returned opaque
installed inventory records that image identity; the executable-only API returns
no runtime binding and cannot substitute for it.

The host revalidates returned package fields, format and conflicting duplicates
using the same metadata checks as the XML projection. Each container has a fresh
random attempt label; cleanup discovers only that attempt's full container ID and
removes by ID, never by a reusable name after a failed creation. These are local
daemon safeguards, not authenticated cross-process ownership or receipt protocols.

The current exact probe pin must include
the QEMU CVE-2024-4467 fix; an arbitrary caller pin is not security clearance.
The API places its private snapshot, temporary appliance files, cache and socket
directories beneath a short `/tmp` workspace. The concrete launcher uses a
bounded RAM-backed `/tmp` and accounts for the helper's held input, private copy
and an additional 2 GiB appliance/cache margin. Before factory snapshot creation,
admission checks that the entire container memory grant and the additional
host-held copy fit concurrently, not just the minimum appliance estimate.
Admission uses actual subject size, not a fixed 8 GiB allocation. The container
memory and swap limit are equal, and declared CPU, memory, workspace capacity and
executor timeout cannot be silently weakened. A request too small for the subject
fails before inspection. Capacity is point-in-time availability, not reservation.
Cancel inspection before `Close` when immediate shutdown
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

Runtime selection is currently a diagnostic, explicitly supplied opaque local
capability. It is not an authored Definition selector or compiler Plan input.
The live factory join is separate integration work. It must bind the observed
image/tools to the inspected output's existing Plan and Build identities; this
runtime capability alone does not authorize disk SBOM, publication or qualification.

Source `toolchain.lock.json` pins the frozen acquisition inputs and helper source,
not an image
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
