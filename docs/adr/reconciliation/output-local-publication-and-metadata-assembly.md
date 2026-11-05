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
Helm outputs expose `helm.defaultDigest` in AICR's `sha256:<64 lowercase hex>` format. Delivery projects
the existing publication blob digest checked against the chart-content layer; manifest identities and
publication records are unchanged. Contract tests compare it with exact packaged archive bytes, retain it
with missing or corrupt payloads during metadata inspection, reject a mismatched publication binding,
omit it for other output kinds and cover filtered JSON. The lifecycle test exercises source-chart packaging
through release export. These tests do not establish AICR bundle execution or external registry acceptance.
Export requires only the immutable publication reference and preserves every named output, native platform,
aggregate image-index digest/child membership and exact evidence/source binding. Source repository/commit
descriptions come from output-bound source SBOM properties, without inferred Git tags or a release-wide
source pin. Different revisions of one repository remain valid descriptive records. The inspection scope and
individual checks performed are separate from publisher-declared qualification. No NVKE role dictionary,
installer schema, separate Plan/mapping or unpublished workspace path remains. Consumer-owned code selects
roles, compatible revisions, platforms and complete installation dependencies. The
[release navigation reference](../../release-navigation.md) owns the versioned contract. Targeted lifecycle,
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
`release pull` layer downloads share the bounded HTTP/2 peer-reset reacquisition used by registry and
checked-publication pulls: at most three attempts within the command deadline, each removing its partial
download and re-hashing the complete layer. Resets after the declared size, integrity failures and
cancellation are not retried. `httptest` contracts cover recovery, a tampered reacquisition, exhausted
attempts and prompt cancellation; no external large-disk `release pull` has been re-run since.

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
environment values; nested transfer actions consume the supported `env` context. Authored
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
writer are admitted only for the exact trusted workflow commit. The maintained hosted nightly uploads no attachments. These safeguards and job consolidation
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
finalizers use the narrowest of 16, 32 or 64 fixed FNV-1a buckets whose fullest bucket holds at most 64
jobs to forward exact original results; finalizers that fit 16 buckets keep their membership, and committed
16-bucket bundles regenerate byte-identically. At a given width, addition does not rebalance existing
membership; 64-bucket overflow, or a layout over the finalizer parser's token budget, fails before
generation returns output. Collectors have no
token permissions, credentials, checkout or artifacts and introduce no producer execution barrier. Finalization
requires successful collectors, their exact Plan-derived partition and strict bounded reports before recovery.
The closing summary counts original outcomes, not collector success. Local contracts cover duplicate,
missing, extra, failed, skipped, canceled, oversized and inconsistent reports, including build-only Plans.

An opt-in `githubExecutionPartitions` projection now generates bounded same-commit workers beneath one
Definition/Plan/Run/Release. Focused local tests cover deterministic grouping, original job membership,
worker/caller generation, exact-member tamper refusal and manifest retirement. The maintained development
input projected to 21 workers with a 368,371-byte entry before timeout splitting and fragment forwarding (below). OIDC-signing jobs remain inline to preserve the
entry certificate identity; generated caller/workers pass actionlint. These are
local generation measurements, not product execution evidence. A separate minimal diagnostic at commit
`902f3febd10cd61abecb82571f66283857169851` observed successful result forwarding on
[success](https://github.com/dsx-ai-factory/nvkr/actions/runs/40606449617),
[intentional failure](https://github.com/dsx-ai-factory/nvkr/actions/runs/40606454753), and
[cancellation](https://github.com/dsx-ai-factory/nvkr/actions/runs/40606620670).
The cancellation report reached the finalizer intact; its diagnostic assertion failed because the dependent
was cancelled rather than skipped. The failure run also showed that an ordinary descendant remains skipped
after a successful unconditional recovery of a failed ancestor. The projection now carries those ancestor
results through recovery boundaries; focused contracts, actionlint, yamllint and package lint pass.
The full local gate, build, diff checks and all-owner workflow regeneration checks pass on Go 1.27.3
Darwin ARM64. Earlier attempts encountered Helm cancellation cleanup failure and pinned catalog fetch
timeouts; unchanged reruns passed after transport recovered, without increasing timeouts or weakening
authentication.

The full partitioned development [Run 40727132359, attempt 1](https://github.com/dsx-ai-factory/nvkr/actions/runs/40727132359/attempts/1)
at commit `28df13596fa63a13060f94fb8f535d9f936393a5` completed with all 601 jobs successful,
including 21 partition reporters and one finalizer. The retained finalizer input contains 16 collector
reports covering exactly 563 original operations, all successful, with no omissions or duplicates against
generated operation and runner-bootstrap membership. Its private final Run carrier was retrieved through
the existing request-bound, digest-checked transport. The Run record reports `complete` in the
`pipeline-attested` lane with exactly 69 expected and observed named outputs, matching Build Digests,
and Plan digest `sha256:9ff03b878c6e326163ccbfc752a3c34cb59fd723dedfc3a346493c429974034f`.
Inspection of the retained release metadata checked 391 metadata blob hashes and matched all 69 output
descriptor digests and Build Digests to that Run. Each named output's publication reference matched a
non-dry-run verification record reporting successful signature and provenance verification. The release
verification record reports the same for publication root
`ghcr.io/dsx-ai-factory/nvkr/development/runs/40727132359/attempts/1/releases@sha256:6a430259c9fbe2d169f0ec966fcf0405937c948198f5bec081a086ecc1e83a46`.
The observed signer is the entry workflow `nvkr-dev.yml@refs/heads/codex/nvke-execution-partitioning`
under the GitHub Actions issuer. These are inspected hosted execution records, not a new independent
local full-payload readback or qualification result. This is earlier-head evidence: it establishes the
successful full development execution path at `28df1359`, not at the current branch head. Full-product
failure/cancellation recovery and production qualification remain unproven.
The production workflow remains flat.

On 2026-10-28/29, an independent consumer exercise read that same Release from Darwin ARM64, using NVKR
built at `75f154fe4c6a81decca9942c07b347003a99e23e` with Go 1.27.3. It exercised the earlier-head Run above,
not a hosted execution of the current head. Only read-only registry pulls were used. Anonymous access to
the private root failed with `UNAUTHORIZED`, so metadata and payload reads used the operator's existing
GHCR read access through the default keychain. `release export --reference <root>` checked the complete
metadata closure. It reported Definition `sha256:20636464d35ad094ca1143832e9f62caf4e9acf15c98803e645220b4984a2e67`,
Environment `sha256:5e8dd9cdf7d052deaa75ec08281b62f7536b3c2e4153c744b7531408d98faebf`, the Plan digest above,
which also matches the committed development Plan at the Run commit, content subject
`sha256:f09d12404c59f7f42c432cb7561002f57ca09565dacf4680f36c71fa2bf91836`, the Run's invocation ID and
declared qualification `diagnostic`. It listed 69 named outputs: 56 OCI (including 4 aggregate
indexes), 6 Helm, 4 disk and 3 executable, plus 120 evidence records. One root, one Plan and one invocation
are consistent with partitioning that changes only execution organization.

`release pull --reference <root> --output <name> --out <new dir>` then retrieved and integrity-checked five
selected payload closures. They were `nvkectl-linux-arm64` (executable), `controller-chart` (Helm),
`capi-controller-image` (Docker schema-2 image), `cwe` (two-platform aggregate index) and
`nvke-node-image-arm64` (plain QCOW2 disk). An independent standard-library re-hash outside NVKR
reproduced each byte identity against the exported description:

- The `nvkectl` file (mode 0755) hashes to its Build Subject `sha256:889204e16350949560010aa1d153311acd9c6e285c59fa976b5b6e8a86b112f6`.
- The chart archive hashes to its packaging SBOM's bound inspection input `sha256:46d02c099079defbe97b1e60009cdbfedce9496af6d9250c8438689572277938`.
- Every layout blob hashes to its content address, with no missing closure descriptor.
- The image's top manifest is its output digest `sha256:0bd9a073959ec3bf769ced2a5e0cde2d666f0be35ea03d334bb4529e851ce46a`.
- The aggregate layout's `index.json` bytes hash to the output digest `sha256:110f8e8ca18822450cd6587d8a8061d8915ced452ec7cb337620945214aa7e4c`, and its children are exactly the two declared platform manifests.
- The 3,344,171,008-byte disk hashes to its recorded (not here authenticated) disk-metadata payload digest `sha256:67f0060aec03753f3f3834656835cc194ad452f5f8f7888bdd421a5d740bad76` and begins with the QCOW2 magic.

The disk pull failed closed twice with `NVKR-E-FETCH-FAILED` after an HTTP/2 peer `PROTOCOL_ERROR`, leaving
no destination. A diagnostic pull with `GODEBUG=http2client=0` succeeded unchanged otherwise. Unlike
the `delivery.Pull` path, `release pull` at that build read the output layer without the existing HTTP/2
peer-reset reacquisition, so large disk pulls were transport-sensitive; later layer recovery is described above.

Signature, provenance and membership authentication did not complete. `scripts/demo-release-consumer.sh
--runtime docker` ran `pipeline-attested`/`artifact-publication` verification with the following inputs:

- Identity `^https://github\.com/dsx-ai-factory/nvkr/\.github/workflows/nvkr-dev\.yml@refs/heads/codex/nvke-execution-partitioning$`, the authored development Environment pattern narrowed to the exercised branch.
- Issuer `https://token.actions.githubusercontent.com`.
- Cosign 3.1.3 Linux AMD64 `sha256:4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71`, as pinned by the development smoke workflow.
- The existing inline registry configuration, mounted read-only.

It failed closed with `NVKR-E-SIGNATURE-INVALID` while reading the root's OCI verification bundle, because
that request was anonymous. `release verify` supplies no registry credential to its consumer, and the
signature-bundle reader treats an empty credential as anonymous without consulting a keychain. Metadata
and payload reads do use the keychain. The supported CLI therefore cannot authenticate a private-registry
Release, and `authenticity` scope shares the same reader. `client/v1` accepts an explicit credential, but
supplying one would mean extracting a registry secret, so that step was not taken.

This exercise establishes independent consumer readback of the complete metadata closure and of five
payload closures' exact bytes. It does not establish signer authentication, provenance or SBOM
verification, membership authorization, an accepted Release, qualification, or deployment acceptance.
Hosted verification records remain the only signature evidence for this Release. Development publications
expire under attempt cleanup, so later repetition may be impossible.

The execution-model extraction keeps scheduling semantics in factory's provider-independent graph:
typed success/completion conditions, recovery prerequisites, disjoint operation membership, acyclic
partition contraction, and exact individual outcome accounting. GitHub derives that graph from typed
stages and validates its final inline/worker placement before rendering. Local equivalence contracts
compare flat ancestor evaluation with partition result evaluation at capacities 1, 2 and 64, enumerating
success, failure, cancellation and skipped operation outcomes with and without Run cancellation.
These contracts do not establish another adapter or full-product provider cancellation recovery.
The extraction passes the full local gate with uncached tests on Go 1.27.3 Darwin ARM64, build,
all-owner workflow regeneration, and the targeted extended partition contracts including actionlint and
yamllint. Regeneration retains the exercised development bundle byte-for-byte, including its Plan digest,
through the current head. The hosted Run above predates this internal extraction and executed earlier helper
code; bundle identity does not make it a hosted execution of the refactored code. No hosted Run of the
current head is recorded.

Partitioned failure handling is contract-tested locally on the reference Plan and its finalizer, not
externally exercised. The fixture compiles six repeated reference outputs into 72 operations; it does not
cover the maintained product's native-disk, aggregate or Helm stage diversity. The contract evaluates
outcomes over the model's partitions and hands them to the finalizer in collector shape; rendered
worker/inline placement, worker guard expressions and worker report reconstruction are outside it. On
that compiled partitioned per-output Plan, simulated provider outcomes for release verification failure,
cancellation after the first worker, successful and failed recovery after an ancestor failure, and skipped descendants match
flat ancestor evaluation for every original operation. Those outcomes, in the caller's collector shape,
pass through finalizer recovery: each restore outcome follows its exact producer result, and restore
validation refuses every incomplete set with `NVKR-E-PROVENANCE-MISSING`. This includes an inconsistent
description in which only an operation that restores nothing failed. A worker that never reports, and
missing, foreign, duplicate, malformed, unsupported, truncated or status-inconsistent member reports and
canceled collectors, are refused with `NVKR-E-INVALID-CATALOG` before any recovery work. Finalizer
cancellation returns `NVKR-E-TIMEOUT`. A lost worker is refused as an unaccounted report; it is not
recorded as a per-operation cancellation. GitHub cancellation timing, worker startup failure and runner
cleanup are not shown by these contracts. No Run has yet exercised a full-product partitioned failure or
cancellation. Before the merge with main, the change passed the full local gate on Go 1.27.3 Darwin
ARM64. Uncached `go test ./...` passed with `-timeout=90m`; under Go's default 10-minute timeout the
lifecycle package timed out on that loaded shared host. The CI factory and lifecycle shards passed locally
inside the 20-minute `GO_TEST_TIMEOUT` shard default. The gate also covered golangci-lint 2.14.0 with zero
issues, vet, formatting, license headers, shellcheck, the CI-tool tests and vulnerability scanning. After
the merge with main, merge-gate Actions run 40962543119 passed every factory, lifecycle and package shard.

Partition grouping now orders each dependency depth by the bounded finish time of declared job timeouts
and splits a depth at the widest finish gaps up to a 30-worker target. The regenerated development bundle
keeps its Plan bytes, moves from 21 to 30 workers and passes exact drift checks. Unit contracts cover
ordering, gap splitting, pinned entry jobs and coded rejection of missing bounds or unknown pinned jobs.
The [scheduling harness](../../execution-partition-scheduling.md) simulates the maintained NVKE and hosted
diagnostic Plans with estimated durations anchored to recorded disk, signing and verification timings; it
estimates a 17–26% shorter development makespan across three assumed capacity scenarios and no makespan
loss on any measured Plan. These are local simulated contracts, not hosted wall-time evidence. The hosted
development Run above used the original depth grouping; the timeout grouping has not been externally
exercised.

Entry-workflow reduction keeps every signer inline. Workers now export per-consumer result fragments, and the
entry joins one fragment per worker instead of re-projecting each member from the worker's complete report.
Regeneration through `run generate` leaves the development Plan byte-identical. Against the 30-worker timeout
layout, the entry falls from 373,817 to 264,038 bytes. The workers grow by 185,152 bytes in total, so the complete bundle grows by 75,373
bytes, and the largest file is now the entry.
Signer jobs and the finalizer are byte-identical. Expanded parsing events in the entry are unchanged at
13,308, because signers dominate them: 139 OIDC jobs account for 10,984 events. The flat default producer
was then at 394,695 bytes and 47,055 events, which is 94.1% of the 50,000-event bound. Its 139 signers
accounted for 10,984 of those events. Contracts resolve every forwarded fragment back to the exact
Plan-derived collector members, statuses and worker dependency reads. They also check that an unreported
worker's fallback reproduces the replaced projection's null results and empty statuses. They also require each committed
producer's OIDC jobs to sit in the entry workflow named by its anchored signer policy, and that policy to
admit no other committed workflow file. Lowering refuses worker or caller `id-token` declarations with
`CodeEngineCapability`. These are local generation and contract results. The hosted Run 40727132359 above
predates fragment forwarding, so hosted execution of the new forwarding, including failure and cancellation
reporting, is unrecorded. Signer relocation remains a proposed ADR-18 §8 design with no implementation.

The maintained release Environment now also selects `githubExecutionPartitions`. The NVKE v0.23.0 DOCA OVS
producer added one source fetch per release disk and took the flat release workflow to 47,731 events,
above the 47,500 maintained threshold. Partitioned, the release entry has 272,696 bytes and 13,333 events;
the largest of its 30 workers has 4,947. Every OIDC job stays in the entry, so the signer identity
`nvkr-run.yml@refs/heads/main` is unchanged. The maintained bundle contract checks the private-image
credential boundary on the flat reference that the workers must project exactly. These are local generation
results. No partitioned release Run is recorded; the hosted development Run above is the closest evidence.

Earlier flat-layout measurements recorded 564 original jobs and 16 collectors, down from
596 original jobs and 16 collectors in the private-storage draft based on the same upstream code.
The production dependency graph then had 1,375 edges, down from 1,438. Logical Plan identity and original
per-node result accounting remain unchanged by scheduling. Shared preparation selects the current
`github.job` internally, removing a redundant caller input while retaining exact projection checks.
Those production measurements were 380,112 bytes and 47,451 expanded YAML events; development was
359,349 bytes and 47,008 events. The named ARM64 nightly and upstream-OVN entries were respectively 182,314/183,231 bytes and
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
