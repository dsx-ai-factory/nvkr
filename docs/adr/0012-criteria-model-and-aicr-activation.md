# ADR-12 — Target platforms & AICR rendering inputs

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-12 in the consolidated NKD ADR set.
- **Relates to:** ADR-11 (direct producer selection), ADR-05 (ReleaseSet / Inventory), ADR-14 (delivery), ADR-10 (builder
  types), ADR-19 (deployment topology boundary).
- **Scope:** target and output-input admission in both evidence lanes; AICR qualified execution retains its
  separate Linux-only receipt and publication limits.

## Context

Build targets and accelerated-runtime generator inputs select the exact bytes an output must produce. A target
selects producer/toolchain/artifact routes. AICR inputs select a pinned upstream profile and its rendering
settings explicitly. Neither requires an NKD Recipe, deployment topology, or a generic criteria query.

## Decision

### 1. Concrete target platforms

Every Definition output declares `platform: linux/amd64` or `linux/arm64`; no wildcard, missing value or
host-derived default is admitted. Catalog components declare sorted supported `platforms`, with complete exact
pin/toolchain routes for those declarations. Selecting an unsupported route fails before external work.
`pkg/nkd/target` owns the platform and architecture domains.

Targets remain distinct from execution-host requirements. A VM producer's eligible host, KVM/TCG route and
capability policy are execution contracts, not target defaults. Source builds retain the native-target container
model; a foreign host requires its admitted execution mechanism. The typed `{{.Arch}}` producer token is resolved
from the explicit target. Other command values are never inferred from deployment context.

### 2. Output-owned rendering settings

AICR output authoring may supply `aicr.bundle` containing `deployer`, `storageClass`,
`acceleratedNodeSelectors`, `acceleratedNodeTolerations` and `workloadSelectors`. These are concrete generator
arguments, not a declaration of deployed nodes or proof that a cluster accepts the output. Ordinary component
outputs cannot carry AICR bundle settings. Cluster views, node roles, catalog categories, OS/Kubernetes context
and placement affinity are removed and fail strict decoding rather than becoming ignored fields.

`aicr.NormalizeBundleInputs` owns authoring admission, independent copies, deterministic list sorting and the
single default: omitted or empty `deployer` means `helm`. The admitted deployers are `helm`, `argocd`,
`argocd-helm`, `flux` and `helmfile`. Scalar strings must be canonical UTF-8 without control characters or
surrounding whitespace; selector/toleration lists must contain unique `key=value` entries. Normalization never
drops duplicates or repairs malformed strings.

The generated IR carries the complete effective `bundle`, including its explicit deployer. It is part of
`ir.AICRActivation` and its logical identity projection. Changing any rendering setting changes Build Digest;
omitted and explicit defaults have the same identity. Compilation and executable IR admission validate the
complete effective values again, rejecting missing defaults or noncanonical order. Rendering consumes those
same graph-bound values without a second cluster lookup or implicit settings source.

A single producer may emit a disk containing several stack layers. Disk format, encryption and producer
metadata govern its admission; a delivery category is unnecessary. Plain and FDE disks are separate components
selected by separate single-component outputs. Disk and VM lowering remain unavailable.

### 3. Explicit AICR profiles

The catalog's `aicrProfiles` map contains named exact profiles. An AICR Helm output supplies
`aicr: {profile, generator}` with optional `bundle` settings and its target platform, with no ordinary
components. The compiler validates the selected generator tool against the profile's executable pin. This is a producer choice for one output, not
an authored group or a renamed Recipe. Generic parameters, reserved axes, matching leaves and inheritance are
removed; ambiguous or unknown input fields fail strict decoding.

The profile and its generator tool both come from the output's selected catalog stack, including an explicit
output override. Run-wide SBOM and signing tools come from the Definition's base stack. A shared tool name must
select the same complete pin across all uses; conflicting pins fail selection before graph construction.

A profile pins both the canonical upstream AICR recipe digest and the exact generator executable digest.
The concrete generator inputs (`service`, `accelerator`, `os`, `intent`, optional `platform`) are validated by
`aicr.ValidateValues`. They, the effective bundle settings and both pins enter `ir.AICRActivation` and logical
Build Digest. The activation's upstream pin is named `profileDigest` to distinguish it from NKD `buildDigest`. The upstream AICR command,
`recipe.yaml` format and digest-verification protocol are unchanged. Profile labels and repository transport
remain outside build identity.

AICR graphs have no ordinary build/pull producers and retain a terminal graph node; the lifecycle render adapter
owns actual rendering and observation. Ordinary graphs require an artifact producer. A low-level provider
emitter without AICR rendering must reject the graph before emitting a verify-only workflow.

### 4. Qualified AICR execution and publication boundary

The CLI adapter resolves the executable once, captures those exact bytes as an anonymous sealed executable
capability, verifies the capability against the output-affecting generator pin, and reuses that same capability for recipe
generation, both evidence-digest checks, and bundle rendering. The generated recipe is likewise held anonymously,
and every later generator phase receives that exact descriptor, never a mutable pathname. This authority-bearing
subprocess protocol is supported on Linux only and fails closed on platforms without descriptor execution.

A qualified render writes first into an untrusted private generator stage. After every generator phase finishes,
NKD descriptor-walks that stage, rejects symlinks and non-regular entries, captures every file as an immutable
held capability, and requires the captured `recipe.yaml` to equal the already-verified recipe capability. It then
materializes a distinct commit tree **only from those held capabilities**, recomputes its tree identity, publishes
it with an atomic no-replace primitive, and rechecks the visible destination. Generator-stage path replacement
therefore cannot choose the published bytes.

The resulting tree digest is a derived release input, and it must be bound into the signed AICR reference
before that reference can claim a rendered bundle. The signed ValidationVector schema can carry both
`generatorDigest` and `bundleDigest`. The current CLI deliberately rejects `attest --aicr-bundle`: no supported
cross-process publisher adapter can join the same-process render receipt to signed vector bytes. The local output
directory is diagnostic state and is omitted from the signed reference, so standalone rendering is locally
verified diagnostic output, not a release-authorizing bundle. A later content-addressed publication reference may
be added only when it is stable across hosts.

The authority-bearing render result is an opaque, same-process `QualifiedResult`, minted only by the built-in
verified-snapshot adapter. The public `Result` is diagnostic: even a caller-composed result whose paths and
digests are internally consistent cannot authorize an AICR reference. There is no SDK bundler extension
interface. A future adapter needs its own authenticated execution-evidence design before it can mint the opaque
capability; structural fields are not proof that the pinned generator ran.

The frozen v2 tree object has `schemaVersion: nkd.dev/aicr-bundle/v2` and an `entries` array sorted by relative
slash path. Each entry is exactly `{path, type, mode}` for a directory or `{path, type, sha256, mode}` for a file;
`sha256` is the lowercase digest of exact file bytes, and `mode` is `0644` or `0755` according only to whether any
executable bit is present. Empty directories are identity-bearing because a consumer can branch on their presence;
other non-executable read/write-bit differences do not affect identity. The object is encoded with ADR-01's
canonical codec and hashed under `nkd.aicr-bundle`. A qualified result requires exact held `recipe.yaml` and
regular `checksums.txt` members in addition to rejecting every non-directory/non-regular entry.

## Consequences

Target route admission and AICR rendering inputs have explicit owners and no selector fallback. AICR preserves
its upstream identity and held-execution protocol without an NKD Recipe or cluster-composition model. Changing an
AICR activation value, rendering setting or either pin changes Build Digest; renaming its catalog profile does not.

Deployment systems own topology and placement outside NKD. Pinning only the generator executable does not
identify its dynamic loader, interpreter or library closure. A future demonstrated dependency requires an explicit hermetic image/closure contract; the executable pin must not be described as stronger than
it is. Qualified execution remains Linux-only until another platform supplies equivalent capabilities or an
authenticated producer receipt.

## Alternatives considered

- **Retain category/role metadata.** Rejected because it does not contribute to current production or release
  correctness. Downstream deployment systems own their placement model.
- **Treat AICR's upstream recipe as an NKD Recipe.** Rejected because the external generator protocol is an
  independently pinned input, not an NKD authoring lifecycle object.
- **Infer compatibility from deployment context.** Rejected because a declaration is not artifact inspection or
  runtime evidence. An external consumer must establish deployment compatibility independently.

## Acceptance evidence

`pkg/nkd/target`, `pkg/nkd/aicr/bundle_inputs_test.go`, `pkg/nkd/buildgraph/aicr_bundle_test.go` and `pkg/nkd/ir`
cover concrete targets, exact pins, rendering-sensitive identity, canonical defaults, owned copies, strict
legacy rejection and fail-before-output admission. Repository checks and external evidence limits are recorded
in reconciliation.

Existing AICR module contracts preserve rejection before execution for malformed/changed executable pins,
held executable and generated-file identity across pathname replacement, cancellation around the no-replace
commit point, descriptor-rooted tree capture, durable publication, symlink/special-file rejection, and stable
mode/content/directory identity. Public `Result` values remain diagnostic and cannot acquire the private
execution receipt. These tests do not prove an external generator run or qualified publication round trip.

End-to-end qualified acceptance still requires a publisher adapter joining the opaque render receipt to signed
vector bytes and a delivery consumer that recomputes the exact tree before use. Structural schema checks alone
cannot establish either boundary. Pipeline-attested AICR output follows its separate lifecycle observation lane.
