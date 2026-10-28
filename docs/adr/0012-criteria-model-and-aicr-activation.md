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

Ordinary native Definition outputs declare `platform: linux/amd64` or `linux/arm64`; the local executable
diagnostic also admits `darwin/arm64`. No wildcard, missing value or host-derived default is admitted.
The explicit standalone Debian exception is defined in section 2.
Catalog components declare sorted supported `platforms`, with complete exact
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

### 2. Debian producer targets and local execution

The separate `target.ResolveDebian` description resolver accepts an exact source-declared finite set of
distribution/platform routes. Distribution is an opaque source-owned distribution/ABI profile key, not an
APT publication suite, toolchain tag or Debian package version. Standalone omissions resolve to profile
`ubuntu-24.04` and `linux/amd64`; explicit values may select another declared route. This is a documented
compatibility baseline, not a statement of NVIDIA-wide policy. Missing default support fails instead of
choosing another route or using the execution host's architecture.

A package dependency instead inherits both fields from a complete consuming OS/image target. Explicit
architecture or distribution conflicts fail; an incomplete consumer cannot use the standalone defaults.
The resolver validates the entire canonically sorted unique route set, including unselected declarations,
and returns a value description. Source pins, target-specific toolchain pins, the consumer binding and actual
ABI compatibility still require their owning boundaries; the label does not prove them.

The catalog admits an explicit `debian-package` source producer with `debian.routes`: each complete route
contains its typed target, exact digest-pinned toolchain, source-owned argv, artifact path and private output
directory. Competing common command/path/toolchain fields are rejected. Every declared component platform
needs a route; the entire finite set is admitted before selection, including unselected pins and commands.
Distribution-specific source inputs are exact route argv, not a new template grammar or inferred environment
variable. Existing `{{.Arch}}` substitution still comes from the selected platform. Debian control Version is
not inferred from the normalized source tag.

Direct `buildgraph.Selection` may select exactly one such producer through explicit `debian-package` format.
Resolution freezes the concrete target and owns copies of route argv. Compilation revalidates the resolved
description and lowers that source-owned archive path without an executable/APT wrapper. The typed target,
selected command and toolchain enter the IR logical projection and `nvkr.build` identity. Equivalent omitted
and explicit defaults have the same identity; changing distribution or architecture changes it. Source and
toolchain transport mirrors retain the same identity when their exact content pins remain unchanged.
Catalog snapshots bind every route; whole-producer overlays own copies and canonical cloning rejects
malformed text rather than changing it.

Definition also admits exactly one source producer in `format: debian-package`, with an optional
`debian.distribution` profile key and optional standalone `platform`. Only this format admits those omissions;
ordinary output admission is unchanged. Factory compilation records the resolved typed target and platform
in its BuildPlan, independently from the authored omissions. Plan validation rederives the target, selected
routes and complete graph from the original Definition and catalog snapshot; caller-modified target fields
cannot replace that derivation. Its required output references the raw source archive, with no executable/APT
wrapper or ordinary metadata-only SBOM operation. Exact canonical Plan round-tripping preserves the binding.

Local execution admits one standalone inspected Debian output under ADR-10/13 on a matching native Linux
host. It joins the original producer, retained-content SBOM, terminal receipt and unsigned local predicate;
lifecycle retains the exact package and evidence through its existing durable transaction. An authenticated
compiled OS/image consumer target, ABI compatibility and install acceptance remain unimplemented.
Every direct factory node request, GitHub generation, whole-graph pipeline execution and standalone pipeline
node execution still rejects this output before work. Publication or signing rejects before catalog I/O.
Scanner-only compilation requires a complete native Environment private-volume inspection profile before
catalog I/O; explicit/default producer/utility conflicts fail there. The selected catalog scanner executable
pin and platform must match that profile. The offline SBOM node binds the raw archive, producer result and
acquired scanner; terminal observation depends on the inspected SBOM. Full Plan rederivation retains these
bindings. Ordinary path-based SBOM work cannot consume a serialized producer description as authority.
APT/OCI destinations remain transport settings outside the producer's Build identity.

NVSentinel selects separate image, Helm and Debian source producers with coordinated pins. Its current
OS example separately builds an ordinary host-package material through the same Debian recipe for the
existing composer/Packer route. It does not consume or authenticate the standalone Debian receipt, SBOM or
predicate. A future consumer join must preserve exact target, named output, archive and evidence bindings;
matching source recipes or an offline installed-byte comparison cannot grant that authority.

Dated producer, image, chart and OS observations, artifact identities and their limits belong to
[reconciliation](reconciliation.md) and the [example](../../definitions/nvsentinel/README.md), rather than
this contract. Native package installation, runtime service/API/GPU behavior, upgrades, source security
acceptance, ABI compatibility, qualification and publication require their own acceptance evidence.

ADR-10's collector, original session and exclusive named invocation retain the source-created bytes through
checked cleanup. The local scheduler uses that original completion for profile-bound inspection and final
observation under ADR-13; descriptions cannot recreate it. Controlled native tests establish ownership and
exact-byte/evidence joins, not real Debian tooling, NVSentinel installation or consumer compatibility.

### 3. AICR rendering — deferred

This ADR previously also owned an accelerated-runtime rendering path: a catalog-declared `aicrProfiles` map, an
output-owned `aicr` producer selecting a pinned upstream profile and generator, output-owned `aicr.bundle`
rendering settings bound into Build Digest, a qualified same-process render/publish adapter
(`pkg/nvkr/aicr`), and a `nvkr.aicr-bundle` typed digest family for the rendered tree. That implementation has been
removed: it had no concrete consumer, and the rendering/publication boundary (§4 of the prior revision) was never
closed end-to-end. `canonical.KindAICRBundle` remains a permanently reserved, retired digest domain (ADR-01 §3) so
no future family can silently reuse its identity.

Reintroducing accelerated-runtime rendering requires a new decision once a concrete use case exists; it is not
implicitly re-admitted by this ADR.

## Consequences

Target route admission has an explicit owner and no selector fallback.

## Alternatives considered

- **Retain category/role metadata.** Rejected because it does not contribute to current production or release
  correctness. Downstream deployment systems own their placement model.
- **Infer compatibility from deployment context.** Rejected because a declaration is not artifact inspection or
  runtime evidence. An external consumer must establish deployment compatibility independently.

## Acceptance evidence

`pkg/nvkr/target` and `pkg/nvkr/ir` cover concrete targets, exact pins, and fail-before-output admission.
Debian resolver/compiler contract tests cover default equivalence, target-sensitive and transport-pure identity,
complete route admission, strict catalog loading, snapshot/overlay ownership and early execution rejection.
Consumer inheritance/conflicts are resolver contracts only; no actual OS/image binding, package build,
toolchain runtime, ABI or installation acceptance is established.
Repository checks and external evidence limits are recorded in reconciliation.
