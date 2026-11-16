## Maintained input and schema boundaries

The NVKE four-family pilot adds independent core (21 outputs), services (34), telemetry (17) and disks (4)
Definitions under `inputs/definitions/nvke/`, with corresponding `github-nvke-<family>` Environments and
named production bundles. Their selected output union is the combined candidate's 69 outputs, except `nico-reference-profiles-fixture`, plus 8
family-only outputs (two core, six services) that the combined Definition omits for workflow headroom, retaining
aggregate native children, bound chart/image relationships and complete disk material selections. They
reuse shared catalog/pin owners and publish into separate family prefixes beneath the existing candidate
namespace. `make nvke-split-generate` and `make nvke-split-check` own the family subset; the repository-wide
workflow targets include it. These are authored-input and generation changes, not a new lifecycle or
cross-Run assembly contract. Actions history retrieved on November 14, 2026 contains one successful standalone
[core Run](https://github.com/dsx-ai-factory/nvkr/actions/runs/40641309939), with older membership, and no Runs
for services, telemetry or either disk family. These workflows are dispatch-only; the absence of Runs does
not establish a build failure. Current standalone family publication and independent consumer acceptance
remain unrecorded. The combined production and qualification paths remain until consumer and qualification
cutover; the GPU site composite below owns the fixed development bundle. Named producer qualification remains unsupported, and so does named development for these plain family Definitions; a composite may have a named development bundle (ADR-26). The pilot
emits no automatic association file or parent Release. Consumers independently verify exact family
references and own any required association or compatibility decision. See the
[pilot inputs](../../../inputs/definitions/nvke/README.md#family-pilot) and
[partitioning strategy](../../definition-partitioning-strategy.md) for scope and historical measurements.
Proposed [ADR-26](../0026-definition-composition.md) designs a composite of these families as one Release;
units U1–U8 are implemented and contract-tested, with historical publication evidence below. The authored
[GPU site composite](../../../inputs/definitions/nvke/README.md#gpu-site-composite) pins the five families
and the GB200 AICR Definition, which inherits their `sbomTool`, declares the eight components the Ubuntu 24.04 overlay replaces as variants of the two disk families, and has
a generated named production bundle and the fixed development bundle, both enrolled in `make workflows-check`
and catalog maintenance. The [historical 83-output hosted publication](adr-26.md#hosted-gpu-site-publication)
records one complete composite Run; current-candidate execution and independent consumer verification remain
unrecorded. The six-member finalizer fits
ADR-18's 16 result-collector buckets.
A fifth, `disks-ubuntu24` family selects four Ubuntu 24.04 disks that the combined candidate omits; see
[Ubuntu 24 node-image inputs](ubuntu-24-node-image-inputs.md#ubuntu-24-node-image-inputs).

The [GPU system matrix](../../../inputs/definitions/nvke/README.md#gpu-system-matrix) maps GB200, GB300, VR200
and the site controller to family outputs in documentation only. It adds no Definition axis, input or
generated file. NVKR records no GPU driver, fabric or runtime acceptance for any of them; that evidence
belongs to AICR and NVKE. No maintained Definition yet selects the matrix's offline CAPI repository. The core
family selects `controller-profiles`, `disks-ubuntu24` selects the Ubuntu 24.04 disks, and the GPU site composite
above selects the composite site Release recorded in the historical publication above. No maintained Definition selects the
ARM64 AICR CLI: publication accepts one bundle entry per component build subject, so its pinned attestation
companion cannot be published beside it yet. The GB200 AICR bundle
Definition has one local Run; its three outputs are also present in the hosted composite recorded above.

The maintained release node-image catalog selects the NVIDIA OVN fork `v2.4.2`
(`8af51273d52dfa07454435d18d37230e582d797a`) for `ovn-kube-ubuntu`, `ovn-kubernetes-chart` and
`nvke-cni-ovn`. It reuses the native NVIDIA demo producers and pins, so the release disks stage
`nvke-cni-ovn-{charts,images}-2.4.2`, which NVKE `v0.23.0` still requests for its default tenant `cniVersion`.
The native NVIDIA overlay now leaves the release snapshot unchanged. Dependent overlay predecessor
digests and generated bundles were regenerated, and the upstream 1.4.0 demo overlays still replace all
three producers. The release, development, disks, diagnostics and Ubuntu 24 Environments fetch the
public fork anonymously. Catalog validation, Plan generation and contract tests
cover this selection. No hosted or local disk build, package-index inspection or default-version tenant
OVN create has exercised it.

The 2026-10-20 integration of upstream `9857feeda03d2e7f529e1baef2b4a331a011e54c` reproduced
`CodePipelineDrift` in all four family bundles. Regeneration through `make nvke-split-generate` refreshed
eleven JSON files, including all four Plans and manifests; family Environments, entry workflows and action
templates remain unchanged. Independent inspection retains the exact disjoint union of 73 complete native
build records and 69 output records against the updated combined Plan. The extended exact-regeneration
contract passed in 4.05 seconds and the complete focused family/credential suite in 32.747 seconds on
2026-10-20, with Go 1.27.2 on Darwin ARM64 and recorded staged tree
`e9187e1e6ce5e5fda6d26e04b912d10622f559c0`. The
[current measurements](../../definition-partitioning-strategy.md#current-local-validation) record each family.
The full local gate passed at 21:54:35 UTC on 2026-10-20 on staged tree
`2ff2a400055ae9cfe429b5c0bcfeb55bfaaedb41`, with Go 1.27.2 on Darwin ARM64 and golangci-lint 2.14.0:
`make check GO_TEST_FLAGS='-count=1 -json -p=1'`, `make build` and both diff checks. All 58 default Go
package suites, static/shell checks and 19 Python tests passed; the vulnerability scan found none.
Delivery took 544.323 seconds, factory 462.554 seconds and lifecycle 363.894 seconds. Subsequent integration
through upstream `07181055` changed only documentation and formatting commands; Go sources, tests, authored
inputs and generated bundles remain identical to the tested tree. The final formatting commands passed
large-inventory, spaced-filename, read-only and failure fixtures plus `make fmt-check`; the full suite was
not repeated for those formatting/documentation changes. All-owner workflow regeneration, workflow lint, targeted metadata/retry lifetime race
checks and formatting failure/read-only fixtures also passed. New-head hosted CI remains pending.
Native Linux signing/pipeline race and pinned-cosign interoperability were not rerun locally; raw local
logs remain unpublished. These local contracts establish no hosted family acceptance.

The maintained closure inventory checks every committed producer bundle and the production qualification
pair offline; no development qualification pair is committed, and the plain-development qualification generator
is contract-tested separately on a compiled fixture. It runs without skipping when private sources are unavailable. It checks exact policy selections, member
bytes/modes and complete namespace inventory; authenticated recompilation remains owned by workflow checks.
The existing combined/development/nightly credential matrix now states each disk expectation explicitly,
including the development exemption. The separate actual disks-family assertion retains four producers.
Additional PinSet contracts compare registry, resolved-catalog and snapshot admission at declaration edges,
including the 1,024-member boundary and NVKE accounted-member bound; diagnostic text uses the shared limit.
These are coverage and generation changes, not new authority or evidence of registry access.

Upstream's [formatting job](https://github.com/dsx-ai-factory/nvkr/actions/runs/39494503833/job/118876005825)
failed before `gofmt` because the expanded shell command exceeded Linux's per-argument limit. Format,
check and tidy targets adopt upstream `07181055`'s recursive `gofmt` invocation on the module source
directories; formatter failures remain errors and the check remains read-only. This fixes repository CI
invocation, not artifact execution.

Combined qualification retains same-Run live authenticated receipts and the existing publication root,
with upstream's 59-minute operation, 60-minute step and 90-minute job budgets. Development retry reuse stays
within its original Run and publication namespace. Lazy OCI verification retains exact subjects and
independent signature/provenance checks within one verification operation. ADR-24 now implements the
provisional CLI catalog, strict discovery/loading and local-plan profile dispatch; broader migration,
published adapters and actual upstream/native-platform catalog acceptance remain deferred. Local native-tool
support does not admit hosted family execution or named family qualification.

Historical validation was recorded during the preceding `c01c9132` integration, later committed as
`250f842cb448347e2f6b1e9f2f373eb98e41e6e5`. Its extended split comparison passed in 4.25 seconds, the focused suite in 34.227 seconds,
and the full uncached `make check GO_TEST_FLAGS='-count=1 -json -p=1'`, build and diff checks passed at
23:32 UTC on 2026-10-19. All 58 packages, static/shell checks and 19 Python tests passed; the vulnerability
scan found none. Its exact-head [hosted Merge Gate](https://github.com/dsx-ai-factory/nvkr/actions/runs/39342927832)
passed at 23:43:57 UTC. The [historical measurement table](../../definition-partitioning-strategy.md#historical-validation-at-250f842c)
records dimensions and scope. Raw local logs remain unpublished and omit a complete tested working-tree
hash and direct toolchain dump; they cannot independently bind those measurements to the final commit.
Hosted CI is separate evidence, not a receipt for those local commands. The earlier all-owner workflow check's unchanged-byte comparison applied
only to the preceding integration, not the current catalog and generated JSON changes. The linked historical
section gives reproducible immutable old/new ref-and-path checks and their exclusions.

Every helper bootstrap retains the authored dispatch guard before checkout, helper compilation and private
CI-storage upload, including helpers without catalog credentials. Negative contracts reject missing or
broadened guards, unadmitted feature branches, tags and push events across build-only, publication and
named-runtime helpers. No hosted family execution of this boundary is recorded.
[ADR-18](../0018-pipeline-ir-and-ci-adapter-emission.md) owns the contract;
[bootstrap dispatch tests](../../../pkg/nvkr/factory/github_bootstrap_dispatch_test.go) cover refusal.

All five family Environments retain the maintained production `NVKR_REGISTRY_WRITE` publication policy.
The exact `m2-candidate/disks` and `m2-candidate/disks-ubuntu24` prefixes also admit the existing pinned M2 toolchains for separate preparation
with the fixed independent registry reader. Focused contracts reproduce and fix the previously omitted
preparation, require it before each family's four disk producers and source-vendor execution, and reject lookalike,
child, other-family and foreign-owner exceptions. Credential projection and cleanup contracts pass; they
do not establish provisioned registry access or hosted family execution.

The services family requires private chart pin-set revalidation even though its selected producers need no
Git fetch node. Source-access admission now also recognizes the actual pin-set sources in revalidated
catalog snapshots. It retains rejection of unrelated repository entries and the existing step-scoped
credential projection: catalog recompilation may receive these credentials; artifact acquisition, build
and publication do not. This closes a local admission gap for acquired-only selections, not a new source
trust or credential scope. No hosted private-source read or services publication is established by this
change; [ADR-20](../0020-artifact-publication-and-target-configuration.md#2-target-configuration-surface) owns
the contract.
Focused local contracts pass for root/output pin-set-only sources, mixed admitted/unrelated access,
ordinary-source and unknown-repository refusal, local-engine admission and snapshot substitution without
partial output. A local Git fixture composes real acquired-only loading with rendering: the locked commit
survives a moved upstream tag; malformed metadata, stale versions and stale provenance return no Plan or
bundle. Exact catalog credential helper/mirror configuration is confined to revalidation steps.
[Source-access contracts](../../../pkg/nvkr/factory/source_access_test.go),
[real loader composition](../../../pkg/nvkr/factory/source_access_loader_test.go).

Resolved snapshots reuse the catalog owner's intrinsic PinSet declaration checks. Malformed file paths and
unsupported formats first reproduced acceptance in low-level compilation and rehashed root/output Plan
validation, then were rejected with no Plan or credential-bearing bundle. Valid base catalogs and explicit
producer overlays remain supported. These semantic checks and snapshot identity do not authenticate
upstream contents; authored loading retains reciprocal membership, lock and exact pinned-tree checks.
[ADR-22](../0022-catalog-pinning-and-the-lock-contract.md#7-derived-pin-sets-one-source-may-curate-other-sources-pins)
owns the boundary; [compiler contracts](../../../pkg/nvkr/factory/pin_set_semantics_test.go) cover it.

The maintained family matrix asserts combined/core/services catalog credential confinement and
telemetry/disks absence, including raw-secret aliases and exact repository/helper/mirror bindings while
preserving legitimate source-fetch credentials. All nondisk families explicitly reject the private-toolchain
exception and emit no reader preparation. Default tests compare all authored family Definitions and
Environments to committed Plans; full compilation/rendering remains extended. During the preceding
`c01c9132` integration, the focused
family matrix passed in 34.227 seconds, including the 73/69 identity comparison and the actual
disks-family credential regression in 0.83 seconds. These are local
contracts, not evidence of private registry access, hosted family execution or consumer acceptance.

The [M2 Definition instructions](../../../inputs/definitions/nvke/README.md) own example-specific selection and input
preparation. The candidate requires 69 named outputs. It retains the previous 37 and adds source-built
CAPNICo and explicitly named reference-profile fixtures, the existing provisioned infrastructure provider, source-packaged NVKE telemetry pipelines,
and fourteen acquired runtime-image components delivered separately for both native architectures.
The selected NVKE/CAPI sources and NVKE telemetry pipelines dependency charts own their versions; Helm test-only images are
excluded. `nico-reference-profiles-fixture` identifies reference test data in the release membership itself,
not fleet configuration or a complete legacy OSProfile set; the core family also delivers that build as `controller-profiles`.
Provider manifests, image-reference relocation and installation remain consumer work, not delivery proved here.
Native children are locked separately from source-owned index parents, and materialization authenticates actual
membership. Controlled source/pin/image tests and exact-source approval do not prove external layer acquisition,
scanner acceptance or complete production input approval. The
[acquisition observations](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#temporary-spire-source-selection) retain the historical mixed-source candidate and HTTP-403 limits.
The maintained release and native demo inputs now select the same stock NVKE `v0.23.0`
source, `2cfd00081b468d85a4894ec17e9deac13c9d8169`, for OS assembly, agent, Calico,
kubeadm, SPIRE packaging and DOCA OVS acquisition, including the renamed Packer targets, disk filenames,
material schema and required Ubuntu-series input. NVKE telemetry pipelines is packaged from its own repository,
`dsx-ai-factory/nvke-telemetry-pipeline`, at `nktp/v0.1.0`, the release that `v0.23.0`'s site values target.
The native installer bundle still installs `v0.23.0`'s in-tree chart until NVKR selects an NVKE release without it
([#465](https://github.com/dsx-ai-factory/nvkr/issues/465)). SPIRE uses that release's revision-4
package and source-owned public acquisition/packaging commands; no pre-DSX source
rollback is selected. The default package's removal of DSX IMDS is an upstream release
contract, not a missing private dependency waived by NVKR. Historical DSX source,
distribution and compatibility questions are recorded in
[NVKE #1641](https://github.com/dsx-ai-factory/nvke/issues/1641); the older HTTP-403
observations are not a current v0.23.0 build blocker. No source gate, submodule rejection
or security requirement is weakened. Current-source execution remains unverified here
until its own complete recorded attempt succeeds. Standalone plugin compilation does
not establish native execution/boot acceptance. NVKE telemetry pipelines acquisition and
offline Helm packaging were exercised locally; no successful combined hosted Run of these release additions is claimed.

[Run `38809161348`](https://github.com/dsx-ai-factory/nvkr/actions/runs/38809161348) at
`e3743a70b6e0bd19ccf53b7160ab0a5f276d0e66` failed during NVKE telemetry pipelines Helm extraction and the AMD64
reference-profile fixture build. Helm archive ownership restoration conflicted with the vendor container's
dropped capabilities; the fixture's named Alpine context selected an ARM64-only manifest for both targets.
The catalog now extracts Helm without restoring archive ownership and selects the existing pinned Alpine
index containing both native children, retaining the previous ARM64 child bytes. These input corrections
preserve vendor isolation and the source-owned Dockerfile. The two missing-artifact verification failures
and final completeness rejection were downstream effects; aggregate publication was skipped. A fresh
complete hosted Run is still required to establish the corrected candidate's execution and publication.

The NICo catalog additionally declares source-built `nico-flow-image` and `nico-core-image` producers
from the infra-controller `v2.3.0` pin, on the generic rootless BuildKit route with digest-bound bases.
Core backs the umbrella chart's `global.image`, and Flow is the one REST image the packaged charts
reference. Since `v2.3.0` Flow is a standalone release outside the umbrella, so the catalog also packages a
`nico-flow` chart beside `nico` and `nico-prereqs`. No maintained Definition selects these five outputs:
development Run [41789718662](https://github.com/dsx-ai-factory/nvkr/actions/runs/41789718662) showed that
source admission refuses the `v2.3.0` tree's absolute systemd symlink under `pxe/`, so the core family
dropped them until that is resolved. Core chains the source's native build, runtime and release Dockerfiles in
one producer per architecture, because BuildKit admits no material inputs. The generated hosted budgets
differ by architecture: the ARM64 Core build has its own 60-minute job, while AMD64 build and preparation share
one 120-minute job. A cold Rust workspace build may not fit either, and hosted routes admit no longer
execution budget; a hosted Run must establish it. Both producers are designed and contract-tested by catalog validation. A recording-stub contract runs the
Core command for both architectures: it checks the native Dockerfile selection, the handoff of each base's OCI
layout and digest to the release stage, and that a failed base or a missing, duplicate or malformed
intermediate digest stops before the release stage. Both bases receive the Kea package version from the
source's `dev/docker/kea.version`, which their `v2.3.0` Dockerfiles require; a missing or empty file stops
before BuildKit. Their `FROM` lines carry upstream's digest pins. BuildKit execution is not exercised. NVKR source snapshots
omit `.git`, but the release Dockerfile's `cargo new` creates an empty Git repository. A reproduction of the
pinned `v2.2.2` version crate in that state stamped the `VERSION` and `CI_COMMIT_SHORT_SHA` arguments and
left the build date blank. `v2.3.0` adds upstream [#6896](https://github.com/dsx-ai-factory/infra-controller/pull/6896)'s
`CI_COMMIT_TIMESTAMP` argument for that date. The Core command derives all three from the source lock through
`versionStamp`: the pin's tag, its commit truncated to eight characters and its commit date in UTC, so a pin
update restamps them without editing the command, and a missing or empty stamp stops before BuildKit. Flow stamps the same pin-derived version; its Dockerfile
declares no revision argument, so source inspection indicates its commit reports `unknown` and its build time
is the build's wall clock. No completed Core image has confirmed this metadata. At the `v2.2.2` pin, the Flow
build command built both architectures locally in the pinned BuildKit image (AMD64 under emulation). A local ARM64 Core attempt at that pin with the
catalog command as it was before intermediate-digest validation built both source-owned bases, bound them by digest and compiled for about 220 seconds before
the compiler was killed by signal 9 in an 8 GiB Docker VM; no Core image was produced. Neither has been rebuilt
at `v2.3.0`, and no hosted Run has built, published or signed either image or the Flow chart.

The core family selects `controller-profiles` from the curated `nvke-nico-reference-profiles` producer in place
of `nico-reference-profiles-fixture`: one Release cannot publish the same image under two outputs, which the publication root rejects.
NVKE's controller deploy item names `profilesArtifact:
controller-profiles` (`deploy/nico/controller/release.yaml:20` at `v0.22.0`, `v0.23.0` and main `5293831c`), and a
`--release` render fails when the Release lacks it; the stack config's `images.profiles` overrides it but cannot
replace it. The image holds the two NICO test config profiles and their runbooks, not fleet configuration; NVKE's
full fixture tree, with its empty and failure-injection profiles, is not selected. The combined Definition does not
select it. It is contract-tested by the family membership matrix and the regenerated core bundle only; no hosted
Run has built or published it, and no NVKE render has consumed it. CAPI provider manifests and a clusterctl local
repository are not delivered.

Current Definition v5/catalog v3/IR v4/Plan and artifact-graph v5 reject retired recipe, cluster, category,
role, composition and staging fields, including empty legacy fields. Runs/Releases use v4 and validation vectors retain v3; publication roots and Inventory/ReleaseSet use v4; disk manifests use v2. Removed compiler-handoff,
execution-settings, AICR, release-authorization/reproduction-result/waiver and qualified-publication families
remain reserved and nonminting. Historical signed bytes are not converted or reinterpreted. Equal Build
Digests never waive another output's execution or evidence slots; cross-output reuse is not implemented.

The public modules enforce one owner per invariant. [ADR-17](../0017-cli-sdk-foundation-and-package-topology.md)
and [Development](../../../DEVELOPMENT.md) describe current package/lifetime ownership. Completed cleanup,
organization and migration reports do not remain a second status ledger. Tests in the owning packages retain
strict input admission, held-byte substitution, durable attempt commit, cancellation/join, terminal evidence
and exact output/Plan bindings; passing repository gates do not establish external acceptance. A repository
architecture test limits production `context.Background` and `context.TODO` roots to an exact inventory of
process roots and bounded cleanup, so a work path cannot substitute a fresh root for a missing caller context.
It does not inventory deliberate detachment through `context.WithoutCancel`.
