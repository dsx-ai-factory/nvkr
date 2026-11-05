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

The current candidate selects stock NVKE `v0.23.0` at
[`2cfd00081b468d85a4894ec17e9deac13c9d8169`](https://github.com/dsx-ai-factory/nvke/commit/2cfd00081b468d85a4894ec17e9deac13c9d8169)
through `catalog pin --tag` for all maintained NVKE product-build source roles,
including SPIRE packaging and the new DOCA OVS role. Chart versions, native demo source admission and catalog
overlay bindings follow that same release. The release replaces Ubuntu's `openvswitch-switch` with NVIDIA's
DOCA OVS 3.5.0 and makes a `doca-ovs` archive a required node-image input. Each maintained node-image
catalog therefore adds an `nvke-doca-ovs` producer. Its vendor stage runs the source-owned DOCA Makefile and
fetcher, which check every deb against NVKE's committed per-package SHA-256 lock. Its offline build admits
exactly the locked eight-package set as the sixth image-inputs material. Release and diagnostic producers
select the Ubuntu 26.04 packages, and the Ubuntu 24 overlay replaces that producer with the 24.04 set.
Release producers keep NVKE's unmodified netplan files. The ARM64 and hosted AMD64 Packer toolchains contain
the fetch prerequisites. Hosted nightly producer
[Run 41383109720](https://github.com/dsx-ai-factory/nvkr/actions/runs/41383109720) used the same source-owned
fetch on a GitHub-hosted ARM64 runner. That Run is not evidence for the tagged release producers. The tagged
CAPI exec-auth init image now selects NVKE's `alpine:3.24.3` parent, and the nightly's separate current-main
overlay is retired. NKTP selects OpenTelemetry kube-stack chart 0.25.1 and its operator, target-allocator,
LGTM and cleanup-kubectl image updates. The two independent nightly
test-harness selections retain `b4f90e2c4fc4ed3d07efc18a22066a7e0b9b99c8` because the
release omits their reviewed candidate entrypoint; they are not product-build pins.
The Packer toolchain lock's `validatedNVKE` record remains historical validation of
`726f76944404e1106626b8333d3576bdf67b575b`, not evidence for the current release.
Hosted validation and a complete fresh local E2E/conformance result for v0.23.0
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
[ADR-16](../0016-release-gates-and-digest-first-selection.md) records earlier caller deadlines, joined cleanup
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
The signed storage download that follows each locator ZIP redirect had no retry. In GPU site composite
[Run 41884498057](https://github.com/dsx-ai-factory/nvkr/actions/runs/41884498057), the metadata assembly job, which
reads 406 locators, failed with that storage request's HTTP 503 on three consecutive attempts. The storage download now
uses the same bounded transient-server budget, with recovery and exhaustion cases in the contract suite.

Private-carrier repository admission in [Run 42100660131](https://github.com/dsx-ai-factory/nvkr/actions/runs/42100660131),
[ARM64 node-exporter signing job](https://github.com/dsx-ai-factory/nvkr/actions/runs/42100660131/job/127518111891),
reported an unauthenticated source identity during restore. That path previously classified every non-200
repository response as a trust failure, so the diagnostic does not establish the original HTTP status.
Repository and package admission GETs now permit at most four requests for HTTP 5xx, 429 or a 403 JSON
secondary-rate-limit response, using the existing bounded `Retry-After` parser and cancellable backoff.
All retryable API responses refuse exhausted or ambiguous primary quota before waiting or retrying.
The operation has an eight-minute bound; individual requests retain their thirty-second timeout. Final HTTP
failures report their status with fetch/availability codes; invalid identities and privacy metadata in HTTP
200 responses remain trust failures. A failed conditional request invalidates its representation before retry.
`pkg/nvkr/cistore/privacy_http_test.go` covers recovery, exhaustion, server delays, primary-quota refusal,
cancellation, credential scope and stale-response rejection. Simulated-clock tests through repository and
package admission pin the eight-minute operation budget, thirty-second request deadlines, successful retry
beyond thirty seconds and precedence of an earlier parent deadline. These are local contract tests; no hosted
recovery with this fix is recorded.

The explicit `githubDevelopment` Environment setting selects separate fixed workflow/Plan/manifest paths
with the same compiler, complete graph and helper. The committed development bundle now uses the 83-output
[GPU site composite](../../../inputs/definitions/nvke/README.md#gpu-site-composite), including all eight native plain/public-bootstrap
FDE disk outputs of both Ubuntu series, output-local publication, SBOM attestation, cosign signing, verification,
metadata assembly and finalization; the combined 69-output candidate no longer has a development bundle. It retains the candidate's existing disk
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
namespace binding and generated credential/owner separation. Composite qualification is deferred (ADR-26 U8),
so the committed development pair (`nvkr-dev-qualify.yml`, `nvkr-dev-smoke.yml`, `.github/nvkr-dev-qualification/`)
is retired with the move to the composite; the qualification path itself remains contract-tested against a
compiled plain development Plan.
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
