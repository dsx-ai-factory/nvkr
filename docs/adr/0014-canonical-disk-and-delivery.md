# ADR-14 — Canonical disk & delivery

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-14 in the consolidated NKD ADR set.
- **Relates to:** ADR-02 (identity/purity), ADR-05 (evidence / disk manifest), ADR-12 (categories, role
  composition), ADR-19 (delivery phases), ADR-04 (verify).
- **Satisfies:** FR-13 (canonical disk boundary), FR-14 (deterministic assembly).

## Context

NKD turns verified artifacts into deterministic delivery artifacts: packages, images, and bootable node disks.
The disk story has a load-bearing boundary — RAW is the only canonical disk artifact. Delivery is packages-first
and BYO-OS, and a Node SKU is a role-filtered composition of node-level category artifacts (ADR-12). This ADR
fixes the canonical disk model, the envelope-verification mechanism, the encryption boundary, and the
delivery/SKU model.

## Decision

### 1. RAW-canonical; derived envelopes verified by the decoded logical digest

The **RAW logical byte stream is the canonical disk artifact**. `qcow2`, `vmdk`, and `vhd` are **derived
envelopes**, verified by **decoding** them and hashing the **decoded logical stream** — never the container-file
bytes, which carry non-reproducible padding and UUIDs. A `DiskManifest` records `{ logicalDigest (over the
decoded RAW LBA stream), block manifest, optional per-file content manifest, envelope refs (container digest =
provenance only), pre-encryption boundary, role }`. Byte-identity attaches to the decoded logical stream; a
divergence names the first divergent block or path.

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

The role-SKU staging CLI is likewise unavailable until it consumes an authenticated ValidationVector receipt
whose recipe and exact component bindings are recomputed and equal to the current resolution. An unsigned
`expected-digests` pathname is diagnostic data, not authority.

Block manifests are resource-bounded contract objects: block size is `4 KiB..16 MiB`, and a manifest carries at
most `100,000` block identities. The implementation checks caller-controlled block size before opening the input
and derives the entry count from the held regular-file size before scanning or allocating the digest slice.
Larger disks require a coarser block size; an oversized serialized manifest fails validation.

### 3. Pre-encryption / FDE boundary

A **pre-encryption boundary marker** is part of the `DiskManifest`. A request to assert byte-identity on
encrypted or LUKS bytes **fails closed** with a distinct coded error: ciphertext is never hashed and mistaken for
a reproduction. **FDE is an off-by-default overlay** applied *after* the canonical pre-encryption RAW digest is
computed and attested, and the encrypted output gets its own **non-reproduction** identity.

### 4. Delivery: packages-first, role-subset Node SKUs, ISO, provisioning bundles

- **Packages-first / BYO-OS.** Node-level categories deliver as **`.deb` / `nix` packages plus control-plane OCI
  images**, installed post-provision and decoupled from the base OS.
- **Node SKU = role-filtered subset.** A Node SKU (`control-plane`, `worker`, `gpu-worker`) is the role-filtered
  subset of node-level category artifacts (ADR-12), assembled deterministically through a **staging manifest
  hashed into the artifact** — so a control-plane disk or package set is distinct from a worker one and
  **individually reproducible**.
- **ISO is a second canonical class**, because an ISO is a flat logical stream, not a decode-to-RAW envelope.
  Determinism comes from pinned GPL tools, a frozen clock, sorted directory order, and a fixed volume id.
- **Provisioning bundles** (PXE / iPXE / HTTP) are **produced and digest-referenced, not served**: the boot
  server is operator or engine infrastructure, base URLs are emit-time flags, and boot scripts reference
  artifacts by digest.
- **Cluster-level categories** are the engine's **helm-deploy** phase (ADR-19), not a disk artifact.

## Consequences

**Positive**
- A bootable node image is verifiable regardless of the envelope it ships in.
- Node SKUs are individually reproducible and role-scoped, matching the category composition.
- The encryption boundary is enforced in code, not by omission.
- Packages-first keeps delivery decoupled from the base OS.

**Negative / costs**
- The ext4 residual may land as a bounded masked region rather than full byte-identity, depending on the spike.
- `qemu-img` and the ISO/boot GPL tools must be pinned and are a subprocess dependency.
- Qualified `qemu-img` execution is currently Linux-only; a named temporary executable is not an acceptable
  portability fallback.
- Producer, boot, encryption, package, and role-SKU staging commands remain unavailable until their receipt and
  adapter contracts exist; repository scripts are development references, not release authority.

**Neutral**
- The `DiskManifest` serializes through the canonical codec (ADR-01) and folds into the evidence/ValidationVector
  (ADR-05).

## Alternatives considered

- **Hash the container (`qcow2`) file bytes.** Rejected — non-reproducible writer metadata; this is the reason
  RAW is canonical.
- **Treat ISO as a derived envelope decoded to RAW.** Rejected — an ISO is not a block image of the RAW, so there
  is no meaningful decode-to-RAW.
- **Bake the substrate into a kitchen-sink image.** Rejected — packages-first, thin-base, BYO-OS is the primary
  delivery.

## De-risking spikes

1. **`qemu-img` decode determinism** — the decoded logical stream is byte-stable across versions for `qcow2`,
   `vmdk`, and `vhd`.
2. **ext4 residual** — eliminate it with a deterministic populator, or record a bounded, declared **masked
   region**; this decides whether the manifest needs a masked-region field.

## Follow-ups

- **ADR-05:** the disk logical digest is an artifact digest and evidence input in the ValidationVector.
- **ADR-19:** node-level SKU delivery is the provision phase; cluster-level categories are the helm-deploy phase.
- **ADR-20:** produced artifacts publish to operator-configured targets.

## Acceptance evidence

Current contract evidence in `pkg/nkd/assemble/*_test.go` covers canonical RAW and ISO identities, per-block
manifests, strict role-scoped staging, pre-encryption rejection, qualified exact held `qemu` executable, input,
and output capabilities, format/encryption/backing-file probes, decoded logical identity, no-follow regular-file
capture, platform fail-closed behavior, bounded block-manifest allocations, and mutation or subject-substitution
negatives. CLI and local-runner admission tests prove unsupported producer, boot, encryption, qualified-package,
and unauthenticated staging lanes fail before catalog, graph, input, output, or subprocess I/O, while
existing-byte `DiskManifest` emission and ordinary unqualified development packaging remain available. Masked
regions deliberately remain unsupported.

End-to-end acceptance requires real pinned `qemu` versions decoding `qcow2`, `vmdk`, and `vhd` fixtures to the
same logical stream, a post-attestation encryption handoff, reproducible ISO and package fixtures, and a delivery
consumer that recomputes the staging and logical identities before use.
