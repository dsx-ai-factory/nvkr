# ADR-14 — Canonical disk & delivery

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-14 in the consolidated NVKR ADR set.
- **Relates to:** ADR-02 (identity/purity), ADR-05 (evidence / disk manifest), ADR-12 (targets and rendering), ADR-19 (artifact membership), ADR-04 (verify).
- **Satisfies:** FR-13 (canonical disk boundary), FR-14 (deterministic assembly).

## Context

NVKR turns verified inputs into digest-identified delivery artifacts: packages, images, and bootable node disks.
The disk story has a load-bearing boundary — RAW is the only canonical disk artifact. Delivery is packages-first
and BYO-OS as the target model, while the current NVKE compatibility path ships a source-owned aggregate image.
Each named output selects its exact producers and delivery format. This ADR fixes the canonical disk model,
the envelope-verification mechanism, the encryption boundary, and artifact delivery.

## Decision

### 1. RAW-canonical; derived envelopes verified by the decoded logical digest

The **RAW logical byte stream is the canonical disk artifact**. `qcow2`, `vmdk`, and `vhd` are **derived
envelopes**. Qualified reproduction is verified by **decoding** them and hashing the **decoded logical stream**,
never by substituting container-file bytes, which carry non-reproducible padding and UUIDs. A `DiskManifest` (`nvkr.dev/disk-manifest/v2`) records `{ logicalDigest (over the
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
  different delivery artifact. Content validation alone does not establish qualified reproduction; that verdict
  requires the exact decoded canonical RAW comparison.
- **Qualified reproduction:** independently rebuild and exact-match the ADR-07/08 expectation for the decoded
  canonical RAW subject. Matching materials or filesystem inventory cannot replace this comparison.

Installer-generated timestamps, filesystem identifiers, journal state, machine identity, generated keys, and
network-resolved packages can change logical disk bytes. Pre-staging and pinning dependencies improves
hermeticity and auditability but does not by itself normalize those outputs. An installer-built Ubuntu disk is
therefore pipeline-attested until recorded evidence demonstrates the qualified RAW comparison; the architecture
must not convert content validation into a weaker `Reproduced` verdict.

The public lifecycle may record that exact QCOW2 delivery identity before the qualified reproduction and
full-disk evidence adapters exist. The process-local observer accepts only a producer-owned held regular-file
capability, never a caller pathname or digest. It revalidates the complete Plan, resolves the unique disk root
and successful producer outcome, checks the producer's unqualified result against the compiled instruction,
then independently streams the held bytes to recompute exact SHA-256 and size and checks a bounded QCOW2 v2/v3
header: signature, version, absence of backing-file references, cluster-bit range, positive virtual size,
absence of QCOW-layer encryption, and the v3 header length. The `qcow2` format remains the compiler/producer
declaration; this framing check is not a full decoder or compatibility verdict. The result binds the logical
Build and Plan identities, producer, component, format, producer-declared `none` or `luks2` content encryption,
and successful outcome. That `Encryption` field does not assert QCOW container encryption; a nonzero QCOW
encryption method is rejected. The execution result owns and closes the held capability; observation and close
are serialized. A held payload's streaming operation must stop and return on context cancellation before its
lifetime permit can be released. This record is exact delivery-byte observation only. It does not decode RAW,
inspect encrypted content, establish compatibility or qualification, construct an SBOM, or authorize publication.

The observer and private scheduler population seam have controlled contract coverage, including held-file
no-follow regular-file capture, pathname replacement, byte substitution, cancellation, duplicate-result and
close races. The local unencrypted direct-Packer callback is connected and passes the VM driver's package-issued
held result through that seam after capability admission. A serialized local result or caller-composed path,
digest, or result cannot replace the in-process capability. This path has contract coverage only; no real
Docker/Packer/NVKE bake is claimed.

A successful local lifecycle attempt streams the held QCOW2 once into an attempt-owned temporary file while the
held Packer capability rechecks its collection digest and the observer checks size and QCOW2 framing. Lifecycle
syncs the complete file, applies its read-only mode, syncs that metadata, and closes the descriptor before
binding the Run. After binding succeeds and the held producer result closes, an attempt marker covers atomic
no-replace publication of `outputs/<output-name>.qcow2` and the lifecycle record. After successful rollback, a
returned commit failure records a failed Run. Removing the marker and syncing its attempt directory is the durable
success commit point; descriptor cleanup after that point cannot change success into failure. An interrupted
transaction remains explicitly marked and cannot be mistaken for success. This local retained file is the
observed delivery payload; it is not publication, qualified reproduction, or a caller-selected export path.

At NVKE commit `6df693a`, the [Make target](https://github.com/dsx-ai-factory/nvke/blob/6df693a9748a78aafdf96e1f2d03772530783487/os-image/packer/Makefile#L148-L185)
produces a QCOW2 and the [hosted workflow](https://github.com/dsx-ai-factory/nvke/blob/6df693a9748a78aafdf96e1f2d03772530783487/.github/workflows/os-image-artifact.yml#L399-L410)
later creates the published QCOW2 with another `qemu-img convert -c`. Source inspection alone does not establish
byte parity between the Make-path file and the published file, so NVKR must not claim it. Parity requires a
source-owned finalization entrypoint or an explicit pinned transform followed by exact output verification. NVKR
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

The shared probe passes the declared driver explicitly (`vhd` maps to QEMU's `vpc`), never auto-detects or
requests `--backing-chain`, and rejects encryption, backing files and QCOW2 external data-file metadata before
decoding. The shared probe pin must carry the [CVE-2024-4467 fix](https://ubuntu.com/security/CVE-2024-4467)
that prevents opening QCOW2 data files under `BDRV_O_NO_IO`; a digest pin alone does not establish that patch.
Explicit `vpc` selection permits fixed VHDs that auto-detection would classify as RAW; this is format
admission, not evidence of decode determinism. VMDK admission is limited to the single-file VMDK4 sparse
branch: before launching QEMU, a bounded binary first-sector check requires `KDMV` magic and nonzero
little-endian capacity. QEMU's descriptor-backed layouts open extent files even during `info` with `NO_IO`,
so text descriptors, legacy VMDK3 and zero-capacity binary descriptor redirects are rejected before the probe.
This is a subset admission check, not a VMDK descriptor parser. QEMU validates the remaining format, including
grain tables and any stream-optimized footer. Its QAPI result must report `monolithicSparse` or
`streamOptimized` with exactly one extent naming the held input file (`/proc/self/fd/N`) and covering the full
virtual size before conversion. Other VMDK layouts require separately admitted material closures.
The separate diagnostic installed-package gate remains QCOW2-only. Complete QCOW2 incompatible-feature
admission in the lifecycle's bounded header check remains unresolved; the header observer is not a
self-contained-image or full decoder verdict.

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

The authoring schema can represent the current NVKE `luks2` component without asserting that qualified boundary.
The [current FDE design](https://github.com/dsx-ai-factory/nvke/blob/6df693a9748a78aafdf96e1f2d03772530783487/os-image/docs/full-disk-encryption.md#L14-L34)
ships a shared unlock-capable passphrase in a LUKS slot and GRUB for first-boot TPM binding. ADR-10 admits
the documented `changeit` value only as an explicitly public diagnostic input; it has no confidentiality
requirement and is not a private credential smuggled through public variables. ADR-18/20 admit native ARC
publication of this output only with `build-provenance-v1`, binding the actual `luks2` declaration and exact
delivery bytes without inspected-content or reproduction claims. An actual private passphrase embedded in those files violates
ADR-20's no-secret rule. Cloned disks also retain the original volume key after TPM keyslot enrollment,
so the published candidate image supplies no deployed confidentiality claim. Schema validity and diagnostic byte
retention are not publication or firstboot acceptance.

### 4. Delivery follows explicit artifact formats

A Definition selects the exact components of each executable, package, image or chart output. Actual producer
metadata and format requirements govern admission; there is no node-role/category taxonomy or role-scoped
staging manifest. Different disk contents are separate selected producers. The NVKE aggregate disk is an explicit
output whose embedded inputs retain traced material identities and full-disk SBOM obligations.

Mixed executable bundles retain loose tools and declared tarball assets as separate named members.
Factory packages declared tarballs with mode `0644`; executable tools retain mode `0755`. The compiled
producer type selects this mode, never the source file's permissions or filename extension. Bundle
identity covers the mode as well as exact member bytes. Existing bundles retain their original identity;
rebuilding a tarball previously packaged as executable changes the bundle identity.

ISO is a separate canonical class because it is a flat logical stream, not a decode-to-RAW envelope.
Deterministic production would require pinned tools, a frozen clock, sorted directory order and fixed volume
identity. Current existing-byte measurement does not implement that production capability.

NVKR produces artifacts and verified references. Downstream consumers own boot serving, installation and Helm
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
  the target delivery. The current NVKE aggregate disk remains an explicit compatibility output while its
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

Current contract evidence in `pkg/nvkr/assemble/*_test.go` covers canonical RAW and ISO identities, per-block
manifests, strict legacy-role rejection, pre-encryption rejection, qualified exact held `qemu` executable, input,
and output capabilities, exact explicit probe argv including `vhd` to `vpc`, format/encryption/backing/data-file
probes, QCOW2-only diagnostic admission, VMDK single-file sparse pre-probe and extent-binding negatives
using a controlled probe, decoded logical identity, no-follow regular-file
capture, platform fail-closed behavior, bounded block-manifest allocations, and mutation or subject-substitution
negatives. CLI and local-runner admission tests prove unsupported producer, boot, encryption, qualified-package,
and removed staging interfaces fail before catalog, graph, input, output, or subprocess I/O, while
existing-byte `DiskManifest` emission and ordinary unqualified development packaging remain available. Masked
regions deliberately remain unsupported. Factory/lifecycle/delivery contracts additionally cover the
process-local exact-QCOW2 observation described above. They use a controlled producer callback and do not
establish production VM execution, NVKE disk production, full-disk evidence, compatibility, or publication.
The real-QEMU VMDK fixture replay is opt-in through `NVKR_TEST_QEMU_IMG` and its exact executable pin;
ordinary CI does not run it. Its [ARM64 replay evidence](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/disk-inspection-observations.md#vmdk-codec-admission)
keeps the earlier and linked follow-up replay identities separate from the contract suite
and does not establish qualified reproduction.

End-to-end acceptance requires real pinned `qemu` versions decoding `qcow2`, `vmdk`, and `vhd` fixtures to the
same logical stream, a post-attestation encryption handoff, reproducible ISO and package fixtures, and a delivery
consumer that recomputes the exact artifact and logical identities before use.
