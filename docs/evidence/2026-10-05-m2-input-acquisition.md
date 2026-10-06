# M2 input diagnostic observations, October 5, 2026

This is a dated source/admission diagnostic, not a successful acquisition, native
bake, publication, qualification or M2 acceptance record. The current handoff is
the separate [input map](../m2-input-evidence-map.md); the maintained selection is
in [the candidate Definition](../../definitions/nke-m2/README.md).

## Exact source and local gate

Candidate repository head `9a7e1446bde5c14381b0e72f9ca9a41a29592816` was stacked
on source-fetch head `5fdf8338838be785bcc7e2026de131345f24f906`. On macOS,
`make check` passed format, vet, lint (zero issues), shellcheck, race tests with
71.3% repository coverage, and the Go vulnerability scan (no vulnerabilities).
`make build`, `make workflows-check` and an additional uncached
`go test ./... -count=1` passed. These are local contract gates, not native Linux
runtime or external registry proof. The native helper/CLI cross-compiles passed
for Linux AMD64 and ARM64; cross-compilation does not exercise those runtimes.

The unchanged maintained bundle drift check was `make workflows-check`, whose
Makefile owner builds the generator with `main.version=dev` and
`main.commit=unknown` and then executes:

```sh
bin/nkd-exec run generate --engine github \
  --definition definitions/nke-deploy/definition.yaml \
  --environment definitions/nke-deploy/environment-github.yaml \
  --root . --check
```

It returned:

```text
sha256:7edadcb49c6ac7348c695e112f57df187c4de25efbb213cff26f20d4cb1b78a9
```

### Reproducibility-command follow-up

The earlier snapshot did not retain its cross-compile command lines. The following
commands were therefore rerun separately on the five-review-finding working-tree
follow-up to `bb69f066bc85e81ec42ec1bc1bf0d0ef792110e2`. They do not retroactively
authenticate the earlier cross-compile observation. The helper is the CLI binary
(`cmd/nkdctl`); these flags follow the existing helper build owner without running
its lock-writing or image-assembly steps:

```sh
validation_dir=$(mktemp -d /private/tmp/m2-pr95-crosscompile-XXXXXX)
for arch in amd64 arm64; do
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 GOTOOLCHAIN=local \
    SOURCE_DATE_EPOCH=1 GOFLAGS='' \
    go build -trimpath -buildvcs=false -ldflags=-buildid= \
      -o "$validation_dir/nkd-exec-$arch" ./cmd/nkdctl
  file "$validation_dir/nkd-exec-$arch"
done
make workflows-check
```

Both builds passed with the repository-pinned Go 1.27.1. `file` identified ELF
Linux x86-64 and AArch64 binaries respectively. The drift check again returned
the same digest above for the exact `nke-deploy` inputs, not the `nke-v1` inputs.
Cross-compilation is not native execution, image publication or runtime approval.

## Candidate admission refusal

From that exact built source, this command used a fresh private output directory:

```sh
bin/nkd-exec run generate --engine github \
  --definition definitions/nke-m2/definition.yaml \
  --environment definitions/nke-m2/environment-github.yaml \
  --root /private/tmp/m2-candidate-admission-uuPChd
```

It exited 16, `CodeInvalidCatalog`, wrapping the catalog owner's
`CodeUnverifiedRef` for the authored PostgreSQL index reference while loading
the image catalog. The output directory remained empty. This earliest refusal
blocks all 24 image selections in the combined catalog; it is not a 20-success/
four-refusal execution. Missing native execution classes, approved published
toolchain pins and full inspector profiles remain independent admission gaps.

## Read-only source review snapshot

The following commands were refreshed while preparing the follow-up:

```sh
gh pr view 1211 --repo dsx-ai-factory/nke \
  --json headRefOid,baseRefName,state,mergedAt,reviewDecision,mergeStateStatus
gh api repos/dsx-ai-factory/nke/compare/main...b30117c25b2dcdf6c16021dae88a2eafdafcae61 \
  --jq '{status,ahead_by,behind_by}'
```

They returned source head `b30117c25b2dcdf6c16021dae88a2eafdafcae61`, base
`codex/os-release-baseline`, state `OPEN`, `mergedAt: null`, review decision
`REVIEW_REQUIRED` and merge state `UNKNOWN`. The comparison was `diverged`,
ahead nine and behind twenty. `UNKNOWN` is not a green merge or source-approval
decision. Neither this snapshot nor an exact commit pin supplies protected-ref
approval. Refresh it before any source-acceptance decision.

## Unretained acquisition observations

No complete Helm, kubectl, Bash or native image acquisition diagnostic with exact
commands, retained bytes and terminal read-back is provided by this record.
Historical private chart-default comparisons and image media annotations lack
retained diagnostics; they must not establish current acquisition or coverage.
No native Packer/inspector publication, capture-enabled original live disk join,
complete disk SBOM, signing or consumer verification was exercised here.

## Later generic parent binding scope

The historical candidate-refusal command above retains its exact original source and result.
The generic OCI binding source change supersedes that engineering limitation; it does not
satisfy the remaining native execution-class, inspector, approval or publication gates.

The owning read-only metadata diagnostic is:

```sh
NKD_TEST_M2_IMAGE_PARENT_READBACK=1 go test ./pkg/nkd/catalog \
  -run '^TestMaintainedM2ImageParentReadback$' -count=1 -v
```

The diagnostic authenticates the two source-owned parents against the original four native
lock subjects without writing a lock or downloading layers. Maintenance must use the normal owner:

```sh
go run ./cmd/nkdctl catalog pin-artifacts --dir definitions/nke-m2/image-catalog \
  postgresql-image spire-bash-image
```

Both selected parents must be pinned in the same transaction; the complete lock validator will
not permit leaving either authored index under-bound. The resulting diff must add only
the two authenticated parent fields and retain all 24 existing native digests. A changed
native digest is not authorized by the protocol addition.

### Parent-binding maintenance and static integration observations

On the source follow-up to repository head
`0b18ae1af2ad7383c8a8cdce3a2c3110c764274a`, the owner command above succeeded.
Compared with that head, its lock diff was exactly two added `imageParentDigest`
fields, with no deleted or changed lines. All 24 existing native child digests and
all source pins remained unchanged. Repeating the identical command reported both
components already pinned for two architectures and `no changes`. Before and after
that repetition, the exact lock SHA-256 was
`da53cf2c9f9be3229740838ed23ad325ac3076a674c7c286676bfe05622170ff`.
This is authenticated parent/native metadata maintenance, not layer acquisition,
scanner acceptance or source-owner approval.

Mandatory fixture tests load and Resolve/Compile all 24 native image rows and retain
the independent 20 tag-backed rows. The separate complete-output test authenticates
the actual chart pin-set Git source, so it is explicit opt-in rather than a unit test
that depends on ambient GitHub credentials. This local invocation used the existing
NKE object store, without modifying its checkout or any pins:

```sh
env GIT_CONFIG_COUNT=1 \
  GIT_CONFIG_KEY_0=url./Users/ikislenko/Workspace/github/nke/.insteadOf \
  GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/nke \
  NKD_TEST_M2_ALL_OUTPUT_GRAPHS=1 \
  go test ./pkg/nkd/buildgraph -run '^TestMaintainedM2All34OutputGraphs$' -count=1 -v
```

All 34 rows passed in 3.642 seconds. The Git owner rechecked the selected source's
commit, tree and epoch. An earlier credential-free Linux ARM64 invocation failed
the six chart rows with `CodeFetchFailed` because Git could not read a GitHub username;
that is not recorded as successful 34-row native proof. The output membership is
unchanged, and no catalog-load authentication bypass or synthetic receipt was added.

The same follow-up used a fresh private output directory for this command:

```sh
go run ./cmd/nkdctl run generate --engine github \
  --definition definitions/nke-m2/definition.yaml \
  --environment definitions/nke-m2/environment-github.yaml \
  --root /private/tmp/m2-bound-candidate-admission.aq8tF3
```

It progressed beyond image catalog loading and exited 29, `CodeEngineCapability`:
no execution class satisfied the requested platform, resources, capabilities and
timeout. The directory remained empty. The missing execution classes, approved native
toolchain closures, full inspector profiles and source-review gates remain blockers.

### Native ARM64 controlled-contract validation

The repaired dirty source follow-up above head `0b18ae1` was frozen in a 37-entry
source/test/lock/module manifest with SHA-256
`29f57b3f379b025f8a7e34e25075d73b14e44673cae14dcdb60cfb25b94cd491`.
All entries verified before and after the native run. The existing local diagnostic
image ID `266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`
was inspected as `linux/arm64`; observed execution reported `aarch64`, uid 502/gid
20 and Go 1.27.1. This is a local test image identity, not an approved published
Packer or inspector image pin.

The run used `--pull=never --user=502:20`, read-only source at `/src`, read-only
module cache at `/go/pkg/mod`, image-default HOME, and a container-owned temporary
directory for TMPDIR/GOCACHE. `GIT_CEILING_DIRECTORIES=/src` prevented discovery of
the mounted worktree's inaccessible host administrative pointer. No credentials,
Git safe-directory exception, machine configuration, publication or CI trigger
was supplied. The explicit external full-34 opt-in variable was unset.

```sh
go test -race ./pkg/nkd/catalog ./pkg/nkd/buildgraph ./pkg/nkd/ir \
  ./pkg/nkd/pipeline ./pkg/nkd/factory \
  -run 'Test(NativeImage|ImageBinding|ImagePin|PrebuiltImage|ResolvePrebuiltImageBinding|ImageParent|MaintainedM2|BoundImage|ResolvedImageDependencies|ReleaseExecutable|ArtifactPinEquality|SelectedLayerMaxInt64Bound|BoundedBodyRepeatedReadAfterLimit|PinSetImageRuntimeSelection)' \
  -timeout 5m -count=1
```

All five packages passed: catalog 2.273s, buildgraph 1.187s, IR 1.013s, pipeline
2.261s and factory 1.073s. These include controlled TLS parent/native/config
authentication and original-image materialization, copy/replay/cancellation joins,
24 native image graph rows, legacy tag rows, pin-set version projection and exact
MaxInt64/repeated-exhaustion body regressions. AMD64 platform rows are data cases
executed by ARM64 code, not native AMD64 runtime proof. The explicit full-34 test
was skipped, not passed natively. There is no real upstream layer acquisition,
scanner, Packer bake, signing, publication, qualification or M2 launch proof.

The final requested dead-wrapper cleanup removed the private `resolveImageDigests`
function; its only caller was a test. The runtime guard now exercises actual
`resolvePrebuiltTarget`, requiring `CodeUsage` and no child/parent pins for both
unsupported-runtime rows. The final 38-entry frozen source manifest SHA-256 was
`f3cc012029ce6704e28afd35bdb9ec7b6ac64f4c462c5b3dfcfc21c5cc37ed26`.
An actual Linux ARM64 race follow-up passed both guard rows in 1.014s with the same
diagnostic image/user/default-HOME route, and all 38 hashes matched before and
after. This narrow follow-up is distinct from the prior five-package native run.

After cleanup, `make check` passed format, vet, lint (zero issues), shellcheck,
the full repository race suite (71.0% coverage), and the pinned vulnerability
scan (no vulnerabilities). `make build`, `make workflows-check` and
`git diff --check` passed; the maintained `nke-deploy` generation digest remained
`sha256:7edadcb49c6ac7348c695e112f57df187c4de25efbb213cff26f20d4cb1b78a9`.
These observations describe the frozen dirty follow-up, not a signed source head,
current GitHub CI result or production release acceptance.
