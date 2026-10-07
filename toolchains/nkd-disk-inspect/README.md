# Diagnostic disk inspection runtime

This image supplies upstream `virt-inspector`, `guestfish`, libguestfs, a Linux
appliance kernel, QEMU, `dpkg-deb` and the exact native Syft archive selected by
the source lock for the full content helper. The build records package inventory
and the inspector/codec/guestfish/package-reader/scanner executable hashes.
The [native ARC preparation record](../../docs/evidence/2026-10-06-native-arc.md) contains final-source native ARM64 and AMD64 assembly, executable measurements and appliance filesystem probes.
This source configuration is not a published or qualified NKD toolchain.
Both native `images` slots and license/security approval gates remain unresolved.
The [October 6 candidate exception](../../docs/evidence/2026-10-06-native-arc.md#candidate-publication-exception)
separately authorizes internal publication and M2 selection of the recorded native images;
it does not claim completed organizational reviews.
The Dockerfile
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
podman build --platform linux/arm64 --build-arg TARGETARCH=arm64 \
  -f toolchains/nkd-disk-inspect/Dockerfile \
  -t localhost/nkd-disk-inspect:local-arm64 .
```

The separate native AMD64 command is:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local \
  go build -trimpath -buildvcs=false \
  -o bin/nkd-disk-inspect-amd64 ./cmd/nkd-disk-inspect
podman build --platform linux/amd64 --build-arg TARGETARCH=amd64 \
  -f toolchains/nkd-disk-inspect/Dockerfile \
  -t localhost/nkd-disk-inspect:local-amd64 .
```

Run each assembly on its matching Linux host after the final helper source is
composed and tested. Cross-compiling the helper does not exercise it, and merely
selecting `--platform` does not prove a native host. The repository now supplies `scripts/prepare-native-toolchains.sh` and the manual
`Prepare native NKD toolchains` workflow for native Docker assembly and measurement of both architectures.
They require a clean checkout and retain exact image archives alongside measurements for later publication.
For Docker preparation, `NKD_BUILD_ARCHIVES` selects a read-only named build
context containing the official release filenames, including
`syft_1.51.1_linux_ARCH.tar.gz`. The Dockerfile checks the pinned archive digest
before extraction and never falls back to downloading a missing local archive.
`NKD_BUILD_CA_BUNDLE` supplies trusted public CA certificates through an ephemeral
secret readable by APT's downloader. Archive bytes are not secret mounts.
Preparation checks native tool versions, helper argument admission and an isolated
libguestfs appliance filesystem round trip before retaining the image archive.
It does not publish images or issue approved profiles. The full NKD inspection
route has been exercised only with Podman; the AMD64 Docker appliance probe is
scoped separately. Do not treat a local image tag, package
version, or executable digest as runtime-closure authority. No registry digest,
NVIDIA security clearance or license-allowlist approval is asserted here.

## Local measurement, not publication admission

On each native host, set `inspection_arch` to `amd64` or `arm64` for the image
just built. Inspect its OS/architecture and retain its observed local ID, then
read the five profile executable hashes plus the helper hash and package inventory from those same local
bytes. These commands do not pull, mount host paths, inspect credentials or
publish anything:

```sh
inspection_arch=arm64
inspection_image="localhost/nkd-disk-inspect:local-${inspection_arch}"
podman image inspect --format '{{.Os}}/{{.Architecture}} {{.Id}}' "${inspection_image}"
inspection_id="$(podman image inspect --format '{{.Id}}' "${inspection_image}")"
podman run --rm --pull=never --platform "linux/${inspection_arch}" \
  --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  --user 65534:65534 --entrypoint sha256sum "${inspection_id}" \
  /usr/bin/virt-inspector /usr/bin/qemu-img /usr/bin/guestfish \
  /usr/local/bin/syft /usr/bin/dpkg-deb /usr/local/bin/nkd-disk-inspect
podman run --rm --pull=never --platform "linux/${inspection_arch}" \
  --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  --user 65534:65534 --entrypoint cat "${inspection_id}" \
  /usr/share/nkd-disk-inspect/packages.tsv
```

Use `inspection_arch=amd64` on the separate AMD64 host. Record the exact source
commit, runtime, image ID and full output together. Local IDs and these measurement
commands are diagnostic descriptions; they do not authenticate the sandbox or
replace a reviewed native repository digest, license/security review, full-content
runtime exercise, or original live Run evidence. Publication needs its separately
approved owner workflow; none is supplied by these commands.

The opt-in `TestDiskInspectionRuntimeReal` and
`TestDiskInspectionRuntimeInventoryReal` accept
`NKD_TEST_DISK_INSPECT_RUNTIME` to select a Docker or Podman executable and
default to `podman`. The pipeline test records held executable and daemon
identities; the SBOM test checks the issued inventory's exact subject, image and
tool bindings. Selecting Docker enables these validation routes; it does not
mean a Docker run has been recorded.

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
65534, network-none, read-only root, all capabilities removed, no requested devices
or credential mounts, no-new-privileges, an init reaper and bounded process count.
Native Podman inspection commands also select `/dev/null` as the default mounts
file, including at container start, to suppress automatic `mounts.conf` inputs.
Docker receives no Podman-specific flag. An unsupported override fails closed;
the adapter never retries without it. This suppresses that documented native
mechanism, not arbitrary OCI hooks, FIPS mounts or a malicious daemon.
The helper is part of the selected image closure and receives only authenticated
stdin bytes, not a host pathname. No runtime download or image pull is performed.
The caller-selected executable, daemon configuration, OCI hooks and host kernel remain trusted in this
local development route. The held runtime is rechecked for freshness, not
compared with an independently supplied executable or daemon pin; it
does not authenticate provider isolation or grant qualification.

The adapter records and rechecks the runtime-reported image ID. The recorded
Podman exercise measured a config digest binding immutable rootfs content,
including the helper, dynamic loaders, libraries, QEMU, supermin and packaged
kernel. The unexercised Docker route must not inherit that config-digest claim:
the adapter does not establish which image-store identity its `.Id` represents.
Inspector and codec byte pins are checked again inside the selected image. The
returned opaque installed inventory records that observed image identity; its
`RuntimeBinding` is descriptive, not a repository-approved pin or production
admission receipt. The executable-only API returns no runtime binding.

The host revalidates returned package fields, format and conflicting duplicates
using the same metadata checks as the XML projection. Each container has a fresh
random attempt label; cleanup discovers only that attempt's full container ID and
removes by ID, never by a reusable name after a failed creation. These are local
daemon safeguards, not authenticated cross-process ownership or receipt protocols.
Create's bounded, validated full ID also selects attached start. The fixed
launcher requests `--log-driver=none`; the
[Oct 5 native ARM64 replay](../../docs/evidence/2026-10-05-nke-os-inspector-launcher.md)
checked attached report delivery and start by create ID against the unchanged
Oct 2 image. Docker and native AMD64 execution of these changes remain unexercised.
Cleanup discovery diagnostics revoke a result but do not prevent removal of a
unique valid owned ID. Ambiguous or absent identities are never removed.

UID/GID 65534 is deliberately unchanged. On a rootful daemon without user
namespaces it can share per-UID accounting with host `nobody`; the trusted-daemon
diagnostic does not claim subordinate-identity or provider isolation. Automatic
user-namespace selection needs a separately admitted and exercised capability,
not an untested launcher flag.

The current exact probe pin must include
the QEMU CVE-2024-4467 fix; an arbitrary caller pin is not security clearance.
The API places its private snapshot, temporary appliance files, cache and socket
directories beneath a short `/tmp` workspace. The concrete launcher uses a
bounded RAM-backed `/tmp` and accounts for the helper's held input, private copy
and an additional 2 GiB appliance/cache margin. Before factory snapshot creation,
admission compares the full container memory grant plus the additional
host-held copy with host availability and the NKD process's cgroup-v2 headroom,
not just the minimum appliance estimate. This is a conservative caller-side
observation, not measurement or reservation of the daemon's separate scope.
Different caller/daemon cgroup limits can reject an otherwise usable daemon or
permit a container later killed by its enclosing limit.
Admission uses actual subject size, not a fixed 8 GiB allocation. The container
memory and swap limit are equal, and declared CPU, memory, workspace capacity and
executor timeout cannot be silently weakened. A request too small for the subject
fails before inspection. Capacity is point-in-time availability, not reservation.
Cancel inspection before `Close` when immediate shutdown
is needed. Installed-package inspection currently supports one Linux Debian
package-manager root; no-root or multi-root layouts return `engine-capability`.
The configured operation deadline covers queueing, inspection, full subject
rehashing and post-run runtime checks; it is not extended after a late scan.
Cancellation/deadline remains `timeout` under ADR-03, and no unchecked report
can escape by receiving a fresh post-run budget. Owned cleanup alone gets its
separate ten-second deadline. A failed joined create client is not proof that
daemon registration is absent, so cleanup reconciles the unique attempt label
until an owned ID appears or that deadline expires.
An encrypted guest volume can therefore be rejected as an unsupported layout,
not necessarily a detected encryption marker.
The attached helper's supported failure exit statuses retain their NKD error
classes. Unknown, signaled and runtime-only exits, including ambiguous generic
CLI statuses `1` and `2`, remain `envelope-decode`; cancellation of the caller's
operation remains `timeout`. Diagnostic text is never parsed for an error code.

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

Source `toolchain.lock.json` pins frozen acquisition inputs and records the helper
source location and build metadata. It does not bind a helper source commit or
standalone helper bytes. The selected complete image binds the actual helper
bytes; the source lock is not an image digest or runtime closure.
`go test ./toolchains/nkd-disk-inspect` checks the
declared pins and architecture mappings against the Dockerfile, not its complete
grammar or runtime closure. No AMD64 executable measurements are invented. Executable
hashes generated inside a local image are descriptive, not independent evidence.

Installed-package inventory is not a complete disk SBOM. It excludes uninstalled
Debian packages, staged OCI archives and loose files. Completing CycloneDX needs
the live authenticated acquisition/consumption closure and semantic reconciliation
of transformed content. A staged-output-only trace, including one missing guest
APT acquisition records, cannot admit full-disk SBOM or publication. FDE and
qualified reproduction remain unsupported by this diagnostic capability.
