# Source-owned APT capture

This change captures actual guest APT package archives and checks the capture's
byte correspondence. It does not issue acquisition-origin authority, an installed
inventory, a live-Run receipt or a full disk SBOM (ADR-10 and ADR-13).

## Field ownership

| Capture field | Authoritative operation | Claim limit |
| --- | --- | --- |
| Architecture | Explicit Packer input, checked against `dpkg-deb` package architecture | `all` packages are accepted for either target |
| Name, version, package architecture | `dpkg-deb --show` on retained package bytes | NKD currently treats these as source-reported metadata |
| Digest, size | Actual retained `.deb` and exported tar member bytes | Not a reproduction or source-origin identity |
| Repository URIs | `python3-apt` versions with exact version, architecture, SHA-256 and size | Metadata locations, not observed redirect chains or authenticated repository signatures |
| Member path | Digest-derived `packages/<sha256>.deb` | A tar member name, never a filesystem grant |
| Capture archive identity | NKD's held-file capability and exact archive hashing | Not a live-Run or final-QCOW2 binding |

The minimal `nkd.dev/package-acquisitions/v1` capture document is separate from
the build-material manifest. Build inputs have component bindings; guest APT
events have package archive sizes and repository metadata locations. Neither
description authenticates an acquisition simply by being well-formed.

## Source and byte checks

The NKE source owns the optional APT `Pre-Install-Pkgs` hook. Its native v1
protocol supplies the actual archive paths immediately before dpkg invocation.
GNU xargs passes newline-delimited paths directly to cp, without shell evaluation
of those paths. Numbered backups prevent repeated filenames from overwriting
previous bytes. The recorder deduplicates exact bytes, validates package metadata
with dpkg, matches repository metadata with python3-apt and produces an
uncompressed USTAR capture. Exported bytes are rehashed before no-replace
publication. No new third-party parsing dependency is introduced.

Capture is disabled by default. When enabled, both Packer variants export a
separate capture through the existing file-download provisioner, before index
and SSH-key cleanup. The source removes its capture configuration, archives and
recorder from the guest. Capture settings must be part of the selected producer
configuration, not silently enabled by an adapter.

NKD verifies the held capture without extraction. It rejects noncanonical JSON,
unsupported architecture, links, undeclared members, duplicate/unsorted records,
digest/size substitution, unsafe repository metadata locations and nonzero data
after the tar terminator. Limits are 16,384 packages, 1 GiB per package, 8 MiB
manifest and 8 GiB total archive. Source export additionally limits captured
package bytes to 7 GiB; capture storage is charged to the existing VM workspace.
Copying may fail earlier if workspace storage is exhausted, which fails the
build rather than producing an accepted capture.

## Validation and remaining acceptance

The recorded source exercises used NKE `f1eddbf62ff5866d593a8eb05d8eb2fcac80be1c`.
The source candidate is rebased onto `02b33339426031782ead68b8b9b05c64c5f4718b`,
whose intervening change does not alter the OS capture files.
The NKD baseline is `d1a4ec9403a936b154b16f4c53e3d3f6daa4c739`.
Both architectures have contract coverage. Native APT parsing and real Packer
configuration validation cover both architectures, plain/FDE and capture on/off.
These validations do not boot a VM.

NKD's full completion gate passed with `GOFLAGS=-count=1 make check`, including
uncached race tests, vet, lint, shellcheck and vulnerability checks. The uncached
non-race suite `go test ./... -count=1` also passed. The final gate log is retained
at `/private/tmp/nkd-apt-capture.NQ7NNU/nkd-check.log`. The NKE OS module's native
`make -C os-image test` passed, including QEMU/entrypoint and Go tests. Packer
format checking passed. Packer validation used the existing installed Packer
1.15.4 and QEMU plugin closure, without `packer init`.
The final NKE Packer suite passed 77 tests (two opt-in skips); the separate native
APT/Packer capture suite passed all 11 tests with no skips. Logs are retained at
`/private/tmp/nkd-apt-capture.NQ7NNU/nke-packer-tests.log` and
`/private/tmp/nkd-apt-capture.NQ7NNU/nke-native-capture-tests.log`.

Each real capture was separately admitted through `heldfile.OpenContext` and
`ObserveAcquisitionArchive` in the opt-in `TestObserveRealAcquisitionArchive`.
Its `NKD_TEST_ACQUISITION_ARCHIVE` variable selects an existing real capture for
this diagnostic test; it grants neither source nor live-Run authority.

A real ARM64 Ubuntu 26.04 container acquired `hello` version `2.10-5build1`.
Its retained archive is 25,642 bytes, SHA-256
`8ab9e5cf329e88413dd502edd5e0907550132470ecb926d500d0d23a30c735d8`.
NKD accepted the actual 30,720-byte capture, SHA-256
`684ea432b9002ebb14fefcf81920e644dbd92d3afa7de8b24636f203d9e5504c`.
This demonstrates native APT cache-cleanup survival, dpkg/python-apt matching and
cross-language capture acceptance, not an NKE autoinstall or full image bake.
The retained local capture is `/private/tmp/nkd-apt-capture.NQ7NNU/arm64-capture.tar`.

The same real APT exercise also passed for AMD64 under local container emulation.
Its actual `hello` package is 26,068 bytes, SHA-256
`b6e9bef3c865aa17757cd9ffa820c05a37833f8a6b58287afc9f32dedb345965`.
The capture is 30,720 bytes, SHA-256
`4a23f41797a0a762a410f45d5725ca65e76377face354d8e62a67d82b05cc0f5`,
retained at `/private/tmp/nkd-apt-capture.NQ7NNU/amd64-capture.tar`.
This is not native AMD64 KVM evidence.

Task C acceptance remains incomplete until a real NKE bake demonstrates the
installer/target capture handoff, and NKD admits and collects this extra payload
while binding it to the live Run, exact producer and exact QCOW2. The current
disk driver does not yet collect this declared acquisition carrier. Downloads
outside APT, base-ISO contents and later package events outside the capture
window also need explicit coverage. Missing historical repository metadata fails
closed; a guessed package URL is never substituted. Do not promote this capture
to a complete acquisition closure or disk SBOM.
