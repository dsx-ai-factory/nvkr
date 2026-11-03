## M3 MVP scope and acceptance boundary

The initial scope decision retains
producer-only disk evidence and defers disk inspection, waivers, offline-PR and verify-only qualification.
The accompanying implementation plan separates
artifact authentication, native smoke, evidence publication and acceptance demonstrations. These are task
scope and planning records, not organizational ADR ratification or operational qualification evidence.

The current scope makes embedded-component declarations optional, as recorded in the
product specification and owning ADRs. Omission is not applicable; explicitly authored declarations still
require strict consistency with their
referenced pinned upstream metadata. That check supplies no effective customized or installed composition claim.
Source, patch, configuration and material bindings remain required. Full inspection and material reconciliation
remain deferred hardening.

This ledger uses the following work-package labels. A label names a unit of work; it does not mean that work
is approved, complete or operationally accepted. Each entry states its own implementation status and the
acceptance that remains.

| Label | Scope | Ledger entry | Acceptance criteria |
|---|---|---|---|
| M3-A | Complete-publication artifact authentication | This section | [Epic M3](../../epics-v1-spec.md#epic-m3--qualification) |
| M3-B | Native delivered-tool smoke | This section | [Epic M3](../../epics-v1-spec.md#epic-m3--qualification) |
| M3-C | Qualification verdict and evidence publication | This section | [Epic M3](../../epics-v1-spec.md#epic-m3--qualification) |
| M3-D | Positive and negative external acceptance and independent review | This section | [Epic M3](../../epics-v1-spec.md#epic-m3--qualification) |
| M4-B | Connecting manifest issuance to the CLI and generated workflow | [M4 release manifest issuance](m4-release-manifest-issuance.md#m4-release-manifest-issuance) | [Epic M4](../../epics-v1-spec.md#epic-m4--automated-releases) |
| M4-C | Release manifest schema and issuance | [M4 release manifest issuance](m4-release-manifest-issuance.md#m4-release-manifest-issuance) | [Epic M4](../../epics-v1-spec.md#epic-m4--automated-releases) |
| M4-D | Tags, publisher exclusion, consumer verification instructions and interruption demonstrations | [M4 release manifest issuance](m4-release-manifest-issuance.md#m4-release-manifest-issuance) | [Epic M4](../../epics-v1-spec.md#epic-m4--automated-releases) |

M3-A authenticates the complete selected publication against
its producing Run, original attempt identities, commit and input identities, and returns subject-bound results without
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

Hosted artifact checks verify every output's complete payload sequentially, then release non-executable
payload scratch before the next output. Their narrower delivery owner retains checked controls, evidence and
exact executable bundle bytes; it cannot export discarded payloads. Ordinary full-publication export retains its
complete payload owner. The check receipt retains this verified owner and a frozen Plan; closing it revokes copies, cancels and
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

For production and development, the successful authenticated source attempt anchors producer completion. Root/output predicates and
publication origins may retain earlier attempts of that same workflow run under ADR-20's retry contract.
The same builder and exact Plan/input bindings remain mandatory. Output preparation precedes or equals
output signing, which precedes or equals aggregate assembly; aggregate assembly precedes or equals root
signing, which precedes or equals the selected source attempt. Each output predicate must match its original
signing invocation. Missing origins, other runs, future attempts, changed Plans and incomplete evidence fail
closed. A failed earlier attempt does not invalidate its independently authenticated successful outputs.
The gate preserves original evidence, verifies the complete artifact set again and never relabels production.

M3-B now has a Linux native delivered-tool smoke owner
and an authenticated complete-set receipt issuer. `lifecycle.RunNativeToolSmoke` selects exact held payloads
from A's retained publication and frozen Plan, derives expected versions/commits from pinned inputs, and runs
only fixed version profiles for nvkectl, Helm, kubectl, Kind, clusterctl and ORAS. The Docker owner verifies the pinned native image
and actual ELF platform, then executes held bytes as nonroot with no network, credentials, host mounts or
Docker socket, a read-only root, private temporary storage and bounded resources/output/lifetime. Closing A
cancels and joins dependent work. The smoke policy must cover every executable member; explicit OCI/APT
non-tool declarations are checked against exact output membership. Disk and Helm outputs retain explicit
not-applicable rules. Missing, ambiguous or unsupported applicability fails closed. Generation and runtime
share pure Plan/policy scope admission: exact profiles, expected versions/source stamps, OCI/APT membership
and selected native images are checked before rendering or bundle access. Contract tests include AMD64-only,
ARM64-only and both-platform scope, missing/extra membership and refusal before the first bundle lookup.

The fixed reusable worker signs a canonical native report with an independent worker identity. The report
binds complete A results, the full policy digest, its original qualifier attempt, native image/runtime identity,
and each output/member payload digest, size, expected and observed version/commit. The final owner selects
the latest job per native platform in the same qualifier run, no later than its current attempt, and requires
success. An earlier successful worker is reusable only with the same workflow/commit, exact A results and
policy; a later failed or unfinished worker prevents stale-success fallback. The owner downloads each selected
attempt's artifact and authenticates the same signing leaf's SAN/issuer, original caller workflow URI/commit
and Run-attempt URI. It retains original worker invocations while binding aggregate evidence to the current
qualifier invocation. It recomputes all expected members from retained A and requires
complete, disjoint native reports before issuing `lifecycle.ToolSmokeReceipt`. The receipt owns frozen
canonical evidence and shares revocation across copies; decoded documents and caller-provided passes cannot
issue it. Native, aggregate, provider, same-leaf invocation and mutation/cancellation contract tests pass under
Linux race detection. A real Docker ARM64 fixture executed all three fixed profiles against held fixture ELF
bytes with the intended isolation and clean environment. This establishes local runtime mechanics, not real
NVKE payload or hosted AMD64/ARM64 acceptance. Failure diagnostics cover every known native member,
preserving actual observations and distinguishing failed execution from checks never evaluated. Completed
member checks remain pass when later cleanup or revocation fails the report. Failure-only rows are rejected
by successful canonical report validation. Native diagnostic cases pass targeted Linux race tests.

M3-C now exposes Linux `run qualify --policy` and an
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
`inputs/policies/nvke-qualification.yaml`. Its generated pair is `.github/workflows/nvkr-qualify.yml` and
`.github/workflows/nvkr-smoke.yml`, with the separately owned qualification manifest. These inputs and workflows
are configuration for post-merge acceptance, not evidence of production readiness or organizational approval.
The user has accepted merging the reviewed implementation/configuration before a complete producer run with
its successful-attempt handoff and hosted/independent positive and negative acceptance runs. Authenticated
partial retries are permitted.

On October 18, [producer Run 39191524720, attempt 2](https://github.com/dsx-ai-factory/nvkr/actions/runs/39191524720/attempts/2)
completed successfully at `4487a965d25e24dfd745124487d6bb3915fb6780`.
[Qualification Run 39210357351, attempt 1](https://github.com/dsx-ai-factory/nvkr/actions/runs/39210357351/attempts/1)
at `537595f65289daa1b35713f70edf6717b3bda828` failed in both native worker jobs and the final qualifier with
`NVKR-E-VERIFY-FAILED: production provenance belongs to another workflow or Run attempt`. The refusal
preceded delivered-tool execution and qualification signing. Read-only handoff inspection found 70 original
predicates: 68 output predicates from producer attempt 1, with only `nvke-node-image-amd64` and the aggregate
release root from attempt 2. The downloaded 4,855,296-byte carrier archive matched layer digest
`sha256:9d6a61c71dbd6bc993006c74e97f0b04497f7da58e39eb424ce9c12838cf48e9`.
This establishes the partial-retry shape rejected by the former exact-attempt gate. It supplies no independent
signature/full-payload verification, passing smoke, qualification attachment or acceptance of the retry changes above.

Targeted regression tests pass in `pkg/nvkr/factory/qualification_predicate_test.go`,
`pkg/nvkr/lifecycle/qualification_retry_test.go`, `pkg/nvkr/qualificationorigin/smoke_download_test.go` and
`pkg/nvkr/lifecycle/qualification_smoke_aggregate_test.go`. They cover mixed producer attempts, re-signing,
aggregate/finalizer reuse, original publication and worker-certificate bindings, numeric latest-worker
selection and refusal to fall back after a newer failure or unfinished worker. Negative cases retain rejection
of changed inputs/policies/subjects, other runs/workflows/commits, missing origins and future attempts.
The targeted qualification/native-smoke suite also passes with race detection on Linux using Go 1.27.2
across `factory`, `lifecycle`, `qualificationorigin` and `qualification`; Linux lint and full Fern validation pass.
These provider/signature fixtures establish contract behavior; successful hosted retry qualification remains unproved.

The [attempt-3 finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/39191524720/job/117957092152)
completed successfully on October 19 at 08:42 UTC, reusing the original producer publications and emitting a
fresh handoff. It triggered [qualification Run 39224632381](https://github.com/dsx-ai-factory/nvkr/actions/runs/39224632381)
at `22831fb16867b7ce28a99ca846279b2cb622ee71`, which includes the retry correction.
Its [AMD64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39224632381/job/117967824568)
failed while materializing `nvke-node-fde-arm64` with `no space left on device`, before native smoke or
qualification signing. Its [ARM64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39224632381/job/117967824107)
finished downloading the release but rejected the additional disk snapshots: 20,136,460,288 bytes required
with 12,484,796,416 bytes available. The [final qualifier](https://github.com/dsx-ai-factory/nvkr/actions/runs/39224632381/job/117975647169)
also exhausted storage while downloading `nvke-node-fde-arm64`. The former delivery path downloaded the complete
release into one scratch layout before constructing another retained snapshot. This records successful finalizer reuse and a hosted storage
failure, not complete artifact verification or successful retry qualification. Sequential verification and
reduced payload retention require a fresh hosted run after their local contract checks.

Registry-backed delivery and lifecycle contracts cover sequential complete payload verification, retained
executable bundle reads, full-format parity, exact evidence bindings, missing sibling rejection, cancellation
and cleanup. The storage fixture first failed against the cumulative pull path and passes with sequential
checking. Interrupted disk reads are retried without retaining failed payloads or repeating prior output
observations; integrity, close and filesystem failures remain terminal. Image-index import reuses the verified
held closure directly; a regression rejects the former intermediate-layout path when system temporary storage
is unavailable and verifies exact child bytes, shared-blob membership and unchanged publication index bytes.
Binding mismatch, closed-owner and canceled-operation contracts prevent output before admission. The uncached full gate and build pass
on Linux with Go 1.27.2, as do targeted delivery/lifecycle race checks and full Fern validation. These local
fixtures do not establish successful hosted qualification.

[Qualification Run 39279436309, attempt 2](https://github.com/dsx-ai-factory/nvkr/actions/runs/39279436309/attempts/2)
uses the merged storage correction at `bb0fc777751455df34b61043b7fcf1c27d8e6c96` and the original
producer's successful attempt-4 handoff. Its [AMD64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39279436309/job/118189301921)
and [ARM64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39279436309/job/118189302977)
both reached the twenty-minute authentication/smoke step limit without returning CLI diagnostics or
uploading signed smoke evidence. The [final qualifier](https://github.com/dsx-ai-factory/nvkr/actions/runs/39279436309/job/118199918963)
also reached its twenty-minute step limit at 17:43 UTC. The logs do not identify the active verification
phase or prove a storage failure. A separate regression reproduced an unbounded standalone SBOM verifier invocation;
that call now applies the existing three-minute cosign budget and refuses late success. That follow-up
introduced nineteen-minute hosted deadlines inside twenty-minute steps for cleanup and diagnostics;
the current complete-operation budget is described below. Generated terminal commands use `exec` for signal delivery.
These changes bound failure and improve diagnostic availability; they do not establish the cause of the
hosted timeout or successful completion of the complete candidate. The Linux Go 1.27.2 full gate and build,
targeted signing/qualification race checks, and full Fern validation pass. The regressions cover earlier
caller deadlines, cancellation, joined verifier cleanup, late-success refusal and both generated lanes.

A read-only metadata inspection of that candidate counted 69 outputs and 120 distinct SBOM bundles.
The first bundle (`sha256:d1b935653e29be84a619074c319848e168c6bbc5394401c723c3404f7691eb21`,
for `nvke-cni-calico` in `nvke-node-fde-arm64`) passed standard pinned cosign 3.1.3 verification on
Linux ARM64 in 0.974 seconds. This isolated bundle check did not reproduce the timeout; it neither checked
the complete payload closure nor exercised native smoke or qualification publication.

[Qualification Run 39309062376](https://github.com/dsx-ai-factory/nvkr/actions/runs/39309062376)
uses merged commit `c014f854fccf22ecead82daa7da084cd5d7b30aa` and the original producer's successful
attempt-5 handoff. Its [ARM64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39309062376/job/118253636589)
and [AMD64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39309062376/job/118253637003)
both completed payload integrity checks for all 69 complete outputs, including four disks and the native
children of four image indexes, then verified the required SBOM bundles. Disk filesystem/content inspection
remains unperformed. Their nineteen-minute operation deadline expired during the subsequent output
signature/provenance checks, after 15 and 16 outputs respectively. Both returned coded timeout diagnostics
and exit 31 before native smoke or signed worker evidence. The remaining timeout rows reflect the expired
shared context, not independent evidence that each remaining artifact is defective. The dependent
[final qualifier](https://github.com/dsx-ai-factory/nvkr/actions/runs/39309062376/job/118263498701)
also completed all 69 payload and SBOM checks, then returned exit 31 at its nineteen-minute deadline after
five output signature/provenance checks. It published no qualification subjects. The Run completed with
failure at 19:53:33 UTC. These runs exercise the storage correction and diagnostic deadline; they do not
establish complete qualification.

A bounded read-only Linux ARM64 replay of the first real output's stock signing adapter took 33.018 seconds:
30.699 seconds were spent in two cosign bundle downloads, with normal signature and exact production-predicate
verification succeeding. That subject had five signature, five provenance and five SBOM bundles from
historical publication attempts. Pinned cosign 3.1.3 fetched all 15 before predicate filtering on each download.
The lazy acquisition implementation checked the same subject and exact predicate through the stock adapter
in 6.865 seconds, using the same pinned binary, held-descriptor execution and normal transparency checks.
The two recorded cryptographic invocations verified signature and provenance separately. A second stock
replay of `csi-node-driver-registrar-image`, the first output to time out in the ARM64 worker, passed both
checks and its exact expected predicate in 15.004 seconds. These bounded read-only replays did not exercise
the complete publication, native smoke or qualification signing.

[Qualification Run 39327048621](https://github.com/dsx-ai-factory/nvkr/actions/runs/39327048621)
used `7f67ce63c19b2aab56e119f0bd27376a385c6e69`, including lazy OCI evidence acquisition and production
release-manifest issuance, with the original producer's successful attempt-6 handoff. Its
[AMD64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39327048621/job/118315486080)
completed all 69 payload, membership, input and SBOM checks, then 40 output signature/provenance checks
before the nineteen-minute deadline. The other 29 rows reflect the expired context. Its
[ARM64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39327048621/job/118315486516)
and [final qualifier](https://github.com/dsx-ai-factory/nvkr/actions/runs/39327048621/job/118324467119)
instead expired while materializing an ordinary output, before artifact-check reporting. Their completed
output counts are unavailable; the error did not identify the output. All three returned exit 31 before
the Actions hard limit. Neither worker reached native smoke or uploaded evidence, and the final job
reached neither qualification publication nor manifest issuance. The Run failed at 22:03 UTC. Complete
logs confirm the exact trusted checkout and terminal `exec`; no complete qualification follows.

A bounded read-only metadata replay against the same immutable candidate hit its 270-second caller
deadline after 419 logical manifest/blob acquisitions. Its 1,515 HTTP requests included 419 registry pings
and 419 token exchanges; their response-header waits consumed 101.024 seconds, while body reads totaled
about 6.27 MB. A single-variable replay using one operation-scoped registry puller completed all 513
logical acquisitions in 218.294 seconds, including 69 outputs and 120 evidence records, with 70 pings and
70 token exchanges. The resulting root index and canonical root config matched the saved exact candidate.
The baseline was incomplete, so these observations establish reduced negotiation and completion under
the same bound, not a complete-read speedup ratio. The retained metadata declares 23,822,474,523 bytes
of output layers, including 20,136,460,288 bytes across four disks; this inspection did not download them.
Metadata acquisition now reuses that private per-operation puller without removing descriptor, identity,
size, namespace, cancellation or complete-set inspection checks. Checked-output failures also identify
the exact output while retaining their coded cause and releasing prior retained files.

A separate bounded Linux ARM64 stock-adapter replay authenticated the same candidate root and all 69
output subjects against their retained exact production predicates. All 140 signature/provenance
cryptographic invocations passed using pinned cosign 3.1.3, held-descriptor execution and normal keyless
trust checks. The sequential verification phase took 732.998 seconds, with a median of 9.25 seconds per
subject and a maximum of 26.06 seconds. This is 12 minutes 13 seconds for signature/provenance alone;
it excludes payload acquisition, the 120 SBOM verifications, native smoke, qualification publication and
release-manifest issuance. These measurements motivate additional aggregate headroom without establishing
complete qualification or identifying the hosted ARM64/final job's particular slow output.

The current hosted budget is fifty-nine minutes inside a sixty-minute step and ninety-minute job.
This provides operational headroom for the mandatory workload, including the final job's 69 qualification
attestations with independent readback, 69 further member verifications, and release-manifest attestation
with two readbacks. It is not proof of fit or successful issuance. Nested owners retain the original
deadline, earlier caller cancellation wins, and individual signing, metadata and native execution limits
remain. Payload materialization inherits this longer parent deadline. The minute before the Actions limit
permits cooperative cleanup, not a guaranteed return or attachment rollback.

The Linux Go 1.27.2 full uncached repository gate and build pass for this follow-up, including vet,
lint and vulnerability scanning. Targeted delivery, qualification and release-manifest race checks,
canonical workflow regeneration, actionlint, YAML lint and Fern validation also pass. Regressions failed
against the prior source and passed with the correction for repository negotiation reuse, exact-output
failure diagnostics and the enclosing budget; they also cover independent credential admission and
retention of the original deadline through manifest issuance. Independent final review found no
actionable issues. These checks establish the implementation contract, not hosted recovery.

[Qualification Run 39344373891](https://github.com/dsx-ai-factory/nvkr/actions/runs/39344373891)
used `fa1a73fc4bafd1661cdd9ba40ba53fcf518cc782`, including metadata negotiation reuse and the longer
operation budget, with the original producer's successful attempt-7 handoff. Its
[AMD64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39344373891/job/118373320509) and
[ARM64 worker](https://github.com/dsx-ai-factory/nvkr/actions/runs/39344373891/job/118373320831)
both completed all 69 artifact checks: payload integrity, membership, exact Run/inputs, signed evidence,
and signatures/provenance. Both then failed native smoke runtime admission because the hosted Docker CLI
rejected `image inspect --platform`. The commands returned `NVKR-E-ENGINE-CAPABILITY` and exit 29 after
22 minutes 29 seconds and 22 minutes 4 seconds respectively, within the operation budget. All three AMD64
tool results and the one ARM64 tool result were not evaluated, and neither worker uploaded signed smoke
evidence. Complete logs confirm the trusted checkout and terminal `exec`. Full disk filesystem/content
inspection remains outside this gate; passing artifact checks do not establish native smoke or qualification.

The shared local-image observer now uses portable exact-reference inspection and still rejects unexpected
OS/architecture, digest or local image ID, missing configuration, inherited writable volumes and unexpected
environment. Pull/create platform selection and the smoke owner's pre-stream container-identity checks remain.
The regression reproduces the hosted unsupported-option error on the prior source and passes with the
correction; existing image substitution, isolation, cancellation and cleanup cases also pass. This establishes
the command/admission contract, not hosted native smoke success or release-manifest issuance.

The official Docker 28.0.4 client reproduced the unsupported option locally. Portable inspection of both
exact hosted image pins returned the expected platform, manifest digest/local ID and unchanged configuration.
The Linux ARM64 live smoke test then passed all seven fixed profiles in 2.696 seconds with that client and
a local Docker 29.4.2 daemon. The fixture checked nonroot identity, disabled network, absent credentials and
socket, read-only root, no capabilities and private temporary storage. This exercises local runtime mechanics
with controlled ELF bytes, not the delivered NVKE tools or the hosted daemon. The Linux Go 1.27.2 full
uncached repository gate, build and targeted pipeline race checks also pass; independent review found no
actionable issue.

No successful hosted worker/qualifier OIDC signing transcript, subject-bound qualification attachment/read-back
or independent complete-set consumer verification is recorded here. M3-D
retains positive/negative external acceptance and independent review; the earlier M2 candidate cannot satisfy
it. Successful hosted/consumer exercises remain required before claiming
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

### NVSentinel catalog and package inputs

The dedicated [NVSentinel catalog](../../../inputs/catalogs/nvsentinel/README.md) declares OCI images, Helm,
executables and DEB packages at the pinned development source. Existing artifact handlers own those formats.
Debian packages use ordinary `host-package` producers; existing producer-result authentication, bounded
held files and lifecycle staging retain a raw archive. There is no separate Debian scheduler, target resolver,
receipt hierarchy, mandatory content-inspection setup or unsigned-provenance protocol. Source-declared
composition metadata does not claim payload dependency inspection or runtime/ABI compatibility.

The OS experiment invokes unchanged NVKE composer code and adds the exact package to its private `debs/`
folder, regenerates the APT index and copies the two required source network files. Existing Packer code stages
that folder; placement does not install or enable NVSentinel. Current commands add no service unit and
modify no upstream source. Reports of real-tool fixture checks remain unpublished developer-local
observations, without a shared Run/artifact record or a committed test of these commands. They do not
establish independently verifiable OS-material placement or a current product build. Offline input
compilation and catalog-copy checks establish only the authored selections and bindings.

The simplified Ubuntu 26.04 ARM64 SDK/package exercise remains a developer-local operator observation
in the `unqualified-local-execution` lane. No shared immutable Run or artifact reference is available;
local measurements do not establish an independently verifiable current build, reproduction,
OS-material assembly, installation, dependency inspection or runtime acceptance. The other SDK routes
remain unexercised; signing and publication remain deferred.

Historical observations used the retired patched recipes and extra Debian execution protocol. Standalone
package/image/chart payloads were removed during authorized cleanup. Their identities and observation scope
remain in the [immutable prior record](https://github.com/dsx-ai-factory/nvkr/blob/7951d85513caa8fcfa576f9315372ab907f6d9c7/definitions/nvsentinel/README.md).
Those records do not validate the simplified implementation or establish current bytes.

Historical Run `ubuntu26-arm64-native6` completed a Ubuntu 26.04 ARM64 bake with an uncommitted local
toolchain overlay outside the current authored inputs. Its Plan was
`8bf4f5c0741835e5f7778076340d7a7886bc3bae8a9fd0f44bcca737d9dcb96e`; its 2,885,085,696-byte QCOW2
had SHA-256 `4600f8364d75f0869ea0c94e067a685f26067da99266346944576e0145aee807`. Read-only inspection
matched the old installed binary/unit and network configurations to that old DEB/material set. Installation
used the removed provisioner patch. Current inputs do not reproduce that Plan or supply that unit/install path.
Independent boot, service/API/GPU behavior, upgrades, native AMD64 product execution, full-content standalone
Debian evidence, qualification, signing and publication remain unverified or deferred.

### Local qualification demo

The local demo presents six qualification stages: Verify build outputs, Sign build
evidence, Check delivered tools, Sign qualification, Publish artifacts, and Verify
registry contents. A private nonblocking pipe carries bounded owner-reported
stage/item descriptions separately from canonical result stdout and private
diagnostics. The TUI shows role relationships, each stage's input/action/result,
counts of outputs, tools, uploads or checks, and the last observed operation;
navigation cannot change execution. Missing or completed telemetry cannot grant
a pass. The adapter reports the qualification outcome only after child completion,
result retention and validation; automatic registry
cleanup remains independently required before deployment. Portable layout,
failure/privacy and targeted race tests, plus native Linux ARM64 pipe/Runner
tests, cover these presentation contracts. The full `make check` gate passed with
uncached tests; focused presentation, progress transport and lifecycle race checks passed.

On 2026-10-16, live recording `m3-tui-20261016-01` exercised the production dashboard,
private progress pipe and qualification command on native Linux ARM64. The fresh M3 run
reused the retained `m3-video-04` build, displayed all six stages, checked all 29 outputs
and smoked all 10 native tool members. The 814-second capture includes successful command
completion and result retention after publication and independent read-back. Its content
subject remained
`sha256:c811839c5fe210cb9a355ced62973cd8d38c263e9c9ca7da9acabbfb0d1063bf`; fresh evidence was
`localhost:32774/local-m3/evidence@sha256:0d424cd0342e565d1dcba3ecd850f7f7559e1ac618e0b56fb70add8b6c724cd8`.
The retained canonical result has exact-byte SHA-256
`1086398389995fc40c5cbeb5a3211b23f629138e60fa7403e2c365dc8d50e20f`.
The recording and canonical result are retained locally/private to the operator; no
durable shared download is published. The loopback registry reference is not remotely
retrievable, and the hashes identify bytes rather than provide access. Reviewers need
an operator-provided permitted copy to independently inspect this run.
This records local operator trust and `unqualified-local-execution`; it did not rebuild
producers, exercise hosted signing or deploy a cluster.

On 2026-10-16, the automatic `m3-video-04` rehearsal completed its 29-output build and
signing/smoke stages but failed publication: the capability-dropped root registry could not traverse
its non-root caller's mode-0700 storage. Setup now runs the registry with the caller's UID/GID,
retaining private permissions and container restrictions. The non-root Linux ARM64
`TestExternalQualificationRegistryStorage` passed actual OCI upload, read-back and owned shutdown.
Reusing that completed build with fresh registry/scratch/evidence destinations then passed
`run qualify-local`, including all 10 native tool members, three-role signing and complete registry
read-back. Its content subject was
`sha256:c811839c5fe210cb9a355ced62973cd8d38c263e9c9ca7da9acabbfb0d1063bf`, ordinary publication
`localhost:32772/local-m3/ordinary@sha256:53e1e27794954c788712c12dbbb7270ddec4886723af783dd7e095d9799f3bde`, disk
`localhost:32772/local-m3/disk-nvke-node-image-arm64@sha256:5e26a285f90cc56e9ff1530541304659eb77cb046717ce534a3c38199f287fcf`, and evidence
`localhost:32772/local-m3/evidence@sha256:d592c4236557f8c2b91ada36b83c0e87960e94ca81be44621c8acf1194c20a1c`.
These loopback locators belong to the retained laptop VM and remain `unqualified-local-execution`;
the retry did not rebuild producers, exercise hosted OIDC, or deploy a cluster.
The separate `TestExternalLocalM3Consumer` passed its positive case and all 14 negative cases against
this publication in 283 seconds, with only public policy, pinned cosign and registry locators:
invalid role signatures, omitted signatures/provenance, changed compiler inputs/source pins/Run,
wrong policy, missing/extra transport members and altered/missing downloaded disk bytes.
The repository gate passed with uncached tests, as did the affected demo package, Linux ARM64
targeted signing/qualification race tests, generated-workflow drift and documentation checks.

`build` and `e2e` now accept `--qualify-local` (or `build.qualifyLocal: true`) for a fresh attempt.
The adapter prepares once, invokes the same setup, then uses the existing prepared build and local
qualification gate. Its registry is stopped after read-back or failure using a cancellation-independent
bounded cleanup; stop failure blocks deployment. Files remain in the bootstrap's `qualification/`
directory. Explicit setup/stop remain available. Contract tests cover configuration conflicts, flag
overrides and canceled-context cleanup/error propagation. This combined command has not yet been
externally exercised through a fresh complete build/qualify/deploy run; the setup-only and earlier
qualification evidence below retain their original scope.
The simplified flow passed `make check` with uncached tests, targeted automatic-cleanup race checks,
`make docs-check`, and Linux ARM64 cross-compilation. No fresh full native e2e run is claimed.

The demo now offers `qualification-setup` for a fresh prepare-only ARM64 attempt. It generates three
distinct private local keys and a public policy using the maintained exact 29-output NVIDIA/upstream
OVN demo scope, downloads and checks pinned cosign v3.1.3, recompiles the selected inputs, runs key
preflight and starts a pinned disk-backed registry on a dynamic loopback port. It writes prepared demo
YAML and retains private diagnostics. `qualification-registry-stop` checks exact ownership, image,
mount and port configuration before stopping the container, retaining its data, keys and evidence.
Contract tests cover authored scope parity, changed/missing/duplicate outputs, changed components and
formats, unknown networks, registry ownership/configuration refusal, readiness/redirect/cancellation,
wrong executable bytes and unsupported-host admission. Build-only configuration now preserves its
network selection, so a prepared upstream-OVN attempt keeps its selected inputs.
The setup command passed on native ARM64 Linux with prepared attempt `m3-auto-setup-01`, Plan
`sha256:1b5a8df0f16346d5ba8d79e4db98aaff4b50b457ccb354492565b516264199f3`:
actual cosign download and pin verification, three-role key generation, compiler replay, signing
preflight, image pulls and registry readiness succeeded. A separate check confirmed distinct public
keys, mode-0700 setup directory, mode-0600 private keys and a live registry API. The owned stop command
succeeded twice, including its already-stopped case, without deleting data. This is setup evidence;
no new producers or complete build/qualify/deploy sequence were run by this exercise.
The final `make check` passed all 56 package suites with uncached tests, plus its formatting, license,
vet, lint, shell, Python and vulnerability gates. Targeted setup race checks and the documentation
gate also passed. Repeating setup into an existing directory was refused without changing its policy.

The requested local NVKE qualification rehearsal is governed by ADR-16's local qualification contract.
Execution-scope admission now has a shared internal owner independent of GitHub workflow identity:
native image pins, fixed tool profiles, exact output membership and expected versions remain mandatory.
Focused lifecycle tests cover local scope parity and refusal of missing pins, unsupported profiles and
extra membership, while the hosted entrypoint still rejects a missing worker identity.

Captured local preparation now retains the compiler-read closure before producer work. Tests replay
the original Plan after catalog mutation and check capture-before-build, dry-run and capability-refusal
ordering. The demo launcher requests this through `run local --capture-inputs-root=.` and generates
repository-relative catalog/overlay references. Focused CLI and launcher tests cover capture selection,
empty-root rejection, exact invocation admission and relocation of the generated references.
The private local qualification input reader now replays retained inputs without the producer
checkout, compares the original Plan and all frozen Run bindings, and refuses incomplete disk
commits. Contract tests reject changed input, Plan, Definition/Environment and output mappings,
wrong attempts/lanes, linked inputs and noncanonical metadata. This reader returns descriptions
only; it does not authenticate payloads or establish a local builder identity.
The private payload checker now joins every recorded output to factory's package/producer
observation. Retained raw disks use a bounded QCOW2 byte/framing check against the exact compiled
root and saved Run observation, without reconstructing an execution-success receipt. Disk execution
does not persist its in-memory producer result; capture therefore measures the committed QCOW2 directly
against that Run description and compiled producer. Contract tests cover an absent producer-result file and
reject changed executable bytes, missing bundles, changed Run subjects, altered disk bytes and
coordinated disk/producer-record substitution against the original Run. Reopenable workspace paths
grant no signing authority; the capture integration below retains checked bytes separately.
The full local `m3-patched-all-02` build completed all 29 outputs, but its automatic qualification
failed before signing because compiler replay lacked Git credential-helper settings. The demo now
passes the supported source transport environment and retains private stderr diagnostics; focused
process tests cover both. A direct retry exposed the nonexistent disk-result-file dependency addressed
above. The corrected CLI subsequently completed local qualification of those same actual outputs:
all 29 outputs were checked, all 10 delivered tools passed native ARM64 smoke, three independent local
roles signed their evidence, and complete registry publication and read-back verification passed.
The evidence carrier is
`localhost:5055/local-m3/evidence@sha256:b763ed90d42bcd79b7e86de77729f01ed89487073aafd058fa1f50051f20d550`;
the ordinary closure is
`localhost:5055/local-m3/ordinary@sha256:79ca8a4189e74000e316691fcafdacca9fd2baad4fc5a21621e59fed38bbe03d`;
the disk wrapper is
`localhost:5055/local-m3/disk-nvke-node-image-arm64@sha256:35ad048add63cbf303b972e02e520f6b06c2548f1816f5c90690ecfeae094828`.
These references identify a local development registry, not hosted release artifacts. The independent
policy digest is `sha256:fe44188e4d624d5e2caa774c545295705314ab31ad56f1f3796faa0103e363d1`.
The lane remains `unqualified-local-execution`, with producer-only disk evidence. A separate native
consumer process, built from the local patch on upstream commit `adc0715a42f9efaa4725f2e61626736a55d2e879`,
downloaded the complete publication without producer-workspace or private-key inputs and passed.
Its 14 controlled rejection cases cover invalid signatures for each role, missing signatures and
provenance, malformed compiler inputs, a well-formed source tree-pin substitution, mismatched Run
evidence, a wrong policy Plan, missing ordinary/disk or extra disk membership, and altered or missing
disk payload bytes. Source-pin substitution fails the local toolchain overlay's exact catalog binding
with `NVKR-E-INVALID-CATALOG` wrapping `NVKR-E-UNVERIFIED-REF`; malformed compiler inputs return
`NVKR-E-PIPELINE-DRIFT`. These errors were observed against the real publication, not fixture payloads.
A separate qualification attempt used the original delivered bytes and real Docker runtime while a
bounded harness terminated its newly created, exact-label smoke container. The smoke check returned
exit 137 and `NVKR-E-VERIFY-FAILED`; the command reported failure with no publication references,
created no qualification evidence file and left no smoke container. The development registry contained
no repository under the failure attempt's namespace. This exercises refusal before qualification
publication, separately from consumer rejection of already published evidence.
The private build, positive result, independent consumer, source-pin refusal and smoke-refusal
transcripts are retained in the `nkd-demo` VM under
`/mnt/lima-colima-nkd-demo/nkd-local/nvkr-m3-qualification/acceptance-m3-patched-all-02/`.
The signed evidence remains in the local registry at the exact references above; the transcripts
document test execution and do not replace signature or payload verification.
Input-capturing execution now retains `disk-materials.json` before releasing the live factory Run
or exporting its disks. Factory's bounded export uses the original consumed-material observations,
not saved producer-result claims; lifecycle binds each record to the exact named Plan output and
retains the set under the attempt identity. Focused tests cover transitive consumption, defensive
copies, explicit network gaps, altered descriptive result files, refusal of decoded Runs, missing
live observations, cancellation, aggregate size limits and failure before disk export. This adds
retention of descriptive evidence, not signed provenance or a consumer-verification receipt. The
complete `m3-patched-all-02` build retained this envelope and the successful local builder evidence
bound its exact bytes alongside the captured compiler inputs, Plan and Run.
Factory now validates exported material descriptions against the exact named Plan output and
disk subject, complete transitive producer/edge membership, per-consumer manifest consistency,
sorted component membership and explicit network gaps. Focused tests reject substituted subjects,
missing/duplicate members and edges, malformed digests and suppressed network gaps. This same
descriptive validator runs before live export; it issues no receipt and cannot authenticate a
decoded record. Local payload capture now loads the bounded canonical material envelope through
a held directory, rejects incomplete disk commits, and binds the exact attempt, Plan and complete
disk set to the same Run whose payloads it captures. It retains immutable record bytes beside the
payload owners and closes the set if material validation fails. Focused tests reject missing,
linked, oversized, noncanonical and substituted records, duplicate/unknown fields and incomplete
membership; mixed disk/executable capture refuses missing or substituted material evidence.
Complete consumer integration and a real demo exercise remain outstanding.
Local build-evidence preparation now binds the independent policy, retained input byte references,
complete named output descriptions and ordinary/disk transport identities in a bounded canonical
`local-build-evidence/v1` document with explicit `retained-local-run` origin. It compares retained
Plan/Run bytes to replayed descriptions and requires open payload owners. Ordinary and mixed-capture fixture
tests cover exact references, deterministic bytes, changed input descriptions, missing compiler reads,
missing/closed owners, wrong policies, cancellation and signer-role changes. This is unsigned document
preparation, not a qualification receipt. Preparation revalidates retained disk material bytes against
the original Plan/Run before signing. Tests reject empty, malformed and noncanonical replacement records.
The mixed fixture now captures authored catalog/lock/Definition/Environment files and replays them
through the same retained-input loader, rather than supplying an in-memory-only Plan.
The private builder-signing operation now selects pinned local cosign and the builder public key
from the independent policy, requires a genuine exact-document signature receipt, and rechecks
capture bindings after signing before returning a result. Contract tests reject unissued receipts
and prevent invalid evidence from reaching the signer. On October 15, the ordinary and mixed disk/executable fixtures
passed real signing and verification in the `nkd-demo` ARM64 Linux VM using cosign 3.1.3,
digest `sha256:c5d324e091826b0d7a78eb16fef316450b4eb9aaec045611c08ba06f5e73220a`.
Changed document bytes, the qualifier public key and a mismatched private key were rejected.
The mixed test keeps the raw disk payload identity separate from its OCI manifest and binds the
exact retained material envelope. These tests sign controlled fixture documents, including a 512-byte
QCOW2 framing fixture and synthetic executable. They do not prove the actual demo outputs, live disk
material consumption, registry attachment, tool smoke or complete local qualification.
The private local build-evidence reader now accepts only retained attempt files, detached signature
bytes and independently selected public policy. It authenticates the builder statement before payload
work, replays the compiler inputs, captures its own complete payload set and requires exact equality
with the reconstructed evidence document. It owns those snapshots and returns no owner on refusal;
this authenticates a local builder assertion, not original execution or qualification. October 15
real-cosign tests in the same ARM64 VM accepted separately copied ordinary and mixed attempts and
rejected invalid signatures, changed compiler inputs, changed Run attempts and missing payloads.
Accepted snapshots remained usable after moving the copied workspace, and closing a copied handle
revoked shared access. These are fixture-level retained-file consumer checks, not registry read-back,
smoke-result verification or complete qualification consumption.
Local native smoke now derives complete tool membership from the independently verified build and
uses the shared held-executable streamer, fixed profiles and version evaluator. Its separate
`local-tool-smoke/v1` receipt binds exact build-evidence bytes, policy, runtime identity and every
observed tool. Issuance requires complete native coverage and successful runtime cleanup; source
closure revokes it. Verified local build owners now cancel and join active operations before releasing
payloads, with copied owners sharing one lifetime. Ordinary and mixed fixture tests cover successful
orchestration, wrong platforms/versions, runtime and cleanup failure, cancellation, unissued receipts
and source-close cancellation/join. Targeted race checks pass. Hosted and local smoke regression tests
passed on the ARM64 VM's native storage after a macOS run stalled in temporary-fixture removal.
These local orchestration tests use simulated runtime observations; actual demo-tool execution through
this path and final qualification remain outstanding. Local smoke signing now requires a live execution
receipt and the independently selected smoke key, under a cancellable build-owner operation. A separate
authenticated-report owner verifies the smoke-key signature and recomputes complete member coverage
and expected observations from the verified build. Tests reject changed source/policy/runtime bindings,
missing/duplicate members, substituted subjects and versions, unknown fields and unissued receipts.
October 15 real-cosign tests in the ARM64 VM passed with distinct builder and smoke fixture keys and
rejected invalid signatures and a valid builder-key signature over the same smoke report. Copied
authenticated-report handles share revocation. These checks authenticate simulated runtime reports;
they are not actual demo-tool execution or registry transport/qualification acceptance.
Verified build and smoke owners now retain their exact signature bytes. The local qualification
gate binds all four document/signature blobs, the independent policy, attempt and content subject
in `local-qualification-evidence/v1`. It refuses missing, closed, mismatched or incomplete evidence
before calling the qualifier signer. Qualification verification recomputes the complete binding and
accepts only the qualifier key; its borrowed receipt shares revocation and source lifetimes.
October 15 fixture tests in the same ARM64 VM passed real cosign signing/verification with three
distinct builder, smoke and qualifier keys, and rejected changed qualification bytes, invalid
signatures and valid builder-role signatures. Focused host race tests pass. The payloads and smoke
observations remain fixtures; complete registry publication/read-back, actual demo-tool smoke and
public CLI/demo orchestration remain outstanding.
The verified local qualification owner now retains the exact qualifier signature and exports the
complete evidence file set, including all three document/signature pairs and original replay inputs.
Focused host fixtures cover ordinary and mixed disk attempts, missing signatures, closed owners,
changed qualification documents, cancellation and defensive byte copies. The evidence-only carrier now
persists this set as bounded canonical JSON with a synced, protected stage, no-replace publication and
parent sync. Focused host fixtures cover deterministic round-trips, existing destination preservation,
cancellation, linked parent/file refusal, malformed bytes and temporary-stage cleanup. This carrier
does not include payloads or publish to a registry. The private retained-attempt consumer authenticates
all three signing roles, independently replays and captures the complete attempt, compares all handed-off
metadata bytes, and owns cleanup independently of the producer. October 15 tests in the ARM64 Linux VM
passed real-cosign export and fresh-consumer checks for ordinary and mixed disk fixtures, including
persisting and reopening the evidence carrier before consumer verification. They reject
invalid builder, smoke and qualifier signatures, substituted input bytes and a missing payload. Moving
the copied workspace and changing caller-owned evidence buffers do not affect accepted snapshots;
closing a copied consumer revokes it without closing the producer. Focused host tests and race checks
pass, including bounded file-set admission and defensive snapshots. Smoke observations and payloads
remain fixtures; complete payload/evidence publication, registry read-back and actual demo-tool execution
remain unproved.
The private layout consumer now verifies exported ordinary and disk OCI layouts plus evidence without
the producer workspace or result records. It shares bounded compiler-input replay and the complete
smoke/qualification consumer gate with retained-attempt verification. October 15 real-cosign tests in the
ARM64 Linux VM accepted ordinary and mixed disk exports, remained usable after imported layout paths
moved, and rejected missing ordinary transport, extra disk locations, changed compiler inputs and an
invalid qualifier signature. Host input-replay, material, capture and file-set tests and targeted race
checks pass. The same Linux fixture now uploads and downloads ordinary and disk payloads through an
ephemeral Distribution-compatible loopback registry before independent three-key consumer verification.
That integrated fixture passed on October 15, including the existing negative cases. The fixture now
also publishes and downloads the complete evidence envelope through a bounded document OCI transport
before consumer verification; that full payload/evidence registry round-trip passed with real three-key
signatures in the ARM64 Linux VM. Focused host document tests prove deterministic references, no tag
writes, exact-byte read-back and refusal of insufficient bounds, wrong media types, unpinned references
and cancellation; targeted race checks pass. Smoke observations and payloads remain fixtures. Actual
demo-tool smoke, production-sized payload exercises and public CLI/demo orchestration remain outstanding.
The private publication owner now orchestrates that upload/download/consumer sequence. It admits repository
names, credentials, disk-backed scratch capacity and the pinned verifier before uploading, holds the source
lifetime through completion, and retains attempted immutable references on error. October 15 Linux fixtures
with real three-key signatures passed success and deliberately denied evidence read-back for ordinary and
mixed disk sets. Success retained a usable consumer after scratch cleanup; failed read-back returned no
consumer, preserved attempted references and cleaned scratch. The producer remained usable in both cases.
Host tests reject unissued/closed gates, tagged destinations, missing scratch, incompatible credentials and
cancellation before publication; targeted race tests pass. This is private lifecycle orchestration over
fixture artifacts and simulated smoke, not a full NVKE demo run.
The public `run qualify-local` command now delegates to `lifecycle.QualifyLocalRun` for bounded input/key
admission, build signing and independent reopening, native tool smoke, distinct smoke/qualifier signatures,
retained evidence and complete registry read-back. It emits canonical local results after owner cleanup.
October 15 Linux fixtures exercised this entrypoint with real keys and registry I/O for ordinary and mixed
disk attempts. Successful runs retained evidence and returned the local lane; injected smoke failure and
a wrong qualifier private key returned coded failure with no qualification evidence or registry writes.
Wrong-key admission occurred before smoke. Runtime observations were simulated through a private test
seam; actual demo tools were not exercised. CLI mapping, argument rejection and public presentation tests
pass, and the built command's help was checked. The later 29-output rehearsal recorded above exercises
the actual tools and full command; the simulated fixtures retain only their original scope.
The demo now accepts an explicit `build.qualificationPolicy` / `--qualification-policy` and delegates
prepared-Plan admission to the lifecycle policy owner before launching producers. Bootstrap/toolchain
preparation precedes this check. Targeted tests show missing or malformed policies prevent producer
execution. Explicit `build.prepared` / `--prepared` now accepts an existing prepare-only bootstrap
and requires a policy and absent producer Run. It revalidates the fixed invocation and recompiles
current inputs before policy admission. Process fixtures pass for matching inputs and show that
changed input identity, malformed Plan output and failed compilation prevent producer execution;
CLI flag/YAML precedence tests and demo-package lint pass. The complete host demo and demo-CLI
regression suites pass after correcting a test's expected Plan-validation error code; duplicate and
unknown Plan fields and compiler-identity changes are also rejected before producer execution.
The subsequent prepared `m3-patched-all-02` run exercised this two-stage build path.
With an explicit complete `build.qualification` configuration, the build adapter now invokes
`run qualify-local` on the newly completed attempt before returning deployment paths. Process fixtures
cover success, failed status, wrong attempt, nonzero exit despite a passed report, malformed output and
transcript overwrite refusal. The adapter now reserves the transcript before starting qualification;
a process marker test proves that an existing transcript prevents child execution, so this refusal
precedes any child signing or publication. Failed child results remain retained. Targeted demo tests
pass. These fixtures do not execute signing, smoke or registry I/O. The subsequent direct command
rehearsal exercised the real demo outputs and complete read-back; the demo handoff's fixes are covered
by process fixtures. This wiring alone does not establish hosted M3 acceptance.
Lifecycle preflight now validates the complete prepared Plan, policy, registry
destinations, new evidence destination and preliminary disk-backed scratch, and authenticates all three
private keys using fixed challenges through the pinned cosign owner before demo producers execute.
It issues no receipt and does not establish runtime/registry availability or reserve capacity.
October 15 native ARM64 Linux fixtures passed preflight and the retained-attempt entrypoint for ordinary
and mixed disk sets using three real keys and pinned cosign. A builder key supplied for the qualifier
role was rejected during preflight; preflight made no registry requests and wrote no qualification
evidence. Subsequent successful entrypoints exercised the loopback registry with simulated tool-smoke
observations. Focused host tests and lifecycle/demo lint pass; this remains fixture evidence.
`factory.CaptureRetainedDiskPayload` now joins the retained producer check to a private disk-backed
snapshot using the standard OCI disk transport. It checks scratch headroom before copying, removes
temporary paths before returning the held payload owner, and grants no successful execution Outcome.
On October 15, its fixture tests passed on the host and in the `nkd-demo` ARM64 Linux VM: source-path
replacement and source-inode mutation leave captured bytes unchanged; canceled or invalid producer
input returns no owner, and closing one handle revokes a copied handle. Targeted host race tests pass.
These small QCOW2 framing fixtures do not establish capture of the real demo disk, its material
provenance, complete-set publication or local qualification acceptance.
The held delivery disk owner now exports its exact standard OCI wrapper and payload as a no-replace
layout without reopening producer paths. Focused host tests preserve manifest/payload identities and
accept a moved source layout, while refusing mutated held bytes, closed owners, cancellation and existing
destinations without leaked staging. These export and disk lifecycle tests also passed on October 15
in the ARM64 Linux VM on native storage. Targeted host race tests and delivery lint pass. This
supplies a missing transport primitive; it is not complete local payload/evidence publication or registry
read-back, and callers still own disk-capacity admission.
Standalone disk push/pull now preserves the manifest and payload identities through digest-only registry
transport. Pull validates bounded metadata and the exact standard profile before downloading the bounded
payload, then verifies it before commit. Host registry fixtures cover both supported platforms and reject
unpinned references, wrong platforms and insufficient payload bounds without output. Focused race checks
pass. These use small opaque disk fixtures; production-sized disk capacity and external-registry acceptance
remain unexercised. The caller must admit scratch/destination capacity before invoking this transport.
The lifecycle disk-set capture retains frozen output mappings and original outer-publication membership,
matches each held disk to its exact named Run slot, and checks transport/observation consistency.
Factory's ordinary publication capture owns ordinary producer verification and package reproduction once;
the complete capture still compares every held ordinary output and the complete named set to the original Run.
A captured-input mixed fixture restores ordinary producer/package paths only when real factory assembly
starts; the prior lifecycle producer/package observation would reject that fixture. Outer-publication
substitution, changed producers, missing packages and changed Run subjects still fail closed. No wall-clock
improvement is established.
Refusal closes captured handles, including a completed capture whose producer bytes differ from the original
Run. Focused lifecycle and race tests cover that mismatch, late capture errors, cancellation, named ownership
and source mutation after capture. This disk set alone is not a complete held publication or authenticated
source receipt and cannot authorize signing.
Factory now provides ordinary-output capture through the existing publication assembly and
held-byte delivery owner. It derives every non-disk slot from the original Plan, rechecks producer
payloads and required inventories, and binds the original Plan identity without a hosted invocation.
Focused OCI/Docker image fixtures cover exact capture, source-manifest inode mutation after capture,
missing inventory, altered producer records, cancellation and shared-handle revocation. A captured
two-platform image index retains both exact native bindings. Acquired-chart fixtures exercise retained
capture after both HTTPS and OCI acquisition and after relocating the workspace; exported chart bytes,
manifest identity and chart version remain exact. The image, Helm and image-index assembly regression
tests pass. This is diagnostic transport capture, not authenticated builder provenance.
The private lifecycle capture now owns ordinary snapshots and the retained disk set together. It matches
every held ordinary subject, named component set and image-index binding against the replayed Run,
excluding only platform/chart descriptions that delivery derives from the already digest-bound bytes
and ordinary local Run observations do not store. Failure closes the whole capture. Ordinary workspace
capture follows local execution's canonical repository-relative path contract. Focused tests use actual
fixture bundle bytes to reject substitution between path checking and capture, late errors, missing owners
and cancellation; they stream the original executable after moving the complete source workspace and
verify shared-handle revocation. The disk-only route retains its exact named slot through this owner.
A compiled two-output fixture now captures an executable and QCOW2 together, streams both after moving
the source workspace, and verifies closure revokes both. Missing ordinary inventory rejects the mixed
set after disk capture. Its producer records and disk header are synthetic; it does not execute a build.
These fixtures do not establish mixed 29-output capture, authenticated material provenance, delivered-tool
smoke, signing, publication or successful local qualification of the actual demo.
The separate local-key cosign adapter holds pinned Linux executable/key/signature bytes and
verifies the exact document under the selected public key before returning a local-only receipt.
Portable admission tests pass. On October 15, the cross-compiled ARM64 sign test binary ran
`TestLocalCosignRealKeyRoundTrip` successfully in the `nkd-demo` Linux VM using cosign 3.1.3,
verified against the maintained ARM64 executable pin. Real detached signing and public-key
verification passed; changed document bytes, a different public key, missing/invalid signatures
and a private/public key mismatch were rejected. This exercises only the local signing adapter,
not registry publication, complete-set evidence, hosted identities or the demo qualification flow.

The independent local policy now binds exact Definition/Environment/Plan identities, one native
platform and smoke image, a cosign executable pin and distinct builder/smoke/qualifier public keys.
Public-key normalization rejects duplicate roles even with different PEM formatting; exchanging roles
changes policy identity. Strict bounded JSON loading rejects duplicate/unknown fields and trailing
documents. Focused tests also cover Plan/scope substitution and defensive freezing of policy maps.
This is admission only: no local source, artifact or qualification receipt is issued by policy validation.
The demo's optional prepared-Plan admission loads this policy; complete qualification configuration also
requires the owned qualification child to succeed before deployment inputs are returned.

The standalone local rehearsal `m3-patched-all-02`, recorded above, passed complete-demo tool smoke,
production-sized publication/read-back and controlled operational negatives. The earlier scoped exercises
below retain their original limits; they are not the current aggregate status. A live automatic
build/qualify/deploy handoff remains unexercised. No successful combined local M3/demo E2E is claimed.
The local demo's current 29-output selection includes Kind, clusterctl, ORAS, NICoSim tools and a
non-executable NICoSim runtime-assets archive. Fixed Kind, clusterctl and ORAS profiles now have
contract coverage for expected versions/source stamps, exact responses, and refusal of changed
versions, commits, platforms and dirty trees where those fields are exposed. On October 15,
`TestToolSmokeRealDocker` passed all six fixed command profiles in the `nkd-demo` ARM64 VM,
using the maintained Debian image `sha256:a2ca65377c251208de42e1a2722cb949211236c161b634f6f3663754e2d06850`.
The held fixture ELF was `sha256:c02387d8f2cbbdda12cc8df968d9325cc462c5a2489c6ea978b8d1d529b0f7e0`;
it checked nonroot execution, no network, no capabilities, no new privileges, no daemon socket,
the fixed environment, read-only root and writable private temporary storage. This is runtime
mechanics evidence using a controlled fixture, not execution of the selected customer binaries.

On October 15, `TestDeliveredToolsRealSmoke` exercised five actual historical installer binaries from
the native `demo-bootstrap-tools-01` Run (original Plan
`sha256:26efce4248a039802283e794b15a599c3c43a7a6a3a2ebb164b262791e7c8cf3`). Expectations came from
that Plan's producer version stamps, and each held executable was measured before isolated execution.
The first run exposed clusterctl's post-command network update check: its version command timed out.
The shared fixed smoke environment now sets `CLUSTERCTL_DISABLE_VERSIONCHECK=true`; all five then
passed in the same pinned Debian image above. Changing the expected version was rejected for each
real observation. The Linux smoke contracts and real-container isolation fixture also passed after
the environment change. Exact exercised subjects were:

| Tool | Observed version | Payload SHA-256 |
| --- | --- | --- |
| clusterctl | v1.12.3 | `e5fd089114c308341d07f27686fe7a8dd0df6b7e74fc30f61f9c04387fd3384c` |
| Helm | v4.2.4 | `49a95a30747393108bf4b5ada0faafb0a607150347e81a4f4ac7c619fec93050` |
| Kind | 0.32.0 | `3be263ce04370517fcbcf43e6f25670c993ad74101751e18a9b6839afa03c0b7` |
| kubectl | v1.36.0 | `9eabe6314330ff7cbdffd2e92178faad7cb67593dc77d2a7f5323ca398f98592` |
| ORAS | 1.3.3 | `80532bdf9abffe1f62beecaf0076c7ae414159b108d0fa53289123486e3475da` |

Clusterctl and ORAS also matched source commits `1a1852c74072febc3fbf9823c9dd32f4cd6cc747` and
`210747c29c1d38732b3194878dfd8b5a6b9ad7eb`, respectively. This is real five-tool execution evidence,
not authentication of that historical Run or complete current-demo qualification. NVKE CLI, NICoSim,
the current 29-output attempt and its signing/publication flow remain outside this exercise.
Host-native repository lint and Linux-targeted lifecycle/pipeline/sign lint pass with pinned Go 1.27.2
and golangci-lint 2.14.0. Source headers and diff checks passed. These checks establish code contracts,
not complete local-demo or hosted acceptance.
The first uncached host suite exceeded the default ten-minute package timeout in heavy filesystem
fixtures. A sequential retry of delivery, factory and lifecycle with a thirty-minute per-package
limit passed on Go 1.27.2 (303, 249 and 519 seconds respectively). The vulnerability scan reported
no vulnerabilities; Fern checks passed for the published documentation. These results do not replace
the missing complete current-candidate operational exercise.

The pinned NICoSim [server entrypoint](https://github.com/dsx-ai-factory/nvke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/nico-sim/cmd/nico-sim/main.go)
and [CLI entrypoint](https://github.com/dsx-ai-factory/nvke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/nico-sim/cmd/nico-simctl/main.go)
do not expose version commands: the server enters its normal setup path, while insufficient CLI
arguments print usage and exit unsuccessfully. They remain unsupported for version smoke; help text
or startup failure cannot count as a pass. The same missing version interface was confirmed on October 15
in upstream NVKE `a566f7b46a5a5bb30981771053b131003b6f9b1c`; moving to that revision alone would not
resolve the prerequisite. Shared smoke admission now supports an exact
`runtime-assets-archive` output/member rule within a mixed bundle. It requires a source-built
tarball producer and non-executable delivered mode, preserves complete bundle membership, and
requires sibling tool coverage. Policy identity and complete evidence validation bind the rule;
it cannot exempt an executable producer, an acquired archive or a tool-overlapping member.
Factory now packages declared tarballs as mode `0644`, preserving `0755` for tools regardless of
source permissions or filename suffix. Contract tests check both source modes, identical retained
payload hashes and repeatable bundle identity. Rebuilt mixed bundles therefore differ from earlier
bundles that marked archives executable; earlier bytes are not relabeled or accepted by the new rule.
The local demo policy and real selected payload exercise remain outstanding. Dropping NICoSim
executables or treating the whole mixed bundle as not applicable would not satisfy the rehearsal.

On October 15, after explicit user authorization, a local patch added early version commands to both
NICoSim executables in an NVKE checkout based on `a566f7b46a5a5bb30981771053b131003b6f9b1c`.
The module build, uncached tests, pinned lint and coverage-registry check passed. Actual Darwin ARM64
binaries returned version JSON before normal configuration/startup, rejected unsupported arguments,
and created no files in an empty working directory. Both default `dev`/`unknown` stamps and explicit
`local-m3`, exact base commit and `dirty` stamps were exercised. This is an uncommitted local source
patch, not a new authenticated producer revision. A separate native Linux ARM64 build then passed
the server's `build-static` target, a CGO-disabled CLI build and version-package tests with the cached
Go 1.27.2/GCC toolchain. Both binaries returned the exact `local-m3`/base-commit/`dirty`/`linux/arm64`
fields inside the pinned Debian smoke image with networking disabled, a read-only root, nonroot user,
dropped capabilities and no-new-privileges. Exact payload SHA-256 values were
`d80beaca32eb8421b824c934eb2055b9bb2f91db6f1379896d9049cae1c92c15` (server) and
`cd0c9f66b1653d6475ba5332d255947163824e4878f0691323c1d3164bf6395f` (CLI).
This directly exercises version startup; it is not a signed smoke receipt or the catalog's producer
build. Fixed clean and patched NICoSim smoke profiles now have contract tests for source stamps,
patch presence and rejected tree states. The maintained NICoSim catalog declares the version-interface
patch against its retained `6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4` source pin, with exact patch SHA-256
`b45c745b03f1661572f00ce658499a6b260940c36293e03dfff4300ba940e167`. Its ordered bytes enter the existing
authenticated source-patch mechanism; uncommitted checkout bytes alone cannot. Patch applicability
against that exact pinned source was checked. Native attempt `m3-patched-all-02` subsequently built both
tools through this catalog patch, completed their terminal observations and emitted the NICoSim SBOM.
The produced server is 25,742,424 bytes with SHA-256
`6c7ce29798041d64446b8eb42342a72307b85089a7e764de555b1da4240a46f3`; the CLI is 10,705,328 bytes with SHA-256
`cfb827851ea98ff4c37bc5807631b0dcf3bbd44be2c9fce127a2a3eb7d3ff997`. These are scoped producer observations
from a completed 29-output Run and passed the patched profile in the successful local qualification;
the separately built
dirty payloads above cannot pass the patched profile.

The first complete patched-demo preparation on native Linux ARM64 passed bootstrap, toolchain validation
and dry-run for all 29 outputs. Complete smoke-policy admission exposed two fixture gaps: source-built
installer tools use `raw` producers, and the installer-aligned CLI uses its exact full source commit as
its version. The shared checker now admits those declared forms while retaining delivered executable-mode,
native ELF and exact source/version checks. Targeted regression tests passed, and a policy covering all
29 outputs (10 tool members, 17 exact deployment-image exemptions and one runtime-assets member) passed
admission against the prepared Plan. All producers subsequently completed in `m3-patched-all-02`;
the positive local qualification completed against that same Plan.

The first prepared build passed policy and signing-key preflight, then stopped before producers with
`NVKR-E-IMMUTABLE-VIOLATION`: lifecycle had retained compiler inputs before factory established workspace
ownership. Retention now belongs to factory after ownership admission and before runner preparation;
factory independently replays those inputs against the Plan and commits them without replacement.
Regression tests exercise that real factory boundary, reject unowned nonempty directories and mismatched
inputs, preserve existing snapshots, and check that dry-run writes no snapshot. The failed native attempt
is retained. Fresh native attempt `m3-patched-all-02` passed replay and ownership admission, retained
the compiler snapshot and completed all 29 outputs. Local-execution,
CLI and demo-adapter regressions, lint and the license-header check passed. This establishes the
corrected initialization path; it does not establish local qualification.

The local patch on upstream commit `adc0715a42f9efaa4725f2e61626736a55d2e879` passed all 56 packages in
the uncached full Go suite with bounded package parallelism,
formatting, license headers, vet, lint, shellcheck, CI-tool tests, vulnerability scanning, diff checks and
published-document checks. The native attempt also completed installer-tool builds, the patched installer
ISO, NVKE agent, kubeadm distribution and OVN image/chart/CNI producers, followed by the disk and final
Run. The final Run binds Plan `sha256:d552bf3de65cedd0316f4821a7837a6fc1a5e163edc49318d32a5d7113c04213`
and content subject `sha256:7ba2b696a8d392124d534f2ff4020e07167826a98805a4f9cd525a484ebd582e`.
The positive local qualification and controlled refusals passed with the references above.
The demo now supplies a repository-relative workspace from the
repository working directory, matching ordinary-output observation's existing path contract.
Passing repository checks does not establish complete local qualification.
