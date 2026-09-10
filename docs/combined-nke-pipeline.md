# Combined NKE pipeline implementation

## Requested outcome

One maintained Definition must build the NKE CLI, the CWE container image and the NKE Helm chart bundle.
The generated GitHub workflow must observe, publish, sign and verify the resulting outputs and finalize a
complete Run. A successful CLI-only run does not satisfy this change. Existing unrelated specification edits
remain outside this implementation.

## Verified starting point

At the start of this work, the maintained `definitions/nke-v1` inputs selected only `nkectl`. The pinned NKE commit is
`ed9fb09b04fd40bf331d25df2222206ffaca593e`; the local upstream checkout contains that commit. Inspection uses
`git show` and an exported temporary tree, rather than changing the upstream checkout or treating its current
HEAD as the pinned source.

At that commit, `controller/Dockerfile` builds `controller/cmd/worker` with the vendored Go workspace.
The final image also contains Alpine runtime packages, the digest-pinned SPIFFE JWT authentication helper,
the controller UI, license files and an empty profiles directory. The existing diagnostic single-binary
scratch-image assembler does not implement this runtime image.

The public lifecycle initially rejected `format: oci` during lowering. Helm output initially required an AICR
generator and did not accept a chart from a fetched source tree. These were implementation gaps, not input
typos; removing the rejection alone would not establish correct production, observation or consumption.

The pinned source has both a standalone chart at `controller/deploy/charts/cloud-workflow-engine` and an
AICR quickstart Helm tree at `generated/aicr/quickstart/helm`. The latter refers to external dependency charts
and currently defaults to a different image repository and a development tag. Its image reference must be
bound to the CWE image produced by the combined pipeline. With no scope clarification received, implementation
uses the complete quickstart Helm tree as the default; the standalone CWE chart is not a substitute for it.

## Work and acceptance

1. Exercise the pinned CWE compilation offline using the existing exact Go toolchain image. This establishes
   source/toolchain compatibility only; it is not a complete image build.
2. Add the required typed image and source-chart production paths through the existing lifecycle and owning
   executor modules. Preserve declared network/privilege, credential isolation, exact source/tool pins,
   artifact observation, SBOM binding and failed-Run behavior. Update governing ADR contracts where needed.
3. Extend the maintained combined Definition and Environment, deriving all generated files through the public
   generation command. Retain CLI production and add separately identifiable image and chart outputs.
4. Test the actual image configuration and runtime payload, chart contents and image binding, corrupted or
   missing outputs, generated graph completeness and consumer behavior. Run the full repository gate,
   workflow linters and exact regeneration checks before publishing changes.
5. Execute the generated hosted workflow and record all three observed outputs, published immutable subjects,
   real signatures/attestations and complete Run finalization. Pull and inspect the image and chart bundle;
   controlled tests or unrelated earlier passing workflows cannot substitute for this evidence.

## Current experiment

The initial temporary Docker build attempt failed before compilation because `/src/bin` did not exist inside
the read-only source mount. The retry used a separate `/out` mount, leaving the source tree read-only and
networking disabled, and completed successfully. It produced a 94 MiB static CWE binary from the pinned
vendored workspace with the same exact Go toolchain used by the CLI. This proves compilation only, not the
complete runtime image or its deployment.

The experiment's static Linux/amd64 ELF has SHA-256
`1d3b5106915dc856327a8c474921e5a4d3004cb173a611e1c96f67088c2831e1`.
Its temporary command stamped version `ed9fb09b`; this is experimental build evidence, not a published version.

## Implementation progress

The delivery module now imports an observed OCI-export tar through held exact bytes, rejects unsafe/duplicate
archive members, stages privately, and checks the image before exposing its layout. Image inspection binds the
manifest, config, compressed layers and uncompressed layer identities, checks the requested Linux platform,
rejects ambiguous indexes and unreachable blobs, and preserves the original runnable manifest. Publication
closure inspection recognizes native OCI image manifests alongside existing delivery artifacts.

Focused tests cover image publication identity preservation, platform/byte/diff-ID mismatches, unsafe archive
paths, duplicate entries and immutable destinations. Public OCI outputs now lower to a typed image package
and retain their enclosing publication route. Packaging imports the observed producer archive; lifecycle
observation matches the producer record and independently re-imports it before accepting the image. Consumer
projection checks the exact native image and its platform. Acquired-image layouts remain capability-gated;
this initial producer contract requires a built OCI export archive.

Validation for this increment: the complete `make check` gate passed (64.0% coverage), affected-package tests
and lint passed, and the maintained GitHub bundle still regenerates byte-for-byte. Focused lifecycle tests
exercise lowering, producer substitution rejection and native-image consumer platform binding. No generated
files have been changed and nothing from this increment has been committed or pushed.

The full-image experiment uses official rootless BuildKit v0.33.0, pinned to
`moby/buildkit@sha256:80b15f0735e87bab7bf59ec4d695dfb4a7cfb25521cf56dc75d6f256285b63ef`, with the
[documented rootless container settings](https://github.com/moby/buildkit/blob/v0.33.0/docs/rootless.md).
It invokes the upstream Dockerfile with a read-only source mount and a separate output directory. Its
networked Dockerfile execution is an experiment for the wrapped-build lane, not a hermeticity claim.

The full upstream Dockerfile experiment passed. The OCI export archive has SHA-256
`9ace99c1bb0cf365dbb12e508e15d188cc1a1c083833fa852d3c5e3c7d043193`; the importer verified its single-platform
index `sha256:b3d7a4f873cf886bcff421cf8a1826480dc1193ebdf56b165fd534c1135286d2` and image manifest
`sha256:0d88a6556b283072e856c7cf0bb7acce09cf3b5fd179aa6faf1ceb511bee2516`. Docker loaded that manifest, and
offline smoke checks passed for the CWE executable, SPIFFE helper, UI, profiles directory, licenses, CA
certificates and executable help. No database-backed service or cluster deployment was exercised.

The shared launcher now supports `buildkit-rootless`, with explicit network and rootless-container settings,
private output staging and capability rejection for qualified/direct execution or unsupported runtimes. The
maintained Definition and Environments now include CWE alongside the CLI; generation passed with plan digest
`sha256:7ace7d7b4da9622446e501e6343eb7154eaa5e2fbbfa4e1d40f88da3c632c6a9`. This is an intermediate two-output
pipeline. Chart production and binding, the complete three-output inputs, final gates and hosted validation
remain outstanding. The full gate exposed three tests that assumed CLI-only product inputs; those fixtures
were updated to either verify every declared output or isolate their intended CLI failure, and the focused
rerun passed.

The subsequent full `make check` passed (64.1% coverage). Uncached Linux/arm64 race tests for the pipeline and
signing packages also passed, along with actionlint. These gates validate the intermediate CLI/CWE code and
generated workflow; no hosted dispatch has been made for this change. Next is source quickstart Helm archive
production, image-digest binding and inclusion as the third required output before final hosted acceptance.

Source Helm output selection now accepts one archive component independently of an AICR generator. Packaging
extracts the single `helm/` root with the shared bounded transport adapter and normalizes its carrier bytes.
Observation binds the raw archive to its producer result and independently reproduces the package. Focused
tests passed for selection cardinality, lowering, packaging/observation and substituted producer rejection.
The first packaging test exposed the archive writer's requirement that its output live beneath the transport
root; source packaging now writes the carrier inside its private staging directory.

The maintained Definition now selects `cwe`, `nke-helm` and `nkectl`. The third producer archives the complete
pinned quickstart tree offline with sorted names, fixed timestamps and numeric ownership. Its standalone
container experiment passed after moving its temporary output mount to `/out` (the exported read-only tree
lacks the normal launcher's `bin` mountpoint). The archive has SHA-256
`55164d5a876205ace8decd0b46f0640f69bab6926b16d98dabd02144c41f16fc`; all 48 files match the exported pinned
source, including three local chart metadata files and the external dependency chart references. This does
not mean those external charts were vendored or deployed.

The three-output plan digest is `sha256:2ee9993c88afdaa60ee88a4bcd166ee6e0d4c3680ff31b064a236b2c5fde9641`.
Byte-exact generation checking, actionlint, yamllint, source Helm boundary tests, factory/lifecycle/SDK tests
and affected-package lint passed. Factory HTTP-listener tests required an unsandboxed rerun; that rerun passed.
The quickstart values still need binding to the produced CWE image, including consistency of the bundle's
checksums. No combined hosted dispatch or publication has occurred; final gates and external acceptance remain.

Inspection of the pinned `007-controller/templates/controller.yaml` found that its Deployment hardcodes
`ghcr.io/nvidia/nke-controller:0.0.0-dev`; it does not consult the adjacent `image` values. Updating values alone
would therefore leave deployment on the old image. Binding must also make the rendered Deployment select the
produced immutable image, keep checksum metadata consistent, and prove the result through Helm rendering.

The full `make check` gate passed for this three-output increment (64.1% coverage). This validates source
archive support and generated graph consistency; the hardcoded Deployment reference still prevents claiming
the combined bundle is ready for hosted acceptance. All implementation changes remain uncommitted.

The Definition now declares the Helm output's `imageOutput: cwe` and `chart: 007-controller`. Lowering makes
the image package an explicit dependency. Packaging verifies that native image's closure, embeds its manifest
digest in the unique rendered Deployment, removes the development tag and default repository, and regenerates
checksums. Deployment must provide the verified Release's repository; changing that location does not change
the bundle, and a Helm digest values override cannot replace the embedded digest.

The real-input test initially found that GNU tar's `helm/` directory spelling was rejected by the shared
transport reader. A producer transform experiment did not change GNU tar's spelling. The reader now normalizes
one trailing slash only for directory entries before validation and duplicate detection. Contract negatives
cover directory aliases, traversal and repeated slashes.

`TestPinnedNKEHelmBinding` passed against the real source archive and previously verified upstream CWE image.
All three local charts rendered with Helm; both the configured repository and a mirror rendered the exact
image manifest `sha256:0d88a6556b283072e856c7cf0bb7acce09cf3b5fd179aa6faf1ceb511bee2516`. Rendering without
a repository failed as intended, a digest override could not replace the binding, and all 47 checksum entries
matched the packaged tree. This test is opt-in via `NKD_HELM_ARCHIVE` and `NKD_CWE_IMAGE_LAYOUT`; a normal
skipped invocation is not external evidence. It proves rendering, not Kubernetes deployment or external chart
download availability.

The bound three-output plan is `sha256:b216647f9c78cc6d7dc8fc47bb369b21ee678317482993e5d2be206849f7c0c3`.
The full gate passed (64.2% coverage), Linux pipeline/sign race checks passed, and generated-bundle checking,
actionlint and yamllint passed. A subsequent strict multi-document parsing test covers commented YAML
separators; the refreshed full gate and real-input Helm test also passed before commit. Local rendering used
Helm `v4.2.3+g43e8b7f`. Hosted execution, publication, signing and independent consumption of the combined
outputs remain outstanding.
