# Architecture implementation and evidence

Reviewed 2026-09-10. ADR-01–22 are the current engineering proposals, pending named-decider ratification.
Each ADR contains its current lane-specific decision; no amendment chain is needed. Code/tests establish
implementation. Contract tests, recorded external runs, and organizational acceptance are separate facts.

## Current boundary

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 01–03 Identity and errors | Canonical integer-only codec, frozen families, strict decoding, transport-free Recipe identity, whole-graph admission, stable coded errors | Independent-language corpus for every signed family; external failures need continuing contract coverage |
| 04 Signing and consumption | Linux held-executable exact-byte signing/offline verification; OCI keyless adapter; scoped Release consumer with mandatory signature and production attestation, exact closure, identity/issuer and close lifecycle | Hosted keyless signature/attestation production and verification exercised; independent cosign-only and public Release consumption exercised; root rotation remains unexercised. Full v1 release and qualified consumer scopes fail before adapters |
| 05 Trust/membership | Subject-bound vectors, publisher evidence, required Inventory versus realized ReleaseSet; lifecycle freezes required slots before observation | Signed cross-process publication of all authority records; governed coverage profile is deferred |
| 06 Status | Exact checkpoints/declarations, pinned time authority, anti-rollback high-water/CAS contracts | Production discovery/time adapters and rotation; v1 manifest tags, revocation and channel concurrency are unimplemented |
| 07–08 Reproduction | Linux stage-two receipts bind actual helper/runtime/image and terminal bytes; deterministic comparisons and profile matching | Other-platform authority, provider receipts, qualified helper-image admission, independent two-machine bake-off and causal profile graduation |
| 09 Provenance | Deterministic reproduction and distinct SLSA production predicates; exact plan/dependency/subject checks during hosted finalization | Real signed hosted provenance and independent consumption exercised; ongoing provider/service acceptance remains operational |
| 10 Builders | OCI source execution, digest-pinned acquisition and rootless BuildKit wrapped OCI exports; complete upstream CWE Dockerfile builds and offline payload checks passed locally and from the generated hosted Run | Qualified Nix closure observation, other wrapped adapters, fused large-output credential phases and `embeds` remain |
| 11–12 Resolution/AICR | Layered conflict checks, category selection, role/cluster separation, exact AICR activation/generator identity; Linux held generator/render-tree contract | No cross-process qualified render-to-vector publisher; loader/library closure and real downstream activation remain |
| 13 Evidence | Per-observed-artifact CycloneDX SBOM and per-recipe index in generated graph; typed evidence and exact subject checks; mirror-invariant generated evidence | Real signed receipt/scanner publication. Scanner execution and advisory generation are outside v1; unused prototypes are removed; acquired upstream signatures/provenance are not verified |
| 14 Delivery | Typed executable/APT/Helm layouts, including observed source Helm archives with exact producer checks and explicit image-package dependencies; resource-bounded RAW/ISO measurement; Linux held-qemu existing-envelope verification | Source quickstart archive, produced-image binding, checksums and all three local chart renders exercised from independently verified hosted publications. Disk/rootfs production, boot, encryption, unsigned-vector staging and qualified packaging fail admission; no masked-region support |
| 15 Air gap | Outside current scope; former package, CLI and tests removed | No mirror or zero-egress lifecycle implementation |
| 16 Release gates | Exact policy, reproduced-member, evidence/waiver, compatibility, conformance, trusted status and clearance contracts | No qualified production publication round trip; variant fallback is removed; incremental reuse remains diagnostic. V1 M3 qualification/M4 manifests are unimplemented |
| 17 Public lifecycle | Seven objects; compiler-owned output joins, immutable attempts, failed requirements retained; context-first SDK; descriptions cannot restore executable/verification authority | Superseded authoring and diagnostic APIs are removed; no backward-compatibility requirement or general resume protocol |
| 18 CI/execution | Shared helper launch; hosted GitHub Run generation with static-only PR checks, default-branch manual execution, bounded timeouts, non-canceling concurrency, main-restricted deployment environment, grouped offline processing and exact batched restores; build-only GitLab Go adapter; separate qualified emitter remains fail-closed where adapters are incomplete. Finalization checks job/restore success, terminal/SBOM/payload closure and production predicate | Authenticated GitHub fork fetch, combined CLI/CWE/Helm builds, SBOM, packaging, GHCR publication, real keyless signing/verification and complete pipeline-attested Run finalization exercised. Qualified source emission lacks runtime attestation; Tekton/Argo factory emission and non-GitHub signing fail before output. Provider outage can prevent any Run record |
| 19 Cluster views | One authored view, exact composition-to-content realization, reusable roles, GPU closure on amd64/arm64 with synthetic producers | No provisioning engine or external cluster-qualification protocol; legacy wire descriptions grant no authority |
| 20 Publication | Environment-owned routes/tools; derived roots; exact recursive OCI push/pull; binary/APT/Helm and native-image consumer binding; held OCI-export import and unchanged payload identity from Run to Release | Local registry and combined CLI/CWE/Helm GHCR publication exercised; independent private-package cosign and public consumer acceptance passed for all three published references. Direct package/object-store publishers and qualified commit adapters unavailable; segment registration is deferred |
| 21 Generation | Exact Definition/Environment inputs recorded; deterministic manifest-last writes and byte-exact check rejecting unexpected generated files; SDK/CLI share generation owner; current lifecycle regeneration tests | Scheduled bump/release automation, supported-provider transactions and complete same-checkout crash matrix remain |
| 22 Catalog pins | Strict registry/lock, per-module `tagPrefix`, complete source/artifact keys, bounded no-follow reads, cancel/CAS-safe pin writes, chained whole-producer overlays | `pinSet`, `pinnedBy`, extractor/re-derivation, `derivedFrom` and `curatedBy` are unimplemented |

The common positive consumer scope is `pipeline-attested` / `artifact-publication`. It establishes exact
published bytes and build evidence, not M3/M4 full-release acceptance. A stronger unavailable scope never falls
back. Static/dry-run evidence cannot satisfy either production or qualified policy.

Qualified interfaces require package-issued opaque receipts. Caller structs, paths, digest strings, parsed
records and injected success booleans remain descriptions. Authority crossing a process boundary requires the
owning signed protocol, never serialization of an in-process capability.

## Verification of the public model

The completed migration passed formatting, full native unit/race suites, vet, golangci-lint 2.13.1 built with
Go 1.27, ShellCheck, diff checks, deterministic generation, and Linux/arm64 sign/pipeline race tests with
container init and network disabled. Tests cover:

- Output derivation and Environment/Recipe identity separation, including AICR tool changes.
- Frozen required membership, missing/extra/substituted observations, immutable failed attempts and opaque SDK preparation.
- Actual local executable/APT/Helm layouts, complete synthetic GPU closure on both architectures, relocation and stale-package rejection.
- Reference/policy-scoped consumer positives, missing signature/attestation, wrong issuer/subject/lane and forged cluster-binding negatives.
- Hosted producer/restore failures, omitted terminal/SBOM evidence, exact production semantics and deterministic provider generation.
- Typed canonical digest-family separation. Superseded plan/manifest compatibility fixtures are removed.

These tests use controlled producers and private verification seams. They do not establish real GPU builds,
AICR execution, provider jobs, signing services, full-release qualification or qualified operational acceptance.
The current content/demo cleanup has its own checks recorded under repository validation below.

## Recorded external evidence

| Record | What actually ran | Limit |
|---|---|---|
| [Combined NKE hosted Run, 2026-09-10](../evidence/2026-09-10-combined-run.md) | All 20 jobs passed: CLI/CWE/Helm builds, observed artifacts, image-bound charts, GHCR publication, real signing, verification and complete Run; all three independent consumers and downloaded payload checks passed | Pipeline-attested artifact-publication scope; no qualified reproduction or cluster/service deployment |
| [Signed GitHub Run, 2026-09-10](../evidence/2026-09-10-github-signed-run.md) | All nine hosted jobs passed, real cosign signature/provenance verification, complete pipeline-attested Run and passing repository CI; 4m12s versus the 5m17s dry-run baseline | Independent cosign-only and public Release consumption passed after local GHCR login update; no qualified reproduction or full Release-scope acceptance |
| [Authenticated GitHub hosted Run, 2026-09-09](../evidence/2026-09-09-github-authenticated-run.md) | Private fork fetch, pinned CLI build, SBOM, packaging, GHCR publication, registry-digest verification and complete Run record; repository CI also passed | Static-unqualified with dry-run signing; no real signature/attestation verification or signed Release acceptance |
| [First GitHub hosted Run, 2026-09-09](../evidence/2026-09-09-github-run.md) | Actual hosted bootstrap, regeneration, helper transport and failed Run finalization | Internal NKE source fetch failed without cross-repository authentication; no hosted build, publication or signing success |
| [Maintained demo audit, 2026-09-09](../evidence/2026-09-09-demo-audit.md) | Fresh NKE build/publication/recovered CLI, focused CLI demos, provider generation; real AICR render, observation and Helm packaging; old worker-component attempts exposed stale demo scope | Local unqualified and generation evidence; the OS build owns embedded workers, its NKD wrapper is deferred, and at that time no signed publication existed for positive consumer acceptance |
| [Current local lifecycle, 2026-09-08](../evidence/2026-09-08-m1-public-lifecycle-local.md) | Current NKE Definition/Environment, immutable attempt, pinned source build, SBOM, OCI publication/pull-back and recovered CLI execution | LOCAL / UNQUALIFIED; dry-run signing; no authenticated Release consumer or hosted acceptance |
| [M1 local mechanics, 2026-09-04](../evidence/2026-09-04-m1-local.md) | Pinned NKE CLI build, loopback OCI publication/pull-back, dry-run signing | LOCAL / UNQUALIFIED; `signatureVerified: false`; earlier input model |

Dated evidence retains its original commands and identities. The current M1 demo has its own recorded run;
historical success is never transferred by renaming inputs or inferred for other demos and providers.

## Remaining decisions

Named deciders and organizational ratification remain outstanding. Deployment owners must define service
timeouts/retries, registry retention, release-tag concurrency/collision behavior, source-mirror access, protected
environments and transparency disclosure. Qualified owners must supply runtime/provider observations,
authenticated discovery/trusted-time services, measured loader/runtime closure where required, signed external
evidence handoffs, independent reproduction and offline recovery exercises.

Helper delivery is resolved conservatively in ADR-07/18/21: source bootstrap and pinned-image transport are
distinct choices; image identity alone cannot satisfy qualified executable/runtime admission. The qualified
image route remains unavailable until its receipt protocol exists.

## Repository validation

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
retains canonical source/Recipe identity; local and GitLab adapters reject this Environment option. See the
[successful hosted evidence](../evidence/2026-09-09-github-authenticated-run.md) for exact identities and limits.
