## Current boundary

The maintained public workflow is **Definition → Run → Release**, with Environment, Artifact and
Verification Policy as the other public objects. Definition v5 selects exact producers for named outputs,
with a native `platform` or an explicit OCI aggregate `platforms` set;
there is no authored Recipe, cluster topology, placement, inheritance or composition lifecycle. Plans and
internal evidence records are generated or authenticated rather than separately authored public inputs.
Proposed ADR-26 composite Definitions (units U1–U7) are implemented and contract-tested only; see its row below.
Proposed ADR-27 pinned-tool producers are implemented and contract-tested; selecting the OCI layout form waits for
the layout import, and no recorded Run has executed a pinned release tool.
Proposed ADR-28 multi-platform executable outputs are designed only; executable outputs remain single-platform.
Proposed ADR-29 standalone observation is design only; existing conformance admission does not enable it.

Planning, byte-exact GitHub generation, Plan/graph/diff views and metadata-only release navigation are
implemented. Diagnostic pulling retrieves and checks one named output's complete payload closure from a
registry release: executable files, a plain or declared LUKS2-encrypted QCOW2 disk, an unchanged Helm archive,
an OCI image layout including all declared aggregate platforms, an APT repository tree, or a validated
generic bundle archive.
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
The [native ARC record below](native-arc-disk-route.md#native-arc-disk-route) binds the exact output and retained evidence.
The [pinned-source audit](../../../inputs/definitions/diagnostics/fde-audit.md) records retained bootstrap
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
Ordinary Mac and Linux BUILD now share default input selection and local toolchain
preparation. The demo delegates platform-domain admission to NVKR's target owner,
derives Linux input and conformance-output names from that platform, and uses one
host-tool membership/alias path. Platform-specific virtualization prerequisites
remain backend checks. BUILD and E2E share static build admission before
presentation; E2E invokes it before runtime probes, narration or sudo. Missing default input files return a
capability refusal; qualification retains its Linux ARM64 host admission before
the launcher. Shared
prerequisite admission checks command availability,
Buildx and a matching Linux Docker daemon before preparation state. Empty guest
architecture bindings are refused rather than assigned an ARM64 default. Optional
conformance admits the exact named host tools and Linux suite outputs from the
replayed completed Run before deployment, with payload authentication retained by
the conformance owner. Matching AMD64 upstream/conformance default Definitions
are not supplied; this cleanup does not establish a new runtime acceptance result.
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
