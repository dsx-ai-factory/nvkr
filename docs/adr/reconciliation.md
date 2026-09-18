# Architecture implementation and evidence

Reviewed 2026-09-18. ADR-01–22 are the current engineering proposals, pending named-decider ratification.
Each ADR contains its current lane-specific decision; no amendment chain is needed. Code/tests establish
implementation. Contract tests, recorded external runs, and organizational acceptance are separate facts.

## Current boundary

Public lifecycle disk-output support currently stops at authoring-schema validation. Public single-component
`disk-image` outputs and catalog disk/`vm-image` producer metadata are accepted, then build-graph and lifecycle compilation fail closed before graph or
source work. Disk lowering, VM execution, full-disk SBOM generation, OCI or object-store publication, and
artifact-level disk compatibility are unimplemented. Deployment topology and cluster runtime qualification
belong to downstream owners. No existing NKE OS build or publication is claimed.
Generic from-source `buildRequirements` are schema-validated, bound catalog → build IR → Plan, and excluded from
build identity. Other node kinds reject the field until they have an explicit compiler projection. Environment
execution classes now declare routes, host platforms, resource capacity, capabilities, and timeout ceilings.
Compilation deterministically matches each selected requested build, records the exact binding and route in the
full IR and Plan, and Plan validation recomputes the match to reject substitution. These declarations are not
runtime proof. A process-local Linux diagnostic helper now observes CPU affinity, free host memory, available
workspace capacity, host platform and KVM exposure relative to the executor's held workspace-directory
capability. Descriptor duplication is protected against concurrent close/reuse, write and traversal access is
checked, and every duplicate workspace or KVM handle is closed before return. The helper accepts no request or
binding and therefore does not authenticate Environment membership or deterministic selection, own an execution
deadline, reserve resources, prove a provider allocation, or authorize execution. Every current
adapter still rejects selected builds carrying requirements before execution or generated output. The generic
request covers resources, eligible host platforms, typed KVM policy, and executor timeout. VM-image authoring
requires all of those constraint classes. Required KVM is resolution-checked against a same-architecture eligible
host, while preferred and forbidden policies retain TCG routes. VM toolchain pins follow eligible host platforms.
The repository does not yet supply production NKE numeric values.
ADR-10 permits exact-command, including Make, and direct-Packer producer drivers behind the same generic
`vm-image` contract. A dormant runner-owned Packer driver now holds an exactly pinned Linux ELF executable, snapshots an
exact plugin-file closure, consumes an authenticated materialized source, stages only receipt-derived held
component bytes at their manifest-relative paths, runs Packer validation before one exact build selection, and
safely collects only the declared relative artifact. Its subprocess contract is tested with a fake executable
on Linux only. Its private state and environment are not a kernel sandbox; network denial, local-exec/filesystem
confinement, credentials, the dynamic-loader/library closure, and VM host admission remain outer-adapter gates.
No public lifecycle, IR, Plan, or adapter dispatches it, and no real Packer or NKE disk build has exercised it.
The command driver and all disk lowering remain unimplemented.

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 01–03 Identity and errors | Canonical integer-only codec, frozen families, strict decoding, transport-free build identity, explicit execution-metadata exclusion, whole-graph admission, stable coded errors | Independent-language corpus for every signed family; external failures need continuing contract coverage |
| 04 Signing and consumption | Linux held-executable exact-byte signing/offline verification; OCI keyless adapter; scoped Release consumer with mandatory signature and production attestation, exact closure, identity/issuer and close lifecycle | Hosted keyless signature/attestation production and verification exercised; independent cosign-only and public Release consumption exercised; root rotation remains unexercised. Full v1 release and qualified consumer scopes fail before adapters |
| 05 Trust/membership | Subject-bound vectors, publisher evidence, required Inventory versus realized ReleaseSet; lifecycle freezes required slots before observation | Signed cross-process publication of all authority records; governed coverage profile is deferred |
| 06 Status | Exact authenticated checkpoints/declarations and diagnostic anti-rollback high-water/CAS contracts | Unused release-specific trusted-time/clearance integration removed; production discovery/time adapters, rotation, v1 manifest tags, revocation and channel concurrency are unimplemented |
| 07–08 Reproduction | Linux stage-two receipts bind actual helper/runtime/image and terminal bytes; deterministic comparisons and profile matching | Other-platform authority, provider receipts, qualified helper-image admission, independent two-machine bake-off and causal profile graduation |
| 09 Provenance | Deterministic reproduction and distinct SLSA production predicates; exact plan/dependency/subject checks during hosted finalization | Real signed hosted provenance and independent consumption exercised; ongoing provider/service acceptance remains operational |
| 10 Builders | OCI source execution, digest-pinned acquisition and rootless BuildKit wrapped OCI exports; complete upstream CWE Dockerfile builds and offline payload checks passed locally and from the generated hosted Run; `vm-image` command/direct-Packer structural and transport-safe authoring validation; one-to-one component-material bindings, public literal Packer variables, deterministic build-material manifest model, and generic producer runner-requirement authoring; dormant held-ELF Packer validate/build driver with an exact held host-tool `PATH`, exact plugin-file snapshots, receipt-derived held-material staging, private state and declared-output collection, contract-tested on Linux with a fake ELF executable | Kernel sandbox and authenticated dynamic-loader/library closure; real pinned Packer/NKE project execution and evidence; compiler/Plan lowering of typed material edges and public variables into the dormant driver; public lifecycle/IR/Plan and adapter dispatch; command/Make driver, VM lifecycle collection and measured NKE host requirements; qualified Nix closure observation, other wrapped adapters, fused large-output credential phases and `embeds` remain |
| 11–12 Resolution/AICR | Direct output-selected producers and concrete platforms; no Recipe authoring, matching, inheritance, mixins or deployment classification; concrete target admission; exact AICR profile/generator selection and per-output effective bundle settings bound into Build identity; Linux held generator/render-tree contract | Embedded-version finite-set compatibility; no cross-process qualified render-to-vector publisher; loader/library closure and real downstream activation remain |
| 13 Evidence | Per-observed-artifact CycloneDX SBOM and per-build index; pinned offline Syft dependency discovery for binaries/images and Helm/AICR chart metadata discovery; exact payload/scanner inputs and shared packaging/finalization checks; one release index referencing required stage-bound CycloneDX attestation bundles beside their outputs and signed sibling output repository/digest bindings; internal indexes are not published; [all three real NKE artifacts inspected offline](../dependency-sboms.md); typed evidence and mirror-invariant qualified publisher evidence | Full-disk material tracing, inspection, reconciliation and CycloneDX evidence are unimplemented. NVIDIA Run 34540582090 independently verified signatures and provenance, but its SBOMs were outside the signed root. Run 34631274098 published and signed the new layout but failed verification when cosign selected an SBOM bundle instead of legacy SLSA provenance. Cosign 3.1.3 with consistently published Sigstore bundles has pinned-binary local interoperability coverage; a successful new hosted Run remains required. Qualified signed evidence-receipt publication, vulnerability scanner execution and advisory generation remain outside v1; acquired upstream signatures/provenance are not verified |
| 14 Delivery | Typed executable/APT/Helm layouts, including observed source Helm archives with exact producer checks and explicit image-package dependencies; resource-bounded RAW/ISO measurement; Linux held-qemu existing-envelope verification | Source quickstart archive, produced-image binding, checksums and all three local chart renders exercised from independently verified hosted publications. Disk/rootfs production, boot, encryption and qualified packaging fail admission; role-scoped staging and deployment-role disk fields are removed; no masked-region support |
| 15 Air gap | Outside current scope; former package, CLI and tests removed | No mirror or zero-egress lifecycle implementation |
| 16 Release gates | Strict Inventory/ReleaseSet/PromotionPolicy documents, required membership completeness and authenticated ReleaseSet/policy diagnostics | Unused qualified authorization, gate receipts, waivers and publication clearance implementation removed; qualified release acceptance and reuse are deferred. V1 M3 qualification/M4 manifests are unimplemented |
| 17 Public lifecycle | Six objects; compiler-owned output joins, immutable attempts, failed requirements retained; context-first SDK; descriptions cannot restore executable/verification authority; schema validation for single-component disk outputs without category/role labels | Disk output compilation and downstream work; superseded authoring and diagnostic APIs are removed; no backward-compatibility requirement or general resume protocol |
| 18 CI/execution | Shared helper launch; generic resource, host-platform, typed KVM-policy, and timeout requests propagated through full IR and Plan; Environment execution-class validation, deterministic hard-filter/preferred-capability matching, exact full-Plan binding and route, substitution/drift rejection, and fail-before-output adapter gates; point-in-time process-local Linux diagnostic observation of CPU, memory, held-workspace capacity and KVM exposure, with lifetime-protected descriptor duplication and no execution authority; dormant runner-owned Packer subprocess boundary with validate-before-build, bounded cancellation, private state/environment and fail-closed artifact collection; hosted GitHub Run generation with separate Merge Gate PR drift checks, manual-only triggers and default-branch execution, bounded timeouts, non-canceling concurrency, main-restricted deployment environment, named operations with failure-preserving progress logs, grouped offline processing and exact batched restores; GitHub is the sole hosted renderer; unused GitLab/qualified renderers and their hidden bundle workflow are removed. Finalization checks job/restore success, terminal/SBOM/payload closure and production predicate | No Packer kernel sandbox, authenticated loader/library closure, or authenticated local runtime-admission primitive; no adapter consumes the diagnostic observation; no provider allocation proof, qualified runtime attestation, VM Plan, typed command/Packer driver lowering or Plan dispatch, or disk execution. The dormant Packer boundary has Linux fake-executable contract coverage only. Authenticated GitHub fork fetch, combined CLI/CWE/Helm builds, SBOM, packaging, GHCR publication, real keyless signing/verification and complete pipeline-attested Run finalization exercised. Qualified provider emission has no retained implementation; GitLab/Tekton/Argo generation and non-GitHub signing fail before output. Provider outage can prevent any Run record |
| 19 Artifact membership and deployment boundary | Exact required/observed output membership, build/component evidence joins and canonical release content subject; no authored cluster views, generated compositions/realizations, node roles, categories or provisioning interface | Downstream systems own deployment topology, placement, provisioning and runtime qualification; historical topology-bearing records grant no current authority |
| 20 Publication | Environment publication prefix; one compiler-derived release signing route and distinct output repositories; catalog-owned tools; digest-only recursive OCI push/pull; binary/APT/Helm and native-image consumer binding; held OCI-export import and unchanged payload identity from Run to Release | No disk OCI control root, object-store/CDN schema, publisher, read-back proof or consumer. Local registry and combined CLI/CWE/Helm GHCR publication exercised; independent private-package cosign and public consumer acceptance passed for all three published references. Unused qualified transport/root constructors removed; direct package/object-store publishers and qualified commit adapters unavailable; segment registration is deferred |
| 21 Generation | Exact Definition/Environment inputs recorded; deterministic manifest-last writes and byte-exact check rejecting unexpected generated files; SDK/CLI share generation owner; current lifecycle regeneration tests | Scheduled bump/release automation, supported-provider transactions and complete same-checkout crash matrix remain |
| 22 Catalog pins | Strict catalog v3/lock v1, component target-derived artifact pin sets, per-module `tagPrefix`, bounded no-follow reads, cancel/CAS-safe pin writes, chained whole-producer catalog overlays v3; retired Recipe layouts rejected | `pinSet`, `pinnedBy`, extractor/re-derivation, `derivedFrom` and `curatedBy` are unimplemented |

The common positive consumer scope is `pipeline-attested` / `artifact-publication`. It establishes exact
published bytes and build evidence, not M3/M4 full-release acceptance. A stronger unavailable scope never falls
back. Static/dry-run evidence cannot satisfy either production or qualified policy.

Qualified interfaces require package-issued opaque receipts. Caller structs, paths, digest strings, parsed
records and injected success booleans remain descriptions. Authority crossing a process boundary requires the
owning signed protocol, never serialization of an in-process capability.

## Output-only implementation

The current implementation uses Definition v4: named outputs select components and concrete target platforms,
or an exact AICR profile and generator. Catalog v3 supplies pinned producer instructions without categories or
role affinity. The compiler derives each output's IR v3 build graph in the `nkd.build` family. Required output
and evidence joins bind the output name together with its build digest; equal build content does not waive a
second output's execution or observation requirements. Cross-output build reuse is not implemented.

Cluster views, tenant/operator types, deployment context and validation declarations, generated compositions
and realizations, deployment-role fields, the unused provisioning interface, and role-scoped staging manifests
are removed. Generic output completeness, independently observed payload checks, signer/receipt roles, exact
evidence membership, and noncircular release content identity remain. Downstream consumers own topology and
runtime deployment validation. Qualified compatibility/conformance requirements remain proposed contracts;
their unused receipt and authorization implementation is removed rather than retained as a second lifecycle.

AICR renderer settings now belong to each output's optional `aicr.bundle`. The AICR owner validates and
normalizes the effective deployer, storage class, node selectors/tolerations and workload selectors. An omitted
deployer becomes explicit `helm` in the generated graph. Those effective settings participate in Build identity,
and execution converts that output's bound settings into renderer arguments. Different outputs may select the
same profile with different settings; their settings and execution evidence remain independent.

The output-only migration introduced v3 plans, artifact graphs and lifecycle bindings. The locality migration
below advances plans/artifact graphs to v4 and removes lifecycle bindings. Public Runs/Releases, publication
roots and validation vectors retain v3; Inventory/ReleaseSet use v4; disk manifests use v2. The former release
authorization v9, reproduction-result v3 and evidence-waiver v3 implementations are retired. Changed media types follow their owning schemas. Old schemas and
removed fields are rejected rather than reinterpreted, including empty legacy fields. Retired recipe,
cluster/composition/realization and staging digest families remain reserved and cannot mint new identities.
Historical signed bytes and dated evidence remain unchanged.

Whole-producer catalog replacement remains separate from the removed recipe and deployment models. The
generator retires obsolete members only when a prior manifest owns their unchanged bytes and mode; unknown or
edited files fail before replacement. No lock pins, producer commands or NKE checkout changes are required by
this simplification. See the [output-only implementation plan](../output-only-simplification-plan.md).

Focused catalog/buildgraph/AICR/IR, lifecycle/factory, and delivery/release/assemble/canonical contracts and
race checks pass; their focused lint checks pass. Tests retain exact output/evidence substitution negatives
and add strict retired-field rejection, per-output AICR defaults, identity, argument mapping and ownership
checks. Full uncached native tests and `make check` passed, including formatting, vet, lint, ShellCheck and
race/coverage tests. Linux sign/pipeline race tests, both maintained generation checks, catalog/tamper demos
and diff checks passed. A stale AICR test fixture was corrected before the successful full reruns.

The [fresh output-only local Run](../evidence/2026-09-17-output-only-local-build.md) built CWE, Helm and CLI,
generated dependency SBOMs, bound Helm to the observed image, published to the loopback registry and ran the
recovered CLI. It completed with three required and observed outputs under Run v3. Signing was dry-run only;
these results do not establish new signed hosted or authenticated-consumer acceptance.

The preceding [Recipe-removal plan](../recipe-simplification-plan.md) recorded passing native checks,
generation and Linux sign/pipeline race tests for its earlier schema set. Its
[2026-09-17 local NKE run](../evidence/2026-09-17-direct-output-local-build.md) built all three outputs, generated
dependency SBOMs, bound Helm to the produced image, published to the loopback registry and ran the pulled CLI.
That complete `unqualified-local-execution` Run used Definition v3/catalog v2 and dry-run signing. It is
historical evidence for those exact inputs and does not establish acceptance of the current output-only schemas.

## Code locality migration

The [locality implementation record](../codebase-locality-implementation.md) records the completed compiler,
execution, delivery and SDK ownership refactor. Run Plan/artifact graph v4 remove independent compiler handoffs
and copied producer instructions; production predicates use v2 buildType URLs and original Definition/Environment
identities. Release-content canonical bytes are unchanged and now have one delivery owner. Required and observed
output membership remain independent; shared result validation checks each exact planned producer and its bytes.

Full uncached native unit tests and `make check` passed, including the full race/coverage suite. Affected Linux
race tests, NKE/AICR generation checks, Actionlint, reference checks and diff checks passed. The
[2026-09-18 local Run](../evidence/2026-09-18-code-locality-local-build.md) built CWE, Helm and CLI, generated
dependency SBOMs, published and pulled the diagnostic closure, and ran the recovered CLI. Final-source
re-observation matched its producer bytes, output records and content identity. Both held publication roots
correctly rejected attested admission. Signing was dry-run only; this refactor has no new hosted or qualified
execution evidence. Earlier operational records retain their exact schema, code and acceptance scope.

## Unused implementation removal

The [usage and age assessment](../unused-code-assessment.md) covers all production packages, pipeline renderers,
maintained inputs/scripts, SDK entry points and external dependencies. Unused pre-September-11 rendering and
library surfaces are removed; additions from September 11 onward are protected. The removal includes the
GitLab and qualified renderers, hidden bundle workflow, uncalled graph formatter, unreachable qualified
transport/root constructors, closed test-only qualified authorization and vulnerability-report subsystems,
and test-only forwarding/description APIs. GitHub generation, shared execution, live diagnostic authentication
and actual OCI publication remain. Retired bundle, gate-result, waiver, clearance, trusted-time, vulnerability
and qualified-publication allocations stay reserved and reject minting. Full uncached native unit tests and
`make check` passed, with 70.4% race-test statement coverage. Affected Linux/arm64 race checks, byte-exact
NKE/AICR generation, Actionlint, offline dependency/notice checks, local links and diff checks passed.

The Linux gate exposed an existing thread-affinity defect in sandbox setup. Policy application and exec now
share one dedicated locked OS thread; failures retire that thread. The scheduler-handoff regression failed
five times before the fix; both thread regressions and actual filesystem/network probes passed 100 repetitions
afterward. This repairs enforcement of the existing policy. No new artifact build, hosted execution or signing
acceptance is claimed; previous external evidence retains its own scope.

## Verification of the public model

The earlier public-lifecycle migration passed formatting, full native unit/race suites, vet, golangci-lint 2.13.1 built with
Go 1.27, ShellCheck, diff checks, deterministic generation, and Linux/arm64 sign/pipeline race tests with
container init and network disabled. Tests cover:

- Output derivation and Environment/build identity separation, including AICR tool changes.
- Frozen required membership, missing/extra/substituted observations, immutable failed attempts and opaque SDK preparation.
- Actual local executable/APT/Helm layouts, complete synthetic GPU closure on both architectures, relocation and stale-package rejection.
- Reference/policy-scoped consumer positives, missing signature/attestation, wrong issuer/subject/lane and forged cluster-binding negatives.
- Hosted producer/restore failures, omitted terminal/SBOM evidence, exact production semantics and deterministic provider generation.
- Typed canonical digest-family separation. Superseded plan/manifest compatibility fixtures are removed.

These tests use controlled producers and private verification seams. They do not establish real GPU builds,
AICR execution, provider jobs, signing services, full-release qualification or qualified operational acceptance.
The output-only migration has its own completion record above; earlier verification does not transfer
by renaming or reinterpreting records.

## Recorded external evidence

| Record | What actually ran | Limit |
|---|---|---|
| [Code locality, 2026-09-18](../evidence/2026-09-18-code-locality-local-build.md) | Complete Plan v4 and shared producer execution built all three NKE outputs; local publication/pull-back and recovered CLI passed; final-source producer/output and held-publication checks matched | Local/unqualified, dry-run signing; native/Linux/generation gates passed, no new hosted or qualified acceptance |
| [Output-only model, 2026-09-17](../evidence/2026-09-17-output-only-local-build.md) | Definition v4 and catalog v3 built CWE, Helm and CLI without deployment metadata; dependency SBOMs, loopback publication, exact pull-back and recovered CLI execution passed | Local/unqualified, dry-run signing; current native/Linux/generation gates passed, no new signed hosted acceptance |
| [Direct output producers, 2026-09-17](../evidence/2026-09-17-direct-output-local-build.md) | Definition v3 and catalog v2 directly selected CWE, Helm, and CLI producers; all three built and published locally with dependency SBOMs; pulled CLI executed successfully | Local/unqualified, dry-run signing; no new hosted or authenticated consumer acceptance |
| [Cosign discovery failure, 2026-09-11](../evidence/2026-09-11-cosign-discovery-fix.md) | Run 34631274098 created SBOM bundles, published and signed the single-entry layout, then failed provenance verification; cosign 2.6.3 reproduced the exact error locally; cosign 3.1.3 passed standard bundle verification | Local-key interoperability and repository contracts; updated hosted keyless verification/finalization and independent GHCR acceptance remain unexercised |
| [Separated repositories, 2026-09-10](../publication-architecture.md#local-validation) | Actual NVIDIA deliverables published once into distinct loopback repositories, metadata/evidence root published last, full cross-repository pull and exact artifact reference projection; absent copies in the release repository confirmed | Local and unsigned; GHCR permissions and hosted signing/finalization for the new locations remain unexercised |
| [Combined NKE hosted Run, 2026-09-10](../evidence/2026-09-10-combined-run.md) | All 20 jobs passed: CLI/CWE/Helm builds, observed artifacts, image-bound charts, GHCR publication, real signing, verification and complete Run; all three independent consumers and downloaded payload checks passed | Pipeline-attested artifact-publication scope; no qualified reproduction or cluster/service deployment |
| [Signed GitHub Run, 2026-09-10](../evidence/2026-09-10-github-signed-run.md) | All nine hosted jobs passed, real cosign signature/provenance verification, complete pipeline-attested Run and passing repository CI; 4m12s versus the 5m17s dry-run baseline | Independent cosign-only and public Release consumption passed after local GHCR login update; no qualified reproduction or full Release-scope acceptance |
| [Authenticated GitHub hosted Run, 2026-09-09](../evidence/2026-09-09-github-authenticated-run.md) | Private fork fetch, pinned CLI build, SBOM, packaging, GHCR publication, registry-digest verification and complete Run record; repository CI also passed | Static-unqualified with dry-run signing; no real signature/attestation verification or signed Release acceptance |
| [First GitHub hosted Run, 2026-09-09](../evidence/2026-09-09-github-run.md) | Actual hosted bootstrap, regeneration, helper transport and failed Run finalization | Internal NKE source fetch failed without cross-repository authentication; no hosted build, publication or signing success |
| [Maintained demo audit, 2026-09-09](../evidence/2026-09-09-demo-audit.md) | Fresh NKE build/publication/recovered CLI, focused CLI demos, provider generation; real AICR render, observation and Helm packaging; old worker-component attempts exposed stale demo scope | Local unqualified and generation evidence; the OS build owns embedded workers, its NKD wrapper is deferred, and at that time no signed publication existed for positive consumer acceptance |
| [Current local lifecycle, 2026-09-08](../evidence/2026-09-08-m1-public-lifecycle-local.md) | Current NKE Definition/Environment, immutable attempt, pinned source build, SBOM, OCI publication/pull-back and recovered CLI execution | LOCAL / UNQUALIFIED; dry-run signing; no authenticated Release consumer or hosted acceptance |
| [M1 local mechanics, 2026-09-04](../evidence/2026-09-04-m1-local.md) | Pinned NKE CLI build, loopback OCI publication/pull-back, dry-run signing | LOCAL / UNQUALIFIED; `signatureVerified: false`; earlier input model |

Dated evidence retains its original commands and identities. Earlier M1 demonstrations have their own recorded
runs; the completed locality execution is recorded above. Historical success is never transferred by renaming inputs
or inferred for other demos and providers.

## Remaining decisions

Named deciders and organizational ratification remain outstanding. Deployment owners must define service
timeouts/retries, registry retention, release-tag concurrency/collision behavior, source-mirror access, protected
environments and transparency disclosure. Qualified owners must supply runtime/provider observations,
authenticated discovery/trusted-time services, measured loader/runtime closure where required, signed external
evidence handoffs, independent reproduction and offline recovery exercises.

Helper delivery is resolved conservatively in ADR-07/18/21: source bootstrap and pinned-image transport are
distinct choices; image identity alone cannot satisfy qualified executable/runtime admission. The qualified
image route remains unavailable until its receipt protocol exists.

## Historical repository validation

The records below precede the output-only simplification and retain the schema, inputs and acceptance scope
exercised at the time. Current migration completion is tracked above; these passing checks do not establish its
final repository gate or fresh execution result.

The cosign 3.1.3 migration passed the full native gate, uncached unit tests, Linux sign/pipeline race tests,
Actionlint and byte-exact generation. The official pinned binary passed local-key signature/SLSA/SBOM bundle
interoperability, missing-evidence and wrong-subject negatives, and absence of legacy attachment tags.
Identity extraction, output bounds and fail-closed cleanup have always-run contracts. Default yamllint has
the same 187 formatting diagnostics as the baseline. The [migration evidence](../evidence/2026-09-11-cosign-discovery-fix.md)
records the new pin and limits; hosted keyless verification/finalization with this pin remains unexercised.

The preceding SBOM-attestation change passed the full native `make check` gate and uncached `go test ./... -count=1`,
with 70.0% race-test coverage, actionlint and byte-exact workflow regeneration. Linux sign/SBOM race tests passed
with networking disabled. An offline synthetic cosign 2.6.3 experiment used executable digest
`sha256:7c78a7f2efc00088bd788a758db6e0928e79f3e0eb83eb5d3c499ed98da4c4f4`, matching the catalog, to create a
standard bundle, recover its exact canonical document and verify an ephemeral local-key signature. That fixture
explicitly omitted transparency logging; production keeps transparency and identity checks enabled. Native
registry tests demonstrate attestation referrer discovery and absent copies in the release repository. Default
yamllint formatting findings remain separate from the passing release gate. No hosted attestation-only release
has been independently accepted yet. Run 34631274098 published and signed that layout but failed verification;
the [discovery fix](../evidence/2026-09-11-cosign-discovery-fix.md) records its cause and local validation.

On 2026-09-08 the consolidation/demo cleanup passed native `go test ./... -count=1`,
`go test -race ./... -count=1`, `go vet ./...`, formatting, golangci-lint 2.13.1 built with Go 1.27,
ShellCheck and `git diff --check`. Linux/arm64 sign/pipeline race tests also passed in Go 1.27 with container
init and network disabled. An initial restricted-sandbox `make check` could not bind three local test listeners.
The final complete `make check` passed with listener access and the compatible linter (63.3% total coverage).

The earlier NKE lifecycle generation check passed with manifest identity
`sha256:1b999d9b9dfe2c7a2688c4a82aafe3b20267cb6f9d13d74a554d0c9fee5192e3`. Both GPU architectures and both hosted
providers passed generation and byte-exact checks. NKE/GPU planning, the supported resolution matrix and its
unsupported-architecture negative, isolated pin-change/drift/malformed-lock demonstrations, and six tamper cases
passed. Release-consumer tests check path containment, hexadecimal tool pins and missing-policy refusal.
M1/consumer help and usage rejection also passed. Identity-rejection contracts remain in the verification
modules; the demo does not infer a specific rejection reason from cosign's generic subprocess-failure code.

Documentation checks preserve all 29 SADD template anchors and six diagrams, resolve local links in active
guides, and leave 22 primary ADRs without retired amendment references in active content.
Superseded plans, requirement exports and migration fixtures are removed; maintained Definitions and retained demo journeys use current lifecycle or focused catalog/identity
contracts. The [repository cleanup](../repository-cleanup.md) separates reference catalogs and test fixtures,
removes checked-in GitLab CI material, and records every domain package’s disposition.

The migrated M1 demo subsequently passed against the existing NKE clone and Docker loopback registry in 39
seconds; its exact identities and dry-run limitations are recorded above. The 2026-09-09 audit repeated that
journey and exercised real AICR rendering/Helm packaging. Attempts to run the old individual worker-component
demo exposed stale scope: the NKE OS build owns the whole embedded worker payload under the existing milestone
contract. Those component-build fixtures do not define the v1 delivery path. The NKD OS-image wrapper remains unexercised; positive signed artifact-publication consumption is recorded in the later evidence above. The later authenticated
GitHub Run recorded above establishes hosted CLI build/publication success with dry-run signing. Generation checks and
controlled contract producers do not establish those operational results.

The [GitHub configuration audit](../github-audit.md) replaces stale generated factory payloads with the current
NKE Run bundle, rejects extra generated files, and limits execution to default-branch manual dispatch. Its
local completion gate, uncached tests, Linux signing/pipeline/provenance race checks, workflow linters and
NKE/AICR regeneration checks passed. The subsequent first hosted dispatch passed bootstrap/regeneration and recorded an honest failed Run after internal source access failed; see the dated evidence above.

The subsequent authenticated fork dispatch at `c15e534` completed all generated jobs and published the CLI
to GHCR. Its repository CI passed. Source authentication is scoped to the matching fetch execution step and
retains canonical source/build identity; local and GitLab adapters reject this Environment option. See the
[successful hosted evidence](../evidence/2026-09-09-github-authenticated-run.md) for exact identities and limits.

Current authoring uses catalog source/lock entries for tool releases and Definition purpose selectors;
Environment publication is one untagged repository prefix with generated output routes. Tool asset pinning
was exercised against official Syft, cosign and AICR releases. Digest-only publication is covered by a local
registry round trip; the first NVIDIA hosted publication was independently verified, but the new single-entry SBOM-covered layout
requires a fresh hosted run. The current SBOM-attestation path has synthetic local-key format/verification
evidence with pinned cosign and Linux signing race coverage; these do not establish keyless/transparency acceptance.

GitHub action dependencies now use catalog source constraints and the same pin/bump lock writer. Run plans
carry resolved action commits; both GitHub renderers admit repository and major-version contracts before
output. The embedded action lock and separate action updater are removed. Migration retains the existing
four action releases, re-resolved from upstream; this is not a new hosted execution result.

The [demo language audit](../evidence/2026-09-10-demo-language-audit.md) exercised the current combined NKE
local build/publication/CLI-consumption path and AICR render/package/publication path successfully. Both Run
records are `unqualified-local-execution`; their plans are `static-unqualified`. Generation, catalog-change
and tamper demos also passed. M1 wording now distinguishes these local results, earlier real personal-namespace
signing, and the remaining current-namespace, trigger/helper-demonstration and governance acceptance gaps.
