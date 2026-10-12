# Source-owned APT capture

This contract describes the source-owned guest APT capture format and NVKR's bounded held-archive
verifier. The verifier establishes archive/member byte correspondence. It does not issue
acquisition-origin authority, installed inventory, a live-Run receipt or a full disk SBOM.
[ADR-10](adr/0010-pluggable-builder-types.md) owns the producer/carrier handoff;
[ADR-13](adr/0013-supply-chain-evidence.md) owns evidence authority.

The standalone [OS development inputs](../definitions/nvke-os/README.md) and the newer
[M2 candidate](../definitions/nvke-m2/README.md) have different source selections. M2 pins the
merged capture-capable NVKE source and explicitly enables capture. Its generic disk collector can retain
the declared carrier under the original Run/QCOW2 owner; that integration does not strengthen this
standalone verifier's claim.

## Field ownership

| Capture field | Authoritative operation | Claim limit |
| --- | --- | --- |
| Architecture | Explicit Packer input, checked against `dpkg-deb` package architecture | `all` packages are accepted for either target |
| Name, version, package architecture | `dpkg-deb --show` on retained package bytes | NVKR currently treats these as source-reported metadata |
| Digest, size | Actual retained `.deb` and exported tar member bytes | Not a reproduction or source-origin identity |
| Repository URIs | `python3-apt` versions with exact version, architecture, SHA-256 and size | Metadata locations, not observed redirect chains or authenticated repository signatures |
| Member path | Digest-derived `packages/<sha256>.deb` | A tar member name, never a filesystem grant |
| Capture archive identity | NVKR's held-file capability and exact archive hashing | Not a live-Run or final-QCOW2 binding |

The minimal `nkd.dev/package-acquisitions/v1` capture document is separate from
the build-material manifest. Build inputs have component bindings; guest APT
events have package archive sizes and repository metadata locations. Neither
description authenticates an acquisition simply by being well-formed.

## Source and byte checks

The NVKE candidate owns the optional APT `Pre-Install-Pkgs` hook. Its native v1
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

NVKR verifies the held capture without extraction. It rejects noncanonical JSON,
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

The [retained byte-correspondence observations](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#apt-capture-correspondence)
record exact tested source blobs, native ARM64 and emulated AMD64 capture/package digests, and the
committed recheck log. The original source hook was developed in NVKE #1211; the later
[merged-source handoff](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#merged-capture-source-handoff)
records its separate approval and source selection. A later source approval cannot retroactively expand
an earlier diagnostic's execution scope.

Ordinary tests include a Python `tarfile` fixture with exact archive and manifest-blob SHA-256 assertions.
Its synthetic payload is neither a Debian package nor installer execution. The opt-in
`TestObserveRealAcquisitionArchive` separately opens actual existing capture bytes through
`heldfile.OpenContext` and checks `ObserveAcquisitionArchive`:

```sh
NVKR_TEST_ACQUISITION_ARCHIVE=/path/to/capture.tar \
  go test ./pkg/nvkr/material -run TestObserveRealAcquisitionArchive -count=1 -v
```

The variable selects a diagnostic input, not a source/live-Run authorization. Compare logged identity,
size, architecture and package count with the retained record; a different archive establishes its own
byte correspondence. Those checks do not exercise APT cleanup, dpkg/python-apt matching, installer-to-target
capture handoff, autoinstall or a full image bake. The historical real archives were local rather than
retained Git/CI artifacts; their exact recheck log and source identity remain recorded.

The source carrier collector is implemented. Complete capture acceptance still needs original same-attempt
installer-to-target coverage and evidence joining the exact capture, producer, live Run and final QCOW2.
Base-ISO content, downloads outside APT and events outside the capture window require separate coverage.
Missing historical repository metadata fails closed; a guessed package URL is never substituted.
Do not promote this diagnostic to complete acquisition closure or a full disk SBOM. M2's explicit
build-provenance scope defers deep disk reconciliation to M3; current status belongs in
[reconciliation](adr/reconciliation.md).
