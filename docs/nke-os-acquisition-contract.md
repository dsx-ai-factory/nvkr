# Source-owned APT capture

NKD adds a diagnostic verifier for the guest package capture format emitted by
an unpinned NKE source candidate. The maintained NKE OS source does not produce
this capture. The verifier does not issue acquisition-origin authority, an installed
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

The NKE candidate owns the optional APT `Pre-Install-Pkgs` hook. Its native v1
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
digest/size substitution, repository locations outside the bounded-description
policy, and nonzero data
after the tar terminator. Limits are 16,384 packages, 1 GiB per package, 8 MiB
manifest and 8 GiB total archive, with 1–32 repository locations per package and
at most 4,096 bytes per location. These are sorted raw HTTP(S) descriptions with
a host and without credentials, queries or nonempty fragments, not normalized
URLs or authorized fetch targets. Equivalent spellings can remain distinct;
the verifier does not validate transport ports or path semantics. Name and
version are bounded display tokens; an admitted dpkg reader owns Debian
metadata validation against retained bytes. Empty captures are rejected, matching
the candidate recorder's nonempty-package requirement.
Source export additionally limits captured
package bytes to 7 GiB; capture storage is charged to the existing VM workspace.
Copying may fail earlier if workspace storage is exhausted, which fails the
build rather than producing an accepted capture.
This is not canonicalization of the whole tar container. Header metadata and
member-block padding are not package claims; all archive bytes, including those
regions, remain bound by the exact archive digest.

## Validation and remaining acceptance

The [dated diagnostic record](evidence/2026-10-03-apt-capture-correspondence.md)
records the actual tested verifier source blob, both exact retained capture
digests, and the committed recheck log. The source hook belongs to the separate
[NKE candidate PR](https://github.com/dsx-ai-factory/nke/pull/1211), not this
verifier or the maintained NKE Definition. The ordinary suite includes a small
Python `tarfile` fixture with exact archive and manifest-blob SHA-256 assertions;
its payload is synthetic, not a Debian package or installer execution.

Each real capture was separately admitted through `heldfile.OpenContext` and
`ObserveAcquisitionArchive` in the opt-in `TestObserveRealAcquisitionArchive`.
Its `NKD_TEST_ACQUISITION_ARCHIVE` variable selects an existing real capture for
this diagnostic test; it grants neither source nor live-Run authority.

The original diagnostic record attributes an ARM64 `hello` acquisition, version
`2.10-5build1`, to a native Ubuntu 26.04 container.
Its retained package is 25,642 bytes, SHA-256
`8ab9e5cf329e88413dd502edd5e0907550132470ecb926d500d0d23a30c735d8`.
NKD accepted the actual 30,720-byte capture, SHA-256
`684ea432b9002ebb14fefcf81920e644dbd92d3afa7de8b24636f203d9e5504c`.
NKD's retained recheck shows byte correspondence, and the Python fixture checks
cross-language format acceptance. Neither retained check exercises APT cache
cleanup, dpkg/python-apt matching, NKE autoinstall or a full image bake.
The capture is not published as a retained CI artifact.

The original record attributes the AMD64 capture to local container emulation;
NKD's retained recheck verifies only its archive-to-manifest byte correspondence.
Its actual `hello` package is 26,068 bytes, SHA-256
`b6e9bef3c865aa17757cd9ffa820c05a37833f8a6b58287afc9f32dedb345965`.
The capture is 30,720 bytes, SHA-256
`4a23f41797a0a762a410f45d5725ca65e76377face354d8e62a67d82b05cc0f5`,
not published as a retained CI artifact.
This is not native AMD64 KVM evidence.

Live guest package capture integration remains incomplete until a real NKE bake demonstrates the
installer/target capture handoff, and NKD admits and collects this extra payload
while binding it to the live Run, exact producer and exact QCOW2. The current
disk driver does not yet collect this declared acquisition carrier. Downloads
outside APT, base-ISO contents and later package events outside the capture
window also need explicit coverage. Missing historical repository metadata fails
closed; a guessed package URL is never substituted. Do not promote this capture
to a complete acquisition closure or disk SBOM.
