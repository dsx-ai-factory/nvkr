# ADR-12 — Target platforms

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-12 in the consolidated NVKR ADR set.
- **Relates to:** ADR-11 (direct producer selection), ADR-05 (ReleaseSet / Inventory), ADR-14 (delivery), ADR-10 (builder
  types), ADR-19 (deployment topology boundary).
- **Scope:** target admission in both evidence lanes.

## Context

Build targets select the exact bytes an output must produce: a target selects producer/toolchain/artifact
routes. Neither requires an NVKR Recipe, deployment topology, or a generic criteria query.

## Decision

### 1. Concrete target platforms

Every native Definition output declares `platform: linux/amd64` or `linux/arm64`; the local executable
diagnostic also admits `darwin/arm64`. No wildcard, missing value or host-derived default is admitted. Catalog components declare sorted supported `platforms`, with complete exact
pin/toolchain routes for those declarations. Selecting an unsupported route fails before external work.
`pkg/nvkr/target` owns the platform and architecture domains.

An OCI output may instead declare `platforms`, a nonempty sorted unique subset of the supported platform
domain. It is mutually exclusive with `platform` and selects exactly one image component. The single public
output is a standard OCI image index whose concrete native children exhaust that frozen set; no
`platform: multi`, architecture inference, or extra attestation descriptor is admitted. A chart referring to
this output must declare a platform contained in the required set. Executable outputs remain separate named
native artifacts; the image-index contract does not create an executable aggregate.

The Darwin exception admits HTTPS executable acquisition and CGO-free `oci-hermetic` host-package or
tarball producers. Its payload target is `darwin/arm64`; its explicit `toolchainPlatform` is `linux/arm64`.
The compiler sets `GOOS=darwin`, `GOARCH=arm64` and `CGO_ENABLED=0`. Images, disks, CGO producers, hosted
execution, signing and qualified authority do not inherit this exception. Linux target admission is unchanged.

ADR-10 separately admits the local `native-go` diagnostic builder for host-matching executable packages,
including CGO. Its `toolchainPlatform` equals its payload platform, and the toolchain identity binds the
installed Go SDK tree and C compiler bytes. It requires an explicit local Environment binding and declared
host network; it provides no hosted, signing, publication, qualified or container-isolation claim.

Targets remain distinct from execution-host requirements. A VM producer's eligible host, KVM/TCG route and
capability policy are execution contracts, not target defaults. Linux source builds retain the native-target container
model; Darwin executable cross-builds use the explicit exception above. A foreign host otherwise requires
its admitted execution mechanism. The typed `{{.Arch}}` producer token is resolved
from the explicit target. Other command values are never inferred from deployment context.

### 2. Retired AICR profile model

This ADR previously also owned an accelerated-runtime rendering path: a catalog-declared `aicrProfiles` map, an
output-owned `aicr` producer selecting a pinned upstream profile and generator, output-owned `aicr.bundle`
rendering settings bound into Build Digest, a qualified same-process render/publish adapter
(`pkg/nvkr/aicr`), and a `nvkr.aicr-bundle` typed digest family for the rendered tree. That implementation has been
removed: it had no concrete consumer, and the rendering/publication boundary (§4 of the prior revision) was never
closed end-to-end. `canonical.KindAICRBundle` remains a permanently reserved, retired digest domain (ADR-01 §3) so
no future family can silently reuse its identity.

[ADR-25](0025-generic-oci-bundles-and-aicr-producers.md) admits current AICR rendering through ordinary
pinned source producers and the generic `bundle-oci` artifact format. It does not restore these retired
fields, the old qualified adapter or its digest domain. Upstream hardware recipe support and deployment
acceptance remain explicit limitations of the selected inputs.

## Consequences

Target route admission has an explicit owner and no selector fallback.

## Alternatives considered

- **Retain category/role metadata.** Rejected because it does not contribute to current production or release
  correctness. Downstream deployment systems own their placement model.
- **Infer compatibility from deployment context.** Rejected because a declaration is not artifact inspection or
  runtime evidence. An external consumer must establish deployment compatibility independently.

## Acceptance evidence

`pkg/nvkr/target` and `pkg/nvkr/ir` cover concrete targets, exact pins, and fail-before-output admission.
Repository checks and external evidence limits are recorded in reconciliation.
