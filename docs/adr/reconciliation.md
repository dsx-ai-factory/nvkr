# Architecture implementation and evidence

Reviewed 2026-10-08. ADR-01–22 own the lane-specific engineering contracts pending named-decider
ratification. This ledger owns current implementation and remaining acceptance. Code and contract tests,
recorded external execution, and organizational approval are separate facts. Historical source and
schema results do not transfer to a changed candidate.

## Current boundary

The maintained public workflow is **Definition → Run → Release**, with Environment, Artifact and
Verification Policy as the other public objects. Definition v4 selects exact producers for named outputs;
there is no authored Recipe, cluster topology, placement, inheritance or composition lifecycle. Plans and
internal evidence records are generated or authenticated rather than separately authored public inputs.

Planning, byte-exact GitHub generation, Plan/graph/diff views and metadata-only release navigation are
implemented. Local immutable attempts and a Linux `pipeline-attested` / `artifact-publication` consumer
are implemented. Browsing does not verify payload bytes, signatures or consumer policy. A supported
artifact-publication result does not satisfy full v1 release or qualified acceptance; unsupported stronger
scopes fail before work.

Ordinary local non-disk terminal observation reopens and hashes producer bytes in the host process, using the
same filesystem view as collection after private BuildKit output promotion. It executes no producer code or
signature verifier and remains static-unqualified. This is not a container-isolation or runtime-authentication
claim; producer execution, other offline utilities and the held-disk observation path retain their existing
contracts (ADR-18 §8).

Local unencrypted direct-Packer execution retains an exact held QCOW2, observes its bytes and commits the
disk with its immutable Run record. Ordinary local disk publication remains closed. The explicit native
ARC hosted route is separately admitted under ADR-18. The
[recorded 34-output M2 candidate](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md) completed generated native
disk builds, publication, signing, verification and Run finalization under a validation-branch exception.
Its `build-provenance-v1` evidence binds live producer/material subjects and explicitly defers inspection.
This is scoped candidate pipeline-attested delivery, not a claim of main-only workflow execution,
independent full-payload disk consumption, strict full-disk inspection, organizational toolchain approval
or completed M1–M4 acceptance.

Strict disk inspection and reconciliation remain the M3 requirement. Controlled receipt/export/transport
contracts and retained-disk diagnostics exist, but a successful real native NKE live-Run full-content join
has not been demonstrated. Qualified release authorization, full v1 qualification/tag/revocation automation,
qualified provider adapters, encrypted VM execution and air-gap operation remain unavailable.

## Support by owner

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 01–03 Identity and errors | Canonical integer-only codec, frozen typed families, strict decoding, transport-free Build identity, whole-graph admission and stable coded errors | Independent-language corpus for every signed family; continuing external-failure contracts |
| 04 Signing and consumption | Linux held-executable exact-byte signing/verification, pinned cosign keyless OCI adapter, policy-bound artifact-publication consumer with mandatory signature/provenance and exact closure | Earlier independent cosign and public/private consumer acceptance is recorded; M2 independent full-payload disk consumption and root rotation remain unproved. Full release and qualified scopes remain closed |
| 05 Trust and membership | Subject-bound vectors, publisher evidence, required Inventory versus realized ReleaseSet, frozen lifecycle output slots | Signed cross-process publication of all authority records; governed coverage profiles |
| 06 Status | Diagnostic document/checkpoint authentication, protected high-water reads and receipt-derived byte-CAS before withdrawal interpretation | Caller epoch is diagnostic. Production discovery, trusted-time/clearance integration, rotation, immutable v1 tags, revocation and channel concurrency are unimplemented |
| 07–08 Reproduction | Linux stage-two helper/runtime/image and terminal-byte receipts, deterministic comparisons and profile matching | Provider receipts, other-platform authority, qualified helper images, independent two-machine bake-off and profile graduation |
| 09 Provenance | Separate deterministic reproduction and SLSA production predicates; exact Plan/dependency/subject finalization checks; acquired-image parent/native URI distinctions | Recorded hosted signatures/provenance do not establish qualified reproduction; parent-bound qualified acquisition provenance is unavailable |
| 10 Builders | OCI source builds, digest-pinned acquisition, rootless BuildKit wrapped exports, direct-Packer disk schema/IR/Plan lowering and connected unencrypted local execution; exact component/material bindings and acquisition-carrier retention | Command/Make material handoff and held-output execution, general hosted/qualified VM adapters, encrypted secret channel, qualified Nix observation, fused general wrapped-job phase isolation and embedded-version consistency remain closed |
| 11–12 Resolution | Exact output-selected producers and concrete platforms; complete explicit disk material closure; predecessor-bound whole-producer overlays | Finite-set embedded compatibility and accelerated-runtime rendering remain deferred; no matching, mixins, wildcards or deployment classification |
| 13 Evidence | Exact offline binary/image dependency scanning and bounded Helm metadata inventories, including acquired raw charts; stage-bound signed CycloneDX bundles; M2 live disk producer/material evidence with explicit deferred inspection; guarded installed-inventory and full-content export receipt APIs | Real native NKE full-content inspection/reconciliation and original-live-Run joins remain unexercised. Vulnerability scanning, acquired upstream-signature verification and qualified signed evidence publication are outside v1 |
| 14 Delivery | Executable/APT/Helm formats, byte-preserving acquired charts, image-bound source charts, RAW/ISO measurement, Linux held-qemu envelope verification, local held-QCOW2 retention and streamed OCI disk wrappers | Independent full-payload signed M2 disk read-back, real strict receipt-to-package/finalization and unmodified Helm/ORAS binary interoperability remain unexercised; encrypted/object-store delivery is unavailable |
| 15 Air gap | No mirror implementation or CLI retained; supported offline signature primitives remain | Air-gap product lifecycle and zero-egress acceptance require a new admitted design |
| 16 Release gates | Inventory/ReleaseSet/PromotionPolicy documents, exact member completeness and signed diagnostics | Qualified receipt/clearance assembly, release-specific trusted time, M3 qualification and M4 manifests/revocation remain unimplemented |
| 17 Public lifecycle | Compiler-owned output joins, immutable attempts, failure records, process-owned snapshots, exact observations, deterministic disk retention and SDK/CLI delegation | No general resume protocol or authority restored by serialization; strict local publication, encrypted disks and qualified lifecycle remain unavailable |
| 18 Execution and GitHub | Shared local/hosted producer launcher, held source/vendor lifetime, capability admission, manual-only GitHub generation, exact optional branch dispatch allowlist, bounded non-canceling concurrency, scoped credentials and unconditional finalization; explicit native ARC disk grouping | Default-branch push publication and in-repository PR build triggers remain deferred; arbitrary self-hosted/other-provider routes and qualified emitters fail closed. Provider configuration and fresh runtime evidence remain separate |
| 19 Downstream boundary | Exact required/observed output membership, component evidence joins and noncircular release-content identity | Downstream owners supply topology, placement, provisioning and runtime qualification; historical topology records grant no current authority |
| 20 Publication | Digest-selected sibling repositories, signed evidence membership, native OCI/Docker subjects, bounded metadata navigation and per-output assembly/finalization; candidate M2 hosted signing/publication completed | Main-only Plan execution, independent M2 full-payload disk consumption, strict full-content publication and object-store/CDN transports remain unproved or unavailable; registration is deferred |
| 21 Generation | Exact recorded input selection, manifest-last ownership, deterministic checks/retirement, canonical Plan inspection, linked graphs, semantic diffs, atomic pin maintenance plus regeneration and review patch workflows | Automatic PR publication and scheduled/private-source service acceptance are unexercised; lock and generated files are separate transactions; complete crash matrix remains |
| 22 Pins | Catalog v3/lock v1, per-module tags, source/artifact/action/tool pin maintenance, no-op/check/CAS semantics, derived pin sets, same-release maintenance groups, whole-producer overlays and exact toolchain substitutions | Pin-set loading needs pinned Git access. Curated graph/SBOM edges and embedded-version observation remain deferred. Exact pin identity is not external acquisition or production acceptance |

## Native disk execution and inspection

A disk output selects one root and every transitive material producer, without unrelated extras. IR v4
binds command/direct-Packer form, source/material edges, public variables, source-owned manifest projection,
disk metadata and selected native toolchain. The held observer independently re-hashes the regular QCOW2
and checks bounded header/binding data; this is not a complete decoder, compatibility verdict or inventory.
Lifecycle retention rechecks the held bytes, syncs file/mode metadata, and commits disk plus Run record
under an attempt marker. Marker removal and directory sync are the durable success boundary; interrupted
attempt reuse fails rather than becoming complete.

Generic source build requirements and Environment execution classes bind deterministic route selection into
the Plan, outside logical Build identity. Local VM admission checks exact Plan/node/class bindings against
current Linux host, affinity/cgroup-capped capacity, held workspace and selected KVM exposure. A selected
KVM handle remains live through the deadline-scoped callback, with fresh checks before work. Availability
is not reservation, provider allocation or proof that QEMU consumed that descriptor.

The direct-Packer runtime owns authenticated private-volume input transport, exact native toolchain and
daemon admission, read-only material mounts, fixed environment/host user, validation before one declared
build and held-output collection. It performs no plugin initialization or image pull during producer
execution. Preparation/validation stay offline; build networking needs explicit Environment authorization
and exposes the default bridge, not an outbound-only firewall. A narrow root extraction/ownership stage
precedes non-root consumer work. Local execution has no subordinate-ID/user-namespace guarantee.
Success, returned failure and cancellation own bounded cleanup; hard-crash orphan recovery remains deferred.
ADR-10/18 own the complete driver and native runner contracts.

Attempt-owned material manifests and bounded source-owned build logs are diagnostic. Unsafe entries fail
closed; ordinary log loss/overflow records incomplete diagnostics without discarding a separately verified
disk. Retention closes before immutable Run recording and does not mask the primary build failure. FDE
secrets are not admitted through this sink.

Diagnostic installed-package inspection of a retained QCOW2 is distinct from full content inspection. The
[recorded native observations](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/disk-inspection-observations.md) cover exact ARM64 subjects,
unchanged-helper replays, 730 installed packages and scoped mount/cancellation behavior. They do not prove
current-helper execution, Docker/native AMD64 inspection, original-live-Run joins or complete disk coverage.
The diagnostic runtime binds caller-selected runtime/image/tool identities; those are not automatically
repository-approved pins or an authenticated provider boundary.

Optional local lifecycle inspection keeps the original factory owner open while it snapshots and scans.
An opaque installed-inventory receipt binds attempt/output/Plan/Build, exact QCOW2 digest/size and tool/runtime
identities. Receipt substitution, sibling replay, post-close use, cancellation and failed commit are rejected.
A process-wide permit owns queueing, snapshot creation, scan/cleanup and close; it reserves no kernel resources
against unrelated processes. Opted-in inspection failure fails the Run and discards uncommitted delivery
bytes; default builds remain build-only. Controlled tests exercise these boundaries, not a real joined NKE bake.

The separate full-content profile binds the native inspector/codec/guestfish/Syft/dpkg-deb image/tool closure,
resource budgets, exact acquisition carriers, staged materials and exported filesystem. Class-separated
correspondence, observed file hashes, method-scoped CycloneDX subjects and original-owner export receipts
are implemented with controlled issuer/export and negative tests. They do not establish actual native NKE
scanner discovery or a successful strict live receipt-to-package-to-hosted-finalization path. Modern mixed
Docker-save archives, prefixed OCI members, foreign/unknown attestation descriptors and nested Debian package
databases remain unsupported. Mounted-tree export does not prove coverage of unmounted/unrecognized extents.
Nested carrier file identities are not invented installed paths; only top-level material hashes participate
in material/file correspondence. Private-volume capacity admission is not quota/mount-policy proof.

Top-level OCI image materials admit only bounded, uncompressed OCI layouts. Complete closure and every
descriptor/configuration must match the consumer platform before each unique image is scanned through a
private Docker-archive projection; observations remain bound to the original held carrier and closure is
revalidated afterward. Docker/gzip, prefixed members, unsupported attestations and malformed carriers fail
without partial inventory. Shared member/image and pre-deduplication component limits still apply. Real
NKE material complexity, cold-cache timing and full-matrix inspection remain unverified.

ADR-13 owns the exact evidence vocabulary and bounds: 32 MiB documents, 64 MiB signed bundles and 16 MiB
composition reports; provenance retains its independent 4 MiB bound. Oversize evidence fails without
truncation. A structural file ceiling does not guarantee byte fit. Shared disk codecs reject external backing
and data files; a QEMU pin must include the applicable QCOW2 `NO_IO` fix rather than treating any digest as
patch evidence. Neither diagnostic inspection nor these guarded APIs grants qualified disk reproduction.

The native ARC disk adapter contract now derives execution/job budgets from real producers, the selected VM
class and the strict inspection join, leaving separate cleanup headroom. The shared terminal hour also covers
public image pulls and per-output offline publication preparation; the fifteen-minute provider reserve is
shared by pre-steps and successful carrier packing/uploads. No per-step transfer/max-byte or remaining-provider-time
proof is implemented, so near-ceiling transfer headroom remains an operational risk (ADR-18 §3). Failure-only Packer diagnostic transport
selects only one successfully sealed sink-issued route with bounded retention; early admission failures cannot
select workspace content. Both-native generation and tampering tests cover this projection, not a fresh
ephemeral-runner timeout or diagnostic-upload exercise.

Strict inspection separately retains bounded, sanitized phase/timing diagnostics with original held-subject
coordinates and timeout summaries. A separate parsing ceiling rejects excessive diagnostic input while
draining the child; truncation preserves the last accepted phase. Diagnostics grant no receipt and timing
does not enter evidence identity. Hosted retention uses one atomically published, no-replace file and an
optional, failure-tolerant, always-run upload with one-day retention and a separate five-minute allowance.
Required evidence uploads remain mandatory. M2 build-provenance mode emits neither inspection nor this
upload. Rebuilt-helper deployment, full native inspection and corrected production timeout behavior remain
unexercised.

The [October 7 PR #97 contract record](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-07-pr97-review-fix-contracts.md)
records native Linux ARM64 race execution of the sealed diagnostic coordinator and
the explicitly enabled strict hosted-completion test. Both use controlled ELF/bytes
with concrete public owners; neither establishes a real NKE bake, provider upload,
hosted finalization or independent consumer acceptance.

## Experimental OS source producers and OVN outputs

The separate `definitions/nke-os-source` catalog builds public OS binaries from
pinned Git sources. Its read-only `nke-os-source-build.yaml` workflow exercises
`pipeline.Local` diagnostics on generic native AMD64/ARM64 hosted runners without
internal-source credentials. This is not full package-folder migration, OS-image
integration or a generated Run/Release. Helm's held `ExecNode` preparation remains
blocked by pristine external fixture links; local diagnostic preparation does not
admit that production route.

[Hosted run 37829955849](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37829955849)
passed all public-source producer builds on native AMD64 and ARM64 at
`335ace1e75829c061eac92caf42d8da0bc2fbd80`. It exercises unqualified
`pipeline.Local` execution, not OVN, full QCOW2 builds, production publication
or the held `ExecNode` preparation route.

The separate `definitions/nke-ovn-source` Definition selects native image and
digest-bound Helm OCI outputs for both architectures. Contracts compile the
four-output Plan, enforce six same-platform image bindings, reject missing
slots/outputs and cross-platform joins, exclude publication destinations from Build
identity and check byte-exact generation. `values.digestFormat: sha256-hex` supports
templates that supply their own algorithm prefix; unknown formats fail before
chart mutation. Ordinary hosted ARM64 producers select `ubuntu-24.04-arm` when
`runsOn` is unset; explicit selections and VM/ARC routing retain precedence.

These are experimental candidate inputs, not maintained M2 membership or proof of
GHCR publication, signed Release acceptance or OS integration. Internal fork access
requires separate acceptance. Authenticated cross-output image/final-chart handoff
into OS material composition remains absent ([issue #116](https://github.com/dsx-ai-factory/nke-recipes/issues/116)).
Rebuilding equal pins does not establish same-byte correspondence; opt-in producer
and Helm OCI interoperability tests do not establish hosted or release acceptance.

## Output-local publication and metadata assembly

The GitHub `per-output-v1` mode retains full payload/producer/evidence checks for each exact named output.
Each output publishes and verifies its artifact and signed metadata root. Aggregate assembly authenticates
those roots, production predicates and SBOM bundles against the complete Plan, same workflow run and frozen
requirements. Aggregate signing/verification and finalization use only bounded metadata/evidence, with no
payload-layer download. The independent Release consumer still checks complete payloads.

ADR-20 admits earlier-attempt output roots only within the same GitHub run and unchanged Plan, with exact
repository/builder and preparation/signing/consuming-attempt ordering. Contracts cover retries, mixed
attempts, re-signing and finalizer reuse; hosted retry acceptance remains unexercised. Cross-run reuse,
remote rollback and an atomic multi-repository transaction are not provided. Failed/missing provider jobs
still prevent completion. Finalization restores the six exact control carriers; assembly/finalization need
registry read access, not OIDC or package-write permission. Custom static credentials, local execution and
dry-run signing reject this mode.

The root reader rejects extra blobs and bounds control/evidence to 512 MiB per root. Full and metadata-only
readers share exact schema/media-type checks. `release browse` projects checked metadata and evidence,
config-declared platforms and derived sibling locators, without reading payload layers or verifying policy.
Filtering follows complete metadata validation; detached views establish no registry-availability claim.
[Registry browsing of the recorded candidate](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md#metadata-browsing-validation)
retains the external scope and binary identity.

The generator/validator/writer enforce a 500,000-byte workflow limit. YAML compaction must preserve expanded
semantics; oversized bundles fail before writes. Native offline disk preparation shares the ARC group to
avoid restoring its full material closure on publication runners. Consumed carrier deletion reduces duplicate
storage but does not establish a measured hosted storage bound.

## Native ARC disk route
The current Makefile drift gate and the single checked-in `nkd-run.yml` select the M2 candidate Definition
and Environment, replacing the earlier six-output deploy selection at that fixed generated path. Dispatch
does not choose among Definitions, and no parallel generated six-output deploy workflow is maintained there.
The retained deploy and three-output inputs can be generated into a scratch root with their matching
byte-exact check. This records the current configuration, not a ratified canonical-product/path migration or
publication-namespace decision; that named-decider governance remains pending.

ADR-18 now permits the explicit `githubARC: native-kvm-v1` operator-trusted policy for the two native KVM pools.
The grouped helper retains original material and disk owners through reconciliation and packaging; generation
contracts cover both architectures, individual-node bypass rejection and unsupported policy rejection. Source
fetch, bootstrap and release credentials remain outside ARC jobs. Native preparation/measurement is provided
separately. M2 now generates all 34 outputs with two native disk jobs from the same Definition,
an exact catalog overlay and measured native inspection profiles. Four toolchain images were
published to internal GHCR packages under the user's scoped candidate exception, with exact
manifest/config read-back. This is not completed organizational license/security review.
Private pulls use a separate packages:read step whose temporary credentials are removed before
execution; cached exact image identities are revalidated by the held runtime.
Both native builds and terminal checks completed in run `37445475840`; both deep disk inspections timed out.
The user earlier moved deep disk/content inspection and dependent reconciliation to M3. The narrower candidate
`diskEvidence: build-provenance-v1` selects live producer/material evidence with signed uninspected scope,
normal packaging/signing/publication and unchanged subject/provenance/membership/completeness gates.
The [inspection deferral record](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-inspection-deferral.md) records the scope and validation.
The strict inspection path remains available. The October 7, 2026 PR #97 task chat contains Ilya Kislenko's
instruction: "Preserve main/#106: producer provenance for M2, inspection for M3". This is a task-chat instruction,
not an independently published decider approval; no such approval record is supplied here.
It supersedes the task's October 6 proposed strict M2 target while retaining main/#106's AMD64-only product
acceptance floor. M2 accepts producer provenance with its remaining delivery/consumer gates; M3 requires full
inspection, material reconciliation and strict evidence. The native ARM64 route and this candidate Definition's
additional ARM64 outputs are an implementation extension. Both native jobs and every declared output remain
required for this candidate's complete Run and Release; the product floor waives none of those requirements.
Organizational ADR ratification remains separate.
Both candidate disk jobs succeeded in run `37486058412`, but the aggregate release download failed with ENOSPC
before publication. The [dated carrier evidence](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-release-carrier-transport.md) records
that failed attempt. The subsequent validation-branch run `37533806963` at commit `625980e` passed
all candidate outputs and aggregate publication, signing, verification and finalization. That historical run
predates the consumer-policy and timeout changes here; it is not evidence for this updated head, current main
execution, strict inspection or independent full-payload consumer acceptance. Per-output storage peaks remain unmeasured.
The [October 6 preparation record](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-native-arc.md) retains final-source
ARM64 and AMD64 Packer/inspector assembly, executable measurements, native tool validation
and unprivileged libguestfs appliance filesystem probes. The earlier preparation Environment admitted
`codex/m2-arc-validation` through `githubDispatchBranches` and its signer identity.
The October 7 PR #97 test temporarily authored `codex/m2-native-arc-foundation` as an exact dispatch/signing
exception. [Run 37661490433](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37661490433)
at `562a609` completed with conclusion `cancelled`. Provider annotations rejected the exact branch under
`nkd-release` (sample attestation job `112931037919`, finalizer `112933977061`) and `nkd-arc`
(native ARM64 job `112932239664`); the failed jobs had no executed steps. This is a deployment-policy refusal,
not disk-build, signing or release acceptance evidence. The temporary authored exception was removed after
that cancelled test; dispatch and signer policy select `main` only again.
The external policy read-back retained `codex/m2-arc-validation` and `main` for `nkd-arc`, and
`codex/m2-arc-validation`, `feat/nke-deploy-pipeline` and `main` for `nkd-release`; it did not admit the exact
PR #97 branch. Removing an authored exception neither changes those provider policies nor bypasses them.
The source secret name exists. Package Actions read access, native pool capacity and successful exact-head
execution remain external acceptance requirements.


## Maintained input and schema boundaries

The [M2 Definition instructions](../../definitions/nke-m2/README.md) own example-specific selection and input
preparation. The candidate requires 34 outputs: ten CLI/controller/chart/operator/disk outputs plus twelve
third-party runtime images per native architecture. The chosen runtime-image set follows pinned deployment
values, not chart defaults; chart-test images and NKE images outside the disk retain explicit owner questions.
Native children are locked separately from source-owned index parents, and materialization authenticates actual
membership. Controlled source/pin/image tests and exact-source approval do not prove external layer acquisition,
scanner acceptance or complete production input approval. The
[acquisition observations](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#temporary-spire-source-selection) retain source
approval, mixed-source candidate and HTTP-403 limits without copying locks into this ledger.

Current Definition v4/catalog v3/IR v4/Plan and artifact-graph v4 reject retired recipe, cluster, category,
role, composition and staging fields, including empty legacy fields. Runs/Releases and validation vectors retain v3; publication roots and Inventory/ReleaseSet use v4; disk manifests use v2. Removed compiler-handoff,
execution-settings, AICR, release-authorization/reproduction-result/waiver and qualified-publication families
remain reserved and nonminting. Historical signed bytes are not converted or reinterpreted. Equal Build
Digests never waive another output's execution or evidence slots; cross-output reuse is not implemented.

The public modules enforce one owner per invariant. [ADR-17](0017-cli-sdk-foundation-and-package-topology.md)
and [Development](../../DEVELOPMENT.md) describe current package/lifetime ownership. Completed cleanup,
organization and migration reports do not remain a second status ledger. Tests in the owning packages retain
strict input admission, held-byte substitution, durable attempt commit, cancellation/join, terminal evidence
and exact output/Plan bindings; passing repository gates do not establish external acceptance.

## External run references

The references above identify exact Runs and archived observations with their source, lane, and limits.
Raw records belong with CI or release artifacts; earlier investigations remain in Git history.
Immutable source links intentionally pin different commits to preserve each observation's historical snapshot.
Run `37533806963` at `625980e` records the 34-output candidate, including both disk outputs and aggregate
finalization. Its branch exception and `build-provenance-v1` mode do not establish independent full-payload
disk consumption, strict inspection, qualification, or organizational acceptance. Historical layout or
source success never establishes a current source, policy, full inventory, or qualified result.

## Remaining decisions

Named-decider ratification and organizational toolchain/source/identity approval remain separate from candidate
execution. Owners must supply registry retention, release-tag concurrency/collision and interrupted-publication
protocols, source access and protected-environment policy, native runner-pool isolation, transparency approval,
main-only execution and independent consumer evidence. M3/M4 need the separate qualification identity,
signed verdicts/waivers, immutable manifest/tag/channel and revocation behavior.

Qualified owners must supply authenticated runtime/provider observations, discovery/trusted time, measured
loader/runtime closure, independent signed evidence, exact reproduction and any newly admitted offline lifecycle.
Pinned-image helper transport cannot substitute for executable/runtime receipts. No missing stronger capability
falls back to a diagnostic result, a v1 signature or an earlier historical success.

Current publication-root v4 binds the exact Definition, Environment and Plan identities. Optional consumer
expectations are checked before external work; the lifecycle owner authenticates the complete output match
and issues a policy/publication-bound opaque receipt. Current readers reject historical v3 roots, including
the Run 37533806963 demo release. That browsing record does not establish current-schema external acceptance.

Consumer Verification Policy binds `minimumDiskEvidence` into its typed digest. Pipeline-attested policy
defaults to `build-provenance-v1`; explicit `strict` requires inspected-disk evidence. Qualified policy
defaults to strict and rejects the producer-only minimum. Every disk in the authenticated closure must
meet that policy before a capability is issued. Synthetic publication contracts cover enforcement and
failed-policy cleanup; they do not establish production disk-release acceptance.
