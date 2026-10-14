# Architecture implementation and evidence

Reviewed 2026-10-13. ADR-01–22 own the lane-specific engineering contracts pending named-decider
ratification. This ledger owns current implementation and remaining acceptance. Code and contract tests,
recorded external execution, and organizational approval are separate facts. Historical source and
schema results do not transfer to a changed candidate.

[ADR-23](0023-upstream-artifact-behavioral-validation.md) additionally proposes upstream artifact
test interfaces outside that consolidated set until acceptance. Implementation and local CLI proof
are tracked below; governance, CI and broader acceptance remain pending.

## Current boundary

The maintained public workflow is **Definition → Run → Release**, with Environment, Artifact and
Verification Policy as the other public objects. Definition v5 selects exact producers for named outputs,
with a native `platform` or an explicit OCI aggregate `platforms` set;
there is no authored Recipe, cluster topology, placement, inheritance or composition lifecycle. Plans and
internal evidence records are generated or authenticated rather than separately authored public inputs.

Planning, byte-exact GitHub generation, Plan/graph/diff views and metadata-only release navigation are
implemented. Diagnostic pulling retrieves and checks one named output's complete payload closure from a
registry release: executable files, a plain or declared LUKS2-encrypted QCOW2 disk, an unchanged Helm archive,
an OCI image layout including all declared aggregate platforms, or an APT repository tree.
Local immutable attempts and Linux `pipeline-attested` consumers for `authenticity` and
`artifact-publication` are implemented. Authenticity verifies signatures and evidence without artifact
payload downloads; artifact-publication retains payload integrity checks. Browsing does not verify payload
bytes, signatures or consumer policy. Pulling checks the selected output's integrity without signature or
policy verification. Neither supported verification scope satisfies full v1 release or qualified acceptance; unsupported stronger
scopes fail before work.

Ordinary local non-disk terminal observation reopens and hashes producer bytes in the host process, using the
same filesystem view as collection after bounded BuildKit output transport. It executes no producer code or
signature verifier and remains static-unqualified. This is not a container-isolation or runtime-authentication
claim; producer execution, other offline utilities and the held-disk observation path retain their existing
contracts (ADR-18 §8).

Rootless BuildKit's fixed UID is independent of the caller's UID. Its private-volume transport preserves
authenticated source modes and limits ownership changes to attempt-owned volumes, with a read-only source
mount for the non-root producer. The declared export returns through the bounded archive owner before normal
collection. Controlled tests and a targeted native probe are distinct from the complete fresh NVKE acceptance
run; the earlier UID-mismatched bind-mount failure produced no accepted OS image.

Local unencrypted direct-Packer execution retains an exact held QCOW2, observes its bytes and commits the
disk with its immutable Run record. Ordinary local disk publication remains closed. The explicit native
ARC hosted route is separately admitted under ADR-18. The
[recorded 34-output M2 candidate](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md) completed generated native
disk builds, publication, signing, verification and Run finalization under a validation-branch exception.
Its `build-provenance-v1` evidence binds live producer/material subjects and explicitly defers inspection.
This is scoped candidate pipeline-attested delivery, not a claim of main-only workflow execution,
independent full-payload disk consumption, strict full-disk inspection, organizational toolchain approval
or completed M1–M4 acceptance.

Strict disk inspection and reconciliation are deferred hardening outside the approved M3 MVP. Controlled receipt/export/transport
contracts and retained-disk diagnostics exist, but a successful real native NVKE live-Run full-content join
has not been demonstrated. Qualified release authorization, full v1 qualification/tag/revocation automation,
qualified provider adapters, secret-valued encrypted VM execution and air-gap operation remain unavailable.

The maintained M2 candidate selects 37 named outputs, retaining the previous 35 and adding native AMD64
and ARM64 public-bootstrap FDE disks. Native ARC publication admits those disks only with explicit
`build-provenance-v1`: actual producer encryption, wrapper annotations, signed disk metadata and expected
output membership must agree. Strict inspection/publication, qualified reproduction and private-passphrase
FDE remain closed. The consumer's strict evidence policy still rejects producer-only disks. This integration
has no recorded combined-pipeline, FDE publication or ARM64 FDE execution result; the earlier AMD64 diagnostic
Run below establishes only its exact bake and raw transport.

The separate FDE diagnostic route admits direct Packer only with explicit public `luks_passphrase=changeit`.
It retains raw QCOW2 bytes locally or through a generated native ARC build-only workflow, without release
publication, signing, inspection or confidentiality claims. Its separate `definitions/nvke-fde` selection
contains one AMD64 output and the complete material closure. Runtime admission and generated transport
contracts are complemented by [Run 38443178035](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035),
which completed the pinned FDE target and finalized a `static-unqualified` Run at code commit `68c219845398b8ef5890e5c76a13d4901a22630d`.
The [native ARC record below](#native-arc-disk-route) binds the exact output and retained evidence.
The [pinned-source audit](../../definitions/nvke-fde/fde-audit.md) records retained bootstrap
files, SSH key distinctions, a GRUB cleanup defect and cloned-volume-key implications. The public value
requires no GitHub secret; private passphrases remain unsupported.

## M3 MVP scope and acceptance boundary

The [October 12 decision](https://nvidia.atlassian.net/browse/NKX-15959?focusedCommentId=25021826) retains
producer-only disk evidence and defers disk inspection, waivers, offline-PR and verify-only qualification.
The [implementation plan](https://nvidia.atlassian.net/browse/NKX-15959?focusedCommentId=25021930) separates
artifact authentication, native smoke, evidence publication and acceptance demonstrations. These are task
scope and planning records, not organizational ADR ratification or operational qualification evidence.

The current scope directed by the user makes embedded-component declarations optional, as recorded in the
product specification and owning ADRs. Omission is not applicable; explicitly authored declarations still
require strict consistency with their
referenced pinned upstream metadata. That check supplies no effective customized or installed composition claim.
Source, patch, configuration and material bindings remain required. Full inspection and material reconciliation
remain [NKX-18052](https://nvidia.atlassian.net/browse/NKX-18052).

[M3-A](https://nvidia.atlassian.net/browse/NKX-18043) authenticates the complete selected publication against
its exact producing Run, attempt, commit and input identities, and returns subject-bound results without
qualification signing authority. The Linux `run check-artifacts` command now takes `--run` with an exact
attempt URL and `--policy`, with an optional workspace. `lifecycle.CheckQualificationRun` owns policy loading,
source authentication, handoff download, captured-input recompilation and publication/evidence joins. The
independent policy approves repository, workflow and default branch and pins cosign. The provider discovers
the commit from the selected attempt and authenticates successful completion and the actual default branch;
downloaded records cannot select those trust decisions.

Generated production real-signing `per-output-v1` finalizers restore the original per-output publication references, signature records
and predicates and upload one `handoff.json` as `nvkr-qualification-<run>-<attempt>` after success. The collector
joins bounded aggregate metadata, exact output origins and saved route signing records to key original
predicate bytes by the root and delivered payload references; it does not restore or read payload packages. The bounded
canonical `qualification-handoff/v1` record includes the original Plan and compiler-captured Definition,
Environment, base/output catalogs, overlays, registries, locks, patches and directory facts. Replay preserves
logical paths, enforces byte/count/depth limits and has no authored-file checkout fallback. Catalog-owned
pinned upstream Git validation still runs with its existing context and credentials; offline replay remains
outside MVP. The old `CheckQualificationFiles` module adapter remains available for explicit diagnostics.
Historical Runs without the complete handoff cannot use the simplified path. Development finalizers retain
their Run record but emit no production qualification handoff. Legacy aggregate producers
retain their publication path but emit no qualification handoff: they lack invocation-bound release metadata
and the per-output route records needed for complete original evidence transport. Explicit capability admission prevents
export or consumption from implying otherwise. The file diagnostic adapter now shares the Run adapter's
independent credential-host admission before transport; missing/mismatched hosts refuse explicit pull
credentials, while anonymous and host-scoped keychain operation remain available.

The check receipt retains the verified publication and a frozen Plan; closing it revokes copies, cancels and
joins registered operations and then releases held bytes and any owned source receipt. It grants no execution
or signing authority. The command writes diagnostic JSON, not a serialized capability. Contract fixtures in
`pkg/nvkr/factory/compile_snapshot_test.go`, `pkg/nvkr/factory/github_handoff_test.go`,
`pkg/nvkr/qualificationorigin/selection_test.go`, `pkg/nvkr/qualificationorigin/handoff_download_test.go`,
`pkg/nvkr/lifecycle/qualification_run_test.go` and `pkg/nvkr/lifecycle/qualification_lifetime_test.go` cover input replay/relocation, original evidence transport, exact
attempt/policy admission, bounded download failures and cancellation/closure. These complement source refusal,
candidate decoding, Plan/provenance substitution and per-output evidence contracts; no provider or hosted
qualification acceptance is inferred from them. A read-only October 13 capture and replay of the maintained
35-output NVKE inputs reproduced the saved Plan digest
`sha256:2a59420b680dc5d5411b570fd72d7ae763cfa7657b02370712e54f092ec9181e` from 14 authored files,
10 directory facts and 59,613 authored bytes, with existing pinned Git validation enabled. This is compiler
compatibility evidence, not a producer Run, artifact check or qualification result.
Additional contracts cover the embedded check's 1,024-selected-producer boundary, exact signer admission
under literal substring policies, and canceled evidence traversal with retained slot-specific errors and
complete-set refusal. Traversal failures are explicitly labeled in each output's evidence diagnostic.
Late source-receipt revocation is reported by a separate failed `source-lifetime` check for each output,
with its overall verdict failed and no check receipt. Previously completed artifact checks retain their
actual results; successful reports keep their existing six-check shape. First/final evidence-callback
revocation fixtures cover this diagnostic boundary, not external provider acceptance.

All root/output production predicates must name the exact authenticated source attempt. If output metadata
carries publication origin, its preparation and signing invocations must both match. Earlier-attempt output
reuse under the existing publication retry protocol cannot satisfy this MVP gate. That publication behavior
is unchanged; the gate never relabels earlier bytes as current-attempt production. A qualifying M2 handoff
therefore also needs one complete exact-attempt set and its original production predicates.

[M3-B](https://nvidia.atlassian.net/browse/NKX-18044) now has a Linux native delivered-tool smoke owner
and an authenticated complete-set receipt issuer. `lifecycle.RunNativeToolSmoke` selects exact held payloads
from A's retained publication and frozen Plan, derives expected versions/commits from pinned inputs, and runs
only the fixed nvkectl, Helm and kubectl version profiles. The Docker owner verifies the pinned native image
and actual ELF platform, then executes held bytes as nonroot with no network, credentials, host mounts or
Docker socket, a read-only root, private temporary storage and bounded resources/output/lifetime. Closing A
cancels and joins dependent work. The smoke policy must cover every executable member; explicit OCI/APT
non-tool declarations are checked against exact output membership. Disk and Helm outputs retain explicit
not-applicable rules. Missing, ambiguous or unsupported applicability fails closed. Generation and runtime
share pure Plan/policy scope admission: exact profiles, expected versions/source stamps, OCI/APT membership
and selected native images are checked before rendering or bundle access. Contract tests include AMD64-only,
ARM64-only and both-platform scope, missing/extra membership and refusal before the first bundle lookup.

The fixed reusable worker signs a canonical native report with an independent worker identity. The report
binds complete A results, the full policy digest, the exact qualifier attempt, native image/runtime identity,
and each output/member payload digest, size, expected and observed version/commit. The final owner downloads
only successful exact-attempt worker artifacts and authenticates the same signing leaf's SAN/issuer, caller
workflow URI/commit and Run-attempt URI. It recomputes all expected members from retained A and requires
complete, disjoint native reports before issuing `lifecycle.ToolSmokeReceipt`. The receipt owns frozen
canonical evidence and shares revocation across copies; decoded documents and caller-provided passes cannot
issue it. Native, aggregate, provider, same-leaf invocation and mutation/cancellation contract tests pass under
Linux race detection. A real Docker ARM64 fixture executed all three fixed profiles against held fixture ELF
bytes with the intended isolation and clean environment. This establishes local runtime mechanics, not real
NVKE payload or hosted AMD64/ARM64 acceptance. Failure diagnostics cover every known native member,
preserving actual observations and distinguishing failed execution from checks never evaluated. Completed
member checks remain pass when later cleanup or revocation fails the report. Failure-only rows are rejected
by successful canonical report validation. Native diagnostic cases pass targeted Linux race tests.

[M3-C](https://nvidia.atlassian.net/browse/NKX-18045) now exposes Linux `run qualify --policy` and an
opaque-receipt publisher in `pkg/nvkr/qualification`. Its source comes only from the authenticated independent
default-branch `workflow_run` invocation and exact successful source event. The command verifies the trusted
checkout's generated workflow pair and full policy digest before signer/provider/candidate work, repeats A,
authenticates all native worker reports, then consumes the live complete B receipt. It admits every required
verdict before signing and attaches and verifies the same complete-set evidence for each exact named output.
The strict canonical `nvkr.dev/qualification-evidence/v2` schema and `nvkr.qualification-evidence` digest family
bind the gate version, complete A report/input identities, source and qualification invocation, full trusted
policy, signed worker observations, smoke verdicts and external-suite status. Complete predicates exceeding
the signing adapter's 4 MiB bound are refused before signer construction. Serialized diagnostics and decoded
evidence do not recreate receipts or authorize a release. Operational signing/attachment failures can leave
earlier attachments but never count as successful qualification. The former file-based publication entrypoint
now refuses capability admission before I/O; file-based A diagnostics remain available. M4's release consumer
remains outside this stack. A final smoke-authentication refusal now returns a failed output verdict for
every authenticated A output, preserving the genuine artifact results and original error/cause without
asserting that unauthenticated payload execution failed. Focused refusal tests pass under Linux race.

External suites such as NVKE's are a future authenticated, exact-subject integration point. Explicitly named
outside-MVP suites are recorded as `not-evaluated` with reason `outside MVP scope`; there is no default
passing suite. Requiring any suite returns a capability error before provider/artifact work until the
authenticated integration exists. An upstream test adapter, process exit status or caller-supplied pass
boolean alone is not qualifying evidence.

Current C contract tests cover strict schemas and complete subjects, independent invocation/source-event
binding, forged or closed receipts, failed checks before signing, evidence substitution and oversize refusal,
cancellation, shared receipt revocation, partial attachment failures and immutable worker-output publication.
Signature/provenance fixtures exercise distinct signatures and exact production predicates, including
substituted production evidence. Focused Linux race tests cover the native/aggregate lifecycle, qualification,
provider, signature and CLI boundaries. These tests do not prove hosted service behavior.

Earlier real cosign 3.1.3 execution with a local key and a loopback OCI registry exercised qualification
attestation attachment, download and cryptographic verification in `pkg/nvkr/sign/oci_interop_test.go`.
That earlier v1 minimal protocol fixture was not a complete passing M3 evidence record; its transcript does
not establish the new v2 complete-set path, hosted OIDC identity or production registry behavior.

`run generate --qualification-policy` renders the original producer bundle plus a separately owned
`workflow_run` qualifier and fixed same-commit reusable native worker. The worker is a generated GitHub
Actions workflow, not a separately authored service or operator entrypoint. The generated pair has strict
ownership, policy/Plan identities, exact file hashes/modes, manifest-last publication and read-only drift checks. It uses
exactly the selected native executable platforms (one or both), pinned bootstrap tools/images, explicit
trusted source-secret mappings and pull-only worker registry credentials. Only the final qualifier has
registry write permission. It runs after worker success or failure to make complete refusal diagnostics
reachable, while excluding skipped/unknown dependency results, cancellation and untrusted source triggers.
Scheduling does not relax provider job success, worker signature or complete-set receipt admission. Rendered
workflow contracts and actionlint cover the condition; actual hosted failure scheduling remains part of
external acceptance. The runtime checks the pair against the independent loaded policy before candidate work.
The maintained candidate records concrete verifier/native-image selections and exact smoke scope in
`definitions/nvke-m2/qualification.yaml`. Its generated pair is `.github/workflows/nvkr-qualify.yml` and
`.github/workflows/nvkr-smoke.yml`, with the separately owned qualification manifest. These inputs and workflows
are configuration for post-merge acceptance, not evidence of production readiness or organizational approval.
The user has accepted merging the reviewed implementation/configuration before the fresh complete producer
attempt and hosted/independent positive and negative acceptance runs. No artifact build or publication workflow
was dispatched for this implementation.

No real hosted qualification Run, worker/qualifier OIDC signing transcript, subject-bound attachment/read-back
or independent complete-set consumer verification is recorded here. [M3-D](https://nvidia.atlassian.net/browse/NKX-18046)
retains positive/negative external acceptance and independent review; the earlier M2 candidate cannot satisfy
it. The real producer handoff and hosted/consumer exercises follow merge and remain required before claiming
production readiness or closing operational acceptance. Acceptance must capture the actual reusable worker
REST job names, native execution on both architectures, worker and qualifier certificate SAN/issuer, same-leaf caller-workflow commit and Run-attempt claims, exact
source/qualifier identities, complete attachments/read-back and wrong-identity rejection. The verifier uses
fixed exact job names and claims; mismatch fails closed rather than widening identity admission. Current
fixtures and earlier local-key transcripts do not satisfy this criterion.

The M3-A catalog check accepts an optional authored `embeds` declaration selecting an exact component map
from a bounded YAML/JSON manifest read at the producer's pinned commit/tree. Omission is `not-applicable`
for every producer kind. An authored declaration still requires complete matching membership, exact
versions/source pins and strict shapes; a missing or invalid referenced manifest fails. An explicit empty
map is accepted only against an empty selected upstream map. `pkg/nvkr/catalog/embedded_test.go` owns these
contracts and defensive receipt checks. This authenticates raw upstream metadata only, without applying
patches, configuration or material substitutions, and supplies no effective-composition or installed-inventory
claim. The maintained candidate does not need new upstream component manifests to qualify. Exact producer
inputs/materials, source attempts, signatures, provenance, complete output membership and applicable tool
smoke remain required. Concrete candidate policy pins do not establish the real producer/hosted acceptance
that remains outstanding after merge.

## Support by owner

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 01–03 Identity and errors | Canonical integer-only codec, frozen typed families, strict decoding, transport-free Build identity, whole-graph admission and stable coded errors | Independent-language corpus for every signed family; continuing external-failure contracts |
| 04 Signing and consumption | Linux held-executable exact-byte signing/verification, pinned cosign keyless OCI adapter, policy-bound authenticity and artifact-publication consumers with separate capabilities; authenticity checks root/output signatures, production provenance and SBOM bundles without payloads; qualification predicate attachment/read-back verification with real local-key cosign interoperability | Authenticity has contract coverage only; no external consumer run recorded. Earlier independent cosign and public/private artifact-publication acceptance is recorded; qualification hosted OIDC, M2 independent full-payload disk consumption and root rotation remain unproved. Full release and qualified scopes remain closed |
| 05 Trust and membership | Subject-bound vectors, publisher evidence, required Inventory versus realized ReleaseSet, frozen lifecycle output slots | Signed cross-process publication of all authority records; governed coverage profiles |
| 06 Status | Diagnostic document/checkpoint authentication, protected high-water reads and receipt-derived byte-CAS before withdrawal interpretation | Caller epoch is diagnostic. Production discovery, trusted-time/clearance integration, rotation, immutable v1 tags, revocation and channel concurrency are unimplemented |
| 07–08 Reproduction | Linux stage-two helper/runtime/image and terminal-byte receipts, deterministic comparisons and profile matching | Provider receipts, other-platform authority, qualified helper images, independent two-machine bake-off and profile graduation |
| 09 Provenance | Separate deterministic reproduction and SLSA production predicates; exact Plan/dependency/subject finalization checks; acquired-image parent/native URI distinctions | Recorded hosted signatures/provenance do not establish qualified reproduction; parent-bound qualified acquisition provenance is unavailable |
| 10 Builders | OCI source builds, digest-pinned acquisition, rootless BuildKit wrapped exports, direct-Packer disk schema/IR/Plan lowering and connected plain/public-bootstrap execution; exact component/material bindings and acquisition-carrier retention; M3-A pinned embedded-declaration checker and contract fixtures | Optional upstream metadata integration is not an MVP prerequisite. Command/Make material handoff and held-output execution, general hosted/qualified VM adapters, encrypted secret channel, qualified Nix observation and fused general wrapped-job phase isolation remain closed |
| 11–12 Resolution | Exact output-selected producers and concrete platforms; complete explicit disk material closure; predecessor-bound whole-producer overlays | Finite-set embedded compatibility and accelerated-runtime rendering remain deferred; no matching, mixins, wildcards or deployment classification |
| 13 Evidence | Exact offline binary/image dependency scanning, separate `InspectDebianArchiveMetadata` diagnostics and bounded Helm metadata inventories, including acquired raw charts; stage-bound signed CycloneDX bundles; M2 live disk producer/material evidence with explicit deferred inspection; guarded installed-inventory and full-content export receipt APIs | Factory callers retain ordinary hint-free dependency inspection; no maintained factory output invokes the separate Debian metadata diagnostic. Dependency SBOM attachment and serialized BOM validation reject its method. Native Debian delivery and payload dependency inspection remain deferred. Real native NVKE full-content inspection/reconciliation and original-live-Run joins remain unexercised. Vulnerability scanning, acquired upstream-signature verification and qualified signed evidence publication are outside v1 |
| 14 Delivery | Executable/APT/Helm formats, byte-preserving acquired charts, image-bound source charts, RAW/ISO measurement, Linux held-qemu envelope verification, local held-QCOW2 retention and streamed plain/public-bootstrap FDE OCI disk wrappers | Independent full-payload signed M2 disk read-back, real strict receipt-to-package/finalization and unmodified Helm/ORAS binary interoperability remain unexercised; private-passphrase FDE, strict FDE and object-store delivery are unavailable |
| 15 Air gap | No mirror implementation or CLI retained; supported offline signature primitives remain | Air-gap product lifecycle and zero-egress acceptance require a new admitted design |
| 16 Release gates | Inventory/ReleaseSet/PromotionPolicy documents, exact member completeness and signed diagnostics; M3-A Run/policy artifact checks, generated exact-attempt handoff and held complete-set receipts; M3-B isolated native tool smoke and authenticated complete-set receipts; M3-C policy-only orchestration, v2 qualification evidence, receipt-only publisher, complete pre-sign gates and failure diagnostics | Real candidate native smoke, hosted worker/qualifier identities, qualification signing/attachment and independent M3 consumer acceptance remain unexercised. Qualified clearance, release-specific trusted time and M4 manifests/revocation remain unimplemented |
| 17 Public lifecycle | Compiler-owned output joins, immutable attempts, failure records, process-owned snapshots, exact observations, deterministic disk retention, diagnostic output pulling, bounded descriptive progress, typed result browsers for interactive commands, read-only stored Run inspection and SDK/CLI delegation; Linux M3-A Run/policy CLI, captured-input replay and retained file diagnostic orchestration; M3-C policy-only qualification orchestration with generated-policy admission | No general resume protocol or authority restored by serialization; actual M3 candidate/provider acceptance remains unexercised. Strict local publication, private-passphrase/strict FDE publication and qualified lifecycle remain unavailable. Fresh live dashboard Run, native Linux race acceptance and human usability validation remain unexercised |
| 18 Execution and GitHub | Shared local/hosted producer launcher, held source/vendor lifetime, capability admission, manual-only GitHub generation, exact optional publication-branch allowlist, separate full-development branch workflow, mode-specific concurrency and artifact retention, scoped credentials and unconditional finalization; at most two optional summaries per attempt with operation details in logs; explicit native ARC disk grouping, raw build-only disk transport and optional native hosted platform maps (including separate CLI output slots); authenticated independent qualification invocation/source-event binding; generated fixed native worker and qualifier with separate credentials | Maintained candidate qualification is configured for post-merge acceptance; a fresh complete exact-attempt handoff and hosted/independent-consumer evidence remain unproved. Default-branch push publication and in-repository PR build triggers remain deferred; arbitrary self-hosted/other-provider routes and qualified emitters fail closed. Provider configuration and fresh runtime evidence remain separate |
| 19 Downstream boundary | Exact required/observed output membership, component evidence joins and noncircular release-content identity | Downstream owners supply topology, placement, provisioning and runtime qualification; historical topology records grant no current authority |
| 20 Publication | Digest-selected sibling repositories, signed evidence membership, native OCI/Docker subjects, aggregate runnable OCI indexes with exact native-child evidence, bounded metadata navigation, selected output payload-closure retrieval and per-output assembly/finalization; candidate M2 hosted signing/publication completed; public-bootstrap FDE build-provenance publication supported; development attempt namespaces and scoped GHCR cleanup | Main-only Plan execution, independent M2 full-payload disk consumption, strict full-content publication and object-store/CDN transports remain unproved or unavailable; registration is deferred |
| 21 Generation | Exact recorded input selection, independent fixed production/development layouts, manifest-owned semantic action interfaces and Plan-derived invocation data with expanded-step validation, manifest-last ownership, deterministic checks/retirement, canonical Plan inspection, linked graphs, semantic diffs, atomic pin maintenance plus regeneration and review patch workflows; separately owned qualification pair and runtime policy drift admission | Latest stable-workflow, semantic-interface and controlled-transfer regressions are unrun; semantic-action hosted execution, automatic PR publication and scheduled/private-source service acceptance are unexercised. Producer matrices and general callable workflows remain deferred. Qualification generation does not make both owner writes one atomic transaction; lock and generated files are separate transactions; complete crash matrix remains |
| 22 Pins | Catalog v3/lock v1, per-module tags, source/artifact/action/tool pin maintenance, no-op/check/CAS semantics, derived pin sets, same-release maintenance groups, whole-producer overlays, exact toolchain substitutions and authored embedded-version metadata authentication | Pin-set loading needs pinned Git access. Curated graph/SBOM edges remain deferred. Optional metadata consistency is not disk-content inspection, external acquisition or production acceptance |
| 23 Artifact test interfaces | Not merged; upstream interface, Go Plan helper and diagnostic adapter are separate drafts | [Implementation guide and local CLI proof](https://github.com/dsx-ai-factory/nvkr/blob/65d13f4becb358015dead41fd1bc440d13118556/docs/artifact-testing.md) retain their stated limits. Maintained-pin adoption, kind, CI and CAPD/CAPNICo acceptance remain outstanding; no conformance or qualification authority |

## Feature-branch pipeline development

The explicit `githubDevelopment` Environment setting selects separate fixed workflow/Plan/manifest paths
with the same compiler, complete graph and helper. The committed development bundle now uses the full
37-output M2 Definition, including all four native plain/public-bootstrap FDE disk outputs, output-local publication, SBOM attestation,
cosign signing, verification, metadata assembly and finalization. It retains the candidate's existing disk
and source limitations; development execution cannot approve those production exceptions. Development
finalization retains its Run record without emitting the production qualification handoff; explicit handoff
admission returns a capability error before qualification evidence collection. Production handoffs retain seven days.

Unsigned development remains hosted build-only, static-unqualified, automatically cancelling older
same-branch Runs and retaining transfers for one day. Signed development requires the prescribed
`nvkr-dev.yml` branch identity, GitHub issuer, `per-output-v1` and reserved GHCR development namespace.
It uses `nvkr-dev-release`/`nvkr-dev-arc`, fixed dedicated read/write credential names and no repository-token
package-write permission. The native ARC labels and exact runtime/evidence admission are unchanged.
Source and pinned-toolchain read credentials retain their separate fixed preparation steps.
Provider pool/credential/ACL configuration is an external prerequisite, not established by rendering.

The factory owner derives registry targets beneath the current run and attempt without mutating committed
Plan bytes or Build identity. Subject/reference, metadata and predicate checks reject another attempt's
publication. Production authenticated earlier-attempt reuse is unchanged; development retries rerun all
jobs or dispatch a complete new Run. Signed-development helpers/carriers and all final Run records retain
seven days; disk diagnostics retain one day. Full signed development is non-canceling.

The development cleanup module inventories only private packages in that reserved attempt namespace.
It authenticates account, package and exact completed workflow-attempt metadata; package, version and
attempt timestamps must all be at least 24 hours old. It rechecks before whole-package deletion, fails
closed on unknown/ambiguous/API state, and records confirmed partial progress without retrying ambiguous
mutation. Preview is the default. The separate daily cleanup workflow executes only default-branch code
with a cleanup-only credential; it never loads branch executables or downloaded artifacts. Production and
older unscoped packages remain outside this protocol.

Definition/factory contracts cover admission, identity, complete M2 graph retention, exact rendering,
credential/environment scope, retention, runtime namespace projection and cross-attempt rejection.
Cleanup API fixtures cover eligibility, authentication errors, pagination, races and mutation failures;
workflow contracts enforce default-branch scheduling and secret scope. The dispatch helper retains its
clean/current pushed-commit preflight. These tests do not establish live GitHub execution, actual credential
scope, ephemeral runner isolation, successful keyless services, GHCR deletion or physical storage reclaim.
No new external Run or deleted package is claimed. ADR-04/18/20/21 own the contracts.

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
An unjoined metadata observer retains only its exact dependent resources and
keeps independently bounded cleanup for the rest. Its stop-issued object
locators may be saved in a private no-replace `observer-retained.json` beside
the attempt diagnostics; they are non-authoritative inspection data, never a
cross-process adoption/deletion capability. Persistence failure stays visible
without permitting an artifact or skipping independent cleanup. Controlled
contracts distinguish successful private retention, no-replace/storage failure
and unjoined ownership; this is not live orphan-recovery acceptance.
ADR-10/18 own the complete driver and native runner contracts.

Attempt-owned material manifests and bounded source-owned build logs are diagnostic. Unsafe entries fail
closed; ordinary log loss/overflow records incomplete diagnostics without discarding a separately verified
disk. Retention closes before immutable Run recording and does not mask the primary build failure. Private
FDE secrets are not admitted through this sink. The explicit public bootstrap value is diagnostic data;
admitting it does not prove that upstream removes guest SSH host keys or every installer/initramfs copy.

Diagnostic installed-package inspection of a retained QCOW2 is distinct from full content inspection. The
[recorded native observations](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/disk-inspection-observations.md) cover exact ARM64 subjects,
unchanged-helper replays, 730 installed packages and scoped mount/cancellation behavior. They do not prove
current-helper execution, Docker/native AMD64 inspection, original-live-Run joins or complete disk coverage.
The diagnostic runtime binds caller-selected runtime/image/tool identities; those are not automatically
repository-approved pins or an authenticated provider boundary.

Optional local lifecycle inspection keeps the original factory owner open while it snapshots and scans.
An opaque installed-inventory receipt binds attempt/output/Plan/Build, exact QCOW2 digest/size and tool/runtime
identities. Receipt substitution, sibling replay, post-close use, cancellation and failed commit are rejected.
A process-wide permit owns queueing, snapshot creation, scan/cleanup and close; it reserves no kernel resources
against unrelated processes. Opted-in inspection failure fails the Run and discards uncommitted delivery
bytes; default builds remain build-only. Controlled tests exercise these boundaries, not a real joined NVKE bake.

The separate full-content profile binds the native inspector/codec/guestfish/Syft/dpkg-deb image/tool closure,
resource budgets, exact acquisition carriers, staged materials and exported filesystem. Class-separated
correspondence, observed file hashes, method-scoped CycloneDX subjects and original-owner export receipts
are implemented with controlled issuer/export and negative tests. They do not establish actual native NVKE
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
NVKE material complexity, cold-cache timing and full-matrix inspection remain unverified.

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

The [October 7 PR #97 contract record](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-07-pr97-review-fix-contracts.md)
records native Linux ARM64 race execution of the sealed diagnostic coordinator and
the explicitly enabled strict hosted-completion test. Both use controlled ELF/bytes
with concrete public owners; neither establishes a real NVKE bake, provider upload,
hosted finalization or independent consumer acceptance.

## Local ARM64 development inputs

The `definitions/nvke-local-dev-arm64` combined example selects 29 outputs in one
Run without publication/signing: OS, components, installer-aligned site, tools,
profiles and simulator. Focused selections remain available; the historical
22-output component subset is not the complete demo. This is not a complete
source-built distribution. See the
[quickstart](../local-nvke-demo.md) and [source gaps](../full-nvke-source-build.md).

Local static-unqualified mixed Plans admit offline, non-disk utility
observation/SBOM/packaging in the child helper while VM/held-disk execution
stays with the original Run owner. Network utility acquisitions remain
unsupported. Relocated image observation retains compiled Plan identity.
[Mixed-utility tests](../../pkg/nvkr/factory/local_mixed_utility_test.go)
use controlled OCI bytes and a descriptive, unexecuted VM; they do not prove
a real joined bake or extend hosted, signing or qualification admission.

The Go downstream consumer invokes `nvkr` for build, verification and
extraction, and NVKE's pinned staged installer for deployment. Kind hosts
management services; CAPNICo/NICoSim provisions QCOW2 tenant VMs. Full E2E success
requires two Ready nodes, workload/DNS checks, exact disk/artifact bindings and
owned cleanup. The explicit `--cleanup` route retains that full acceptance
scope. Default `run`/`e2e` instead hand off a retained local environment to the
shell, with separate `status` and explicit `cleanup` commands. A live retained
owner, its serialized description and historical Ready time are not current
Kubernetes health, joined cleanup or a release receipt. The actual launch/peer
selection, detach, operation cancellation, partial retention and explicit
teardown have controlled native Linux ARM64 process fixtures; these are not a
fresh NVKE retained deployment or proof of SSH/logout/cgroup persistence.
Live access presentation executes bounded read-only bundled kubectl API/node
queries for the site and tenant and keeps raw responses private. Its focused
tests do not establish real Ubuntu runtime acceptance. New retained work does
not retroactively change the exact-revision complete recording below.
Cobra/Viper configuration supplies descriptions, not receipts.
The combined build reuses bootstrap executables and the measured toolchain;
catalog substitutions retain their own exact predecessor binding. `--jobs`
bounds concurrent ready producer and packaging operations in the existing
factory scheduler. The upstream NVKE readers
still require Python; the demo does not reimplement them.

E2E prepares its exact pinned installer automatically with bounded Git in a
fresh private attempt directory; an explicit matching checkout remains read-only.
Replay still requires a checkout. Preparation checks build prerequisites before
acquisition; controlled tests cover pin/dirty/source/path/cancellation failures
and prepare-before-build ordering, not current external Git authorization or
fresh tenant acceptance.

Ordinary local source Git and installer preparation now use the existing
`internal/sourcegit` owner to exclude ambient global hooks, fsmonitor, filters,
templates and line-ending conversion. Developer credential and selected proxy/TLS
trust settings are projected only into network fetch; explicit absolute local-repository redirects remain
available. Its transport override admits HTTPS, SSH and local files, not cleartext
HTTP/Git or external helpers. Clone fixes upload-pack, disables local-path
optimization with `--no-local`, enables transferred-object checks and disables
recursive submodules; ref discovery also fixes upload-pack.
Neither expands developer credential projection beyond fetch. Real Git fixtures
exercise TLS-verified helper fetch, plaintext refusal and exact local clone/ref
results; these are not current private-source access or held receipt evidence.
Ordinary local sources reject indexed gitlinks and nonempty, untracked, missing,
symlinked or mismatched root `.gitmodules` descriptors. A regular tracked empty
descriptor requires Git's empty-HEAD and index/worktree correspondence checks;
the maintained OVN fork's inert descriptor does not authorize nested sources.
Git owns the bounded index-mode query; its Git 2.38+ capability floor is named on
query failure. Ignoring submodules during status is not evidence of clean nested sources.
The ordinary adapter accepts operation-first arguments only; its explicit
directory owns repository selection. The demo may consume one leading `-C`
before that admission; additional global options are refused before repository
queries or command-log creation. Git still owns operation argument syntax.
Privacy-safe refusal diagnostics name static configuration families and
redirect indices, never configuration values, paths or URLs. Ordinary fetch and
configuration-query failures explain the supported fetch-only credential policy
without assigning an authentication cause or changing cancellation codes.
Real Git fixtures cover exact checkout bytes, callback exclusion,
local redirects and authenticated fetch. The E2E build adapter preserves these
bounded, owner-validated redirects, and the NVKE clone launcher appends its two
redirects instead of discarding other local source inputs. These fixtures are not proof of current
private-repository access; the stricter held hosted-source admission is unchanged.
The downstream site installer now takes a full detached private checkout of its
exact source pin instead of enabling sparse checkout, which Git configures through
the deliberately rejected `extensions.worktreeConfig` delegation. Real Git
regressions cover this failure, unchanged source bytes, the exact historical pin,
cancellation and continued rejection of delegated configuration. Command admission
errors retain the failing phase without exposing arguments or source paths. These
tests do not establish a completed Ubuntu site replay.

Maintained OS Definitions, catalog execution and Packer toolchain assembly select Docker.
The older authored Podman Environment and its TCG Definition are removed; generic
NVKR Podman support remains. The ARM64 catalog baseline retains the already-published
historical M2 Docker candidate, not a newly measured current toolchain. Local Docker
launchers still freeze their freshly validated image into an attempt overlay.
Static Plan and wrapper tests cover these selections, not a new native bake.

The local OS selects the PR #113 NVKE/NVIDIA OVN fork at pinned commit
`12ca20e67b516f1a023da85a22deeaf8d2d70d0b`; profile packages and tenant requests
select its `2.4.0` chart stream. The combined Definition also declares standalone
OVN OCI image and bound Helm OCI outputs. Disk-embedded and standalone outputs
are independently produced; this does not close original-owner cross-output
reuse or establish equal bytes. The recorded exercises below were performed
against the superseded pin `d7dc5d8c811fc037322a619775f5ef9bfe51c7a0` and its
`1.6.0` stream; they retain that scope and do not transfer to the current pin.
Fork graph/profile tests and actual pinned-chart
Helm rendering with loopback OCI push/pull were reported locally, without a
retrievable CI/Release evidence attachment for these exercises. An author-reported
local attempt at `de31a87` completed the combined artifact Run,
including the fork OS bake, with 29 expected and 29 observed outputs in the
`unqualified-local-execution` lane. Its exact Run identity and original records
remain private; no retrievable external artifact is cited here.
Artifact completion does not establish tenant acceptance or default OVN startup.
For the current `2.4.0` pin, static catalog/overlay/graph contract checks and a
local pinned-chart Helm rendering with loopback OCI push/pull are recorded, again
without a retrievable evidence attachment. No Run, OS bake or tenant exercise has
been executed against it. That pin also ships its single-node-zone values file as
a link to `values.yaml`. Image binding admits regular files only, and a copy would
carry the source image digests that binding replaces in `values.yaml`, so an
install selecting the copy would merge them back over the bound values. The chart
producer withdraws that member instead; the packaged chart omits it and the
source-documented install path using it fails closed on a missing file.

The selected fork's Ubuntu runtime omits `firewall-cmd`, while its non-DPU
executable initialization requires it even when firewalld is disabled.
[OVN issue #1](https://github.com/dsx-ai-factory/ovn-kubernetes/issues/1)
records the source paths and observed startup failure: artifact builds can
succeed without a Ready tenant. A canonical source-owner fix and reviewed pin
update are necessary for that NVIDIA selection, but are not shown to be
sufficient for tenant, workload/DNS or cleanup acceptance. No local catalog
patch is applied. Neither contract checks nor earlier deployment replays close
this NVIDIA-specific gate.

Separately, the retained continuous attempt above used the explicitly selected
`kind-calico` diagnostic and ended with a two-node readiness timeout, not a
successful E2E video or an OVN startup test. That timeout's cause was not
established by this recording; it cannot be attributed to the uninstalled
NVIDIA OVN startup path. Later acceptance evidence below retains its own exact
revision and network selection, rather than retroactively explaining this failure.

The downstream demo now selects those standalone NVIDIA OVN outputs for the
Kind site as well as selecting the fork's stream for the QCOW2 tenant. Its private
Kind configuration disables the default CNI and kube-proxy. The site adapter
copies the selected native image through the existing owned registry and exports
the observed bound chart through `nvkr publish chart`; it starts node-local OVS
using that image's existing entrypoint before site OVN, node readiness and CAPI.
These privileged local CNI processes are not an independently isolated provider.
Already-loaded Open vSwitch/Geneve modules are admitted before installer
acquisition or deployment mutation. The adapter does not issue module-load or
unload commands, but the privileged source-owned OVS entrypoint can manipulate
the shared kernel; prerequisite admission is not an isolation boundary.
The changed site topology has no completed live E2E acceptance. The source-owner
startup fix, reviewed pin update and fresh dual-cluster runtime checks remain
required, separately from artifact and adapter tests.
Read-only rendering of the retained chart with its built Helm 4.2.4 executable
was reported locally to preserve the selected image digest across all six site
chart slots. No retrievable rendering artifact is cited for that exercise;
it does not establish validation of the exact values currently installed by the
demo, or a running cluster. Owner validation must also cover real node startup: the
selected [iptables wrapper](https://github.com/dsx-ai-factory/ovn-kubernetes/blob/12ca20e67b516f1a023da85a22deeaf8d2d70d0b/dist/images/iptables-scripts/iptables#L5)
requires `/host`, whereas the selected
[node chart](https://github.com/dsx-ai-factory/ovn-kubernetes/blob/12ca20e67b516f1a023da85a22deeaf8d2d70d0b/charts/ovn-kubernetes/charts/ovnkube-single-node-zone/templates/daemonset.yaml#L324)
mounts `/host/run/dbus`, not that root. This is a source-level compatibility
gap, not an observed subsequent runtime failure or a local workaround.

The explicit `upstream-ovn` demo option is additive; NVIDIA OVN remains the
default and no mode silently falls back. Its authored local Definition replaces
four output names (disk, image, chart and profiles), preserving a 29-output
combined Run. Upstream `v1.4.0` resolves to commit
`d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d` and tree
`ce0cb49cd14f2fc260457e61a5a222c027a07f79`. Whole-producer overlays bind the
upstream image, chart and offline CNI packages; a separate authenticated profile
patch installs stream `1.4.0` and names its distinct served disk. Tenant requests
select that same stream. Opposite-mode outputs are rejected before deployment.
The source-owned image Dockerfile still acquires OVS/OVN and kubectl through APT;
this does not claim hermetic, FIPS/STIG, qualification or Kubernetes compatibility.

The complete alternate Plan compiled locally. Local rendering of the exact pinned
upstream Helm templates was reported to produce seven image references, each bound to the
selected digest through the existing repository/tag helper, without a template
patch; no retrievable external rendering artifact is cited for that local exercise.
Controlled tests cover mode propagation, four distinct output names,
profile package/disk alignment, image binding, six-container node observations
and site-network readiness before Cluster API. These checks alone do not
establish real startup or tenant acceptance. Separately, a fresh complete E2E
on `71b356f5899efb759571f0ea51781ead3f7c466a` passed on 2026-10-11: one
29-output Run, matched upstream site/tenant inputs, two Ready ARM64 QCOW2 nodes,
unchanged zero-retry workload/DNS and natural deletion plus joined owned cleanup.
The [exact-revision evidence](../evidence/local-upstream-ovn-2026-10-11.md)
retains the bindings, runtime checks and recording integrity. This is an
unqualified local diagnostic, not live acceptance of later changes, a supported
release combination, M2 or qualification. This option neither fixes nor
validates the NVIDIA fork's open startup issue.

Fresh profiles selections produce a native OCI image through the existing
rootless BuildKit lane and pinned NVKE profiles Dockerfile. A named build context
selects the existing exact Alpine image digest instead of the Dockerfile's tag;
the additive source overlay changes no upstream NVKE files. ORAS transports the
observed image to the local registry without changing its manifest. The consumer
uses the chart's standard image init container and read-only shared volume,
checking the exact image and successful init exit instead of injecting files
into a Kind host directory. Local-only APT installation and uplink naming are
explicit profile choices, not a general NVKE offline boot contract.
[Profile factory tests](../../pkg/nvkr/factory/nvke_laptop_profiles_test.go)
cover additive patch authentication, native-image compilation, exact staging
bytes and modeled APT/uplink cases. [Consumer tests](../../internal/nvkedemo/site_test.go)
reject substituted images, incomplete/failed init states and alternate volumes.
The opt-in exact pinned source-chart rendering check and the following native
exercises were reported locally; their private attempt paths are not a
retrievable external acceptance record. An isolated
native ARM64 rootless BuildKit test produced manifest
`sha256:197fe058d0fbcff5d7841abfee58067aa05f7cdd21040642fe7e327096b574a0`.
ORAS transport/readback, manifest retrieval from the Kind network and the image's
init copy command passed with exact staged profile/runbook comparison. This is
not by itself a completed NVKR profiles Run or Kubernetes init-container observation.
The retained 29-output attempt above subsequently observed `controller-profiles`
as `kind: oci` with that exact manifest digest. Its site handoff and the actual
`materialize-profiles` init container retained the matching image ID, `/profiles`
path and pod UID; the init container exited zero with reason `Completed`.
This exercises the changed producer/consumer, but its tenant readiness failure
still leaves full E2E acceptance unverified.

E2E now admits cached ARM64 Kind and registry images before installer acquisition
or artifact builds. Deployment authenticates its owned registry's direct IPv4
address on the admitted Kind network and probes `/v2/` from the actual management
node before uploads. Scoped proxy bypass applies only to that private endpoint.
Registry admission measures deduplicated image bytes, the largest serial upload
and 64 MiB headroom against the same 2 GiB ceiling used by its ephemeral tmpfs.
Tests cover the measured 1,122,458,894-byte closure, the exact ceiling and one
byte over, blob-count bounds and the launch argument. This is a capacity
correction, not fresh deployment or workload/DNS acceptance.
Contract tests cover ownership, network/address substitutions and probe failures;
the isolated native registry test exercised the direct route, but does not prove
deployment across all supported host configurations. Network progress now groups
private commands into preflight/setup/cleanup; expected absence probes no longer
emit misleading failures.

The local network owner now admits a single temporary Kind-to-simulator INPUT
rule, scoped to the admitted Kind subnet, its exact gateway and TCP port 18089.
Its version-2 journal records that optional gateway and the exact owner-commented
rule before insertion; existing version-2 journals without this grant remain
recoverable. Site admission probes health from the owned Kind node using its real
proxy environment before installing the site. Host-side health alone cannot
admit this route. Controlled tests cover bounded response checks, cancellation,
private diagnostics, rule substitutions and recovery. Hosts with an INPUT-drop
policy require this scoped route even when host-side health succeeds. A separate
nftables/firewalld input-hook drop can still reject it; the owned-node probe,
not an iptables accept alone, decides admission. The demo does not override that
administrator policy; see the [operator firewall guidance](../local-nvke-demo.md).
Live acceptance of the new route remains unexercised.

## ADR-17 CLI presentation and inspection

Of the 25 public command leaves, 23 expose typed terminal result browsers; local Run
execution additionally supplies bounded operation/output progress. The separate `run check-artifacts`
and `run qualify` commands retain canonical machine-readable diagnostics. The hidden native worker is
not a public command. Owners retain
execution, record, artifact and verification authority. Lifecycle alone finalizes
live telemetry from its returned Run/error; complete output retention and a coded
cleanup failure remain distinct. Safe panes exclude raw diagnostic and private
transport/path data. Command results are detached descriptions within progress;
the SDK inspection DTO has no terminal snapshot method.

The authenticity verification result browser projects the owning consumer's
detached description and labels payload integrity as unchecked. It neither
invokes full-payload verification nor promotes authenticated metadata to payload
verification. Controlled CLI contracts cover scope selection, admission before
owner work, safe projection, owning error codes and unchanged plain JSON bytes.

Non-interactive and explicitly plain output retain existing bytes and owning
failure/diff codes. Interactive `run local` defaults to auto and captures a shared
128 KiB memory tail instead of streaming successful producer output. Nonempty
`CI` keeps auto plain; explicit terminal mode is available. Failed sessions restore
and join before printing the safe summary and private tail. Recovered panic
stacks use the same bounded private sink. No durable general producer log is
created; Packer metadata retention remains separate. Temporary SIGPIPE handling
for checked reporters must preserve child dispositions and restore the parent.

Recorded inspection requires a canonical record bound to its directory's attempt
name and currently producible lane. Every path component is no-follow. Missing
records, including marker-only directories, fail closed rather than asserting
interruption or excluding a live owner. Inspection opens no payloads and restores
no execution authority. q confirms closure/cancellation; Ctrl+C closes static
views with the owner's outcome and cancels live work with stop/join. Command
browsers offer only supplied Changed/Failed filters; Plan/recorded-Run views use
All/Active/Failed. The [lifecycle guide](../lifecycle.md#choose-local-run-presentation)
owns navigation, mode selection and screenshots.

Controlled contract tests cover exact output bytes, required membership,
coalescing, cleanup outcomes, inspection substitution, no-follow paths,
responsive navigation and terminal failure/cancellation. Opt-in real TTY
restoration and SIGHUP fixtures exist; their invocation and env gates are in
[Development](../../DEVELOPMENT.md#opt-in-terminal-presentation-checks).
Earlier Darwin/Linux working-tree captures are not fresh execution evidence for
this revision. A live NVKE producer Run, Linux race acceptance and human usability
validation remain unexercised. Simulation screenshots establish presentation only.

## Local ARM64 demo presentation

The demo CLI automatically selects a terminal dashboard for interactive standard
input/output. Its phase map, observed state and bounded public activity use typed
owner events, not log-derived authority; narrow terminals stack the panes. `--plain`,
pipes and redirected output retain scrolling progress; `--verbose` builds use plain
public-log streaming. Tab changes focus, PgUp/PgDn scroll activity and End returns
to live activity. Tenant stage, count and closed condition changes add activity
immediately; unchanged reported waits add a heartbeat at most once per 30 seconds.
These reports do not prove successful API polls or guest progress. The tenant pane
now prioritizes its blocker, four observed checkpoints, role-bound VM/console
metadata, node counts and independently aged observations. Bounded polling joins
the exact tenant Machine/NicoMachine pair to an owned libvirt domain before
describing its state or console-file size. Only positive growth of the same
file identity counts as activity; baseline, replacement and truncation do not. A closed
role-bound `InstanceCreateFailed` report remains visible before a domain exists.
Unknown bindings, states and reasons cannot advance readiness. No private console
text, instance identifiers or provider messages are projected into public output.
Controlled observer and simulation tests cover binding substitutions, unsafe
files, unavailable observations, cancellation, privacy and 80×24 layout; this
observer has not yet been exercised in a fresh live E2E. Fixed Packer summaries
expose selected boot, transfer, provisioning and post-processing reports without
copying variable paths or addresses. An opted-in, attempt-bound read-only observer
now samples bounded OS build-log metadata into a separate private descriptor
channel. The dashboard accepts it only for the exact reported running build
operation and distinguishes sample age, aggregate bytes and same-file growth
from execution health. Baselines, replacements, truncation and rejected entries
do not establish activity. Names, console text and paths remain private; this is
not a live console viewer, progress receipt or ETA. Controlled tests cover bounded
frames, pipe backpressure, descriptor isolation, collector stop/join, unsafe file
entries and compact layouts. An unjoined stop or attachment fails the build;
bounded diagnostic collection is still attempted. Retention keeps the exact
observer, its work volume, identity workspace and runtime executable/configuration.
Cleanup still attempts independent containers and the input volume and closes
completed staging descriptors. A joined observer's failed removal is retried by
the final reaper; persistent cleanup failure rejects any collected artifact.
Controlled native Linux race tests exercise these cases, not termination of
kernel-blocked runtime launches or successful cleanup under every daemon failure.
Fresh live build acceptance remains unexercised.
While the dashboard owns input, q and Ctrl+C ask for confirmation;
only y/Y requests cancellation. Escape closes details or dismisses confirmation,
never canceling work. Plain/verbose output and the initial sudo prompt retain
signal-driven cancellation without a dialog. Normal cancellation waits for owned
work and cleanup before terminal restoration. During cancellation, f then y
restores the terminal and exits 130 without joining cleanup; resources may remain
and no success is claimed. The exit warning includes the configured workspace
and actual executable as recovery guidance only; it does not prove admission,
journal existence or producer ownership. Recovery requires the original journal
and stopped owned producers and restores only recorded network controls. Renderer
panic, output/input failure and a closed event stream restore the terminal before
waiting for cleanup, while preserving the mandatory owner join and failure.
Normal confirmed cancellation retains its cleanup screen.
Plain guest summaries suppress unchanged
snapshots and independently advancing ages.
Failed deployment stops unfinished active panes without
claiming that their individual checks failed. [CLI contracts](../../cmd/nvke-demo/dashboard_test.go)
and [simulation-screen tests](../../cmd/nvke-demo/dashboard_view_test.go) exercise
layout, bounded/sanitized snapshots, concurrent reports, cancellation joining and
terminal failures. These are controlled presentation/race contracts, not a completed
fresh E2E or live interruption acceptance for this candidate.

The interactive Linux CLI authenticates before opening the
dashboard or starting work, then refresh its existing sudo session using bounded
non-interactive checks while that workflow runs. Deployment and independently
bounded cleanup must not open password prompts. Redirected commands retain the
cache-independent unattended check. No password is handled by the Go reader or
diagnostic writer, no sudo policy is modified, and privileged operations are not
replayed after failure. The initial prompt names its deadline and hidden password
input. Failure diagnostics distinguish deadline expiry, cancellation and other
authentication failures without asserting that a password was submitted or
accepted. Controlled tests and targeted race checks cover authentication before
work, periodic refresh failure, cancellation and refresher joining, including
preservation of unjoined-child errors. Isolated Linux ARM64 PTY tests exercised
foreground ownership/restoration, dashboard checks without suspension and actual
same-terminal sudo validation/refresh. Validation/refresh wiring passed on a non-interactive
sudo-policy host; it does not prove timestamp-cache use, a real password policy, hour-long credential
retention or a complete fresh deployment with this change.
Interactive refresh uses ordinary sudo timestamps rather than a helper-scoped
credential: policy may allow other terminal-session processes to reuse it for
their full sudoers command scope. The host policy is unchanged; this route
requires a trusted execution host, not an untrusted launcher sandbox.

The downstream tenant smoke now waits for the pinned CoreDNS Deployment and
node-local DNS DaemonSet rollouts, within four minutes and the remaining tenant
deadline. Node Ready alone does not establish DNS rollout readiness. The
unchanged zero-retry workload still has to prove real cluster DNS and ARM64
execution. The explicit Kind/Calico diagnostic additionally requires the tenant
Calico node DaemonSet rollout before those DNS checks, within four minutes and
the remaining tenant deadline. A deployment-only replay observed both nodes
Ready and DNS rollouts passing while Calico was still unready; its unchanged
smoke then failed on DNS timeout. The added CNI admission is not proof that the
underlying datapath is fixed. Controlled failure/cancellation tests cover these
gates. An owned-subprocess fixture exercises the actual tenant orchestration:
CNI/DNS rollout failures and canceled waits prevent the single smoke submission.
Kubeconfig, Ready-node, CNI and DNS waits retain their named stage and private
diagnostic location; cancellation or a tenant deadline retains the timeout code
at the public demo boundary. These fixtures do not start real guests or establish
functional DNS. An author-reported local
continuous invocation at `bda67f0` subsequently
observed both ARM64 tenant nodes Ready, both DNS rollouts and the unchanged
workload/DNS smoke passing, but exited 17 at `tenant-deletion-wait`. Its original
recording remains failed, not a successful E2E video. The downstream three-minute
deletion wait is shorter than the selected NKE teardown's worker/control-plane
waits; the last actual teardown phase was not retained, so a timeout-budget
mismatch does not by itself establish the runtime cause. Complete fresh E2E
acceptance, including successful NKE deletion and owned cleanup, remains open.
Cleanup now best-effort retains three bounded private command-output slots and
the final command status in one exclusive private observation file. A canceled
empty poll preserves the last useful inventory. Controlled tests show that
storage failure neither prevents deletion nor changes its result, and no raw
cleanup response reaches public progress. Tenant cleanup now has an explicit
one-hour ceiling with a 45-minute disappearance wait, six-minute diagnostics,
two-minute delete request and two-minute guest-route withdrawal. The subsequent
owned host-runtime cleanup retains its independent eight-minute bound. This finite
downstream policy gives room for the pinned worker-then-control-plane teardown;
it is not an upstream maximum because retries and other activities can extend it.
Controlled deadline tests cover timeout/cancellation and exact ownership. The
later deployment-only replay naturally completed NKE deletion and owned cleanup
with the longer budgets, while preserving its failed smoke result. This does
not establish a fresh successful E2E; that acceptance remains unproven.

[Ordinary adapter tests](../../internal/nvkedemo) cover controlled ownership,
journal, cleanup, artifact and tenant-description cases. Runner tests exercise
bounded subprocess cancellation and joining; CLI tests exercise output-failure
cancellation with controlled callbacks. The live cluster projection now crosses
the concise CLI filter as an owner-generated public result rather than subprocess
log text. Composed `run`/`e2e` and dashboard tests cover immediate tables,
activity eviction, historical post-terminal replay and later cleanup failure.
Retained-owner progress carries the same owner-generated table through its
existing authenticated private control as a single typed result, with unchanged
32 KiB queue/envelope and sequence-gap refusal. Mixed payloads and oversized
results fail before presentation. JSON-relay contracts and native retained
launch fixtures cover typed/plain output and cursor/error behavior; their
synthetic cluster answers are not fresh retained NVKE runtime acceptance.
Synthetic Kubernetes answers establish presentation wiring only, not fresh NVKE
cluster acceptance. DNS/DHCP port admission now invokes a
fixed Go helper through the existing bounded sudo runner before recording host
intent; executable admission precedes artifact inspection. Controlled tests
cover fixed targets, rejected helper arguments, failures before journaling and
child error propagation. This does not prove a fresh tenant deployment.
Set-ID and replaceable-ancestor refusal run in the ordinary helper contracts.
Foreign executable ownership requires the isolated Linux root fixture; the real
TCP/UDP 53 and UDP 67 collision rows additionally require native ARM64 root.
Ordinary hosted tests do not establish those privileged socket observations.
These tests do not prove real SIGINT during
NVKE stages, privileged sudo/libvirt failure, runner/VM restart, or cancellation
at every build/deployment phase. Network-only recovery uses version-2 journals
and never kills serialized PIDs; restore legacy live controls before upgrading.
A full tenant crash/restart and recovery matrix remains unverified.

[TestEndToEnd](../../internal/nvkedemo/e2e_test.go) is explicitly opt-in; ordinary
tests do not build images, deploy tenants or mutate host networking. No durable
fresh-build/deploy acceptance record is supplied by this PR. Private logs,
retained Runs, chart renders and tool probes are not public operational evidence,
complete OS smoke, full-source completion, signing or qualification. Validate
the terminal result and cleanup of the exact invocation being accepted.

Attempt-scoped native toolchain substitution does not update maintained pins.
`publish chart` locally verifies/extracts a pinned Helm archive; diagnostic
registry copies do not constitute NVKR publication or Release authorization
(ADR-18/20). The current NVKE `bootstrap site` route is not a demonstrated
drop-in replacement for the Docker/Kind and NICoSim adapter.

## Experimental OS source producers and OVN outputs

The separate `definitions/nvke-os-source` catalog builds public OS binaries from
pinned Git sources. Its read-only `nvke-os-source-build.yaml` workflow exercises
`pipeline.Local` diagnostics on generic native AMD64/ARM64 hosted runners without
internal-source credentials. This is not full package-folder migration, OS-image
integration or a generated Run/Release. Helm's held `ExecNode` preparation remains
blocked by pristine external fixture links; local diagnostic preparation does not
admit that production route.

[Hosted run 37829955849](https://github.com/dsx-ai-factory/nvkr/actions/runs/37829955849)
passed all public-source producer builds on native AMD64 and ARM64 at
`335ace1e75829c061eac92caf42d8da0bc2fbd80`. It exercises unqualified
`pipeline.Local` execution, not OVN, full QCOW2 builds, production publication
or the held `ExecNode` preparation route.

The separate `definitions/nvke-ovn-source` Definition selects native image and
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
into OS material composition remains absent ([issue #116](https://github.com/dsx-ai-factory/nvkr/issues/116)).
Rebuilding equal pins does not establish same-byte correspondence; opt-in producer
and Helm OCI interoperability tests do not establish hosted or release acceptance.

## Output-local publication and metadata assembly

The GitHub `per-output-v1` mode retains full payload/producer/evidence checks for each exact named output.
Each output publishes and verifies its artifact and signed metadata root. Aggregate assembly authenticates
those roots, production predicates and SBOM bundles against the complete Plan, same workflow run and frozen
requirements. Aggregate signing/verification and finalization use only bounded metadata/evidence, with no
payload-layer download. Aggregate image outputs retain the exact index and native manifest/config membership
in this metadata closure, including platform-scoped evidence. Output-local publication alone owns discovery
image tags; unrelated outputs and the metadata-only release root receive no payload aliases. The independent
Release consumer's artifact-publication scope still checks complete payloads.

ADR-20 admits earlier-attempt output roots only within the same GitHub run and unchanged Plan, with exact
repository/builder and preparation/signing/consuming-attempt ordering. Contracts cover retries, mixed
attempts, re-signing and finalizer reuse; hosted retry acceptance remains unexercised. Cross-run reuse,
remote rollback and an atomic multi-repository transaction are not provided. Failed/missing provider jobs
still prevent completion. Production finalization restores the six aggregate/tool control carriers plus each output’s
original publication reference, signing record and predicate for the qualification handoff: 117 control
carriers for the 37-output candidate, without restoring output payload packages. Development finalization
retains only the six aggregate/tool controls because it emits no qualification handoff. Assembly/finalization need
registry read access, not OIDC or package-write
permission. Production rejects arbitrary static credentials; the explicit development mode uses its admitted
scoped read/write credentials. Local execution and dry-run signing reject this mode.

The root reader rejects extra blobs and bounds control/evidence to 512 MiB per root. Full and metadata-only
readers share exact schema/media-type checks. `release browse` projects checked metadata and evidence,
config-declared platforms and derived sibling locators, without reading payload layers or verifying policy.
Filtering follows complete metadata validation; detached views establish no registry-availability claim.
Artifact views also expose config-derived OS/architecture and Helm chart version (not appVersion), with
native indexes retaining child platforms. Full Helm verification compares the config with Chart.yaml.
Generic release export and JSON browsing share the explicit `nvkr.dev/release-description/v1` schema.
Export requires only the immutable publication reference and preserves every named output, native platform,
aggregate image-index digest/child membership and exact evidence/source binding. Source repository/commit
descriptions come from output-bound source SBOM properties, without inferred Git tags or a release-wide
source pin. Different revisions of one repository remain valid descriptive records. The inspection scope and
individual checks performed are separate from publisher-declared qualification. No NVKE role dictionary,
installer schema, separate Plan/mapping or unpublished workspace path remains. Consumer-owned code selects
roles, compatible revisions, platforms and complete installation dependencies. The
[release navigation reference](../release-navigation.md) owns the versioned contract. Targeted lifecycle,
CLI, delivery and description tests cover complete membership, byte-identical unfiltered export/browse
JSON, source binding, explicit inspection scope and admission. Before the namespace cutover, on 2026-10-10,
the pinned Go 1.27.2 local completion gate passed with uncached tests, vet, lint, license, shell/Python checks
and vulnerability scanning; the full uncached race suite also passed on macOS. Changed published documentation passed MDX and local-link
checks; Fern site compilation was not run because Fern was unavailable.
That pre-cutover validation does not validate the renamed reader. Those contracts do not establish a
downstream consumer's acceptance of this wire schema. No complete offline installation or authenticated
Release acceptance is established by export.

On 2026-10-10, bounded inspection of retained metadata from
[Run 37736529644](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37736529644) reproduced 34 outputs
and five Helm chart versions: controller 0.1.1, PostgreSQL 18.5.15, SPIRE 0.30.1, SPIRE CRDs 0.6.1 and
Temporal 0.74.0. The publication reference is
`ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases@sha256:2fef87f426734a4427f7694aaf5ac181ba11352ea37522349bbb9cda7d84dc42`.
This was metadata inspection only; an earlier independent full-payload attempt authenticated the root and
then failed downloading the ARM64 disk with an HTTP/2 `PROTOCOL_ERROR`. There is no successful full-release
verification result from that attempt. The controller uses `33fb71b2f4224b470ebc628a9165fc4b92870406` and the
CLI uses `ed9fb09b04fd40bf331d25df2222206ffaca593e` from the same NKE repository; matching ARM64 controller/CLI
artifacts are absent. These are inventory facts. A downstream installer decides whether those revisions and
platforms satisfy its requirements; generic export neither reconciles them nor requires an unpublished Plan.
The generic CLI export was exercised against that exact retained metadata cache on 2026-10-10. It emitted
`nkd.dev/release-description/v1` with all 34 outputs, 60 evidence entries and 23 source records: 16 native
AMD64 outputs, 13 native ARM64 outputs and five charts without platform labels. It preserved both NKE
revisions above and five distinct repository-spelling/commit pairs. This was a local-cache metadata export,
not a fresh registry read, payload or signature verification, or installer-compatibility check. These are
historical NKD namespace records: current NVKR readers reject their old schemas and digest families. The
record does not establish current-schema external acceptance or authorize converting signed metadata.
Separate direct Docker builds of the pinned profiles carrier passed the chart's `/bin/sh` and
`cp -a /profiles/. /profiles-out/` operation with container networking disabled on ARM64 and emulated
AMD64, plus the ARM64 local diagnostic profile. These checks establish the runnable carrier only, not
NKD wrapped execution, publication, native AMD64 runtime, fleet profile validity or offline installation.
[Registry browsing of the recorded candidate](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md#metadata-browsing-validation)
retains the external scope and binary identity.

`release pull` selects one exact named output after complete bounded metadata/evidence validation, downloads
only its complete payload closure, and checks format-specific integrity before exposing a new destination
directory. Executables preserve recorded files and modes; disks and Helm outputs preserve exact payload bytes,
including declared LUKS2 disk ciphertext without decryption. OCI layouts preserve native OCI/Docker schema-2
bytes and every declared aggregate platform; APT outputs expose
the checked repository tree of regular files and owner-readable, searchable directories, with every file and
index digest-bound to the downloaded archive. Links and special members are rejected. Lifecycle and delivery
contracts cover these formats, exact selection, malformed or unsupported format metadata, unrelated-payload
exclusion, integrity failures, existing-destination refusal and temporary cleanup. This is diagnostic byte
integrity on macOS and Linux, without signature or policy verification, installation or execution. These
contracts do not establish GHCR interoperability, signing-service behavior,
runtime compatibility, installation or external acceptance of the new format projections; earlier browsing
and publication records do not establish that acceptance.

The generator/validator/writer enforce a 500,000-byte workflow limit. The top-level workflow retains named
jobs, exact dependencies, runner and credential policy, native preflight and checkout. Manifest-owned local
composite actions expose fixed semantic interfaces for execution, input/image preparation, output collection
and carrier transfer. Grouped jobs reuse the node interface; declared optional slots and further individual
upload calls preserve output counts and order without cardinality-specific or numbered variant actions.
One canonical `action-data.json` member holds named operation parameters. The runtime descriptor admits the
Plan and recomputes the selected values; templates retain fixed commands and pinned actions without
accepting executable script data.
Operator setup and timed producer execution stay inline. Controlled upload/download wrappers contain one pinned transport step,
with conditions, timeouts and continue-on-error on their callers. Steps with referenced outcomes/outputs
remain inline, including finalization and the final Run upload used by the closing summary; `recover-inputs`
handles the preceding recovery. Static validation expands templates and invocation bindings for graph and credential checks and
compares every generated member against exact regeneration; the 21,000-byte script limit also covers action
bodies. Credential inputs map only to their
original consuming steps. This replaces numbered-anchor/inline-map compaction without changing carrier
membership or complete finalizer result accounting. Source provider IDs and scheduling order now derive
from target/consumer cohorts, while exact Plan/IR IDs remain unchanged. Static Plan/manifest path headers
replace the changing Plan digest. Ordinary source/artifact/toolchain pins and generator
updates can retain identical workflow bytes when topology, labels/interfaces and visible policy stay
unchanged. Transport-pin stability additionally requires no affected inline call; the summary requires an
inline final Run upload, so upload-action pin changes affect this workflow. Checkout pins, cohort splits/merges, runtime containers/services, runner/capability routes,
permissions, secret references, timeouts, qualification and inline setup remain visible change boundaries.
Complete bundle regeneration and drift checking remain mandatory. Complete native-preflight, pinned-checkout
and shared-preparation steps use descriptive aliases; preparation selects its job through `github.job`.
Matrix scheduling and reusable callable workflows remain deferred, and no fresh hosted execution of this
composite layout is recorded. Oversized workflows still fail before writes. Native offline disk preparation
shares the ARC group to avoid restoring its full material closure on publication runners. Consumed carrier deletion reduces duplicate
storage but does not establish a measured hosted storage bound.

## Hosted artifact transport lifetime

The FDE build jobs in [Run 38480478189](https://github.com/dsx-ai-factory/nvkr/actions/runs/38480478189)
succeeded, but the later hosted SBOM-attestation jobs failed restoring their prepared layouts: the
[AMD64 job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38480478189/job/115601071511)
exhausted disk while snapshotting its carrier, and the ARM64 job failed during extraction. This records a
storage failure, not successful FDE publication.

The local transport implementation now streams extraction while hashing the held carrier, then rehashes
that same inode before publishing extracted roots. It retains the incoming carrier and private extracted
tree without an additional full tar snapshot. Delivery's attestation handoff consumes the disposable preparation:
signed evidence and metadata are prepared first, unchanged reachable blobs move into the final stage, and
the final closure is verified before no-replace publication. It preserves the preparation on pre-claim
failure; after claiming it, a failed operation requires restore/build retry. Retained preparation paths and
final payloads do not share hardlinks. Exact artifact subjects, evidence membership and hosted credential
boundaries are unchanged.

`TestUnpackRejectsTransientSourceMutation` and `TestUnpackChecksCompleteStreamBeforePublication` in
`internal/transportarchive/snapshot_test.go` cover a transient payload or trailer mutation restored before
the final rehash, complete plain/gzip stream validation, and cleanup after growth, truncation or cancellation.
`pkg/nvkr/delivery/attestation_move_test.go` covers the consuming handoff:
`TestAttestPreparedEvidenceTransfersDiskWithoutCopy` checks that the final disk retains its original inode
on both native-platform fixtures, `TestAttestPreparedEvidenceRejectsBeforeClaim` checks admission without
signing or consumption, and `TestAttestPreparedEvidenceFailureLifetime` checks coded failures, exact document
binding, source substitution, cancellation on both sides of the claim and destination collision. Its
same-metadata payload mutation case keeps inode, length and modification time unchanged and still fails
the final byte check after ownership transfer.
`TestAttestedLayoutCommitSyncFailureRollsBack` exercises a returned parent-sync failure. These are local
contract tests with synthetic artifacts and signer callbacks; they do not exercise hosted signing or prove
a complete crash/recovery matrix.

Local validation used Go 1.27.2. After rebasing onto `39c636d0046ddceaa6f4f5677a84db78558eeddd`, the complete
Linux repository suite (`go test ./... -count=1`) passed. The container included an init process for child
reaping, read-only worktree Git metadata and a writable `.nvkr` scratch mount; initial container runs lacked
required fixture support. Post-rebase workflow drift checks, vet, Linux-target lint, license, format,
shell/Python, vulnerability, build and diff checks passed, with no generated-file changes. Before rebasing,
targeted Linux race checks covered transport, attestation ownership and factory carrier operations, and
workflow regeneration was byte-identical. The earlier uncached macOS repository suite reached the ten-minute
package timeout in delivery, factory and lifecycle; all other packages passed. Post-rebase native macOS
`make check` still stops at the two existing Linux-only `smokeWorkerFiles` helper methods reported as unused.
These limits prevent a claim that the complete native macOS gate passed.

The expected local full-payload peaks are carrier plus extraction during restore, and final layout plus
outgoing carrier during packing. These lifetimes do not measure provider ZIP handling, workspace baseline,
or verification's later registry-fetched disk snapshot. No hosted rerun has exercised this implementation,
and it establishes neither a measured hosted storage envelope nor independent FDE consumption.

## Native ARC disk route
The single checked-in `nvkr-run.yml` selects the 37-output M2 Definition and its main-only publication
Environment, including plain and public-bootstrap FDE disks on both native architectures. The Makefile drift
gate repeats the exact Definition and Environment paths recorded in the committed Plan. Dispatch does not
choose among Definitions. Alternate selections can be generated into a scratch root with their matching
byte-exact check. The standalone `codex/fde-image-pipeline` branch used the separate one-output FDE Definition
and build-only Environment for the recorded diagnostic below. Neither candidate selection is a ratified
canonical-product/path migration or publication-namespace decision; that named-decider governance remains
pending. On October 13, 2026, the
`nkd-arc` Environment admitted the exact FDE branch through deployment policy `62934956`, preserving its
existing `main` and M2 validation-branch policies. That provider configuration is not execution evidence.

The build-only finalizer uses the selected native ARC pool and matching helper with a two-hour bound,
restoring and rehashing the complete raw-disk closure without publication authority. Comparable AMD64
material, source and disk-package carriers in [Run 37533806963](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963)
totaled 15,362,463,697 compressed bytes. This prior measurement motivated the runner choice. The FDE
attempt below completed on ARC; peak scratch usage was not measured, so it establishes no general
capacity bound.

On October 13, 2026, [Run 38443178035](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035)
completed all ten generated jobs on `codex/fde-image-pipeline` at commit
`68c219845398b8ef5890e5c76a13d4901a22630d`, attempt `38443178035-1`.
The [native job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/job/115457416653)
ran for 43m 07s, including setup, a 29m 48s grouped build step and all uploads.
Its log records successful Packer target `nke-node-fde.qemu.nke-fde`.
The [finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/job/115483887874)
completed in 6m 22s on a fresh native runner, restoring the complete closure and checking its exact bytes.
The retained Run has `status: complete`, `lane: static-unqualified`, one expected and observed
`nvke-node-fde-amd64` disk output, its eleven-component closure and no failure:

- Plan Digest: `sha256:3f5db9dda12b12accfe2ba10567b5d1b00e99538c469ee2d32ad631e5a91dba7`.
- Output Build Digest: `sha256:b5c4dbb201dbac6e8f5498d67a2bba297f2ee4824766b0b6cf59f3d21a7e19b8`.
- Raw QCOW2 payload: `sha256:c3d6b6694f1b0c626b1df0f61b10a60a5f2bd9fac9d0c37ff2407a0b7b85cede`,
  6,738,411,520 bytes (6.28 GiB).
- Run content subject: `sha256:5c2d10edb27c52e7ae99b8d0def2566762ae11a1e5a07db5feb708bb552dd75a`.

The [image artifact](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11840209683),
[producer result](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11841075777),
[terminal observation](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11840159679)
and [final Run record](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11841676055)
retain the evidence. The payload digest and size agree across the producer and finalized Run; they are
the raw QCOW2 identity, not the Actions ZIP identity. This proves the pinned FDE-target bake and generated
build-only workflow for this attempt. The recorded `luks2` declaration is not independent guest inspection,
boot or TPM-enrollment validation, deployed-clone confidentiality, reproducibility, qualification or
publication evidence. The pinned-source audit's key-material and firstboot limitations still apply.

ADR-18 now permits the explicit `githubARC: native-kvm-v1` operator-trusted policy for the two native KVM pools.
The grouped helper retains original material and disk owners through reconciliation and packaging; generation
contracts cover both architectures, individual-node bypass rejection and unsupported policy rejection. For
publication workflows, source fetch, bootstrap and release credentials remain outside ARC jobs. Native preparation/measurement is provided
separately. M2 now generates all 37 named outputs with four native disk jobs from the same Definition,
an exact catalog overlay and measured native inspection profiles. Four toolchain images were
published to internal GHCR packages under the user's scoped candidate exception, with exact
manifest/config read-back. This is not completed organizational license/security review.
Private pulls use a separate packages:read step whose temporary credentials are removed before
execution; cached exact image identities are revalidated by the held runtime.
Both native plain-image builds and terminal checks completed in run `37445475840`; both deep disk inspections timed out.
The user earlier moved deep disk/content inspection and dependent reconciliation out of M2. The narrower candidate
`diskEvidence: build-provenance-v1` selects live producer/material evidence with signed uninspected scope,
normal packaging/signing/publication and unchanged subject/provenance/membership/completeness gates.
The [inspection deferral record](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-inspection-deferral.md) records the scope and validation.
The plain-image strict inspection path remains available. M2 retains its AMD64-only product acceptance floor and accepts
producer provenance with its remaining delivery/consumer gates. The approved October 12 M3 MVP scope also
retains producer-only disk evidence and defers inspection/material reconciliation to hardening. This task
scope supersedes the earlier M3 assignment without changing any historical signed bytes or establishing
organizational ADR ratification. The native ARM64 route and this candidate Definition's
additional ARM64 outputs are an implementation extension. All four native disk jobs and every declared output
remain required for this candidate's complete Run and Release; the product floor waives none of those requirements.
Organizational ADR ratification remains separate.
Both candidate disk jobs succeeded in run `37486058412`, but the aggregate release download failed with ENOSPC
before publication. The [dated carrier evidence](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-release-carrier-transport.md) records
that failed attempt. The subsequent validation-branch run `37533806963` at commit `625980e` passed
all candidate outputs and aggregate publication, signing, verification and finalization. That historical run
predates the consumer-policy and timeout changes here; it is not evidence for this updated head, current main
execution, strict inspection or independent full-payload consumer acceptance. Per-output storage peaks remain unmeasured.
The [October 6 preparation record](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-native-arc.md) retains final-source
ARM64 and AMD64 Packer/inspector assembly, executable measurements, native tool validation
and unprivileged libguestfs appliance filesystem probes. The earlier preparation Environment admitted
`codex/m2-arc-validation` through `githubDispatchBranches` and its signer identity.
The October 7 PR #97 test temporarily authored `codex/m2-native-arc-foundation` as an exact dispatch/signing
exception. [Run 37661490433](https://github.com/dsx-ai-factory/nvkr/actions/runs/37661490433)
at `562a609` completed with conclusion `cancelled`. Provider annotations rejected the exact branch under
`nkd-release` (sample attestation job `112931037919`, finalizer `112933977061`) and `nkd-arc`
(native ARM64 job `112932239664`); the failed jobs had no executed steps. This is a deployment-policy refusal,
not disk-build, signing or release acceptance evidence. The temporary authored exception was removed after
that cancelled test; dispatch and signer policy select `main` only again.
The external policy read-back retained `codex/m2-arc-validation` and `main` for `nkd-arc`, and
`codex/m2-arc-validation`, `feat/nke-deploy-pipeline` and `main` for `nkd-release`; it did not admit the exact
PR #97 branch. Removing an authored exception neither changes those provider policies nor bypasses them.
The existing provider secret is `NKE_SOURCE_TOKEN`; the rename retains that exact
credential reference, not an unprovisioned `NVKE_SOURCE_TOKEN`. Package Actions read access, native pool capacity and successful exact-head
execution remain external acceptance requirements.

## Framework and product naming cutover

The NVKR namespace migration renames authored inputs, Go packages, public commands,
schema/media types and structured identity families. These contracts do not convert
historical signed bytes or imply external deployment acceptance. Generated records
must be regenerated from the renamed authored inputs.

The current hosted path retains the existing provider-owned `nkd-arc` and
`nkd-release` protected environments and `NKE_SOURCE_TOKEN`. CODEOWNERS uses the
provider-renamed `nvke-write` team with verified NVKR repository write access;
the old `nke-write` slug no longer exists. Published Packer/inspector subjects
stay at their exact
`ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/` addresses with the
existing digests. Their plugin path, inspector executable and report properties
`nkd:diskContentClass`, `nkd:packageFormat`, `nkd:packageArchitecture` and
`nkd:containedIn` remain the pinned in-image ABI. Host-generated evidence metadata
continues to use `nvkr:*`. The external package-capture envelope remains
`nkd.dev/package-acquisitions/v1`, emitted by the selected NVKE producer; internal
NVKR evidence identities remain distinct. Go network recovery preserves its complete
version-2 durable host namespace and shared coordination lock. These are explicit
external/durable contract exceptions, not public namespace aliases.
Retained node, package, Kubernetes and Packer names are compatibility contracts,
not missing rebranding work. Source repinning or an independently changed ABI
requires its own validation and is not part of this naming cutover.

Private toolchain credential preparation includes one narrowly scoped naming-cutover
exception: the maintained `ghcr.io/dsx-ai-factory/nvkr/m2-candidate` publication
prefix can pull digest-pinned images from the existing `nke-recipes/m2-candidate/toolchains/`
namespace. It does not grant a credential to arbitrary images or other publication
prefixes. Remove that compatibility exception only after all selected legacy
toolchain subjects have authenticated replacement pins under the normal publication
namespace and the regenerated Run passes its credential-boundary checks.

The standalone renamed execution helper has measured AMD64/ARM64 executable pins;
its image pins remain unpublished. Any independently chosen provider-name or
runtime-publication migration requires verified provisioning, permissions,
digest measurements and operational read-back; retaining existing contracts
does not require that migration. Contract tests do not establish an external
cutover, hosted publication, independent consumer acceptance or qualification.


## Maintained input and schema boundaries

The [M2 Definition instructions](../../definitions/nvke-m2/README.md) own example-specific selection and input
preparation. The candidate requires 37 named outputs: thirteen CLI/controller/chart/operator/disk outputs plus twelve
third-party runtime images per native architecture. The chosen runtime-image set follows pinned deployment
values, not chart defaults; chart-test images and NVKE images outside the disk retain explicit owner questions.
Native children are locked separately from source-owned index parents, and materialization authenticates actual
membership. Controlled source/pin/image tests and exact-source approval do not prove external layer acquisition,
scanner acceptance or complete production input approval. The
[acquisition observations](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#temporary-spire-source-selection) retain source
approval, mixed-source candidate and HTTP-403 limits without copying locks into this ledger.

Current Definition v5/catalog v3/IR v4/Plan and artifact-graph v5 reject retired recipe, cluster, category,
role, composition and staging fields, including empty legacy fields. Runs/Releases use v4 and validation vectors retain v3; publication roots and Inventory/ReleaseSet use v4; disk manifests use v2. Removed compiler-handoff,
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

On 2026-10-12, a local controlled Debian inspector API fixture run used the implementation at
[source commit `28443004ad73ac6c69df60e10e032f6ca0cb209c`](https://github.com/dsx-ai-factory/nvkr/blob/28443004ad73ac6c69df60e10e032f6ca0cb209c/pkg/nvkr/sbom/debian_dependencies_integration_test.go).
This is a local diagnostic observation, not an authenticated factory Run or approved toolchain. The native
Linux ARM64 test executable was built with Go 1.27.2. The container used
`ubuntu@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3`,
UID/GID `65534:65534`, `--read-only --network none --cap-drop ALL --security-opt no-new-privileges`,
a read-only tool/test mount, and `--tmpfs /tmp:rw,nosuid,nodev,exec,size=256m`.
Syft v1.51.1 ARM64 archive integrity matched the repository disk-inspector source pin
`sha256:a7fd2b784e6664acd44719270574f6cd8c6864fc2b1700bf9099bd1cccda7d7f`;
its measured executable was `sha256:617cef943234e0cae184b4901414ca4059ad6c6923a9c9decb46f49f6a2c6573`.
The runtime's `dpkg-deb` 1.22.6 executable was
`sha256:3cf93403af757e938bda51322774697b8ff1e1d030b333008b2308e0b79a8dd6`.
These native diagnostic measurements are separate from the factory's scanner pin.

| Controlled fixture | SHA-256 of archive bytes | Length |
|---|---|---|
| gzip | `ebf6ec1ed3830fe24ffdc9bd2339d8f427a5dc597ad4295224ac9da8586ef49a` | 612 bytes |
| xz | `ee9bc9e457951f3510c72a564ff09e31095ac187b10e3e935dc5380d8a6f2563` | 748 bytes |

Both fixtures yielded `pkg:deb/nvkr-debian-artifact-fixture@1.0.0?arch=arm64`. Pre-scan digest binding,
original-fixture size stability, and non-archive bytes under a `.deb` hint were checked. This does not
establish corrupt-payload rejection, complete embedded dependencies, source-created package delivery,
signing, publication or installation. This historical revision exercised the filename-selected branch of
`InspectDependencies`; it did not exercise the current separate `InspectDebianArchiveMetadata` API,
the later diagnostic method, same-byte no-hint control or dependency BOM rejection guards.
Native amd64 execution has not been recorded.

On 2026-10-12, a separate local native Linux ARM64 run exercised the exact implementation at
[source commit `8659cd28e5e615a8f490d0a302a419f329edc902`](https://github.com/dsx-ai-factory/nvkr/blob/8659cd28e5e615a8f490d0a302a419f329edc902/pkg/nvkr/sbom/debian_dependencies_integration_test.go).
The Go 1.27.2 test executable was compiled before subsequent API edits. This run used the same Ubuntu image,
Syft archive/executable and `dpkg-deb` executable identities, isolation flags and temporary-filesystem bound
listed above, plus a read-only repository mount at `/workspace` with working directory
`/workspace/pkg/nvkr/sbom` for catalog test fixtures. Both gzip/xz fixture digests, lengths and PURLs matched
the earlier observation exactly.

At this revision, filename-selected `InspectDependencies` returned `syft-debian-archive-metadata/v1`,
with component type `library`, for both controlled packages. The same archive bytes scanned without the hint
returned `CodeProvenanceMissing` and no accepted inventory. Actual fixture results were rejected by
`WithDependencies` with `CodeUnsupported`. The separate
[serialized BOM guard test](https://github.com/dsx-ai-factory/nvkr/blob/8659cd28e5e615a8f490d0a302a419f329edc902/pkg/nvkr/sbom/dependencies_test.go)
rejected an injected diagnostic method with the then-current `CodeProvenanceMissing`; that test used a
controlled component rather than the real scanner result. Scanner protocol, OCI isolation, filename-predicate
and snapshot-mutation contract tests also passed in that runtime. Non-archive bytes were rejected.
This observation does not exercise the subsequent separate `InspectDebianArchiveMetadata` API or establish
corrupt-payload rejection, complete payload dependencies, source-created package delivery, signing,
publication or installation. Native amd64 execution remains unrecorded.

Current contract tests separately assert rejection of `syft-debian-archive-metadata/v1` by dependency SBOM
attachment and serialized BOM validation. These tests do not retroactively strengthen the dated observation
above. The [opt-in regression](../../pkg/nvkr/sbom/debian_dependencies_integration_test.go) must be rerun when
changing either the factory catalog Syft pin or the Syft pin in `toolchains/nvkr-disk-inspect`.

## Remaining decisions

Named-decider ratification and organizational toolchain/source/identity approval remain separate from candidate
execution. Owners must supply registry retention, release-tag concurrency/collision and interrupted-publication
protocols, source access and protected-environment policy, native runner-pool isolation, transparency approval,
main-only execution and independent consumer evidence. The M3 MVP needs the separate qualification identity,
complete authenticated pass/fail verdicts, delivered-tool smoke and external signing/consumer evidence.
M4's manifest/tag, channel and revocation protocols remain separate deliverables; waivers are deferred
hardening outside the M3 MVP.

Qualified owners must supply authenticated runtime/provider observations, discovery/trusted time, measured
loader/runtime closure, independent signed evidence, exact reproduction and any newly admitted offline lifecycle.
Pinned-image helper transport cannot substitute for executable/runtime receipts. No missing stronger capability
falls back to a diagnostic result, a v1 signature or an earlier historical success.

## Consumer acceptance scopes

Current publication-root v4 binds the exact Definition, Environment and Plan identities. Optional consumer
expectations are checked before external work; the lifecycle owner authenticates the complete output match
and issues a policy/publication-bound opaque receipt. Current readers reject historical v3 roots, including
the Run 37533806963 demo release. That browsing record does not establish current-schema external acceptance.

Consumer Verification Policy binds `minimumDiskEvidence` into its typed digest. Pipeline-attested policy
defaults to `build-provenance-v1`; explicit `strict` requires inspected-disk evidence. Qualified policy
defaults to strict and rejects the producer-only minimum. Every disk in the authenticated closure must
meet that policy before a capability is issued. Synthetic publication contracts cover enforcement and
failed-policy cleanup; they do not establish production disk-release acceptance.

Consumer authenticity uses the same pinned reference and policy with a separate closeable
`AuthenticatedRelease` capability. It validates the complete metadata/evidence closure, authenticates the
root and every required named output's signature and subject-bound production provenance, and verifies
required SBOM bundles. It applies the disk-evidence minimum without downloading or reinspecting artifact
payloads. Its description reports `payloadIntegrityVerified: false`; it grants no payload export,
exact-input receipt, qualification or revocation acceptance. Contract fixtures cover rejected signatures,
missing or substituted evidence, whole-release membership, policy enforcement, capability separation and
zero payload-layer retrieval. These are controlled contracts, not a recorded external authenticity run.
