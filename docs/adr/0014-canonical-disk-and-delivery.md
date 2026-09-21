# ADR-14 — Canonical disk & delivery

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-14 in the consolidated NKD ADR set.
- **Relates to:** ADR-02 (identity/purity), ADR-05 (evidence / disk manifest), ADR-12 (targets and rendering), ADR-19 (artifact membership), ADR-04 (verify).
- **Satisfies:** FR-13 (canonical disk boundary), FR-14 (deterministic assembly).

## Context

NKD turns verified inputs into digest-identified delivery artifacts: packages, images, and bootable node disks.
The disk story has a load-bearing boundary — RAW is the only canonical disk artifact. Delivery is packages-first
and BYO-OS as the target model, while the current NKE compatibility path ships a source-owned aggregate image.
Each named output selects its exact producers and delivery format. This ADR fixes the canonical disk model,
the envelope-verification mechanism, the encryption boundary, and artifact delivery.

## Decision

### 1. RAW-canonical; derived envelopes verified by the decoded logical digest

The **RAW logical byte stream is the canonical disk artifact**. `qcow2`, `vmdk`, and `vhd` are **derived
envelopes**. Qualified reproduction is verified by **decoding** them and hashing the **decoded logical stream**,
never by substituting container-file bytes, which carry non-reproducible padding and UUIDs. A `DiskManifest` (`nkd.dev/disk-manifest/v2`) records `{ logicalDigest (over the
decoded RAW LBA stream), block manifest, optional per-file content manifest, envelope refs (container digest =
exact delivery identity and provenance, not qualified reproduction identity), pre-encryption boundary }`.
Qualified reproduction identity attaches to the decoded logical stream; a divergence names the first divergent
block or path.

Exact delivery identity is separate from qualified reproduction identity. The SHA-256 of a QCOW2 file names the
exact delivered container bytes and the full-disk SBOM subject. An OCI manifest that carries those bytes has a
different digest. A future qualified claim names the decoded canonical RAW identity as well; none of these
digests may be substituted for another.

Disk evidence therefore has three non-interchangeable scopes:

- **Delivery identity:** hash, sign, attest, and consume the exact QCOW2 bytes produced by one Run.
- **Content validation:** authenticate the material closure, inspect the final disk read-only, and reconcile its
  packages and selected files with those materials. A second validated build with a different QCOW2 digest is a
  different delivery Artifact. Content validation alone does not establish qualified reproduction; that verdict
  requires the exact decoded canonical RAW comparison.
- **Qualified reproduction:** independently rebuild and exact-match the ADR-07/08 expectation for the decoded
  canonical RAW subject. Matching materials or filesystem inventory cannot replace this comparison.

Installer-generated timestamps, filesystem identifiers, journal state, machine identity, generated keys, and
network-resolved packages can change logical disk bytes. Pre-staging and pinning dependencies improves
hermeticity and auditability but does not by itself normalize those outputs. An installer-built Ubuntu disk is
therefore pipeline-attested until recorded evidence demonstrates the qualified RAW comparison; the architecture
must not convert content validation into a weaker `Reproduced` verdict.

At NKE commit `6df693a`, the [Make target](https://github.com/NVIDIA/nke/blob/6df693a9748a78aafdf96e1f2d03772530783487/os-image/packer/Makefile#L148-L185)
produces a QCOW2 and the [hosted workflow](https://github.com/NVIDIA/nke/blob/6df693a9748a78aafdf96e1f2d03772530783487/.github/workflows/os-image-artifact.yml#L399-L410)
later creates the published QCOW2 with another `qemu-img convert -c`. Source inspection alone does not establish
byte parity between the Make-path file and the published file, so NKD must not claim it. Parity requires a
source-owned finalization entrypoint or an explicit pinned transform followed by exact output verification. NKD
must not silently copy workflow shell into an adapter.

### 2. Envelope codec seam + determinism spikes

One **disk-codec seam** shells a **pinned `qemu-img`** — a GPL tool, so subprocess only, **fail-closed if
absent**, never silently skipped — to decode an envelope to its logical stream. The qualified Linux adapter
captures the exact pinned executable and envelope as immutable held capabilities, gives both probe and conversion
the same capabilities, writes RAW into an anonymous mutable capability, then seals and hashes that exact output.
It does not authenticate one pathname instance and decode another. A platform without descriptor execution —
currently including macOS — fails with an engine-capability error before codec I/O. The executable pin does not
bind the dynamic loader and library closure; that root-owned state remains part of the declared TCB until a
hermetic `qemu` closure is specified. Two spikes remain (below): **decode determinism**, because trust attaches
to the decoded stream rather than to conversion, and the **ext4 full-rootfs residual**.

The implemented command surface is deliberately narrower than the target delivery model. It can measure an
existing RAW or ISO into a typed `DiskManifest`, and Linux can verify an existing `qcow2` envelope through the
exact held codec path. Script-backed RAW/ISO/`qcow2` production, ambient privileged rootfs production, boot
observation, and the post-attestation encryption handoff fail with `engine-capability` **before input, output, or
subprocess I/O**, until qualified adapters return receipts over their exact inputs, realized toolchain/runtime
closure, and output bytes. The legacy `deb`/OCI packager remains available only to the ordinary unqualified
development build. `RunOptions{Qualification: ..., Package: true}` and every authority-bearing CLI route reject
at admission, before graph or path inspection: pathname-reopened, whole-buffer package output cannot feed
attestation or release until a descriptor-held streaming packager publishes through a crash-safe transaction.

Block manifests are resource-bounded contract objects: block size is `4 KiB..16 MiB`, and a manifest carries at
most `100,000` block identities. The implementation checks caller-controlled block size before opening the input
and derives the entry count from the held regular-file size before scanning or allocating the digest slice.
Larger disks require a coarser block size; an oversized serialized manifest fails validation.

### 3. Pre-encryption / FDE boundary

A **pre-encryption boundary marker** is part of the `DiskManifest`. A request to assert byte-identity on
encrypted or LUKS bytes **fails closed** with a distinct coded error: ciphertext is never hashed and mistaken for
a reproduction. **FDE is an explicit transformation** applied *after* the canonical pre-encryption RAW digest is
computed and attested, and the encrypted output gets its own **non-reproduction** identity.

Reproduced or digest-verified plaintext inputs remain independently visible evidence, but they do not make the
encrypted delivery bytes reproducible. Inspection of an authenticated unlocked view can establish composition;
the ciphertext still has only its exact per-artifact delivery identity.

The authoring schema can represent the current NKE `luks2` component without asserting that qualified boundary.
The [current FDE design](https://github.com/NVIDIA/nke/blob/6df693a9748a78aafdf96e1f2d03772530783487/os-image/docs/full-disk-encryption.md#L14-L34)
ships a shared unlock-capable passphrase in a LUKS slot and GRUB until first-boot TPM binding succeeds. ADR-20's
no-secret rule therefore prohibits its publication unchanged. Schema validity is not publication acceptance.

### 4. Delivery follows explicit artifact formats

A Definition selects the exact components of each executable, package, image or chart output. Actual producer
metadata and format requirements govern admission; there is no node-role/category taxonomy or role-scoped
staging manifest. Different disk contents are separate selected producers. The NKE aggregate disk is an explicit
output whose embedded inputs retain traced material identities and full-disk SBOM obligations.

ISO is a separate canonical class because it is a flat logical stream, not a decode-to-RAW envelope.
Deterministic production would require pinned tools, a frozen clock, sorted directory order and fixed volume
identity. Current existing-byte measurement does not implement that production capability.

NKD produces artifacts and verified references. Downstream consumers own boot serving, installation and Helm
deployment. No provisioning bundle or node-SKU interface is retained without a supported artifact producer.

## Consequences

**Positive**
- A bootable node image is verifiable regardless of the envelope it ships in.
- The encryption boundary is enforced in code, not by omission.
- Packages-first keeps delivery decoupled from the base OS.

**Negative / costs**
- The ext4 residual may land as a bounded masked region rather than full byte-identity, depending on the spike.
- `qemu-img` and the ISO/boot GPL tools must be pinned and are a subprocess dependency.
- Qualified `qemu-img` execution is currently Linux-only; a named temporary executable is not an acceptable
  portability fallback.
- Producer, boot, encryption, and qualified package commands remain unavailable until their receipt and
  adapter contracts exist; repository scripts are development references, not release authority.

**Neutral**
- The `DiskManifest` serializes through the canonical codec (ADR-01) and folds into the evidence/ValidationVector
  (ADR-05).

## Alternatives considered

- **Use the container (`qcow2`) file digest as qualified reproduction identity.** Rejected because writer
  metadata can vary; this is why RAW is canonical. Hashing QCOW2 bytes remains required for exact delivery
  identity and the SBOM subject.
- **Treat ISO as a derived envelope decoded to RAW.** Rejected — an ISO is not a block image of the RAW, so there
  is no meaningful decode-to-RAW.
- **Treat matching package/file inventories as disk reproduction.** Rejected — semantic agreement is valuable
  content validation but permits different bytes and cannot satisfy an exact-byte qualified claim.
- **Make aggregate baked images the primary delivery model.** Rejected — packages-first, thin-base, BYO-OS is
  the target delivery. The current NKE aggregate disk remains an explicit compatibility output while its
  embedded components are separated into independent layers.

## De-risking spikes

1. **`qemu-img` decode determinism** — the decoded logical stream is byte-stable across versions for `qcow2`,
   `vmdk`, and `vhd`.
2. **ext4 residual** — eliminate it with a deterministic populator, or record a bounded, declared **masked
   region**; this decides whether the manifest needs a masked-region field.

## Follow-ups

- **ADR-05:** the disk logical digest is an artifact digest and evidence input in the ValidationVector.
- **ADR-19:** exact artifact membership is separate from downstream deployment ownership.
- **ADR-20:** produced artifacts publish to operator-configured targets.

## Acceptance evidence

Current contract evidence in `pkg/nkd/assemble/*_test.go` covers canonical RAW and ISO identities, per-block
manifests, strict legacy-role rejection, pre-encryption rejection, qualified exact held `qemu` executable, input,
and output capabilities, format/encryption/backing-file probes, decoded logical identity, no-follow regular-file
capture, platform fail-closed behavior, bounded block-manifest allocations, and mutation or subject-substitution
negatives. CLI and local-runner admission tests prove unsupported producer, boot, encryption, qualified-package,
and removed staging interfaces fail before catalog, graph, input, output, or subprocess I/O, while
existing-byte `DiskManifest` emission and ordinary unqualified development packaging remain available. Masked
regions deliberately remain unsupported.

End-to-end acceptance requires real pinned `qemu` versions decoding `qcow2`, `vmdk`, and `vhd` fixtures to the
same logical stream, a post-attestation encryption handoff, reproducible ISO and package fixtures, and a delivery
consumer that recomputes the exact artifact and logical identities before use.
