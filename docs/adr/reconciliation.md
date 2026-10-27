# Architecture implementation and evidence

Reviewed 2026-10-15. ADR-01–22 own the lane-specific engineering contracts pending named-decider
ratification. This ledger owns current implementation and remaining acceptance. Code and contract tests,
recorded external execution, and organizational approval are separate facts. Historical source and
schema results do not transfer to a changed candidate.

[ADR-23](0023-upstream-artifact-behavioral-validation.md) additionally proposes upstream artifact
test interfaces outside that consolidated set until acceptance. Implementation and local CLI proof
are tracked below; governance, CI and broader acceptance remain pending.

[ADR-24](0024-imported-validator-catalog.md) proposes an imported-validator catalog, explicit artifact-role
bindings and versioned diagnostic profiles outside the consolidated set. The provisional CLI catalog implements loading, discovery and frozen local-plan dispatch, with contract tests but no new
actual upstream catalog execution. Published-profile migration and controller/chart adapters remain deferred.

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

BuildKit preparation and export inherit the caller's work deadline and cancellation. If the caller has
no deadline, each transport operation receives a single twenty-minute fallback budget, shared across
its staging or export steps. No shorter per-step limit replaces an explicit caller budget. Failure
diagnostics and owned cleanup remain independently bounded. Completion requires the actual helper exit;
there is no progress-only polling subprocess or goroutine. Controlled-clock regressions cover inherited
and fallback deadlines, slow work, cancellation, late nominal success and strict exit status. Measured
archive sizes and caller time remaining are diagnostics, not authority. The causal batch-error regression
preserves the initiating failure before canceling siblings. These contracts do not establish a complete
fresh NVIDIA OVN demo or conformance run.

Local unencrypted direct-Packer execution retains an exact held QCOW2, observes its bytes and commits the
disk with its immutable Run record. Ordinary local disk publication remains closed. The explicit native
ARC hosted route is separately admitted under ADR-18. The
[recorded 34-output M2 candidate](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md) completed generated native
disk builds, publication, signing, verification and Run finalization under a validation-branch exception.
Its `build-provenance-v1` evidence binds live producer/material subjects and explicitly defers inspection.
This is scoped candidate pipeline-attested delivery, not a claim of main-only workflow execution,
independent full-payload disk consumption, strict full-disk inspection, organizational toolchain approval
or completed M1–M4 acceptance.

On October 16, 2026, the user extended the internal M2 candidate publication exception to the four
renamed toolchain subjects below. They were published from the previously measured local archives under
`ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/`. Exact-digest read-back compared every manifest and
configuration byte with its archive and downloaded every compressed layer through the registry client's
digest-verifying reader. Both GHCR packages report `internal` visibility.

| Image | Platform | Published manifest digest | Configuration digest |
| --- | --- | --- | --- |
| packer | linux/amd64 | `sha256:2a8d3ca5b447fe9d4694aa6cd977c6353299b336656b23688bc6dc11b8ab98f2` | `sha256:3f4d0b48004189cdde490973ee9daa4c073856adf92d9ce4c7f2ae17f2f64117` |
| packer | linux/arm64 | `sha256:1685a47997af96b802d050b31c212868e65626c614f5a69488141a9e979f2ad2` | `sha256:3499d012b6b54bde744a423dc9e55cd6e18d4a8272f9d5f9933a05f1ee16219b` |
| inspector | linux/amd64 | `sha256:d330a9f8bc17dad13399b9b56de2434bbf2a4ea8fe5511ee83531f1545de30d5` | `sha256:36aea677046c9453384dd5481e7158eaaf520ba66892f8131679ebed499fea91` |
| inspector | linux/arm64 | `sha256:0eeef5936cc090b1f89961d03a27688adddd9badd3ef236139e26771380b0119` | `sha256:be8b924b9c62aa871e15a263eb47ca8079e7f38cfaac11aea406e5dc72dc4fb4` |

This publication establishes registry transport availability with the publishing credential, not a rebuild
from the current PR head, hosted provenance or completed organizational license/security review. The source
locks retain their unresolved general approval gates. The subsequent
[hosted verification run](https://github.com/dsx-ai-factory/nvkr/actions/runs/38961706554) at
`2479ef56fa29415d4015b69f65e840192edbfb19` successfully pulled all four selected manifest digests with
this repository's `packages: read` `GITHUB_TOKEN` on native AMD64 and ARM64 runners. Both packages report
no linked repository, but the successful pulls demonstrate access for this workflow. That run's subsequent
entrypoint checks failed because Packer's temporary directory was read-only; it establishes pull access,
not successful native tool validation, a complete disk bake or inspection.

Strict disk inspection and reconciliation are deferred hardening outside the approved M3 MVP. Controlled receipt/export/transport
contracts and retained-disk diagnostics exist, but a successful real native NVKE live-Run full-content join
has not been demonstrated. Qualified release authorization, full v1 qualification/tag/revocation automation,
qualified provider adapters, secret-valued encrypted VM execution and air-gap operation remain unavailable.

The maintained M2 candidate selects 69 named outputs, retaining the previous 37 and adding CAPNICo,
explicitly named reference-profile fixtures, the provisioned infrastructure provider, NVKE telemetry pipelines and native bootstrap/telemetry
images. Both native architectures retain plain and public-bootstrap FDE disks. Native ARC publication admits those disks only with explicit
`build-provenance-v1`: actual producer encryption, wrapper annotations, signed disk metadata and expected
output membership must agree. Strict inspection/publication, qualified reproduction and private-passphrase
FDE remain closed. The consumer's strict evidence policy still rejects producer-only disks. This integration
has no recorded combined-pipeline, FDE publication or ARM64 FDE execution result; the earlier AMD64 diagnostic
Run below establishes only its exact bake and raw transport.

The separate FDE diagnostic route admits direct Packer only with explicit public `luks_passphrase=changeit`.
It retains raw QCOW2 bytes locally or through a generated native ARC build-only workflow, without release
publication, signing, inspection or confidentiality claims. Its separate `inputs/definitions/diagnostics/fde-amd64.yaml` selection
contains one AMD64 output and the complete material closure. Runtime admission and generated transport
contracts are complemented by [Run 38443178035](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035),
which completed the pinned FDE target and finalized a `static-unqualified` Run at code commit `68c219845398b8ef5890e5c76a13d4901a22630d`.
The [native ARC record below](#native-arc-disk-route) binds the exact output and retained evidence.
The [pinned-source audit](../../inputs/definitions/diagnostics/fde-audit.md) records retained bootstrap
files, SSH key distinctions, a GRUB cleanup defect and cloned-volume-key implications. The public value
requires no GitHub secret; private passphrases remain unsupported.

Local host observation and request/class matching additionally support Darwin ARM64 with an explicit HVF
kernel capability. Live Mac observation, descriptor-retention and single-use admission contracts are tested;
the existing direct-Packer driver additionally has a private execution-class interface with Linux sealed
descriptors and Mac private native executable snapshots. The shared driver contracts execute on Mac and
Linux ARM64; a catalog-pinned Mac Packer 1.15.4 version probe exercises the real native executable through
that abstraction. The Mac path is local and unqualified, without Linux's same-UID sealed-executable guarantee.
Mac Packer commands use a private session and cancellation group: a restricted-PATH terminal reproduction
stopped the original process-group-only version probe with SIGTTOU, while detached execution returned the
pinned version. This is launcher evidence, not a completed fresh artifact/deployment E2E.
Mac child execution owns a short private temporary directory through process-group cleanup because
Packer plugin Unix sockets cannot use arbitrary-length Run workspace paths. A real plugin validation
reproduced the long-path bind failure; socket lifecycle tests cover success, failure and cancellation.
Captured QEMU tools additionally retain their declared ROM/keymap data in the executable's private
prefix. Those files use the existing native data manifest and byte checks, with mutation rejection and
joined cleanup. Installed-host fallback is not used for data absent from the captured closure.
The local factory resolver additionally authenticates a selected native `packer-toolchain` binary bundle
against its exact Plan digest, actual host platform and purpose, then connects it to the shared Run/session
executor. Native mode requires explicit producer-declared host networking. Controlled tests cover exact-node
binding, prepared source/materials, actual host admission, output observation, SSH dependency execution and
cleanup; they do not establish a fresh Mac QCOW Run, VM deployment or conformance. Native output
collection retains each declared same-invocation acquisition carrier through the existing material validator
and bounded collection policy; malformed, missing, symlinked or over-budget carriers fail the disk build.
Controlled native tests establish retention after workspace cleanup and revocation on artifact close.
The existing acquisition reader accepts both its renamed envelope and the exact original
`nkd.dev/package-acquisitions/v1` envelope emitted by upstream NVKE. Contract tests preserve the source
schema and byte bindings and reject unknown versions; this does not establish a fresh captured Mac image.
Local native preparation captures dependency bundles through the existing delivery owner and validates them
through the Run's Packer resolver. Go and Packer preparations share output-scoped overlay generation and
complete-graph validation. Darwin Packer binds its native bundle digest separately from the Linux vendoring
image. Native firmware uses the same captured file owner and stable logical names rather than installed host
paths; source-location independence, byte binding and joined cleanup are tested. The real 32-output demo
selection compiles with required HVF and host CPU while preserving its source and material selections.
An earlier legacy OS selection needed alignment with the native startup's agent namespace and was
canceled before QCOW assembly. The later native selection's scoped build evidence is recorded below;
it does not establish complete demo acceptance.
The demo BUILD executor additionally accepts explicit Definition, Environment and native NVKR selections
on either host. It compiles through NVKR's Plan owner and uses the same local Run invocation and native
tool preparation. Repository-contained input paths and local-only Environments are checked before
preparation; controlled tests reject changed invocations, escaped paths and publication requests.
Default Linux bootstrap and qualification remain separate from explicit input preparation. This interface
and its real native Plan preparation check do not establish a complete Mac BUILD-to-DEPLOY result.
The normal Mac BUILD additionally selects the same committed product Definition and its Mac Environment,
discovers the documented installed native dependencies, and delegates bundle/input preparation to those
existing NVKR owners. It reuses the Linux utility-image builder and shared catalog-scope/relative-input
projection before the captured local Run. Controlled tests cover invocation order, missing prerequisites,
unchanged explicit selections and capture selection; they do not establish a fresh customer build or deploy.
A separate local execution completed a selected installer archive Run and its native CLI tool Run through
this BUILD interface. That bounded check excluded OS-image assembly and deployment acceptance.
Installed-SDK CGO tools and a real HVF guest have run separately. Apple system utilities execute only at their
root-owned protected paths with matching bundle bytes; other Mac tools use private snapshots. Actual native
utility execution and incorrect-digest rejection are tested. No system security setting changes. Hosted
rendering rejects native bundle locators, and qualified authority boundaries remain unchanged.
The native installer source and CAPI version selection now comes from replayed captured tool-Run inputs, checked against the completed Plan and selected output. The producer emits the source Make value; executable and installer source bindings must agree. Packer discovery projects versions and plugin members from the shared catalog rather than a compiled version list. Contract and actual-source Make checks cover this selection; no complete new tool-Run or runtime acceptance is inferred.
Temporal Helm image tags now project from each admitted output's resolved prebuilt declaration while
retaining its observed child digest. The deployment and nightly consumers use that same projection.
Compiler/save/reload, changed-tag and refusal contracts cover this descriptive selection; no fresh
payload build or runtime acceptance is established.
Prepared image carriers move from asset staging into the attempt's simulator serving directory through
the existing descriptor-relative no-replace publisher. Both fresh private directories share the attempt
filesystem; each runtime image path follows its completed move, including partial failure. The host-only
provider stays outside the serving directory, and guest tools/CA retain their required source copies.
Archive/HTTP fixtures cover inode and byte preservation, occupied/linked input refusal, partial paths and
provider exclusion. Original Runs are untouched; no live time or space improvement is established.
The native Mac TPM adapter uses the supported simulator QEMU override and installed QEMU/swtpm, preserving the inherited network descriptor and process group. Earlier interface and paused-device observations have no recorded attempt/source bindings here and are not acceptance evidence. Guest TPM discovery, site attestation and complete native runtime acceptance require an identified Run.

## External test output and local diagnostic boundary

`testresults` implements bounded JUnit/CTRF descriptions and deterministic serialization in lane
**none (descriptive)**. `validation` dispatches explicit named checks; `conformance` implements the
`kube-conformance` / `hydrophone` diagnostic adapter. Lifecycle authenticates selected product/tools
publications and exports held executable bytes; catalog, buildgraph, acquisition and delivery retain
production ownership. Original reports, product/tool source provenance and before/after observations
are retained. Normal harness exit status is a fact with an unknown cause, never a passing verdict.
Local/disposable acknowledgements are operator declarations, not proof of locality or exclusive ownership.

Contract and process-fixture tests cover guards, capture, cleanup and dispatch. They do not establish
successful external execution of the corrected published-source route. Report parsing, original JUnit
validity, continuous installed-byte proof, AI/user adapters, Definition applicability detection,
qualification signing and promotion from these outputs remain unimplemented.

Explicit complete local diagnostic Runs are now captured through a lifecycle-owned capability,
including original Plan/compiler inputs and held payloads. Product and tools may share one capture;
local reports omit hosted qualification inputs. Delivery retains distinct named native image output and
evidence bindings when their exact manifest bytes are shared. Conflicting aliases, repeated logical names
and duplicate direct descriptors remain rejected; physical closure accounting uses distinct digests.
Round-trip and rejection contracts cover this case; they do not prove a full native conformance run.

The shared native demo has an opt-in Mac/Linux ARM64 conformance hook using
matching suite/tool outputs from the same fresh artifact Run.

The shared native startup imports the ordinary named OVN image under its exact native digest and
projects matching chart values after package installation and before agent startup;
the OS package's independently built image is not treated as the named output.
The shared site startup imports each selected image through containerd's local manifest importer, preserving its repository alias and checking that exact reference through CRI before bootstrap. CRI owns the managed label: pre-setting it skips the config-ID alias required by image-volume mounts. Runtime evidence is limited to the identified Runs in [Local ARM64 demo presentation](#local-arm64-demo-presentation).
The native adapter projects Kubernetes version from the selected NVKE installer's shipped site-spec template, rechecking those verified payload bytes at execution boundaries. Site packages, bootstrap, tenant requests, readiness and optional conformance share that source-selected version; current authored conformance inputs select 1.36.3.
Tenant waiting now refreshes the exact owned NVKE tenant and worker topology through the existing
consumer owner before each kubeconfig/node observation. It publishes only the existing typed conditions
and refuses NVKE's exact `Provisioning=False` / `Provisioned=False` failure predicate; missing or Unknown
conditions do not prove failure. API progress advances only after a successful decoded node query, and
controller Provisioned does not replace the separate node, cloud-init, DNS or workload gates. Owned
subprocess fixtures cover condition transitions, transient reads, replaced identity, stage ordering and
retained resources. These contracts do not establish fresh runtime acceptance of this change.
Native tenants use NVKE's generic CAPI NicoCluster/NicoMachineTemplate input with source-generated join-token bootstrap and retained mTLS. NVKR supplies package installation and exact local image imports as pre-kubeadm commands; NVKE owns credentials and its final agent restart. Because commit-string agent versions can bypass the controller's semver-gated cloud-init check, the adapter independently observes each fresh Ready node's actual host cloud-init status without wait mode through a temporary Pod with a recursively read-only host mount. The shared conformance invocation selects an attempt-owned scratch directory on its output filesystem, retaining the factory's disk-backing/headroom checks. Recorded runtime evidence and its consumer/artifact revisions appear in [Local ARM64 demo presentation](#local-arm64-demo-presentation). The old native Release-template rewriting and compiled template-hash lists remain removed.
The site-create request carries the source-selected MachineImage boot script,
rendered with the existing NVKE disk-policy template and current local disk URL.
Its admitted iPXE selection also supplies DHCP address mode to both site readiness
and bootstrap address checks; standalone guests retain the static two-address contract.
Owned-subnet, reserved-address and duplicate-address rejection remain shared.
These source and shell contracts do not prove fresh runtime import, DHCP callback
identity or conformance acceptance. Strict manifest observations remain required.

Linux retains sealed executable descriptors. Mac local diagnostics hold authenticated Darwin
bytes and execute private Mach-O snapshots, checked before/after each command
and removed after joined process cleanup. This detects replacement between
commands, without Linux's sealed-descriptor guarantee against hostile same-user
writers. Published validation, signing and qualification admission remain Linux-only. Focused owner,
adapter and dispatch tests establish those contracts; the full native demo plus conformance remains
externally unverified. Existing validation-vector v3 and qualification-evidence v2 are unchanged.
[ADR-16 §6](0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
owns diagnostic execution and passive external qualification; [ADR-18 §3](0018-pipeline-ir-and-ci-adapter-emission.md#3-helper-owned-isolation-and-narrowly-admitted-native-arc-disks)
owns the host isolation posture. The [operator reference](../external-test-output-contract.md)
owns prerequisites, selection and capture limits. These proposed contracts establish no organizational ratification.

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
| M3-A | Complete-publication artifact authentication | This section | [Epic M3](../epics-v1-spec.md#epic-m3--qualification) |
| M3-B | Native delivered-tool smoke | This section | [Epic M3](../epics-v1-spec.md#epic-m3--qualification) |
| M3-C | Qualification verdict and evidence publication | This section | [Epic M3](../epics-v1-spec.md#epic-m3--qualification) |
| M3-D | Positive and negative external acceptance and independent review | This section | [Epic M3](../epics-v1-spec.md#epic-m3--qualification) |
| M4-B | Connecting manifest issuance to the CLI and generated workflow | [M4 release manifest issuance](#m4-release-manifest-issuance) | [Epic M4](../epics-v1-spec.md#epic-m4--automated-releases) |
| M4-C | Release manifest schema and issuance | [M4 release manifest issuance](#m4-release-manifest-issuance) | [Epic M4](../epics-v1-spec.md#epic-m4--automated-releases) |
| M4-D | Tags, publisher exclusion, consumer verification instructions and interruption demonstrations | [M4 release manifest issuance](#m4-release-manifest-issuance) | [Epic M4](../epics-v1-spec.md#epic-m4--automated-releases) |

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

### Local M3 demo rehearsal

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

## M4 release manifest issuance

M4-C adds the `pkg/nvkr/releasemanifest` owner and the
registered `nvkr.dev/release-manifest/v1` schema and `nvkr.release-manifest` family (ADR-01). Issuance takes
the live M3-A artifact-check receipt, the exact canonical v2 qualification evidence and an independently
selected qualification identity and policy digest. It refuses failed, incomplete, noncanonical, mismatched,
wrong-identity or wrong-policy evidence before signer construction, then authenticates the exact evidence on
every member subject before attesting the manifest to the publication root under the qualification identity
and verifying the exact attached bytes. The artifact-check receipt now also projects each output's source lane
and exact predicate digest from its already verified production provenance. `pkg/nvkr/sign` adds the distinct
`https://nvkr.dev/release-manifest/v1` attestation type over the same exact-predicate attach and verify path
used by qualification evidence.

`pkg/nvkr/releasemanifest/manifest_test.go`, `pkg/nvkr/releasemanifest/issue_test.go`,
`pkg/nvkr/lifecycle/qualification_release_members_test.go` and `pkg/nvkr/sign/release_manifest_test.go` cover
the strict schema and deterministic identity, exact provenance coverage and closed source lanes, pre-signer
refusals, a later member's missing or wrong-signer evidence despite earlier surviving attachments, manifest
attachment and verification failures, receipt revocation and cancellation between steps, and type, subject,
identity and issuer substitution in the cosign adapter. These are contract tests with fakes and a stubbed
cosign runner.

M4-B connects issuance through `run qualify --issue-manifest` and the generated production qualifier.
The qualification owner retains the same live source, invocation, artifact and smoke receipts through
publication and issuance, and freezes the exact canonical published evidence independently of mutable
diagnostics. The manifest adapter derives its expected qualification identity and policy digest from the
single independently loaded policy snapshot. Development policies refuse production issuance before
provider or publication work. A later manifest failure preserves qualification diagnostics and returns a
nonzero outcome; no surviving attachment is reported as a completed release. CLI and orchestration
contracts cover terminal failure reporting, receipt lifetime and lane isolation; generation contracts
require issuance only in the production final signer. These are local contract tests, not hosted acceptance.
No hosted Run, OIDC manifest attestation, registry read-back or independent consumer verification is
recorded. The tag rule, tag creation, publisher exclusion, consumer verification instructions and
interruption demonstrations remain M4-D.

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
| 11–12 Resolution | Exact output-selected producers and concrete platforms, including local Darwin ARM64 executable acquisition and explicit Linux-toolchain cross-builds; complete explicit disk material closure; predecessor-bound whole-producer overlays | Finite-set embedded compatibility and accelerated-runtime rendering remain deferred; no matching, mixins, wildcards or deployment classification |
| 13 Evidence | Executable-target SBOMs select architecture-specific toolchain pins for Linux and Darwin ARM64 while preserving native Go compiler identities, without widening execution admission; exact offline binary/image dependency scanning, separate `InspectDebianArchiveMetadata` diagnostics and bounded Helm metadata inventories, including acquired raw charts; separately scoped archive member/hash inventories for source tarball assets in executable bundles; stage-bound signed CycloneDX bundles; M2 live disk producer/material evidence with explicit deferred inspection; guarded installed-inventory and full-content export receipt APIs | Archive inventories describe files, not package dependencies; their hosted producer path remains unexercised. Factory callers retain ordinary hint-free binary inspection; no maintained factory output invokes the separate Debian metadata diagnostic. Dependency SBOM attachment and serialized BOM validation reject its method. Native Debian delivery and payload dependency inspection remain deferred. Real native NVKE full-content inspection/reconciliation and original-live-Run joins remain unexercised. Vulnerability scanning, acquired upstream-signature verification and qualified signed evidence publication are outside v1 |
| 14 Delivery | Executable/APT/Helm formats, byte-preserving acquired charts, image-bound source charts, RAW/ISO measurement, Linux held-qemu envelope verification, local held-QCOW2 retention and streamed plain/public-bootstrap FDE OCI disk wrappers | Independent full-payload signed M2 disk read-back, real strict receipt-to-package/finalization and unmodified Helm/ORAS binary interoperability remain unexercised; private-passphrase FDE, strict FDE and object-store delivery are unavailable |
| 15 Air gap | No mirror implementation or CLI retained; supported offline signature primitives remain | Air-gap product lifecycle and zero-egress acceptance require a new admitted design |
| 16 Release gates | Inventory/ReleaseSet/PromotionPolicy documents, exact member completeness and signed diagnostics; M3-A Run/policy artifact checks, generated exact-attempt handoff and held complete-set receipts; M3-B isolated native tool smoke and authenticated complete-set receipts; M3-C policy-only orchestration, v2 qualification evidence, receipt-only publisher, complete pre-sign gates and failure diagnostics; M4-C release-manifest schema and receipt-gated issuance | Real candidate native smoke, hosted worker/qualifier identities, qualification signing/attachment and independent M3 consumer acceptance remain unexercised. M4 manifest issuance is invoked by `run qualify --issue-manifest` and the generated production qualifier, with local contract coverage only; hosted OIDC attestation, registry read-back and independent consumer verification remain unexercised. Qualified clearance, release-specific trusted time, M4 tags and revocation remain unimplemented |
| 17 Public lifecycle | Compiler-owned output joins, immutable attempts, failure records, process-owned snapshots, exact observations, deterministic disk retention, diagnostic output pulling, bounded descriptive progress, typed result browsers for interactive commands, read-only stored Run inspection and SDK/CLI delegation; Linux M3-A Run/policy CLI, captured-input replay and retained file diagnostic orchestration; M3-C policy-only qualification orchestration with generated-policy admission | No general resume protocol or authority restored by serialization; actual M3 candidate/provider acceptance remains unexercised. Strict local publication, private-passphrase/strict FDE publication and qualified lifecycle remain unavailable. Fresh live dashboard Run, native Linux race acceptance and human usability validation remain unexercised |
| 18 Execution and GitHub | Shared local/hosted producer launcher, held source/vendor lifetime, capability admission, manual-only GitHub producer generation, exact optional publication-branch allowlist, separate full-development branch workflow, mode-specific concurrency and artifact retention, scoped credentials and unconditional finalization; inline named jobs with ancestor-preserving dependency reduction and bounded exact-result collectors with diagnostic hosted evidence; at most two optional summaries per attempt with operation details in logs; explicit native ARC disk grouping, raw build-only disk transport and optional native hosted platform maps (including separate CLI output slots); authenticated independent qualification invocation/source-event binding; generated fixed native worker and qualifier with separate credentials; separately authored downstream NVKE E2E adapter configured only for native ARM64 ARC, outside product execution | Production hosted qualification, retry qualification of the qualifier itself and independent-consumer acceptance remain unproved. The [development record](#feature-branch-pipeline-development) documents successful qualification of the earlier main-source candidate; hosted validation of the current v0.22.0 selection remains pending. The downstream adapter trusts privileged pinned NVKE code and yields unsigned diagnostics; its first scheduled attempt failed before runtime and no successful combined cycle is recorded. Default-branch push publication and in-repository PR build triggers remain deferred; arbitrary self-hosted/other-provider routes and qualified emitters fail closed. Provider configuration and fresh runtime evidence remain separate |
| 19 Downstream boundary | Exact required/observed output membership, component evidence joins and noncircular release-content identity | Downstream owners supply topology, placement, provisioning and runtime qualification; historical topology records grant no current authority |
| 20 Publication | Digest-selected sibling repositories, signed evidence membership, native OCI/Docker subjects with consistent named-output aliases and independent evidence, aggregate runnable OCI indexes with exact native-child evidence, bounded metadata navigation, selected output payload-closure retrieval and per-output assembly/finalization; consumer held-closure export bound to exact-input receipts; candidate M2 hosted signing/publication completed; public-bootstrap FDE build-provenance publication supported; development attempt namespaces and scoped GHCR cleanup; hosted GHCR package privacy admission using metadata-derived destination references and an exact-root-bound push | Package admission has local contracts, not a hosted private-publication or public-source access proof; independent grants, historical provider payloads and future visibility changes remain operational obligations. Hosted GHCR legacy layouts without the metadata contract fail closed. Main-only Plan execution, independent M2 full-payload disk consumption, strict full-content publication and object-store/CDN transports remain unproved or unavailable; registration is deferred |
| 21 Generation | Exact recorded input selection, independent fixed production/development layouts, manifest-owned semantic action interfaces and Plan-derived invocation data with expanded-step validation, output-local ordinary job membership with addition/removal and failure-accounting contracts, stable result-collector membership and exact retirement of legacy producer children, manifest-last ownership, deterministic checks/retirement, canonical Plan inspection, linked graphs, semantic diffs, atomic pin maintenance plus regeneration and review patch workflows; separately owned qualification pair and runtime policy drift admission; optional named production namespaces independently own their manifest, lock, paths and retirement; opted-in named runtime Plans compile current source pins within the complete reviewed workflow/action and Environment contract and distribute frozen data through same-Run helpers; provider artifacts contain JSON locators while private CI carriers use a digest-bound GHCR transport with package visibility and linked-repository admission | Producer generation emits manual-dispatch workflows, not cron; optional qualification generation emits its fixed event/call pair. Authored downstream scheduling has separate ownership. Combining development with a nonempty named bundle is rejected before catalog resolution or generated-output work. The inline layout has guarded dispatch admission and isolated exact-result/failure diagnostics; complete production execution, automatic PR publication and scheduled/private-source service acceptance remain unexercised. The runtime Plan handoff and private CI carrier transport have local contracts; a hosted private-carrier producer-to-E2E cycle remains unexercised. Public-repository execution requires an independently admitted storage credential/access policy; public-linked storage fails closed. Historical Actions payloads and caches are not migrated by this implementation. The workflow `catalog update-ci` renders (installed as `catalog-refresh.yaml`) opens PRs with a GitHub App installation token minted in its `pull-request` job by the SHA-pinned `actions/create-github-app-token` action for this repository only, with contents, pull requests and workflows write, and revoked by the action when the job ends; the workflow token stays read-only (shape, action inputs, bot sign-off identity and step behavior contract-tested with a stubbed `gh`; a manual dispatch on 2026-10-16 failed at the earlier hand-rolled token step with an unattributed 404). No scheduled run, token exchange, push, PR, automatically started PR check, close or branch deletion has been exercised, so M4 acceptance item 1 is implemented but not demonstrated and M4 is not claimed. The review commit is created through the GitHub API with a DCO trailer for the App's bot user (contract-tested; GitHub signing and attribution not verified). Pin-only fixtures assert the production, development and qualification workflows stay identical; a topology, label, interface or action-pin change that regenerates a closure workflow is published in the same PR. One enrolled policy is supported until generated bundle roots are per-Definition. A branch that already holds the candidate is left alone, a closed unmerged PR whose head commit carries the same candidate stays closed even after its branch is deleted, fork PRs from a branch with the same name are ignored, a lookup page that could be truncated fails the step, and an unchanged Definition closes its stale PR and deletes its branch (contract-tested with a stubbed `gh` that applies each call's real filter). The ruleset, last-push approval and code-owner settings that protect the bot branch are repository settings that this workflow does not enforce; the release owner or an administrator must validate them before scheduled writes are relied on. The policy names the dependent development Run, qualification pair and additional named Runs (the two nightly bundles, checked and regenerated through the runtime-Plan owner because they compile current pins at dispatch); refresh admits each additional Run's catalogs before compiling it, and checks, regenerates and transports every member through its owner on an exact allowlist, refuses an unnamed committed bundle and a generator stamp that differs from the committed Plans (contract-tested with real local Git for the production and development bundles, and through the real qualification owner on a fixture with synthetic trust pins that contacts no GHCR or cosign; deployment-image selection is outside the closure and must be coordinated by hand before a moving release), and `make workflows-check` compares the installed workflow with `catalog update-ci` output (not exercised in the hosted gate). Producer matrices and general callable workflows remain deferred. Qualification generation does not make both owner writes one atomic transaction; lock and generated files are separate transactions; complete crash matrix remains |
| 22 Pins | Catalog v3/lock v1, per-module tags, source/artifact/action/tool pin maintenance, no-op/check/CAS semantics, derived pin sets including NVKE beta local-chart version omission and descriptive artifact mappings, same-release maintenance groups, whole-producer overlays, exact toolchain substitutions and authored embedded-version metadata authentication | Pin-set loading needs pinned Git access. Curated graph/SBOM edges remain deferred. Optional metadata consistency is not disk-content inspection, external acquisition or production acceptance |
| 23 Artifact test interfaces | Proposed ADR merged in PR #139; Go Plan helper merged in #156; #138 replaces its Python diagnostic with Go factory/source/execution owners; separate IV1 upstream bootstrap no-build interface candidate described in the [guide](../artifact-testing.md#bootstrap-e2e-interface-candidate-iv1-first-slice), without an NVKR bootstrap execution adapter | [Immutable interface source](https://github.com/dsx-ai-factory/nvke/commit/f33b7afb55d421958540589ca03d206531bfa16b) and its [Linux CLI job](https://github.com/dsx-ai-factory/nvke/actions/runs/39347477494/job/118383877388) record a passing bootstrap contract package within a failed aggregate run, not real bootstrap execution. No immutable real-bootstrap Run exists; the [guide](../artifact-testing.md#bootstrap-e2e-interface-candidate-iv1-first-slice) pins the reviewed follow-up source and local-check limits. [Implementation guide](../artifact-testing.md) separates the Go macOS ARM64/Docker fresh-build proof from historical Python native results. Upstream adoption, authenticated output membership, exhaustive suite acceptance, maintained-pin adoption, kind, CI and CAPD/CAPNICo acceptance remain outstanding; no conformance or qualification authority |
| 24 Validator catalog | Provisional CLI discovery and local-plan profiles: strict bounded catalog/profile loading, deterministic discovery, registered `nvke-cli/v1`, complete local-plan admission and frozen dispatch through factory/sourcegit/pipeline | Discovery, refusal, snapshot edits, both suite/architecture bindings and mocked dispatch are contract-tested; [guide](../artifact-testing.md#provisional-validator-catalog) distinguishes this from actual upstream execution. Native AMD64/ARM64 catalog runtime acceptance, legacy-command migration, new-format published adapters, controller/chart and second-repository adoption remain outstanding. Existing published authentication/capture semantics, unknown exhaustive completeness, unauthenticated artifact membership and no qualification authority are preserved |

## Feature-branch pipeline development

On October 20, the beta.6 [development Run 39467682189](https://github.com/dsx-ai-factory/nvkr/actions/runs/39467682189)
failed all four OS-image producers during OVN builder preparation, before Packer. The unchanged
`quay.io/projectquay/golang@sha256:506a668eb4c763a964ffc4d4ad0125d45d75aac899392be7e5d39380f8452564`
returned `MANIFEST_UNKNOWN` both on the runners and in a direct registry read. Authored OVN builder
arguments now select the Go 1.26.10 multi-platform index
`sha256:d5acf07940fb5e6e2d07542abd853cece623d95ffd364769ec825aab5471d3b8`; registry reads verified
its AMD64 and ARM64 manifests and recorded Go version in a point-in-time manual check.
The default pin-binding contract checks catalog literals without contacting the registry; it does not
continuously establish availability. The selected standard source Dockerfile requires the
existing DNF-based builder family, and its vendored JSON adapter remains incompatible with Go 1.27.
The separate fork at `8af51273d52dfa07454435d18d37230e582d797a` uses
[Dockerfile.ubuntu.nv](https://github.com/dsx-ai-factory/ovn-kubernetes/blob/8af51273d52dfa07454435d18d37230e582d797a/dist/images/Dockerfile.ubuntu.nv),
which hardcodes Go 1.26.9 at `sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6`
and ignores `BUILDER_IMAGE`; its unused argument is removed, not credited with a compiler repair.
This builder correction does not itself change the NVKE, SPIRE or harness source selections. A complete
hosted build with the replacement remains pending.

The current candidate selects stock NVKE `v0.22.0` at
[`400c24fb6465b042be883d6b62472528520d6291`](https://github.com/dsx-ai-factory/nvke/commit/400c24fb6465b042be883d6b62472528520d6291)
through `catalog pin --tag` for all maintained NVKE product-build source roles,
including SPIRE packaging. Chart versions, native demo source admission and catalog
overlay bindings follow that same release. The two independent nightly
test-harness selections retain `b4f90e2c4fc4ed3d07efc18a22066a7e0b9b99c8` because the
release omits their reviewed candidate entrypoint; they are not product-build pins.
The Packer toolchain lock's `validatedNVKE` record remains historical validation of
`726f76944404e1106626b8333d3576bdf67b575b`, not evidence for the current release.
Hosted validation and a complete fresh local E2E/conformance result for v0.22.0
remain unrecorded here; the successful records below retain their original source scope.

At NVKR `36dd99fe3aa0c7dc6cce72b272da93d6ab8ba355`,
[development Run 39343181108, attempt 1](https://github.com/dsx-ai-factory/nvkr/actions/runs/39343181108/attempts/1)
completed all 580 jobs successfully, including all four OS disk builds, output publication/signing/verification,
aggregate assembly/publication/signing/verification and final completeness. The Nktp package remained
internal with no repository association on API read-back. The aggregate publication root was
`ghcr.io/dsx-ai-factory/nvkr/development/runs/39343181108/attempts/1/releases@sha256:4f00024a7e63a0c862bdc63e77e37953c895837d910cbbbc523614c99c975c22`.
The later production sibling-routing correction does not change this valid development path;
[Merge Gate 39344901584](https://github.com/dsx-ai-factory/nvkr/actions/runs/39344901584)
passed on merged branch head `7a4457ce824659786110e328e98da00ac45d8002`.

[Attempt 2](https://github.com/dsx-ai-factory/nvkr/actions/runs/39343181108/attempts/2)
successfully reran aggregate publication and its dependents. Provider job records retain 573 original
successful name/start/completion/result tuples, including all four disk builds and aggregate assembly.
Retry publication, signing, verification and final completeness passed with the same aggregate digest in the
attempt-2 namespace, using retained output references from attempt 1. Independent development
[qualification Run 39356370279](https://github.com/dsx-ai-factory/nvkr/actions/runs/39356370279)
reached the artifact checks against that exact successful attempt, but both native workers returned
`NVKR-E-TIMEOUT` at the development adapter's separate fifteen-minute deadline, before native smoke.
ARM64 completed payload/SBOM checks and reached output provenance checks; AMD64 expired while
materializing `nvke-node-image-arm64`. The development adapter now inherits the hosted owner's existing
fifty-nine-minute deadline, matching production handoffs, while retaining individual operation bounds.
[ADR-16](0016-release-gates-and-digest-first-selection.md) records earlier caller deadlines, joined cleanup
and why this allowance is not a guaranteed return bound.
Existing hosted-budget contracts cover earlier caller deadlines and cancellation; corrected development
execution in [Run 39358337890](https://github.com/dsx-ai-factory/nvkr/actions/runs/39358337890)
passed the ARM64 artifact checks for all 69 outputs and native tool smoke, then both native workers
failed report signing because Cosign 3 rejected the explicit issuer with its default signing configuration.
The exact-byte signer now explicitly selects detached output compatibility, raw signature/PEM encoding
and public transparency submission. Injected and held-process protocol tests check those options;
[Run 39360715596](https://github.com/dsx-ai-factory/nvkr/actions/runs/39360715596) on
`8422f128e6b776ae5f353165893cd4085414bb4d` passed both native workers: each authenticated all 69
outputs, passed native smoke and signed/uploaded its report. Final qualification also passed all 69
artifact checks and signed/read back qualification attachments for exactly those 69 unique subjects.
The [final result job](https://github.com/dsx-ai-factory/nvkr/actions/runs/39360715596/job/118430919815)
reported `successful: true` with development evidence digest
`sha256:b89ca5cfb49f78ac2fc687f000890ad043813f0c5ba584a351598e6d20eea062`.
Delivered-tool smoke passed for `nvkectl`, `nvkectl-linux-arm64` and `operator-tools`; the other
66 outputs correctly report that tool smoke is not applicable. The same head passed
[Merge Gate 39360705380](https://github.com/dsx-ai-factory/nvkr/actions/runs/39360705380).
These development observations do not establish production qualification,
full-release consumer acceptance or a live local-demo deployment.

At NVKR `92ce9fc65182a965f94ffa8a527828a971e9fc45`, [development Run 39327360895](https://github.com/dsx-ai-factory/nvkr/actions/runs/39327360895)
passed all four disk builds, output publication/signing/verification and aggregate assembly. Aggregate
publication then rejected the Nktp package's repository association with `NVKR-E-TRUST-POLICY-MISSING`.
Read-only GitHub package metadata identified `nvkr/development/runs/39327360895/attempts/1/nktp` as
internal but linked to `dsx-ai-factory/nvke`; the other 138 packages in that run's inventory had no association.
Helm packaging now omits the manifest-level source annotation that can cause GHCR auto-linking while
preserving source URLs in the chart archive and Helm config. Regression coverage checks both properties;
the successful development Run above exercises this packaging correction. Existing published bytes are not rewritten.
The operator disconnected the existing Nktp package; API read-back confirmed internal visibility with no
repository association. Failed jobs were rerun as attempt 2 of the same Run, retaining successful producers.
Attempt 2 restored the original metadata, but aggregate publication incorrectly projected payload references
into attempt 2 and failed with `MANIFEST_UNKNOWN` for the attempt-1 CAPI controller output. Delivery now
derives output and evidence locations from their recorded original publication namespace, rejecting
foreign registries/repositories/runs and future attempts. Regression coverage includes mixed-origin assembly
and production sibling isolation. The successful attempt-2 publication above exercises retained output
namespace resolution, and its complete retry passed. The immutable failed Run cannot acquire
new executable code through a rerun.

The historical October 19 main-source upgrade selected `55612dce98cf0183790fbe5f4776225d63369fc2` through `catalog pin --commit`, retaining all three SPIRE package roles and the separate nightly test-harness exception. Canonical generation and [Merge Gate 39306909978](https://github.com/dsx-ai-factory/nvkr/actions/runs/39306909978) passed on NVKR `5953a0ac`. [Development Run 39306902739](https://github.com/dsx-ai-factory/nvkr/actions/runs/39306902739) remains incomplete: [provisioned-provider signing](https://github.com/dsx-ai-factory/nvkr/actions/runs/39306902739/job/118254801119) failed while fetching ambient GitHub OIDC credentials because the response was not JSON. All four disk outputs nevertheless passed build, publication, signing and verification in that Run. The later successful complete Run and same-run retry are recorded above; this earlier incomplete Run does not supply that evidence. The preceding [beta Run 39276513915](https://github.com/dsx-ai-factory/nvkr/actions/runs/39276513915) failed disk production and completeness. Both plain disk logs reached an absent-file error during provisioning; the [selected source composition](https://github.com/dsx-ai-factory/nvke/blob/55612dce98cf0183790fbe5f4776225d63369fc2/os-image/packer/scripts/compose-image-inputs.py) adds the two missing network configuration files, with [source regression coverage](https://github.com/dsx-ai-factory/nvke/blob/55612dce98cf0183790fbe5f4776225d63369fc2/os-image/packer/scripts/test_build_entrypoint.py). The four successful disk outputs in the main-source Run exercise this source correction; they do not establish complete Run acceptance.

Beta development [Run 39274459086](https://github.com/dsx-ai-factory/nvkr/actions/runs/39274459086)
reached admitted native disk builds and successful SBOM signing after the operator approved its exact branch
in both development environments. Publication jobs
[118139197664](https://github.com/dsx-ai-factory/nvkr/actions/runs/39274459086/job/118139197664) and
[118139250021](https://github.com/dsx-ai-factory/nvkr/actions/runs/39274459086/job/118139250021)
then failed during helper locator acquisition with HTTP 503. The locator API owner now admits ADR-18's
bounded transient-server retries. Recovery, exhaustion, cancellation and server-delay cases are added to
the contract suite; [Merge Gate 39276232245](https://github.com/dsx-ai-factory/nvkr/actions/runs/39276232245) passed on `8dc975de`. Local execution remains skipped at the operator's request.
This is not a successful complete development Run or qualification claim.

The explicit `githubDevelopment` Environment setting selects separate fixed workflow/Plan/manifest paths
with the same compiler, complete graph and helper. The committed development bundle now uses the full
69-output M2 Definition, including all four native plain/public-bootstrap FDE disk outputs, output-local publication, SBOM attestation,
cosign signing, verification, metadata assembly and finalization. It retains the candidate's existing disk
and source limitations; development execution cannot approve those production exceptions. Development
finalization retains its Run record without emitting the production qualification handoff; explicit handoff
admission returns a capability error before qualification evidence collection. Production handoffs retain seven days.

The separate development qualification path now recovers original public metadata controls from an
API-authenticated successful manual development attempt, replays its exact source inputs and delegates to
the existing artifact checks and native smoke owners. Generated `nvkr-dev-qualify.yml` accepts an exact
attempt URL on a reviewed nondefault branch; its source branch may differ. The fixed reusable worker and
final qualifier use development-only credentials and distinct evidence schema/predicate. Production evidence
admission is unchanged. The development pair has independent manifest ownership and runtime drift checks.
Local contracts cover source/qualifier selection, metadata integrity and restoration, production rejection,
namespace binding and generated credential/owner separation. Catalog maintenance names and regenerates the
development pair with its producer, so pin updates cannot leave its manifest stale.
Development qualifier retries may retain earlier successful workers within the same qualification run,
preserving their original branch, source/policy bindings, certificate invocation and private-carrier attempt.
Each output publication retains its original source attempt and namespace. A later aggregate attempt
may reuse earlier same-run outputs after exact invocation and evidence checks.
For [development Run 38951964907, attempt 1](https://github.com/dsx-ai-factory/nvkr/actions/runs/38951964907/attempts/1),
current code reproduced the original Plan digest
`sha256:c6373e0b96f182b596ca89eeb6d7f66ee17b9126e4da236a0c5d583a0d1d12b6`.
On 2026-10-16, the stock pinned Linux ARM64 cosign adapter independently downloaded and verified
both signature and exact original SLSA provenance for all 70 subjects (69 outputs plus release),
using the exact `nvkr-dev.yml@refs/heads/codex/private-ghcr-combined` identity and GitHub issuer.
The immutable release subject was
`ghcr.io/dsx-ai-factory/nvkr/development/runs/38951964907/attempts/1/releases@sha256:d49148699674c485d24ef0de89c2450a2632d42681b031d421bf5c3102e92d0f`.
This read-only laptop/Colima exercise did not execute those hosted outputs or publish new evidence.
That October 16 snapshot did not establish hosted development qualification, qualifier OIDC identity,
hosted native smoke, qualification attachment/read-back or independent-consumer acceptance.
The later hosted success above has its own exact Run and scope.

Unsigned development remains hosted build-only, static-unqualified, automatically cancelling older
same-branch Runs and retaining transfers for one day. Signed development requires the prescribed
`nvkr-dev.yml` branch identity, GitHub issuer, `per-output-v1` and reserved GHCR development namespace.
It uses `nvkr-dev-release`/`nvkr-dev-arc`, fixed dedicated read/write credential names and no repository-token
package-write permission. The native ARC labels and exact runtime/evidence admission are unchanged.
Source and pinned-toolchain read credentials retain their separate fixed preparation steps.
Provider pool/credential/ACL configuration is an external prerequisite, not established by rendering.

The factory owner derives registry targets beneath the current run and attempt without mutating committed
Plan bytes or Build identity. Development now uses production's same-run retry contract: retained
subjects, controls and predicates keep their original attempt bindings; new publication work uses the
current attempt. Other runs, future attempts and changed Plans remain rejected. Development qualification
selects bounded provider-authenticated metadata candidates and rechecks the full original closure.
The retry regression suite and complete repository gate passed in [Merge Gate 39327338735](https://github.com/dsx-ai-factory/nvkr/actions/runs/39327338735) at `92ce9fc65182a965f94ffa8a527828a971e9fc45`. Local test execution was skipped at the user's request. Live partial retry and independent qualification remain pending. Signed-development helpers/carriers and all final Run records retain
seven days; disk diagnostics retain one day. Full signed development is non-canceling.

The development cleanup module inventories only internal packages in that reserved attempt namespace.
It authenticates account, package and exact completed workflow-attempt metadata; package, version and
original and latest attempt timestamps must all be at least 24 hours old. It rechecks before whole-package deletion, fails
closed on unknown/ambiguous/API state, and records confirmed partial progress without retrying ambiguous
mutation. Preview is the default. The separate daily cleanup workflow executes only default-branch code
with a cleanup-only credential; it never loads branch executables or downloaded artifacts. Production and
older unscoped packages remain outside this protocol.

Definition/factory contracts cover admission, identity, complete M2 graph retention, exact rendering,
credential/environment scope, retention, runtime namespace projection and same-run retry admission.
Cleanup API fixtures cover internal visibility, rejection of private/public/missing/empty/unknown visibility at inventory
and deletion recheck, eligibility, authentication errors, pagination, races and mutation failures;
workflow contracts enforce default-branch scheduling and secret scope. The dispatch helper retains its
clean/current pushed-commit preflight. These tests do not establish live GitHub execution, actual credential
scope, ephemeral runner isolation, successful keyless services, GHCR deletion or physical storage reclaim.
No new external Run or deleted package is claimed. ADR-04/18/20/21 own the contracts.

## Ubuntu 24 node-image inputs

The opt-in `inputs/definitions/diagnostics/ubuntu24` selections reuse M2's NVKE source
`ff21ba8c07cafff74b68e238a8876af11e5beded`, including upstream Ubuntu 24 support from
`96ccc50cb7f8ed8bb08f7cf8655906982774f809`. A predecessor-bound whole-producer overlay
selects Ubuntu 24.04.5 installer bytes for each native architecture and a separate
Go 1.27.2 Bookworm agent builder. The authenticated installer material selects the guest
OS; no new Packer variable or NVKR platform axis is introduced. Preparation checks both
nFPM archive and executable pins; the agent stays dynamically linked and must pass the
pinned upstream ELF checker for the Ubuntu loader and GLIBC <= 2.39. Its updater must
remain a static ELF without a program interpreter, matching the pinned upstream check.
Other M2 materials and the Packer host runtime retain their existing scope and exceptions.

Single-output hosted Definitions cover plain and public-bootstrap FDE on AMD64 and ARM64;
the existing native local wrapper also selects the two plain variants. Hosted build-only
finalization retains its one-disk limit. The supplied Environments generate raw-disk
`static-unqualified` Runs without signing or publication. Ubuntu 26 defaults, maintained
M2 membership, and committed workflow bundles retain their existing selections.
`pkg/nvkr/lifecycle/nvke_ubuntu24_test.go` covers compilation, exact closure/pins, changed
Build identity, native KVM admission and deterministic generation. Script fixtures cover
selection and frozen-input routing through the shared release catalog. Disk producers
retain stock NVKE capture source; the material owner accepts its original acquisition schema. The input-layout migration preserves the agent preparation
and build recipe used in the package exercise below. This is contract evidence: no Ubuntu 24 KVM bake,
guest boot, full acquired-payload ABI inspection, agent-start smoke, release publication
or qualification is established by these tests. ADR-10/11/18/22 own the unchanged contracts.

On 2026-10-19 (UTC), a local native ARM64 container exercise ran the exact overlay preparation
and offline build commands against that pinned source with the declared Bookworm image
`sha256:2bcff0c6ad6e8c5353e8c0d5c7f17bfcc87d667d0169615e34db1acc0db985b9`.
Module verification, both nFPM checksum checks, packaging and the ELF guards passed.
The resulting `nke-agent` Debian package, version `0.2.1-81-gff21ba8`, had SHA-256
`5f4267a3af6280db68955e9a3c3cce7ccf67a6af2054517bbfb58a0bad37a132`.
The dynamic agent required at most `GLIBC_2.34`, used `/lib/ld-linux-aarch64.so.1`
and depended only on `libc.so.6`; the updater had no interpreter or dynamic section.
Both extracted package binaries matched the build outputs. This is direct recipe evidence,
not an authenticated NVKR Run, an AMD64 package exercise, or disk/guest acceptance.

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

The shared native downstream path uses host Go CLIs/TUI, one QEMU site and two tenant guests. NVKR owns
tool acquisition/builds and payload verification; the source-owned NVKE CLI owns bootstrap, deployment
and tenant operations. Both host selections and the older Linux simulator output use one dependency
catalog and one simulator producer. The simulator server declares CGO; its API-only CLI does not.
Platform-specific acquired assets have distinct producer IDs and a verified private command-name view.
The shared profile owner rejects Darwin diagnostic payloads for qualified execution and receipt issuance.
Both local execution and terminal result verification apply that admission before fetch/runtime observation;
Linux toolchain identity does not grant qualified authority to a Darwin payload. Unqualified Darwin diagnostics
retain their separate platform admission.
The Darwin simulator uses an explicit local `native-go` toolchain selection prepared by NVKR from the
installed Go SDK and Clang. SDK/compiler byte admission, private compilation, receipt lifetime and rejection
of hosted/signing/publication paths have contract coverage. A local Run at NVKE revision
`69ef9924c4bf0487beba8c3613ae6e0a0260d8f3` built the three Mac CLI executables and completed terminal
observation and packaging: Run `sha256:2ecfb84092206f23acbae658f7575216c00337feb3ab3d10ce8aed701e7f5ae6`.
This local record is not published release evidence. A separate native startup probe opened SQLite, seeded
two sites, served health and authenticated site reads, and joined server shutdown with exit 0, using NVKE's
mock VM backend. It establishes SQLite execution, not guest readiness or full demo acceptance. Linux retains
its source-owned static Make build in the same catalog producer.
The standard `run` and `e2e`
entry points now select that same three-VM deployment owner; the old Kind-site path is no longer their
production action. Build prepares native tools through NVKR and selects the complete local runtime image
closure. Default native `run` and `e2e` now launch the fixed retained service owner: successful
checks return to the shell while the native resource stack stays held until explicit
`cleanup --session`. Presenter cancellation stops work without requesting deletion.
Interactive authentication keeps terminal context through startup privilege
checks. An unattended owner starts in an independent process session; both paths
acknowledge the authenticated held-state handoff before successful return. Linux binds its control to SO_PEERPIDFD;
macOS binds it to the originally captured process audit token and a kqueue exit
observation, requiring CGO/public SDK support. The generic Darwin peer API remains
refused. Controlled launcher/native-scope tests exercise retention and joined
cleanup; a fresh NVKE VM/conformance run of this lifecycle change is not yet recorded.
Native presentation now uses individual guest/preparation/bootstrap/deployment
boundaries instead of the legacy Kind-site checklist. Requested conformance has
its own phase and failed-suite retention does not imply successful
cleanup. Native tenant presentation joins the existing CAPI role/instance mapping
to the private NICo inventory for both ordinary and conformance topologies, and
uses the existing held-path console metadata reader for each exact instance.
Native Linux reuses its admitted libvirt backend and existing held-domain
observer. Mac process state remains unobserved rather than inferred from provider
readiness. Generic CAPI observations use exact Cluster/Machine owner UID joins;
legacy host-id/spec-label checks remain unchanged. Controlled
subprocess and dashboard tests cover the model; a live display of this revision
and complete Mac/Linux runtime acceptance remain unverified.

The downstream Go owner exposes a callback-scoped native site and independently
created/deleted tenants through the same native engine. The provisional
`pkg/nvkr/demo.WithSite` facade and descriptive/opaque type aliases make that
owner importable by separate NVKE or customer modules without another lifecycle
or deployment engine. Independent external-module compile and nil/canceled/zero-handle
admission coverage establish importability, not real VM acceptance. Existing matching
`nvkr` and `nvke-demo` helpers and native host controls are still required. Go
tests can reuse one site for sequential tenants and run conformance explicitly
against a still-owned tenant, preserving each suite's original output in a fresh
directory. Opaque handles expire at scope exit; cancellation joins active calls,
and an unjoined outcome cannot be swallowed by a callback or lost during panic
or test fatal exit. Controlled subprocess tests cover sequential tenants, failed
validation retention, natural deletion and those ownership boundaries. These
contracts do not establish a real site-reuse run or complete Mac/Linux acceptance.
The same downstream owner accepts one or two tenant workers independently of
suite selection through `run.tenantWorkers` or `Config.TenantWorkers`. Omission
retains the ordinary one-worker or conformance two-worker default; conformance
rejects an explicit one-worker selection before work. Controlled tests cover
count propagation through capacity, tenant requests, readiness/cloud-init,
inventory/cleanup and retained-child transport without changing artifact or
source selection. Smaller-profile runtime acceptance remains unverified.

A separate fixed site-identity helper now has one deep `pkg/nvkr/siteidentity`
owner and a thin private Linux ARM64 build target. It reuses the upstream
generated NodeAgent client and GoSPIFFE/gRPC, admits its bounded output before
remote work, and distinguishes not-started, joined failure and unknown remote
completion. Mutation now delegates to NVKE v0.22.0's `ReconfigureSpireAgent` and
`GetSpireAgentStatus`; the duplicated guest shell rewrite and renderer producer
are removed. Failed outcomes and missing responses remain unknown; client
termination does not prove manager-job completion. The upstream Workload API
picker retains the exact caller identity across updates.
Fresh peer metadata requires operation-fresh issuance and remaining
validity as well as unchanged boot/identity and a changed serial. Helper and
native caller revalidate those descriptions with one shared predicate; endpoint
authentication, the expected boot identity and source-owned TPM policy remain
separate requirements. It issues no
bootstrap-stop or release receipt. The native owner requires the shared
helper startup member before guest launch or native network configuration and
delegates captured completed-Run input replay to the lifecycle owner. All native demo
output selection now uses that owner rather than a second Run decoder and membership
validator. Artifact preparation reuses one descriptive Plan/Run per distinct selected
Run within the operation, including installer and identity-source checks; tool
preparation and conformance also reuse their inspected descriptions for sibling outputs.
Every selected Run must retain its original captured compiler inputs and Plan; uncaptured,
noncanonical, nonlocal and unfinished-commit records are refused. Disk selection and
subsequent asset preparation retain separate replay boundaries. The owner checks every
frozen Run binding; delivery payload acceptance remains separate. Controlled captured-input,
selection and payload-substitution fixtures cover these boundaries, not a fresh native
build/deployment. Source host-policy decoding is local and uses the existing
strict YAML admission; CRD schema validation remains at the server create.
After deployment and before temporary bootstrap teardown, it imports
the original host policy once without replacement, using the caller's work deadline
for API discovery, schema validation and creation. Private identity diagnostics
retain the underlying command failure before the public error is redacted.
It checks the controller/CSI context,
permanent registration, derived TPM allowlist, new TPM agent and renewed
same-boot host certificate, then issues its private completion receipt. Joined
site-stage failures retain their resources under the independent owner while the
foreground command returns; unknown completion keeps dependencies unsafe to tear down. The site-stage/retention presentation
leaves tenant and conformance checks pending. Private Linux ARM64 Run
`site-startup-tools-arm64-acceptance-20261021-02` built and verified the shared
three-member output from public source pins for the preceding shell implementation,
not the reduced RPC revision. The delivered helper passed an
isolated invalid-request subprocess check; its command-owned inherited pipes
are adopted before bounded I/O. This producer and command-boundary evidence
does not establish an authenticated handoff through integrated startup, a fresh
customer first run or complete current-source Mac/Linux runtime acceptance.
The [local guide](../local-nvke-demo.md#private-site-identity-operation-helper)
records that boundary.

The Mac network owner now consumes the verified upstream vmnet helper's JSON report and holds its Unix
datagram connection through joined guest/service cleanup. Gateway binding is observed before services
start; API, local disk delivery, callbacks and temporary Lima forwarding share that address. Contract and
race tests cover network bounds and ownership, and an actual helper start/stop/join passed. One real
2-GiB HVF guest using the newly built simulator completed cloud-init, checked the local disk HTTP endpoint and was
naturally deleted in 24 seconds. This local guest probe is not a fresh NVKE OS bake, site deployment,
tenant Kubernetes, conformance, or full Mac/Linux parity result.

Full native Linux runtime parity, complete fresh-machine host dependencies,
a single fresh build-and-run wrapper, NVIDIA OVN acceptance and qualified release claims remain unproved.
QCOW construction remains owned by NVKR's disk-image Run path. The demo-owned direct Packer builder
has been removed. The native Mac runtime can consume an existing local QCOW. An October 17 private
diagnostic at source `15a2accd7d492c629abf63296ce6a701abf188c3` completed all 32
artifact outputs and both native-tool outputs in fresh Runs. Its site started and
both tenant guests joined Kubernetes, but neither became Ready: the observed
OVN node-pod requests exceeded the selected nodes' available scheduling resources.
The original command exited 17 after readiness timeout; three VM deletions,
empty simulator instance records and joined processes were observed. Workload,
DNS and conformance did not run. These retained private records establish that
attempt's build and failure scope, not a public acceptance record, proof of the
subsequent sizing fix, or a successful complete Mac/Linux demo.

The `inputs/definitions/demo/arm64-nvidia-tcg.json` combined example selects 32 outputs in one
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

The historical legacy Kind-site consumer invokes `nvkr` for build, verification
and extraction, and NVKE's pinned staged installer for deployment. In that consumer, Kind hosts
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
The shared bootstrap/SPIRE upstream CA for new demos has a finite 3,650-day
lifetime, matching NVKE's local E2E CA convention. The real certificate regression
verifies trust after 48 hours and rejects expiry; it does not prove live SPIRE
renewal or full retained runtime acceptance. Existing retained roots are unchanged,
and automatic upstream-root renewal remains unavailable.
Cobra flags and typed YAML configuration supply descriptions, not receipts.
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

The local demo now selects stock NVKE `v0.22.0` at
`400c24fb6465b042be883d6b62472528520d6291`, including that release's public SPIRE
packaging. The obsolete full Kind-site adapter and its typed NICo tenant-release
workflow are removed. The maintained host workflow still uses NVKE's temporary
Kind bootstrap. Earlier beta source-rendering and installer dry-run checks retain
their historical scope and do not establish acceptance of the current host workflow.

Maintained OS Definitions, catalog execution and Packer toolchain assembly select Docker.
The older authored Podman Environment and its TCG Definition are removed; generic
NVKR Podman support remains. The ARM64 catalog baseline retains the already-published
historical M2 Docker candidate, not a newly measured current toolchain. Local Docker
launchers still freeze their freshly validated image into an attempt overlay.
Static Plan and wrapper tests cover these selections, not a new native bake.

The local OS selects the PR #113 NVKE/NVIDIA OVN fork at pinned tag `v2.4.2`,
commit `8af51273d52dfa07454435d18d37230e582d797a`; profile packages and tenant
requests select its `2.4.2` chart stream. The combined Definition also declares standalone
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
For the current `2.4.2` pin, static catalog/overlay/graph contract checks and a
local pinned-chart Helm rendering with loopback OCI push/pull are recorded, again
without a retrievable evidence attachment. The equivalent exercises recorded
earlier against the superseded pins `9480bbe1377e9d9dae550c6ae08f3d7b6460dbf7`
(stream `2.4.1`) and `12ca20e67b516f1a023da85a22deeaf8d2d70d0b` (stream `2.4.0`)
retain that scope. The `2.4.2` chart adds an always-installed node-annotation
`ValidatingAdmissionPolicy` and control-plane resource limits; no running
cluster has exercised either. No Run, OS bake or tenant exercise has
been executed against the current pin, which also ships its single-node-zone
values file as a link to `values.yaml`. Image binding admits regular files
only, and a copy would carry the source image digests that binding replaces in
`values.yaml`, so an install selecting the copy would merge them back over the
bound values. The chart producer withdraws that member instead; the packaged
chart omits it and the source-documented install path using it fails closed on
a missing file.


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
The changed site topology has no completed live E2E acceptance. Fresh
dual-cluster runtime checks remain required, separately from artifact and
adapter tests.
Read-only rendering of the retained chart with its built Helm 4.2.4 executable
was reported locally to preserve the selected image digest across all six site
chart slots. No retrievable rendering artifact is cited for that exercise;
it does not establish validation of the exact values currently installed by the
demo, or a running cluster.

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

The opt-in release browser shares the export description projection for chart versions, native image
children, component build subjects, declared disk metadata and complete evidence memberships. Compact
row summaries hide when they would crowd names. Command-item search includes projected kinds and fields,
including platforms and digests; exact dependency links remain usable outside search results. Explicit
JSON and Mermaid output retain their plain bytes in auto mode and reject explicit TUI before acquisition.
Controlled projection, terminal simulation and loopback-registry tests cover these behaviors, including
zero payload-layer requests and rejection of corrupt unselected metadata. These are contract fixtures;
they establish neither external Release acceptance nor human usability validation.

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

The local NVIDIA native KVM Definitions select stock NVKE `v0.22.0` at
`400c24fb6465b042be883d6b62472528520d6291` for agent, kubeadm, OS assembly,
CLI, controller, provisioned provider, profiles and SPIRE packaging. The release
incorporates the public upstream SPIRE/TPM packaging fix and revision-4 package;
there is no separate older SPIRE source selection. NVIDIA OVN image/chart producers
retain `8af51273` / 2.4.2. The custom OVN Debian
producer changes its actual package metadata and payload paths to `nvke-*` while
retaining source-compatible virtual package aliases and bounded image import.
Version stamps derive from the selected NVKE release tag and grant no NVKR Release
authority; the selected controller retains its source-owned legacy per-node compatibility form.
The owning catalogs inspect Debian package names and architectures before accepting
outputs. The disk uses the renamed Packer build, artifact path and material schema.
The 32-output demo and 38-output local conformance selections share the same deployment
closure. The native site catalog derives its four acquired chart pins from that
source's `deploy/nico/nvke-deploy.yaml`; source-local controller and NKTP chart
names and versions come from their exact path-bound `Chart.yaml` bytes. An explicit accounted `chartName`
binds a reviewed local chart rename; otherwise its name must match the release declaration. External member
names and repositories must match their reviewed acquisition identities. Contract tests cover reviewed and
unreviewed renames, unsafe metadata, exact path/version
checks and unchanged external selections; they do not establish installation or fresh runtime acceptance.
The native runtime-image variant
keeps its separately authenticated images and binds their selection to the same NVKE
source. Mac and Linux tools share the same NVKE source pins. Maintained release,
native demo and nightly product-build selections use this release; the independently
pinned nightly test harnesses retain their separate source exception. Selection grants
no Release authority; prior failed Runs remain failed.

The private Ubuntu attempt `ubuntu-9905-e2e-02` used consumer
`990519c1a53335390ce9464c3eee3d8135076b51`, this stock NVKE source and NVIDIA
OVN. Its recorded native result passed site/tenant readiness, per-node cloud-init
and workload/DNS checks with one site and three tenant nodes. Original conformance
capture completed with harness exit 0: 446 passed, zero failed or pending and
7,133 skipped, in 7,416.042 seconds. The original JUnit SHA-256 is
`f1a0ad57581388bdc770c0ed6a1b9cd9f96adc60f869f7194e78f7a319a5a909`;
seven additional passing suite hooks explain its 7,586 XML testcase records.
The frozen Plan includes the existing Hydrophone report-download patch,
SHA-256 `d0014a0f2e39033d5a63a5938b46793e908ccf2cdc1282c8cba1cc7ffe85770b`.
These are private diagnostic records, not published qualification evidence.
The foreground command's exit was not captured independently. Subsequent inventory
and the original CLI's network recovery confirmed no session VMs remaining and
restored network controls on 2026-10-26. This result does not validate later
consumer revisions or the startup configuration below.

The later private Ubuntu deployment attempt `ubuntu-startup-01` on 2026-10-26 used consumer
`453ec2a4fe961c37e4bcebeac793fdbe76c0443c` and reused the completed artifact/tool Runs
`ubuntu-9905-e2e-02` from the earlier source checkout; it was not a cold artifact build of that consumer.
Its original owner `128715751` reached `retained-ready` with one Ready site node and three Ready tenant
nodes at Kubernetes 1.36.3, after the native node, cloud-init and workload/DNS gates. Conformance capture
`nvkr-conformance-2646420987` completed with harness exit 0: 446 passed, zero failed or pending and 7,133
skipped in 7,577.002 seconds. The original JUnit SHA-256 is
`0f3a66ad97caf60347adc56d1e20a4e71fa881d6ddabc72cc100f8197bd389e0`.
This private diagnostic uses the stock NVKE source and Hydrophone patch identified above. The foreground
exit code was not captured independently. It does not establish a fresh Mac build or later consumer acceptance.

The private Mac attempt `mac-readiness-01` at consumer
`8e432f23a47f9934fcff7be523c942aa2742635b` used the same stock NVKE source,
TCG, a 12-GiB site and two 6-GiB tenant selections. Its site API became Ready,
but controller installation failed before tenant launch. The foreground exited
17 at `host-nvke-deploy`; the retained owner reported `held-partial` with no
completed Ready check. Kubernetes recorded a liveness-probe restart during the
first controller startup, followed by persistent migration-lock conflicts.
Read-only inspection found 35 of 71 ARM64 migrations applied; no database repair
was performed. The original failed invocation remains failed. A fresh Mac
site/tenant run is required to validate the startup configuration below.

Default native Mac BUILD explicitly selects TCG and `cpu_model=max`. The source
owns its ARM64 64-KiB kernel and image-composer network files; the native catalog
adds no kernel override or duplicate network archive members. The existing HVF
preparation remains available for separately selected compatible source inputs.
Plan/capture contract tests do not establish a fresh QCOW build. Latest-source
Mac simulator networking/TPM probes and adapter tests remain bounded checks;
a complete selected-image guest boot, site/tenant deployment, conformance and
joined cleanup remain unverified. BUILD does not start those deployment paths.
An October 19 bounded GET for the preceding `8040adb` SPIRE package's exact
ARM64 DSX IMDS binary returned HTTP 403. That remains historical evidence for
that source selection. The current release's
[SPIRE package](https://github.com/dsx-ai-factory/nvke/blob/400c24fb6465b042be883d6b62472528520d6291/os-image/packages/spire-agent/Makefile)
uses revision 4 and its
[locked public SPIRE 1.14.1 and TPM plugin 1.9.0 archives](https://github.com/dsx-ai-factory/nvke/blob/400c24fb6465b042be883d6b62472528520d6291/os-image/packages/spire-agent/lock/1.14.1%2Btpm1.9.0.lock).
That default package does not acquire or provide the DSX IMDS plugin. Native tenants
select join-token attestation and the site uses TPM. This is the selected release's
packaging contract, not an older-source workaround or complete native runtime acceptance.

The shared native host tools select kubectl 1.35.9 on both platforms. This is
within one minor of the temporary Kind 1.36.1 bootstrap API and the permanent
site/tenant 1.36.3 APIs; the separate conformance client selects 1.36.3. The
complete 32-output catalog smoke-policy fixture is explicitly deferred by a
named skipped subtest. Full catalog compilation and production smoke admission
remain required; no version stamps or smoke policy are weakened.

The Mac address adapter reads actual kernel neighbor bindings for MACs in the
fresh private NICo database and projects them through the backend's configured
address-file interface. The existing pinned Go routing codec and system SQLite
read-only CLI own their formats. No host DHCP change, simulator DB mutation or
manual readiness update is used. Initial ambiguity is rejected; after NICo records
its primary address, only that still-observed address is projected, without
selecting another alias. Controlled tests cover binding scope, primary preservation,
missing observations, ambiguity, cancellation and join; full native VM acceptance
of this adapter remains pending.
Linux keeps its existing owned dnsmasq path.

Native cleanup retains the pinned backend's serial files before deleting its
instance: Darwin's `console.log`, or Linux libvirt's `disk.qcow2.ttyS0.log` and
`disk.qcow2.ttyS1.log`. Existing no-symlink file access and bounded context-aware
copying retain the observed prefix even if a live log appends; this is descriptive
diagnostic text, not immutable artifact evidence. Copies remain private and never
replace original retained files. This corrects diagnostic loss, not a demonstrated
guest-startup failure or runtime acceptance.

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

The downstream demo propagates its work deadline through payload transfer,
bundle verification/extraction, host compilation, site startup, DNS rollout,
workload completion and the conformance wrapper. The retained launcher records
that deadline as private scheduling data and its owner preserves it after
detachment; it grants no persistent execution or ownership authority. Generated
guest preparation enforces the absolute deadline through a process-group timeout
with bounded termination grace. Context/transport and real-subprocess tests cover
deadline propagation, cancellation and joining; these do not establish a fresh
E2E or conformance result. Connection/probe bounds, identity validity, independent
cleanup and authored/upstream producer policies remain separate.

Read-only native readiness queries retry an independently bounded command timeout
only after the command owner confirms joined execution and successful diagnostic
drains, while the original deployment deadline remains active. Partial timed-out
responses cannot establish readiness. Caller expiry/cancellation, unjoined work,
simulator exit and changed ownership still stop the attempt; mutation commands are
never replayed. Controlled real-subprocess tests cover timeout recovery and caller
deadline precedence on both hosts; fresh runtime acceptance remains separate.

Site installation uses the selected NVKE installer's existing Helm post-renderer
handoff. A private Helm subprocess plugin delegates YAML transformation to the
selected kubectl's Kustomize, targeting only the site controller Deployment and
container. It adds a startup probe before the unchanged liveness/readiness
probes and derives the startup allowance, Deployment progress deadline and Helm
timeout from the caller's remaining work budget. The original process context
still bounds the whole installation. This prevents the source controller's
migrations from being interrupted by its HTTP liveness check before the HTTP
listener starts; it neither changes NVKE source nor repairs an abandoned
migration lock. The plugin uses private per-invocation scratch, removed after
its joined subprocess returns. Contract coverage and offline rendered-resource
comparison do not establish fresh Mac deployment acceptance.

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

Both native and legacy tenant smoke paths use the same readiness gate for the pinned CoreDNS Deployment and
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
deletion wait is shorter than the selected NVKE teardown's worker/control-plane
waits; the last actual teardown phase was not retained, so a timeout-budget
mismatch does not by itself establish the runtime cause. Complete fresh E2E
acceptance, including successful NVKE deletion and owned cleanup, remains open.
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
later deployment-only replay naturally completed NVKE deletion and owned cleanup
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
at every build/deployment phase. Network-only recovery retains version-3 journals
for the legacy host Kind bridge and version-4 journals for native host-originated
bootstrap transport without a host Kind subnet. The latter rejects Kind/BGP bindings,
Kind forwarding and raw Kind rules; it never infers host grants from a Docker subnet.
Recovery never kills serialized PIDs; restore legacy live controls before upgrading.
A full tenant crash/restart and recovery matrix remains unverified.

The native Linux bootstrap adapter now reuses the private temporary Lima owner,
with explicit QEMU/9p, reviewed source version and singleton image/archive pins;
Mac retains its separate platform prerequisite. Version admission precedes BUILD.
The private upstream `nerdctl.lima` transport uses Lima's supported
`--log-level=error` option through an invocation-owned PATH adapter. This keeps
Lima informational/version warnings out of NVKE's combined image-inspection
output; guest command stderr and exit status remain unchanged. Direct Lima
version, startup and cleanup diagnostics remain intact. Real subprocess
contracts cover exact digest output and guest failure propagation for both
platform bindings. This does not change NVKE sources or establish fresh E2E
acceptance; NVKE's combined-output parsing remains an upstream limitation.
The owner retains each concrete readiness process until joined, stops polling on
an unjoined group and preserves dependent controls when cleanup cannot join it.
Contract tests cover both platform bindings, version refusal and retained-probe
cleanup. The opt-in backend-only pilot checks restricted mounts and attempt-bound
loopback forwarding, retaining rendered configuration and private diagnostics.
One approved private pilot passed actual KVM, exact rendered material selection,
mount and loopback forwarding checks, with natural exit 0 and joined removal of
its VM/state/sockets. Native gateway traffic and complete demo/conformance
acceptance remain separate runtime obligations; see [native Linux bootstrap](../native-linux-bootstrap.md).

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

The separate `inputs/experiments/public-os-sources/catalog` catalog builds public OS binaries from
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

The separate `inputs/definitions/diagnostics/ovn.yaml` Definition selects native image and
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
repository/builder and preparation/signing/consuming-attempt ordering. ADR-16 artifact checks use the same
producer retry boundaries while independently authenticating complete payloads and evidence. Contracts cover retries, mixed
attempts, re-signing and finalizer reuse. The October 18 source/handoff observation above records a successful
producer completion with mixed original attempts; independent hosted retry qualification remains unproved. Cross-run reuse,
remote rollback and an atomic multi-repository transaction are not provided. Failed/missing provider jobs
still prevent completion. Production finalization restores the six aggregate/tool control carriers plus each output’s
original publication reference, signing record and predicate for the qualification handoff, without
restoring output payload packages. Development finalization
retains only the six aggregate/tool controls because it emits no qualification handoff. Assembly/finalization need
registry read access, not OIDC or package-write
permission. Production admits a fixed independent reader/writer credential policy; arbitrary static
credentials remain rejected. Development retains its separate fixed credential policy. Local execution
and dry-run signing reject this mode.

Maintained production/nightly generation selects fixed independent
registry reader/writer credentials, including native toolchain preparation, qualification/smoke and nightly
runtime consumers. Hosted publication now rejects repository-linked packages even while their repository is
private. Production qualification generation rejects workflow-token publication and target credentials
before emitting a bundle, matching its fixed independent writer. Regression cases cover legacy publication,
legacy targets and their combination; local execution of these cases remains deferred to CI.
Image-reader account and token bindings use explicit caller inputs, with empty defaults only
for absent image preparation; composite metadata contains no caller-only variable/secret references.
Code and regression coverage are prepared; local unit execution is intentionally deferred.
[PR CI at 6f8afe62](https://github.com/dsx-ai-factory/nvkr/actions/runs/38983931671) passed before
the composite account-binding correction; it does not prove action loading or registry access.
No hosted production execution of this change is claimed. Provider package ACL migration, explicit Actions-grant removal,
credential provisioning and denial tests remain operator work before public visibility.

[Production Run 38977394488](https://github.com/dsx-ai-factory/nvkr/actions/runs/38977394488),
head `d996200b77848293b18d3861b9f1db1e8b41b423`, attempt 1 completed 579 jobs but its
[finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38977394488/job/117265197726)
exceeded the provider's fifteen-minute job limit. No final Run record or qualification handoff was uploaded.
The [attempt-2 finalizer retry](https://github.com/dsx-ai-factory/nvkr/actions/runs/38977394488/job/117304226181)
failed before finalization because its helper locator retained source attempt 1. A read-only inspection of
artifact `12079866182` verified the 489-byte ZIP against provider SHA-256
`5a63fb9bb456f97a467d9a31a4978a0cf92b0bc1df7a3eafae0a46ec121a4126` and its exact repository,
Run, helper name and attempt-1 request; it did not download the private helper payload.
Release finalizers now have a dedicated 120-minute budget. Production transport explicitly admits retained
same-Run source attempts no later than the consumer, preserving original locator/config bindings through
ordinary and nested restoration. Same-name retry candidates are bounded and all authenticate before
selecting the newest source attempt; tied source attempts and incomplete listings fail closed.
Generated fetch and restore commands disable reuse whenever an explicit Run is supplied.
Development transfers now use the same retry selection. Explicit cross-Run and direct store APIs stay exact-attempt.
The review follow-up adds regression cases for candidate ordering, ties, future/corrupt candidates and
truncated listings; these tests passed in [Merge Gate 39327338735](https://github.com/dsx-ai-factory/nvkr/actions/runs/39327338735), with local execution intentionally skipped.
Focused contracts reproduce the old rejection and cover private payloads, public metadata, mixed source
attempt acquisition, unchanged bindings, future/foreign/malformed source refusal and generated policy
separation. These local checks do not establish hosted retry completion, qualification acceptance or a
measured sufficient runtime for this release closure.
A read-only probe with the updated CLI against this Run's attempt 2 reproduced exit 22 with exact-attempt
selection, then acquired the same helper locator with earlier-attempt reuse enabled. The original JSON
bytes were unchanged (SHA-256 `7ded2ea93203be141c5ac67cea182658b3674a8441b3ea6f4df32fa5446c0853`).
This exercises provider-authenticated locator acquisition only, not private payload restoration or hosted
finalization with the fix.

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
[Run 37736529644](https://github.com/dsx-ai-factory/nvkr/actions/runs/37736529644) reproduced 34 outputs
and five Helm chart versions: controller 0.1.1, PostgreSQL 18.5.15, SPIRE 0.30.1, SPIRE CRDs 0.6.1 and
Temporal 0.74.0. The publication reference is
`ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases@sha256:2fef87f426734a4427f7694aaf5ac181ba11352ea37522349bbb9cda7d84dc42`.
This was metadata inspection only; an earlier independent full-payload attempt authenticated the root and
then failed downloading the ARM64 disk with an HTTP/2 `PROTOCOL_ERROR`. There is no successful full-release
verification result from that attempt. The controller uses `33fb71b2f4224b470ebc628a9165fc4b92870406` and the
CLI uses `ed9fb09b04fd40bf331d25df2222206ffaca593e` from the same NVKE repository; matching ARM64 controller/CLI
artifacts are absent. These are inventory facts. A downstream installer decides whether those revisions and
platforms satisfy its requirements; generic export neither reconciles them nor requires an unpublished Plan.
The generic CLI export was exercised against that exact retained metadata cache on 2026-10-10. It emitted
`nkd.dev/release-description/v1` with all 34 outputs, 60 evidence entries and 23 source records: 16 native
AMD64 outputs, 13 native ARM64 outputs and five charts without platform labels. It preserved both NVKE
revisions above and five distinct repository-spelling/commit pairs. This was a local-cache metadata export,
not a fresh registry read, payload or signature verification, or installer-compatibility check. These are
historical pre-rebrand (`nkd.dev`) namespace records: current NVKR readers reject their old schemas and digest families. The
record does not establish current-schema external acceptance or authorize converting signed metadata.
Separate direct Docker builds of the pinned profiles carrier passed the chart's `/bin/sh` and
`cp -a /profiles/. /profiles-out/` operation with container networking disabled on ARM64 and emulated
AMD64, plus the ARM64 local diagnostic profile. These checks establish the runnable carrier only, not
pre-rebrand wrapped execution, publication, native AMD64 runtime, fleet profile validity or offline installation.
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

Private CI payload transport uses the existing Go registry and bounded archive owners. Actions attachments
carry canonical JSON locators; source archives, helpers, tools and compiled payloads stay in an API-confirmed
private/internal GHCR package with no reported repository association. Separate read/write package credentials
and their configured usernames are scoped to transfer steps. The repository token authenticates repository
metadata and Actions locator reads; it is not a package-credential fallback. Publication retains its separate
private destination and inspected-root admission. GHCR publication jobs also pass their automatic repository
token separately for the source metadata lookup; development publisher tokens remain package-scoped. Local
contracts cover this credential separation and its generated projection; the development publication job
recorded below also exercised these separate bindings.
The v2 carrier uses at most 64 ordered raw chunks of at most
1 GiB, checking each digest, config binding and complete archive before publishing extracted roots. Earlier draft v1 locators
require a new producing Run. Controlled local-registry contracts cover multiple chunks, corrupt/reordered or
truncated bytes, bounds, no-overwrite extraction and joined cancellation; they do not prove GHCR service
acceptance. Raw chunks trade compression for predictable layer bounds. Registry retention, effective hidden
package grants and anonymous/unrelated-user/fork access remain operator responsibilities. Old workflow
artifacts and caches are not migrated or purged. No complete hosted private-transfer Run is recorded.

The generated workflow binds the two nonsecret storage usernames from repository variables into workflow
environment values; nested transfer actions consume the supported `env` context. Authored nightly,
native-toolchain and OS-validation callers supply the corresponding username explicitly.
[Development Run 38842595104](https://github.com/dsx-ai-factory/nvkr/actions/runs/38842595104), commit
`3b0d86f61afab587bd5ed1a1a86d0f1cc9fb4921`, exercised all four bootstrap uploads and downstream helper
downloads. The CI package metadata reported internal visibility and null repository association. Bootstrap
attachment `12022225123` contained only a 437-byte locator JSON referencing a 31,075,328-byte helper archive
at GHCR digest `sha256:0f2688d46c23635668f62ded0dcb90412827b0401dd169d848cf93e25c0ab889`.
The anonymous registry challenge and subsequent anonymous pull-token exchange both returned HTTP 401;
unrelated authenticated users, enterprise grants and fork identities were not exercised.
[Output publication job 116787811052](https://github.com/dsx-ai-factory/nvkr/actions/runs/38842595104/job/116787811052)
succeeded with the separate repository/package credentials. All four native disk jobs passed runner and
private toolchain preparation. SBOM attestation jobs encountered Cosign/Rekor duplicate-entry conflicts;
these partial observations do not establish a complete successful Run or release acceptance.

For the first failed SBOM attestation, Rekor returned the reported existing entry at log index `3272949380`,
integrated at `2026-10-16T02:59:31Z`, before Cosign failed at `03:00:01Z`. The pinned Cosign 3.1.3 dependency
does not recover that conflict into a bundle. SBOM creation now has ADR-04's bounded fresh-invocation recovery;
the exact predicate, subject and full verification remain mandatory. Regression cases were added but not run
at the operator's request; package compilation, static analysis and review passed. Hosted recovery remains
unproved. The subsequent
[development Run 38844396801](https://github.com/dsx-ai-factory/nvkr/actions/runs/38844396801), commit
`01eafd737bf25f6da2a98d70e471a2851a7cf6c6`, reached source and build work but failed private-package admission
before SBOM signing. In
[source job 116792702215](https://github.com/dsx-ai-factory/nvkr/actions/runs/38844396801/job/116792702215)
and [ARM64 build job 116792950777](https://github.com/dsx-ai-factory/nvkr/actions/runs/38844396801/job/116792950777),
execution and carrier packaging succeeded; upload then returned `NVKR-E-TRUST-POLICY-MISSING` because package
access could not be authenticated. Concurrent operator requests using the same personal account received
HTTP 403 with primary quota `5000`, remaining `0`, and reset `2026-10-16T03:39:23Z`.
The generated full-success route requires approximately 5,183 package metadata requests before conditional
revalidation or unrelated account activity: 2,022 for 1,011 uploads, 2,327 for carrier/helper downloads, and
834 for fresh development publication. A reset alone does not provide reliable headroom for that route.
The storage owner now uses ADR-18's operation-local authenticated ETag revalidation while retaining every
network policy check. One download batch shares a backend; its in-memory response cache is bounded to 128
entries and 8 MiB, with exact URL/credential isolation and no stale-on-error fallback. Regression cases
were added but not executed at the operator's request. Compilation, vet, lint and independent static review
passed. An authenticated package API probe at `2026-10-16T03:39:42Z` returned a `200` response followed by
`304`, with primary quota usage unchanged at one request. The provider's `200` ETag used the weak `W/`
prefix while its matching `304` ETag omitted it. The strict string comparison in
[bootstrap job 116798618159](https://github.com/dsx-ai-factory/nvkr/actions/runs/38846651931/job/116798618159)
therefore failed closed; comparison now follows
[HTTP's weak entity-tag rule](https://www.rfc-editor.org/rfc/rfc9110.html#section-8.8.3.2), preserving opaque-tag
equality and every privacy check. This probe establishes endpoint revalidation behavior, not full-Run quota
savings or completion; those remain to be demonstrated.

Six sampled attachments in Run `38844396801` (IDs `12022647313`, `12022133262`, `12023070943`, `12022991328`,
`12022558957`, and `12023125384`) covered helper, tool, artifact, result, source and package carriers. Each
ZIP contained exactly one 440–542-byte v2 locator bound to the exact repository, Run, attempt and artifact
name, with a digest reference to the CI package; no payload files were present. GHCR reported internal
visibility and no linked repository. These samples do not establish the complete final attachment inventory
or effective access grants.

[Development Run 38847118548](https://github.com/dsx-ai-factory/nvkr/actions/runs/38847118548), commit
`8c3a8dbb6af538650d6751c86c1a5bb319541b6d`, finished with 561 successful, four failed and 15 skipped jobs.
All bootstrap jobs and all four native disk producer jobs passed. SBOM attestation, publication, signing
and signature verification succeeded for 64 nondisk outputs and both plain disks.
These successes exercise the new signing implementation but do not prove that a conflict retry occurred. One
[packaging job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38847118548/job/116801143181)
failed while pulling its pinned AMD64 BusyBox utility image; its daemon admission and input restoration had
succeeded. The discarded child-process diagnostic prevents attribution to registry, network or runner
failure. Platform admission now retains bounded stderr solely for fixed diagnostic classification, with
cancellation precedence and no raw-response logging or pull-behavior change. Regression cases were added
but not run at the operator's request; compilation, vet, lint, license, shell and vulnerability checks passed.
These checks are separate from external exercise and do not constitute the complete test gate.

The successful [ARM64 plain-disk producer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38847118548/job/116801591683)
uploaded a 3,350,328,832-byte prepared carrier; Actions artifact `12024935662` was a 558-byte ZIP containing
one 519-byte locator. The ARM64 FDE prepared carrier was 6,450,705,408 bytes; Actions artifact `12023954928`
was a 553-byte ZIP containing one 515-byte locator. Both ZIP digests matched provider metadata, and the
locators matched the exact repository, Run, attempt, artifact name and CI registry reference. Successful
uploads at that commit exercised four and seven raw chunks respectively, inferred from the fixed 1 GiB
chunk bound. Only locator metadata was independently inspected; payloads were not downloaded for this audit.

The [ARM64 FDE SBOM job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38847118548/job/116809539638)
failed before signing while restoring its prepared layout: the hosted filesystem ran out of space.
The runner reported 19 MB free during private download. The [AMD64 FDE SBOM job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38847118548/job/116812945991)
also exhausted storage, while extracting the private carrier during download. At that commit, transport held about two payload-sized
copies during download/restoration and three during subsequent upload, approximately 12.015 GiB and
18.023 GiB for this FDE carrier before tools and workspace overhead. Those peak estimates derive from the
implementation and measured locator size, not a complete filesystem trace. Native production succeeded;
this consumer-capacity failure and the BusyBox pull failure prevent all-output completion and release acceptance.
The [finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38847118548/job/116818167805)
rejected the failed provider-job closure with `NVKR-E-PROVENANCE-MISSING`; it did not report a complete Run.

The next implementation removes intermediate payload carriers whose consumers share the producing stage.
Per-output SBOM jobs now receive only prepared control documents and evidence. The publisher joins their
verified attested controls to the original preparation, moves unchanged blobs without copying, performs full
inspection and publishes the exact privacy-admitted root. Its separate signing handoff contains only bounded
metadata bound to that published digest. Signers retain exact Plan, output, Run and cryptographic SBOM checks;
independent remote verification still checks the complete served payload. Each of the 69 publication/signing
chains removes one full-layout upload and two downloads; each disk route also removes an unused full disk
package upload. Native disk jobs additionally omit ten unused intermediate payload uploads each. These
changes do not move OIDC into producers or publishers.
Single-file CI uploads expose the existing TAR format as a virtual reader; downloads authenticate and extract
ordered chunks directly into private staging. Neither direction writes an extra full outer TAR. At that revision,
factory restore retained its inner carrier alongside extraction for held-source mutation detection and atomic publication.
Bootstrap compilation also releases its own disposable caches after joining the compiler. Repository build,
vet, lint, format, license, shell and vulnerability checks, four-bundle regeneration/drift, Actionlint, YAML
lint and independent source review passed. Regression contracts were added but not executed at the operator's
request; the complete test gate was not run. The generated production and development workflows have
47,399 and 46,956 expanded YAML parsing events respectively, below the 47,500 maintained headroom threshold.
Complete hosted acceptance remains unproved; these checks do not establish a measured storage bound.

[Development Run 38855058706](https://github.com/dsx-ai-factory/nvkr/actions/runs/38855058706), commit
`d88af16e361c269dc27b1a21fc65f34f50fb9e97`, exercised the revised transport and metadata handoffs and
finished with 563 successful, five failed and 12 skipped jobs.
All 65 nondisk outputs completed attestation, publication, signing and independent remote verification.
All four native disk producers passed; both ARM64 disk publications passed, including the
[FDE publisher](https://github.com/dsx-ai-factory/nvkr/actions/runs/38855058706/job/116833935272).
The FDE payload locator (`12028250187`) declared 6,433,993,728 archive bytes; its prepared and attested
control locators (`12028075621`, `12027758133`) declared 61,952 and 193,024 bytes. Plain ARM64 prepared
controls (`12028021304`) also declared 61,952 bytes. Their ZIP digests and exact repository, Run, head,
attempt and carrier-path bindings were checked. No private payload was downloaded for this sample,
and this is not a complete attachment-inventory audit or filesystem peak measurement.

Four late jobs failed before execution because the pinned download action listed only its default first
1,000 artifacts before filtering. At 1,055 and then 1,072 artifacts, it omitted the older Cosign locator;
restoration failed closed with `NVKR-E-VERIFY-FAILED`. The affected jobs were
[ARM64 plain signing](https://github.com/dsx-ai-factory/nvkr/actions/runs/38855058706/job/116834769009),
[AMD64 plain SBOM](https://github.com/dsx-ai-factory/nvkr/actions/runs/38855058706/job/116835093678),
[ARM64 FDE signing](https://github.com/dsx-ai-factory/nvkr/actions/runs/38855058706/job/116836891914) and
[AMD64 FDE SBOM](https://github.com/dsx-ai-factory/nvkr/actions/runs/38855058706/job/116836964138).
The [finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38855058706/job/116837362168) also received
an incomplete locator selection, then correctly rejected the failed provider closure with
`NVKR-E-PROVENANCE-MISSING`. These were locator-discovery failures, not storage or cryptographic verification failures. The pinned
[download action](https://github.com/actions/download-artifact/blob/3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c/dist/index.js)
supported a finite listing override. Generated download templates at that revision set 2,000, and expanded workflow
validation rejected more than 2,000 possible uploads per attempt, including helper, conditional diagnostic
and finalization uploads. Missing or changed overrides failed exact-template validation. That bounded a fresh
generated attempt, not accumulated artifacts across arbitrary reruns. A read-only probe using the exact
pinned action library listed all 1,073 artifacts across 11 pages with the new bound and found the Cosign
locator (`12026349075`) on page 11; an exact-name lookup matched it. This exercised the library's real API
path, not hosted action execution. Four-bundle generation and drift checks, build, vet, lint, formatting,
license checks, workflow/YAML lint and diff checks passed. At that revision the complete hosted path still
required a fresh successful Run; unit tests remained unexecuted at the operator's request.

The next [development Run 38859949586](https://github.com/dsx-ai-factory/nvkr/actions/runs/38859949586),
commit `9ef9c841ebbebef854c30be7beedd8983367234f`, encountered a separate private-chunk read failure in
the [ARM64 SPIRE agent signing job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38859949586/job/116843230395).
Its three locator ZIPs downloaded and matched provider digests with 771 artifacts listed. Private restoration
then returned `NVKR-E-FETCH-FAILED` before signing executed. The underlying read cause was discarded, so
this record does not establish a transient network fault or an integrity failure. Chunk diagnostics now
retain fixed categories and bounded progress counts without provider text. Extraction-only recovery admits
at most three attempts for narrowly typed transient body-read errors, closes responses and checks failed
staging removal, and repeats complete archive authentication before destination publication. Independent
archive, integrity, filesystem and cleanup failures cannot authorize recovery. This adds no outer archive
copy and does not retry publication. Package build/vet, Linux vet, global lint, formatting, license and
four-bundle drift checks passed, with independent source review. Regression contracts are authored but
unexecuted at the operator's request. Recovery of an actual interrupted hosted response remains unproved.

Attempt 1 of [Run 39191524720](https://github.com/dsx-ai-factory/nvkr/actions/runs/39191524720/attempts/1),
at commit `4487a965d25e24dfd745124487d6bb3915fb6780`, failed in the
[AMD64 plain disk publication job](https://github.com/dsx-ai-factory/nvkr/actions/runs/39191524720/job/117875170854)
while restoring private input payloads. The diagnostic was `http2-protocol`, chunk 3 of 4, after
411,041,792 of 1,073,741,824 chunk bytes. The build and SBOM attestation had succeeded, locator acquisition
succeeded, and the actual publication operation was skipped. The
[finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/39191524720/job/117882182746) then rejected
the incomplete provider closure. The retained category does not identify the underlying HTTP/2 reset code
or establish a registry, intermediary or network cause; it provides no evidence of an authentication failure.
That revision classified HTTP/2 errors as nonretryable, so this was not exhausted recovery.

Private chunk recovery now admits typed peer `PROTOCOL_ERROR`, `INTERNAL_ERROR` and `REFUSED_STREAM`
resets under the existing three-attempt extraction budget. It rejects local/unknown protocol errors,
lookalike strings, filesystem wrappers and joined independent failures. Safe categories distinguish the
admitted resets without retaining provider error text. Controlled TLS/HTTP2 fixtures exercise the actual
Go standard-library body error, replay from byte zero, checked response/staging cleanup, retry exhaustion,
cancellation and complete archive verification. These local contracts do not replay the original hosted
failure or establish successful GHCR recovery or release acceptance.

Run 38859949586 externally exercised the locator pagination fix: its successful
[ARM64 plain disk signer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38859949586/job/116850300612)
listed 1,046 artifacts with the 2,000 bound, selected the older Cosign locator (`12028567478`) and both
route locators, restored the private bytes and completed actual signing (`dryRun=false`) at
`2026-10-16T06:50:11Z`. This proves the previously failing late-job discovery path, not complete Run acceptance.

That Run finished with 569 successful, four failed and seven skipped jobs. All four native producers,
69 SBOM attestations and 68 output publications succeeded; 66 outputs completed independent remote
verification. Besides the SPIRE chunk read, the
[AMD64 FDE publisher](https://github.com/dsx-ai-factory/nvkr/actions/runs/38859949586/job/116854048985)
completed its private download but exhausted local disk while unpacking the retained factory TAR. Its
locator (`12030463435`) declared 6,731,396,096 archive bytes; the 564-byte locator ZIP matched provider
SHA-256 `051ab577468a9c39af11b6d77c45beb0893f49a98fa0ea93515942a626eefe1f` and exact Run/head/attempt
bindings. This is a bounded locator sample, not a payload download or peak-space measurement.
The [ARM64 FDE verifier](https://github.com/dsx-ai-factory/nvkr/actions/runs/38859949586/job/116853852574)
failed during remote layer materialization with HTTP/2 `PROTOCOL_ERROR`; its log does not establish the
peer's underlying reason. The [finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38859949586/job/116857487854)
successfully selected its locator from 1,082 attachments, then rejected the incomplete provider closure
with `NVKR-E-PROVENANCE-MISSING`. No complete release acceptance follows from this failed Run.

The follow-up implementation streams Plan-selected factory carriers directly into extracted roots and
publishes disk payloads from checked held descriptors, removing the retained input TAR and disk publisher's
full temporary copy. It preserves complete transport authentication, no-replace publication, source mutation
checks and unconditional post-upload rehash even for registry deduplication. The subsequent
[development Run 38867619968](https://github.com/dsx-ai-factory/nvkr/actions/runs/38867619968), commit
`53e28a56c85cd3818e07574b7eb25add6934687a`, finished with 565 successful, three failed and 12 skipped jobs.
All 65 nondisk outputs and both ARM64 disk outputs completed SBOM attestation, publication, signing and
independent remote verification. The [plain ARM64 publisher](https://github.com/dsx-ai-factory/nvkr/actions/runs/38867619968/job/116874186795)
streamed restoration in 48 seconds and published in 125 seconds; the
[ARM64 FDE publisher](https://github.com/dsx-ai-factory/nvkr/actions/runs/38867619968/job/116875695907)
streamed restoration in 186 seconds and published in 409 seconds. The
[plain verifier](https://github.com/dsx-ai-factory/nvkr/actions/runs/38867619968/job/116876558917)
and [FDE verifier](https://github.com/dsx-ai-factory/nvkr/actions/runs/38867619968/job/116881062118)
completed real verification (`dryRun=false`) of the output and release-wrapper references. These are
successful hosted disk paths at this revision, not measured peak storage, AMD64 disk acceptance or a complete release.

Both AMD64 disk jobs restored inputs but failed KVM admission before building. The initial
[preflight 38869133168](https://github.com/dsx-ai-factory/nvkr/actions/runs/38869133168/job/116868903097)
confirmed device permission failure. A diagnostics-only follow-up at
`4c95efbc1a5f742b7884ff8635c60fe1b1afe337`,
[preflight 38873740116](https://github.com/dsx-ai-factory/nvkr/actions/runs/38873740116/job/116883799563),
reported `/dev/kvm` UID 0, GID 108 and mode 0660; runner UID/GID 1001 and groups 27, 100 and 123 lacked access.
These read-only probes made no runner permission change. Provisioning the device's numeric group,
consistent across eligible nodes, is one repair; the fixed generated bootstrap below is the selected
workflow alternative. The
[finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38867619968/job/116883648755)
recovered private artifacts after listing 1,050 attachments, then correctly rejected the incomplete provider
closure with `NVKR-E-PROVENANCE-MISSING`. Unit contracts remain unexecuted at the operator's request.

The separate [NVKE Run 38912781090](https://github.com/dsx-ai-factory/nvke/actions/runs/38912781090),
head `c47c9d6c8e8ef8e1141eb7e3028ad6c9c5bdb139`, exercised `sudo chmod 666 /dev/kvm` and real KVM guest boot in
[AMD64 job 117014068930](https://github.com/dsx-ai-factory/nvke/actions/runs/38912781090/job/117014068930).
That establishes access after its workflow bootstrap on its observed runner. The shared runner label and
different registration names do not establish the same Kubernetes cluster, node or deployment as NVKR.

The generated native disk step now rejects missing, symlink or non-character `/dev/kvm` paths before
checkout and downloads. When the runner cannot open the device read/write, it makes one fixed,
noninteractive `sudo -n -- chmod 0666 /dev/kvm` attempt with a fifteen-second timeout and five-second kill
grace, then requires a successful runner read/write-open. Permission-change or access failure remains a
coded capability rejection. Already-accessible devices are unchanged. Exact generated-step validation
retains the fixed command, ordering and failure controls; ARC finalizers omit it. The independently authored
OS-validation diagnostic workflow remains read-only.

This is an operator-accepted native ARC permission bootstrap, not SLSA Build L3 isolation or a qualified
execution claim. Mode 0666 can widen access to a host-backed/shared device beyond the runner job; it is
not assumed to be confined to an ephemeral pod. Producer identity, held native capacity/KVM API admission,
guarded Docker projection and the pipeline-attested evidence lane remain unchanged. Unit contracts remain
unexecuted at the operator's request.

For the bootstrap revision, repository build/vet and lint, four-bundle regeneration/drift,
Actionlint, YAML syntax/style checks, formatting, license, shell and diff checks passed; the vulnerability
scan found no vulnerabilities. Generated YAML retains its existing long-line and document-start style.
Expanded YAML parsing counts remain 47,431/46,988/22,120/22,112 for production, development and the two
nightlies, below the 47,500-event headroom bound. Unit, race and Python test suites were skipped at the
operator's request; these static checks do not establish hosted execution.

The subsequent [development Run 38923861766](https://github.com/dsx-ai-factory/nvkr/actions/runs/38923861766),
head `0c302566f259da8bd01a80f9b64ea4c196e40afa`, attempt 1, finished with 575 successful, two failed and
three skipped jobs out of 580. All 69 named outputs, including all four native plain/public-bootstrap FDE
disks, completed SBOM attestation, publication, real Cosign signing and independent remote verification.
All four disk runners already exposed `/dev/kvm` with GID 108 and mode 0666. This exercised the generated
access check without changing permissions and the complete disk build paths; it did not exercise the
conditional chmod repair, establish SLSA Build L3 or qualified execution, or demonstrate TPM first-boot acceptance.

The [aggregate assembly job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38923861766/job/117085717125)
failed before assembly while the pinned download action eagerly started requests for 346 selected locator
artifacts. GitHub returned HTTP 403 for a secondary rate limit with `Retry-After: 1`, despite a primary
quota remaining of 14,441 out of 15,000. This was a provider locator-acquisition failure after every output
verified, not a disk build or signature failure. Aggregate publication, signing and verification were skipped;
the [finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38923861766/job/117086390427)
rejected the incomplete provider closure. No complete aggregate release acceptance follows from this Run.

The follow-up acquisition path moves locator selection and ZIP reads into the Go transport owner. It
looks up each exact selected name serially rather than paging a changing global index, then serially
downloads bounded 64 KiB ZIPs, verifies provider SHA-256, and admits one canonical locator of at most
32 KiB with exact repository, Run, attempt and name bindings before writing the index. Selected-Run metadata
anchors repository IDs and head. API starts are spaced by at least 100 milliseconds, with at most four
attempts only for recognized provider rate limits, cancellable waits and a twenty-minute acquisition bound.
ZIP reads have no generic retry. The 2,000-selection/workflow budget remains; private GHCR payload
authentication and restoration are unchanged, with no additional jobs or payload copies. A read-only local
probe exercised this acquisition path against all 346 assembly locators from Run 38923861766, attempt 1,
checking the source head before acquisition. It completed in 305 seconds and wrote 179,845 bytes of
validated locator JSON without fetching private GHCR payloads. This establishes real API/ZIP acquisition
for that selection; it does not exercise a rate-limit retry, hosted step credentials or complete aggregate
release acceptance. Repository build, vet and lint, Linux AMD64/ARM64 compilation, four-bundle
regeneration/drift, Actionlint, YAML, formatting, license, shell and diff checks passed; the vulnerability
scan found no vulnerabilities. Generated entry workflows and job counts are unchanged. Unit, race and
Python suites remain unexecuted at the operator's request.

[Development Run 38937379327](https://github.com/dsx-ai-factory/nvkr/actions/runs/38937379327), head
`491341bef3015c7f0ffc41666b14b0cca3834c1c`, attempt 1, subsequently exercised the serial locator path in
hosted jobs: 507 helper-preparation steps succeeded and 23 output signature verifiers passed. These are
provider step observations, not an independent audit of every attachment. One
[verification job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38937379327/job/117101950150)
failed before locator acquisition when the Go bootstrap's public module ZIP download returned an HTTP/2
`INTERNAL_ERROR` from its peer. The remaining work was canceled because aggregate completion was no
longer possible. The Run ended with 488 successful, 90 canceled and two failed jobs out of 580; the second
failure was the finalizer's expected rejection of the incomplete provider closure. No aggregate acceptance
follows from this Run.

The bootstrap now separates credential-free public dependency acquisition from a single offline compiler
invocation. Only the exact observed module-proxy ZIP error permits up to three acquisition attempts, with
bounded waits and one shared deadline. Disposable copies of the two small dependency manifests prevent
checkout normalization and must remain exact. Unknown or integrity failures and compiler failures do not
retry. This does not replay producer, publication or evidence operations. Focused contract source and
independent review cover the restriction. The exact generated bootstrap completed on macOS with fresh
disposable Go caches: real dependency acquisition and one compilation succeeded, both manifests stayed
exact, and the supervisor removed its caches. This did not inject or exercise the retry branch. Vet, lint,
four-bundle regeneration/drift, Actionlint, YAML, Python syntax, shell, formatting, license and diff checks
passed; the vulnerability scan found no vulnerabilities. Entry workflows and job counts are unchanged.
Unit/race/Python suites remain skipped; hosted acceptance of this bootstrap change remains open.

The current split transport exports the twelve admitted metadata families directly through Actions,
including metadata-only OCI controls, while binary/source/tool carriers and unknown/mixed contents remain
private GHCR transfers. A held carrier is projected to a bounded text inventory, checked against its exact
record schema or complete metadata layout, and bound to repository/Run/attempt/name before publication.
Consumers retain the serial provider-digest checks, complete Plan selection, no-replace restoration and
existing evidence authentication. Public records require no registry credential or package metadata calls.
The unchanged full-success development graph projects 853 public metadata uploads and 1,202 reads,
removing 2,908 package REST requests (5,501 to 2,593 before retries or conditional revalidation).
This is a static projection, not measured quota usage. Contract cases are authored but unexecuted at the
operator's request. Go compilation for macOS and Linux amd64/arm64, vet, lint, four-bundle drift checks,
Actionlint, YAML lint, shellcheck, license and formatting checks passed; the vulnerability scan found no
vulnerabilities. Full hosted acceptance and an attachment audit of the new format remain unproved.

PR #196 follow-up confines metadata validation-snapshot writes through an `os.Root` handle in addition
to lexical path admission. Pattern downloads retain their artifact subdirectory even for a single match;
dry-run signing does not request cryptographic SBOM verification. Catalog refresh disables Go caches
unconditionally, including private repositories. Stale fixture paths and carrier/token assertions are
updated for consumed preparations, local-only payloads and metadata-only downloads. Local unit suites
remain skipped at the operator's request; hosted checks must confirm these corrections. The initial
registry pull retains ADR-20's thirty-minute default deadline when the caller supplies none; the review
suggestion to leave the initial attempt unbounded is not adopted.
The follow-up coverage now composes actual SBOM, predicate, OCI-control and source-receipt producers with
factory metadata export and restoration; it checks exact bytes, membership and indexed digests. Negative
archive cases cover traversal, links, duplicate entries, invalid layouts and cancellation without a receipt
or retained validation snapshot. Additional cases cover empty/partial nonmerged selection restoration and
complete-selection rejection, storage-account overrides, and a real bootstrap descendant retaining captured
pipes. KVM branch tests execute the emitted script against safe filesystem fixtures and simulated open/repair
results; they neither change real device permissions nor prove runner KVM capability. These additions are
compiled and statically checked; local unit execution remains skipped and hosted results are pending.

For Run 38867619968, the plain ARM64 prepared-payload attachment (`12032867746`) was a 568-byte ZIP
containing one 527-byte canonical locator for a 3,250,255,360-byte private archive. Its provider ZIP SHA-256
`8346896ddd5dba69678d6e0773478a0ecee60bb12bd4d9b32b26dadb90387557`, exact Run/head/attempt and
carrier bindings were checked without downloading the payload. At `2026-10-16T08:11:54Z`, anonymous manifest
requests and their exact anonymous pull-token exchanges both returned 401 for that carrier and the published
plain ARM64 release wrapper. Authenticated package metadata reported internal visibility and no linked
repository for CI storage and that output package. This establishes the sampled anonymous denial, not
hidden authenticated, enterprise or fork grants, future visibility or aggregate release availability.

After Run 38867619968 finished, a complete attachment audit concluded at `2026-10-16T08:36:43Z`. All 1,051 Actions
attachment ZIPs matched their provider SHA-256 digests and contained only one canonical, Run-bound v2
locator each. Their combined size was 592,517 bytes; individual ZIPs ranged from 478 to 591 bytes.
The audit rechecked the terminal failed conclusion, exact head and attempt, and unchanged final inventory
(`sha256:583cc95163932dba53750a022414a95db16d25c2cd7bdae5312a8766507cf2ee`). This verifies every attachment
in this Run; it excludes logs, caches, historical runs, registry ACLs and functional release acceptance.

Remote disk acquisition also admits at most three fresh reads for the narrowly typed one-layer HTTP/2 peer
reset, with checked cleanup, one shared deadline and destination commit outside retry. The public HTTP/2
error type is pinned through `golang.org/x/net v0.61.0`; the Go 1.27 standard library supports that typed
error conversion. This recovery path has not yet been exercised against an interrupted hosted response.
Repository build/vet and lint, affected Linux build/vet, four-bundle drift, formatting, license, shell,
Actionlint and YAML checks passed. The affected delivery vulnerability scan reported no vulnerabilities.
An offline inspection found exact nonoverlapping roots for all 463 development/production and 221
per-nightly stage selections. Independent source review covered nested extraction, held disk publication
and recovery boundaries. The unit/race suites and complete test gate were not run at the operator's request.

Repository workflow contracts disable compiled-output caches and CodeQL database/TRAP/overlay uploads,
and reject runner debug mode before CodeQL initialization. OS candidate post-build files and the storage
writer are admitted only for the exact trusted workflow commit. The nightly public runtime attachment
contains the constructed cycle summary, not raw suite-report files. These safeguards and job consolidation
retain the existing `pipeline-attested` boundary; they do not establish SLSA Build L3 or qualified execution.

Secondary named-runtime helper bootstraps previously restored the three runtime JSON files directly over
checked-in generated members, conflicting with private transport's no-replace rule. They now restore into
fresh staging and invoke the generation owner to admit the complete baseline, exact staged bytes, unchanged
execution closure and current authored inputs before installation. The existing lock, per-file atomic
replacement and manifest-last writer retain ownership; this is not an atomic three-file transaction.
Development bundles and generic transport replacement policy are unchanged. Focused contract source covers
the admission boundary; unit execution is skipped at the operator's request. Build, affected-package vet
and lint, Linux AMD64/ARM64 compilation, four-bundle regeneration/drift, Actionlint, YAML, formatting,
license, shell and diff checks passed; the vulnerability scan found no vulnerabilities. Hosted nightly
acceptance of this repair remains unproved.

The generator/validator/writer enforce a 500,000-byte limit and 50,000 expanded YAML parsing-event limit
for each workflow file. The entry workflow retains
the original named job slots, prerequisite closure, conditions and credential boundaries. Manifest-owned local
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
Complete bundle regeneration and drift checking remain mandatory. Complete native-preflight, pinned-checkout,
shared-preparation and structurally identical uncontrolled operation calls use descriptive aliases.
Complete identical native host sequences also share descriptive aliases for native preflight, pinned
credential-free checkout, current-job helper preparation and one or two uncontrolled node calls.
All producer jobs retain their IDs, runners, timeouts, conditions and credential boundaries in the entry
workflow. Preparation binds its selector through `github.job`; aliases resolve into independent nodes
before per-job substitution. Producer generation emits no reusable child workflows. The separately owned
qualification worker remains callable under its existing qualification contract.
Ordinary scheduling first appends offline observation, SBOM and packaging to a producer stage when that
stage owns every input, effective runner/platform and target agree, and the sixteen-node and summed-timeout
bounds hold. Cross-output joins and credential-bearing signing/publication remain separate. Where that rule
does not apply, scheduling pairs only the same BuildTarget's terminal observation and directly dependent
SBOM with compatible offline runtime. An additional input is admitted only for a pinned pipeline tool
from an independent root acquisition with no BuildTarget, IR instruction or inputs. The pair waits for
acquisition and restores the tool before observation; failure prevents pair completion. Additional source
or output dependencies are not admitted. Packaging with inputs outside one producer stage and other ordinary
nodes own separate jobs; native ARC disk groups retain their existing live-owner membership. Global topological
adjacency and fixed-size chunks no longer choose membership. This makes independent output additions
local to their own jobs, required aggregate assembly/publication, finalizer accounting and opening summary.
An earlier new owner can relocate an identical YAML anchor without changing the expanded existing jobs.
Generation uses only the current Plan, without scheduling state from previous generated files. Packaging
has its own provider success condition, removing accidental unrelated-output barriers; complete finalization
still requires every job and exact carrier. The projected provider graph is checked for cycles.
Redundant direct dependency edges are removed only when the full ancestor closure is preserved. Authored
setup, runtime variables, images or services mentioning `needs` retain ordinary direct lists. Large
finalizers use 16 fixed FNV-1a buckets, each limited to 64 jobs, to forward exact original results. Addition
does not rebalance existing membership; overflow fails before generation returns output. Collectors have no
token permissions, credentials, checkout or artifacts and introduce no producer execution barrier. Finalization
requires successful collectors, their exact Plan-derived partition and strict bounded reports before recovery.
The closing summary counts original outcomes, not collector success. Local contracts cover duplicate,
missing, extra, failed, skipped, canceled, oversized and inconsistent reports, including build-only Plans.

The combined production/development layout has 564 original jobs and 16 collectors, down from
596 original jobs and 16 collectors in the private-storage draft based on the same upstream code.
The production dependency graph has 1,375 edges, down from 1,438. Logical Plan identity and original
per-node result accounting remain unchanged by scheduling. Shared preparation selects the current
`github.job` internally, removing a redundant caller input while retaining exact projection checks.
Production is 380,112 bytes and 47,451 expanded YAML events; development is 359,349 bytes and 47,008 events.
The named ARM64 nightly and upstream-OVN entries are respectively 182,314/183,231 bytes and
22,125/22,117 events, each with 282 jobs and 644 dependency edges.
All four producer bundles keep their jobs inline. The earlier output-local grouping migration removed
accidental unrelated-output failure barriers; dependency reduction preserves the resulting ancestor closure.
The addition/removal regression compiles binary, native binary, collected native binary, source and signed
per-output publication fixtures. The binary fixture covers names sorting before, between and after existing
outputs; the other policies cover an earlier insertion, including possible YAML anchor relocation.
All seven cases preserve existing job IDs, relative emission order, expanded operations, invocation values
and shared actions outside required aggregate accounting. Collected fixtures also
preserve existing bucket membership; removing the output restores the complete original bundle byte-for-byte.
Separate contracts retain package success gating, pinned scanner dependencies and finalizer rejection of
missing, failed or skipped
package/scanner jobs. All maintained generated workflows have been regenerated, pass their exact drift
checks, and pass actionlint and yamllint. These are local contract and static validation results, not hosted
execution or runtime cost measurements.
The maintained rendering test fails above a 95% soft budget for both file bytes and expanded YAML events,
directing membership changes to compaction work. Alias expansion counts scalars, keys and container
start/end events; aliases reduce file bytes without reducing the provider's parsing count. The public
parser implementation supplies the 50,000-event bound, not a permanent provider service contract.
The private-storage draft at `afe87da2132bf91dc663a2cd0bfe6b7adb4e9ee6` exceeded that bound with 69,519
production events. GitHub rejected [Run 38833290328](https://github.com/dsx-ai-factory/nvkr/actions/runs/38833290328)
on push with `Maximum YAML nodes exceeded`; its API reports zero jobs and zero artifacts. That record is
workflow-validation failure evidence, not producer execution. The draft's generated producer trigger
remained manual dispatch. Local parsing admission does not establish hosted acceptance of the combined branch.
On 2026-10-15, a guarded diagnostic of the original 596-job, 2,546-edge representation at
`a49dafff794b360f2722d53912dc824243395ae6` still returned a dispatch 502 after replacing execution bodies
with no-ops. An independent 596-job graph was accepted in
[Run 38771142805](https://github.com/dsx-ai-factory/nvkr/actions/runs/38771142805)
at `dca9b19081ab3f4649b80c7c3b218c7cdc4b1540`; full transitive reduction was accepted in
[Run 38772767000](https://github.com/dsx-ai-factory/nvkr/actions/runs/38772767000)
at `0b1953a0fc97b08a86cb6389859c51402d10416a`. Both skipped all jobs. These isolate the dependency
representation as a relevant cause, without establishing a numerical provider limit or a single internal cause.

The exact regenerated inline production entry was accepted with HTTP 204 in
[Run 38792496084](https://github.com/dsx-ai-factory/nvkr/actions/runs/38792496084)
at `cc84c674551194275a11f6eac8846241593bad94`. All 612 jobs completed as skipped under their original
default-branch guards, with no steps executed or runners assigned. This establishes dispatch admission of
the 1,438-edge representation; it does not establish production execution.

An isolated inline probe exercised exact collector forwarding plus original/reduced failure and skip behavior in
[Run 38773205366](https://github.com/dsx-ai-factory/nvkr/actions/runs/38773205366)
at `554bbc838f7fff660242ade224f4c865f051c0b9`. Its comparison job passed all success/failure/skip
cases, including an always-running successful intermediary after a failed or skipped prerequisite.
The overall failure was intentional. These diagnostic probes do not establish complete producer,
signing, publication, qualification or runtime cost acceptance for the current generated layout.

Producer matrices and general callable interfaces remain deferred. Oversized workflows still fail before writes. Native offline disk preparation
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
The single checked-in `nvkr-run.yml` selects the 69-output M2 Definition and its main-only publication
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
separately. M2 now generates all 69 named outputs with four native disk jobs from the same Definition,
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
producer provenance with its remaining delivery/consumer gates. The approved initial scope also
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
predates the consumer-policy and timeout changes here; it is not evidence for this updated head, current candidate
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
credential reference, not an unprovisioned `NVKE_SOURCE_TOKEN`. Dedicated reader-account package access, native pool capacity and successful exact-head
execution remain external acceptance requirements.

## Maintained input and schema boundaries

The NVKE four-family pilot adds independent core (20 outputs), services (28), telemetry (17) and disks (4)
Definitions under `inputs/definitions/nvke/`, with corresponding `github-nvke-<family>` Environments and
named production bundles. Their selected output union is the combined candidate's 69 outputs, retaining
aggregate native children, bound chart/image relationships and complete disk material selections. They
reuse shared catalog/pin owners and publish into separate family prefixes beneath the existing candidate
namespace. `make nvke-split-generate` and `make nvke-split-check` own the family subset; the repository-wide
workflow targets include it. These are authored-input and generation changes, not a new lifecycle or
cross-Run assembly contract. No hosted family execution, publication or independent consumer acceptance
is recorded. The combined production/development and qualification paths remain until consumer and
qualification cutover. Named producer qualification and named development remain unsupported; the pilot
emits no automatic association file or parent Release. Consumers independently verify exact family
references and own any required association or compatibility decision. See the
[pilot inputs](../../inputs/definitions/nvke/README.md#four-family-pilot) and
[partitioning strategy](../definition-partitioning-strategy.md) for scope and historical measurements.

The 2026-10-20 integration of upstream `9857feeda03d2e7f529e1baef2b4a331a011e54c` reproduced
`CodePipelineDrift` in all four family bundles. Regeneration through `make nvke-split-generate` refreshed
eleven JSON files, including all four Plans and manifests; family Environments, entry workflows and action
templates remain unchanged. Independent inspection retains the exact disjoint union of 73 complete native
build records and 69 output records against the updated combined Plan. The extended exact-regeneration
contract passed in 4.05 seconds and the complete focused family/credential suite in 32.747 seconds on
2026-10-20, with Go 1.27.2 on Darwin ARM64 and recorded staged tree
`e9187e1e6ce5e5fda6d26e04b912d10622f559c0`. The
[current measurements](../definition-partitioning-strategy.md#current-local-validation) record each family.
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

The maintained closure inventory checks every committed producer bundle and both qualification pairs
offline, without skipping when private sources are unavailable. It checks exact policy selections, member
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
passed at 23:43:57 UTC. The [historical measurement table](../definition-partitioning-strategy.md#historical-validation-at-250f842c)
records dimensions and scope. Raw local logs remain unpublished and omit a complete tested working-tree
hash and direct toolchain dump; they cannot independently bind those measurements to the final commit.
Hosted CI is separate evidence, not a receipt for those local commands. The earlier all-owner workflow check's unchanged-byte comparison applied
only to the preceding integration, not the current catalog and generated JSON changes. The linked historical
section gives reproducible immutable old/new ref-and-path checks and their exclusions.

Every helper bootstrap retains the authored dispatch guard before checkout, helper compilation and private
CI-storage upload, including helpers without catalog credentials. Negative contracts reject missing or
broadened guards, unadmitted feature branches, tags and push events across build-only, publication and
named-runtime helpers. No hosted family execution of this boundary is recorded.
[ADR-18](0018-pipeline-ir-and-ci-adapter-emission.md) owns the contract;
[bootstrap dispatch tests](../../pkg/nvkr/factory/github_bootstrap_dispatch_test.go) cover refusal.

All four family Environments retain the maintained production `NVKR_REGISTRY_WRITE` publication policy.
The exact `m2-candidate/disks` prefix also admits the existing pinned M2 toolchains for separate preparation
with the fixed independent registry reader. Focused contracts reproduce and fix the previously omitted
preparation, require it before all four disk producers and source-vendor execution, and reject lookalike,
child, other-family and foreign-owner exceptions. Credential projection and cleanup contracts pass; they
do not establish provisioned registry access or hosted family execution.

The services family requires private chart pin-set revalidation even though its selected producers need no
Git fetch node. Source-access admission now also recognizes the actual pin-set sources in revalidated
catalog snapshots. It retains rejection of unrelated repository entries and the existing step-scoped
credential projection: catalog recompilation may receive these credentials; artifact acquisition, build
and publication do not. This closes a local admission gap for acquired-only selections, not a new source
trust or credential scope. No hosted private-source read or services publication is established by this
change; [ADR-20](0020-artifact-publication-and-target-configuration.md#2-target-configuration-surface) owns
the contract.
Focused local contracts pass for root/output pin-set-only sources, mixed admitted/unrelated access,
ordinary-source and unknown-repository refusal, local-engine admission and snapshot substitution without
partial output. A local Git fixture composes real acquired-only loading with rendering: the locked commit
survives a moved upstream tag; malformed metadata, stale versions and stale provenance return no Plan or
bundle. Exact catalog credential helper/mirror configuration is confined to revalidation steps.
[Source-access contracts](../../pkg/nvkr/factory/source_access_test.go),
[real loader composition](../../pkg/nvkr/factory/source_access_loader_test.go).

Resolved snapshots reuse the catalog owner's intrinsic PinSet declaration checks. Malformed file paths and
unsupported formats first reproduced acceptance in low-level compilation and rehashed root/output Plan
validation, then were rejected with no Plan or credential-bearing bundle. Valid base catalogs and explicit
producer overlays remain supported. These semantic checks and snapshot identity do not authenticate
upstream contents; authored loading retains reciprocal membership, lock and exact pinned-tree checks.
[ADR-22](0022-catalog-pinning-and-the-lock-contract.md#7-derived-pin-sets-one-source-may-curate-other-sources-pins)
owns the boundary; [compiler contracts](../../pkg/nvkr/factory/pin_set_semantics_test.go) cover it.

The maintained family matrix asserts combined/core/services catalog credential confinement and
telemetry/disks absence, including raw-secret aliases and exact repository/helper/mirror bindings while
preserving legitimate source-fetch credentials. All nondisk families explicitly reject the private-toolchain
exception and emit no reader preparation. Default tests compare all authored family Definitions and
Environments to committed Plans; full compilation/rendering remains extended. During the preceding
`c01c9132` integration, the focused
family matrix passed in 34.227 seconds, including the 73/69 identity comparison and the actual
disks-family credential regression in 0.83 seconds. These are local
contracts, not evidence of private registry access, hosted family execution or consumer acceptance.

The [M2 Definition instructions](../../inputs/definitions/nvke/README.md) own example-specific selection and input
preparation. The candidate requires 69 named outputs. It retains the previous 37 and adds source-built
CAPNICo and explicitly named reference-profile fixtures, the existing provisioned infrastructure provider, source-packaged NVKE telemetry pipelines,
and fourteen acquired runtime-image components delivered separately for both native architectures.
The selected NVKE/CAPI sources and NVKE telemetry pipelines dependency charts own their versions; Helm test-only images are
excluded. `nico-reference-profiles-fixture` identifies reference test data in the release membership itself,
not fleet configuration, the runnable demo's `controller-profiles`, or a complete legacy OSProfile set.
Provider manifests, image-reference relocation and installation remain consumer work, not delivery proved here.
Native children are locked separately from source-owned index parents, and materialization authenticates actual
membership. Controlled source/pin/image tests and exact-source approval do not prove external layer acquisition,
scanner acceptance or complete production input approval. The
[acquisition observations](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#temporary-spire-source-selection) retain the historical mixed-source candidate and HTTP-403 limits.
The maintained release and native demo inputs now select the same stock NVKE `v0.22.0`
source, `400c24fb6465b042be883d6b62472528520d6291`, for OS assembly, agent, Calico,
kubeadm and SPIRE packaging, including the renamed Packer targets, disk filenames,
material schema and required Ubuntu-series input. SPIRE uses that release's revision-4
package and source-owned public acquisition/packaging commands; no pre-DSX source
rollback is selected. The default package's removal of DSX IMDS is an upstream release
contract, not a missing private dependency waived by NVKR. Historical DSX source,
distribution and compatibility questions are recorded in
[NVKE #1641](https://github.com/dsx-ai-factory/nvke/issues/1641); the older HTTP-403
observations are not a current v0.22.0 build blocker. No source gate, submodule rejection
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
M4's tag, channel and revocation protocols remain separate deliverables, and manifest issuance has only contract coverage; waivers are deferred
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

A verified publication's held closure can be exported only with its owner's
exact-input receipt and expected aggregate invocation. Named binary/chart
extraction and image export retain delivery ownership. The downstream harness
uses the held publication directly rather than adding another command interface.

The standalone input-staging command and compile-only adapter are removed.
The [NVKE nightly coordinator](../nvke-nightly.md) instead declares scheduled and
manual GitHub execution: complete project producer, exact run correlation,
authenticated publication handoff, existing NVKE Go tests on kind plus real KVM
guests, and separate test/resource-cleanup verdicts. This is downstream repository
CI infrastructure outside the NVKR product lifecycle and v1 acceptance. ADR-16
retains the passive external-conformance boundary; ADR-18 §3 records the adapter's
native runner and trust limits, not a new product runtime engine or ratified decision.
Named generated bundles isolate these project producers from the retained M2
workflow and manifests.

Named-bundle contract tests in `pkg/nvkr/factory/github_bundle_test.go` and
`pkg/nvkr/definition/github_bundle_test.go` cover default behavior, coexistence,
independent drift/retirement, cross-namespace refusal, exact utility Plan paths
and name admission before catalog resolution. Named producers use the disjoint
`nvkr-project-<bundle>` display-name namespace. Same-Definition contracts cover
distinct workflow/concurrency names, display-name validation and exclusion from
the default qualifier's `workflow_run` selection; default/development display names
and all workflow paths are preserved. Those contracts do not prove hosted
execution, complete crash recovery or acceptance of scheduled downstream tests.

NVKE owns guest/cluster provisioning, assertions and teardown. The parent harness
owns host admission, serialization, outer leases, the disk service and journaled
network-control restoration. Successful cycles require both owners' cleanup;
missing cleanup evidence retains private recovery controls. NVKE runs as fully
trusted pinned code with sudo and Docker access under the parent's UID, including
root-equivalent access to its read-only job token. Environment scrubbing is not
an independent sandbox, and self-written verdicts are unsigned diagnostics.

The two maintained profiles select native ARM64 KVM guests and explicit NVIDIA-fork
or upstream OVN. Selection validates these capabilities and exact suite revisions;
there is no caller-supplied security approval marker. ADR-13 keeps artifact vulnerability
scanning and clearance outside v1; the existing repository vulnerability gate is unchanged.
The chart sources replace the local-only image ID with the existing registry-addressable
Go toolchain and checksum-pinned Helm 4.1.4 acquisition. Vendoring captures the executable
and pinned dependency archives; packaging consumes the held source closure offline.
Installer Helm 4.2.4, operator Helm 4.1.4 and independent Cosign 3.1.3 retain explicit
checksum bindings. Acquisition integrity does not grant vulnerability clearance.
The generated nightly source-fetch and native disk jobs prepare their selected private
M2 toolchains through the existing read-only image-preparation owner. Temporary pull
credentials are removed before source or producer execution; the nightly output
namespace does not require republishing the existing pinned toolchains.
The four CLI, controller and chart candidate sources now select stock NVKE
`v0.22.0` at `400c24fb6465b042be883d6b62472528520d6291`.
The nightly consumer projects authenticated profile bytes into the current resource
API and supplies separate site and stack configuration, credentials through the child
environment, and telemetry through the installer values handoff. Both the normal and upstream-OVN
disk URLs, where required by the selected profile, read the same held disk and are
checked against the exact selected Artifact. Focused contract and HTTP race tests
passed for the preceding beta selection, and that CLI validated and rendered the diagnostic site spec. Those checks do not validate the current v0.22.0 source.
The user-approved test-only suite pin is `b4f90e2c4fc4ed3d07efc18a22066a7e0b9b99c8`,
a signed compatibility revision of the earlier `a3206710` suite, with beta.6 deployment
templates and an explicit stack-config/credential/telemetry handoff. Both independently
authored nightly profiles retain this harness pin and its candidate entrypoint, which
is absent from the selected v0.22.0 product source. The [hosted source-contract Run](https://github.com/dsx-ai-factory/nvkr/actions/runs/39487106857/attempts/1)
on NVKR `8df540b83aeaec5e27b865dcd65887882699755b` passed the actual
candidate render function with the beta.6 CLI and the hook argv/environment
regression. This source-contract-only dispatch skipped the ordinary selection,
build, runtime selection and runtime-test jobs. The later workflow selector correction
is covered separately by the shell-versus-Go parity test in the successful
[Merge Gate on `caf85b27`](https://github.com/dsx-ai-factory/nvkr/actions/runs/39487820535).
This verifies configuration composition, not published-artifact or VM
runtime acceptance; full runtime validation of the current selection remains unverified.
Product sources, including SPIRE packaging, select v0.22.0; the test harness retains
its independent pinned revision. This diagnostic selection is an exception to
ADR-23's proposed matching-revision rule, not adoption of that proposal. Missing
candidate hooks still fail admission; the generic rebuilding suite is no fallback.
These source and adapter checks do not establish overall interface adoption or runtime acceptance.
ADR-23 remains proposed and does not authorize this adapter's schedule, runner or
credential policy; broader acceptance remains open.
The authored daily trigger is
retained at the user's explicit request and will execute after merge even though
no combined manual cycle is recorded here.
Existing ARC disk production and NVKE's kind plus ARM64 VM lane have completed
externally, as linked in the nightly guide. The new job retains host/network preflight
and publication verification. The
[first scheduled coordinator](https://github.com/dsx-ai-factory/nvkr/actions/runs/38676875690)
started on `b77229c2cc7bedfc422af506c1cdfbbb655aba48` at
2026-10-15 03:33:24 UTC. Both producer workflows failed and runtime tests were skipped.
The NVIDIA-fork producer lacked its exact private OVN source-access binding;
the corrected Environment passes the existing token only to the selected fetches,
with private/public OVN rendering regression coverage. The upstream producer
sent the NICoSim asset archive to the Helm reader and separately failed disk KVM
admission. The operator reported a subsequent runner repair, without a recorded
combined rerun here. No successful publication-to-runtime cycle, runtime cleanup
verdict or external acceptance of the downstream nightly adapter is claimed.

The archive correction selects source-built tarball assets in executable bundles
through exact resolved-component and producer/output membership. Helm and disk
inspection retain their separate paths. Bounded-reader, serialized-shape and
executor/finalization contracts cover exact subjects and reject method/scanner
substitution. A separate Linux ARM64 diagnostic reconstructed the simulator archive
from pinned source `6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4` with the catalog's
epoch `1791419815`. The archive digest was
`sha256:dcdcf029cb3f2b72e06b9de987ab32c2ecb2e83ae73b92e1eca64235d21af5c6`;
the owning API emitted a validated `archive-content/v1` document with 13 regular
file members and no scanner claim. This was a reconstructed input, not a downloaded
CI artifact or proof of hosted publication. Its file hashes do not establish
package versions or transitive dependencies.
Site-specific NICo provisioning targets and the special TestGrid publisher remain deferred.
Unsigned runtime reports do not mint qualified conformance or promotion receipts.

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
