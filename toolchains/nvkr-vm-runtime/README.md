# Plan-produced VM runtime

This catalog selects an AMD64 runtime assembled by source-pinned mkosi from the Ubuntu snapshot
`20261109T000000Z`. It is intended as a Run-only internal artifact. Run consumer integration is still
incomplete; the existing toolchain images continue to serve the maintained disk builds.

The outer Ubuntu Resolute root contains QEMU, firmware, xorriso and cloud-localds. A separate Ubuntu
Noble root at `/opt/nvkr/inspection` retains the baseline libguestfs and its kernel. Each package record
pins its archive and repository metadata bytes, version, architecture and size. The acquisition path
checks those blob pins; it does not implement upstream repository-signature verification. Exact installed
package sets are checked against mkosi's original JSON reports, retained at `/nvkr/mkosi` in the OCI layer.
These reports are evidence inputs, not an SBOM or a license-review exemption.

Packer 1.16.2 and its QEMU plugin 1.1.8 are ordinary pinned release-executable producers. Their archive,
executable and license companion digests are in the generated lock. Exact license copies are retained
under `/usr/share/doc/nvkr-vm-runtime/licenses`. The plugin checksum file contains the pinned executable's
hex digest, matching Packer's installed-plugin format. Each pin and its dependency/license closure needs
ordinary catalog review; no whole-image exception carries over.

The outer root also pins `slirp4netns` 1.3.3-1 from the same snapshot. Its TAP-descriptor mode lets
NVKR create the network device inside the private consumer namespace, then run the outbound helper
without root or capabilities. The fixed invocation disables host-loopback mapping, enables seccomp,
and provides no inbound forwarding or control API. The existing pins for libc, GLib, libseccomp and
libslirp satisfy its full dependency closure. Its package copyright notices remain installed for
ordinary catalog review. The native consumer fixture exercises helper egress; retained Run scope belongs
to the [ADR-18 ledger](../../docs/adr/reconciliation/adr-18.md).

Measured firmware bytes are copied from their packages to `/nvkr/firmware`. The inspector kernel is
copied to `/nvkr/inspection/kernel` with readable permissions. Original package files remain present.
These copies preserve the baseline bytes and confer no Secure Boot or signing authority.

The native assembly backend requires privileged launcher setup and an explicitly owned executable
scratch filesystem. Assembly runs in a separate user namespace with no mapped host root or external
network. The host's bootstrap utilities are not snapshot-pinned; this route makes no hermetic or qualified
claim. Mkosi emits an uncompressed tar, which NVKR wraps unchanged as a single OCI layer with deterministic
config and manifest. Two fresh assembly roots must produce identical complete OCI closures before a result is exposed.
Each root's snapshot timestamp supplies its source-date epoch. The configuration pins the fixed archive
adapter policy: lexical traversal, numeric ownership and stable PAX header names. The adapter is staged
read-only and selected through mkosi's extra-search-path support. Existing per-root exclusions remove
assembly caches and logs; installed package state remains available for evidence.
The resulting digest changes when any selected source, package, configuration or projected file changes.

The opt-in VM diagnostic is `bash scripts/test-mkosi-assembly.sh /absolute/evidence-directory` from the
repository. It checks concrete production and reproduction, not Run completion or disk equivalence.
ARM64 package records and native runtime proof are not yet provided.

Maintain source and static-tool pins with `nvkr catalog pin --dir toolchains/nvkr-vm-runtime` or
`nvkr catalog bump --dir toolchains/nvkr-vm-runtime`, using explicit source names and reviewed versions.
Never hand-edit `pins.lock.yaml`. Package input updates require re-resolving the complete snapshot closure,
updating the provider-file pins in `registry.yaml`, and reviewing the original installed-package reports.
