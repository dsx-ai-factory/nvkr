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
