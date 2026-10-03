# APT capture byte-correspondence diagnostics

This record is an unqualified local diagnostic, not an NKE installer bake,
acquisition-origin attestation, live-Run receipt, installed inventory or disk SBOM.
The original source acquisition exercise used NKE
`f1eddbf62ff5866d593a8eb05d8eb2fcac80be1c`; PR #1211 rebased the unchanged capture
files onto `02b33339426031782ead68b8b9b05c64c5f4718b`.

## Captured bytes

| Target | Execution | Archive digest | Archive bytes | Manifest digest |
| --- | --- | --- | --- | --- |
| ARM64 | Native Ubuntu 26.04 container APT | `sha256:684ea432b9002ebb14fefcf81920e644dbd92d3afa7de8b24636f203d9e5504c` | 30,720 | `sha256:37eec045cbe8c04ce08e14f8692c6e2ba49d584759945c4d6c5501422a40ec88` |
| AMD64 | Emulated Ubuntu 26.04 container APT | `sha256:4a23f41797a0a762a410f45d5725ca65e76377face354d8e62a67d82b05cc0f5` | 30,720 | `sha256:d5162a0fbd87b1bdcb22a1eb8398f1910d7a696f79ea06e92295fc5c3446ab85` |

Each capture describes one actual `hello` package, version `2.10-5build1`.
The ARM64 package has 25,642 bytes and digest
`sha256:8ab9e5cf329e88413dd502edd5e0907550132470ecb926d500d0d23a30c735d8`.
The AMD64 package has 26,068 bytes and digest
`sha256:b6e9bef3c865aa17757cd9ffa820c05a37833f8a6b58287afc9f32dedb345965`.

## Independent verification

`TestObserveRealAcquisitionArchive` independently opens each retained capture
through `heldfile.OpenContext` and calls `ObserveAcquisitionArchive`. Rechecking
these bytes during the 2026-10-03 review returned the exact archive, manifest,
architecture and package-count values above for both captures, with both tests
passing. The ordinary synthetic suite does not run this opt-in diagnostic.

The original gate logs report `No vulnerabilities found.`, 77 NKE Packer contract
tests passing with two optional skips, and 11 separate native APT/Packer capture
tests passing without skips. Those historical checks are not fresh validation
of subsequent source fixes. They do not start QEMU or prove installer-to-target
hook transfer.

The logs and captures were inspected from local temporary storage during review.
They are not published artifacts and those local bytes are not durably available
to other reviewers. This document preserves the observed results and exact
digests, not a claim that a temporary pathname is shared or durable evidence.

For an independent recheck, supply a capture having one of the exact digests
above as `NKD_TEST_ACQUISITION_ARCHIVE` and run the diagnostic test. A different
capture establishes its own byte correspondence, not reproduction of these
observed bytes. A future retained CI acquisition artifact and original NKE bake
are still required for shared operational evidence.
