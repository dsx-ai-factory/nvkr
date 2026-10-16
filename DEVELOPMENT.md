# Development guide

Use the Go version in [.go-version](.go-version) with `GOTOOLCHAIN=local`, golangci-lint v2 built with a compatible
Go compiler, and ShellCheck. Docker or podman is needed for actual local builds; planning and generation need
neither a registry nor a source checkout. Linux is required for held-executable qualified/signing authority;
macOS supports development, planning and contract tests.

The ordinary test gate (`go test ./...`, `make test`, and `make check`) also requires Bash 3.2 or later,
Git 2.38 or later, `jq`, GNU coreutils `timeout` or `gtimeout`, and a SHA-256 utility (`sha256sum` or `shasum`) on `PATH`.
The catalog maintenance tests also run the rendered workflow steps with `yq` v4. Those tests skip locally when
one of these tools is missing; with `NVKR_REQUIRE_SHELL_TOOLS=1` or `CI` set they fail instead, and the merge gate
sets the variable.
Ordinary local source admission uses Git's bounded `ls-files --format` query to reject indexed gitlinks;
an unsupported query fails with a coded diagnostic naming this Git version floor.
The required [authoring lab regression](docs/examples/authoring-lab/README.md) uses these tools and fails
if a prerequisite is missing. `make check` additionally requires Python 3.12 or later (`python3` on `PATH`)
for `make test-ci-tools`, which runs the CI report parser and all local adapter contract suites using
only the Python standard library. Go-test adapter wrappers report an explicit skip when Python is
absent; that is not full-gate coverage. The tooling gate fails if Python is missing or too old and
runs every suite independently, so a Go-only test pass cannot stand in for the completion gate.
On macOS with Homebrew, install the additional JSON, timeout, and Python tools with:

```sh
brew install jq yq coreutils python
```

The lab discovers `gtimeout` directly on macOS; no `timeout` alias or unprefixed coreutils setup is needed.

```sh
make build
make check
```

`make help` lists targets. The full release-blocking verification includes:

```sh
gofmt -l cmd client internal pkg
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
golangci-lint run
make shellcheck
make test-ci-tools
make vulncheck
git diff --check
```

The operator-facing site published with Fern is a separate gate. Pages are those listed in
[docs/index.yml](docs/index.yml); other documents stay on GitHub. After changing a published page, its navigation
or `fern/`, run `make docs-check`, which needs Node and the Fern CLI (`npm install -g fern-api@latest`; Fern is not
pinned). CI runs the same target as the `docs` job of the merge gate. The Fern checks run on an isolated copy of
only the listed pages, so a published page must not link to an unpublished page or to a file outside `docs/`; use a
GitHub URL for those. Registered release versions are ignored by this check (it needs `yq` once any exist).

Changes to sign/pipeline authority also need Linux tests. Containerized tests require an init process to reap
children; Linux signing fixtures build static helpers while retaining race instrumentation in the parent suite.
Passing these checks proves contracts, not CI, signing-service or registry acceptance.

## Work with product inputs

The [lifecycle guide](docs/lifecycle.md) covers Definition → Run → Release. Begin with `definition validate`,
`run plan`, or `run local --dry-run`. Actual Runs require a new attempt identifier; stored descriptions are not
resume capabilities. The [demo index](demos/README.md) provides bounded generation, identity and local-build
journeys with their prerequisites.

The bundle records its selected Definition and Environment in `.github/nvkr-run/plan.json`.
The maintained release selection is the 69-output [M2 candidate](inputs/definitions/nvke/README.md), including
plain and public-bootstrap FDE disks for both native architectures. The separate
[FDE diagnostic workflow](inputs/definitions/diagnostics/fde.md) selects one build-only disk for branch iteration.
Regenerate with the intended selection after changing its inputs or generator code:

```sh
go run ./cmd/nvkr run generate \
  --definition inputs/definitions/nvke/definition.yaml \
  --environment inputs/environments/github-release.yaml --engine github --root .
go run ./cmd/nvkr run generate \
  --definition inputs/definitions/nvke/definition.yaml \
  --environment inputs/environments/github-release.yaml --engine github --root . --check
```

Use the same generator version and input selection for generation and checking. Generated files are owned by
the generator; edit authored inputs. Selected producer pin changes are build-identity changes; destinations and runtime/signing
configuration belong to Environment. The complete generated Plan is the executable document; there are no separately authored compiler handoffs.

`make workflows-check` builds the generator with the committed `dev`/`unknown` stamp and repeats the
Definition/Environment paths recorded in the committed Plan. A `make build` CLI has a Git-derived version and can legitimately report different generated bytes.
Use [scratch generation](docs/generate-pipeline.md) for v1, deploy, or other examples; generating them with
`--root .` would replace the M2 workflow.

## Test pipeline changes on a feature branch

The committed `nvkr-dev.yml` runs the **same 69-output M2 Definition** as the maintained production
workflow, including native AMD64/ARM64 plain and public-bootstrap FDE disks, SBOM attestation, per-output publication, cosign signing,
verification and finalization. Its generator and helper come from the dispatched branch. The separate
`inputs/environments/github-development.yaml` selects development credentials, protected environments,
signer policy and disposable registry destinations. Its Plan and manifest live beneath `.github/nvkr-dev/`.
The production bundle remains independently owned and checked.

```sh
# After editing generator code or authored inputs, regenerate and inspect both bundles.
make workflows-generate
make workflows-check
git diff --stat

# Commit and push the reviewed changes to your feature branch, then dispatch it.
make pipeline-dev-run PIPELINE_DEV_REPO=dsx-ai-factory/nvkr
```

The dispatch helper requires a clean named branch matching the selected repository's remote commit and
an active registered workflow. It never commits, pushes or changes provider settings. For a preflight without
dispatch, use `python3 scripts/pipeline-dev.py --repo dsx-ai-factory/nvkr --check` after
`make pipeline-dev-check`. `make pipeline-dev-generate` regenerates just the development bundle.

### One-time operator setup

Merge the development and cleanup workflows onto the default branch before dispatching them. GitHub's
[manual-dispatch registration](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)
requires that default-branch presence. Keep the generated development bundle on each tested branch.

Configure these **separate development environments**, retaining the existing production environments:

| Environment | Configuration |
|---|---|
| `nvkr-dev-release` | Admit reviewed feature branches. Store `NVKR_DEV_REGISTRY_WRITE` and `NVKR_DEV_REGISTRY_READ` secrets, plus `NVKR_DEV_REGISTRY_WRITE_USER` and `NVKR_DEV_REGISTRY_READ_USER` variables. |
| `nvkr-dev-arc` | Admit the reviewed development workflow and selected branch refs to the existing ephemeral `arc-amd64-kvm` and `arc-arm64-kvm` pools. Preserve the existing capacity, Docker/KVM and isolation requirements. |
| `nvkr-dev-cleanup` | Admit **only the default branch**. Store `NVKR_DEV_REGISTRY_CLEANUP`; allow its daily scheduled job to run without a per-run approval if automatic cleanup is desired. |

Use a dedicated development publishing account with no production-package write/admin access. Its write
credential needs `write:packages`; its read credential is a separate token restricted to `read:packages`.
The cleanup credential needs package admin access with `read:packages` and `delete:packages`, plus read
access to this repository's Actions metadata. Using the same dedicated account with separate token scopes
lets it access newly created private packages without assigning another account to every attempt.
Check the actual account/package ACLs: names and a registry prefix do not restrict a token's authority.
See [GitHub package permissions](https://docs.github.com/en/packages/learn-github-packages/about-permissions-for-github-packages)
and [package deletion requirements](https://docs.github.com/en/packages/learn-github-packages/deleting-and-restoring-a-package).
Do not use a production administrator's token for these secrets. The generated development jobs grant
no package-write permission to their repository `GITHUB_TOKEN`; verification, assembly and finalization
receive only the dedicated read credential. Only artifact signing and SBOM attestation receive OIDC.

Keep `NKE_SOURCE_TOKEN` available to the fixed source-fetch/catalog-validation steps. Grant the workflow
repository read access to the retained private native toolchain packages. Their temporary read credential
is removed before producer execution. Branch code participates in these operations, so run reviewed branches;
the workflow is not an untrusted-code sandbox. The generator does not provision or attest provider access policy.

The development signing policy accepts this repository's `nvkr-dev.yml` branch identities only, using the
GitHub OIDC issuer. Production consumer policies pinned to `nvkr-run.yml` do not accept those identities.
A real signed development result is `pipeline-attested` under that development policy, with the same explicit
producer-only disk-evidence scope as M2; it is neither production acceptance nor qualified reproduction.
Keyless signatures use the existing transparency services, whose records are not erased by registry cleanup.

### Inspect results and repeat

The helper prints a commit-filtered command for finding the Run. Inspect its head SHA, completion and final
record before using any outputs:

```sh
gh run watch RUN_ID --repo dsx-ai-factory/nvkr --exit-status
gh run view RUN_ID --repo dsx-ai-factory/nvkr --log-failed
gh run download RUN_ID --repo dsx-ai-factory/nvkr \
  --name nvkr-lifecycle-run --dir /tmp/nvkr-dev-record-RUN_ID
```

Each attempt publishes into
`ghcr.io/dsx-ai-factory/nvkr/development/runs/RUN_ID/attempts/ATTEMPT/`.
The aggregate root is `releases@sha256:…`; named outputs and output-local roots are siblings.
Use the digest-pinned references recorded by the Run with the [release navigation tools](docs/release-navigation.md).
Different branches and attempts cannot advance one another's Helm aliases or share disposable package storage.
The Plan records the namespace policy; the execution owner binds actual destinations to the current
repository, workflow, run and attempt without changing logical Build identity or rewriting the committed Plan.

Signed development Runs are non-canceling. To retry, dispatch a complete new Run (or rerun **all** jobs).
Failed-job-only reruns cannot reuse publications from an earlier attempt's disposable namespace. A cancelled
Run can leave partial publications and may have no final record; cleanup discovers packages independently
of successful finalization. CI helpers/carriers and final Run records retain seven days; disk diagnostics
retain one day. After CI transfers expire, start a complete new Run.

### Disposable registry cleanup

The default-branch `nvkr-dev-cleanup.yml` runs daily and supports a manual preview (`apply: false`, the
default). It uses its own compiled cleanup owner and credential; it never executes a feature branch's code
or downloaded artifacts. Preview from the default branch before enabling deletion:

```sh
gh workflow run nvkr-dev-cleanup.yml --repo dsx-ai-factory/nvkr --ref main -f apply=false
```

Cleanup requires private packages in the exact reserved namespace, matching GitHub account and workflow attempt, a completed
attempt at least 24 hours old, and package/version timestamps at least 24 hours old. It rechecks candidates
before deletion. Active, young or ambiguous state is retained; API/admission failures fail the job rather
than claim cleanup succeeded. Whole-package deletion includes that package's versions, signatures and
referrers, avoiding guesses based on tags. The next daily sweep removes eligible packages; this is not an
exact 24-hour expiry or proof of physical storage reclamation. Existing production and older unscoped
publications are outside the cleanup namespace. GitHub retains its own restoration policy.

The cleanup report records candidates and API-confirmed deletions, including partial progress before a
failure. Its CI artifact retains seven days. A timed-out deletion has an unknown outcome and is not retried
within the command. Inspect the next preview before retrying.

The three-output unsigned Environment in `docs/examples/nvke-tutorial/environment-github-dev.yaml` remains a
smaller scratch-generation example. It uses one-day transfers, automatic cancellation and no publication.
It does not supply the committed full-development bundle. Hosted execution, credential/pool setup and
actual registry deletion still need a recorded external Run; local contract tests establish none of those.

## Generated and handwritten GitHub files

The generator owns `.github/workflows/nvkr-run.yml` and every manifest member beneath `.github/nvkr-run/`,
plus the development workflow and its separate `.github/nvkr-dev/` members.
Qualification generation separately owns the production `nvkr-qualify.yml`/`nvkr-smoke.yml` pair
through `.github/nvkr-qualification/manifest.json`; development emits no qualification handoff or workflow pair.
Its checker rejects unexpected managed-directory entries, including symlinks. Human-maintained repository
workflows, Dependabot configuration, and issue templates have separate ownership. Action pins for generated
CI come from its selected catalog and lock, rather than edits to rendered YAML.

Pull requests check both producer bundles and the qualification pair. The maintained publication route admits default-branch manual dispatch;
only its isolated artifact-signing and SBOM-attestation jobs receive OIDC minting permission. Generic hosted
runners are the default, with ADR-18's explicitly admitted native ARC disk routes. Dated validation-branch
exceptions and completed Runs are scoped in [reconciliation](docs/adr/reconciliation.md).

## Test timing and cache use

Use `make test GO_TEST_FLAGS="-json -count=1"` to capture uncached ordinary test events. CI retains the
event stream for seven days and summarizes slow packages/tests, including failures. Its check
cache is scoped by platform, exact Go version, dependencies, and commit; a compatible prior check cache is
restored and an updated cache saved only after successful checks. Other jobs cannot populate that key.

[CI test performance](docs/ci-test-performance.md) owns the measured Merge Gate follow-up and focused
profiling commands.

## Opt-in terminal presentation checks

The ordinary presenter suite uses simulated screens. Real terminal fixtures must
run in an isolated interactive TTY with usable `TERM`, never against the user's
active shell input. Use the pinned Go version:

```sh
NVKR_TEST_RUNPRESENTER_TTY=1 go test ./internal/runpresenter \
  -run '^TestPresenterRealTTYRestoresFlags$' -count=1 -v
NVKR_TEST_RUNPRESENTER_HUP=1 go test ./internal/runpresenter \
  -run '^TestPresenterRealTTYHangupJoinsOwner$' -count=1 -v
go test -race ./internal/runpresenter ./internal/rundiagnostics ./pkg/nvkr/progress \
  -count=1
```

The HUP fixture sends SIGHUP and verifies restoration and joined work; it does not
establish physical PTY-master disconnection behavior. These controlled fixtures
do not execute NVKE producers, qualify outputs, or prove human usability. Run the
same opt-in fixture on native Linux for Linux terminal evidence; cross-compilation
alone establishes build compatibility. A skipped fixture supplies no TTY evidence.

## Opt-in integration checks

The ordinary suite skips integrations requiring external tools, private source, or exceptional host capacity.
A skipped test provides no interoperability evidence. With an installed Helm executable, run the
[mapping fixture](pkg/nvkr/factory/helm_mapping_test.go):

```sh
NVKR_TEST_HELM_BINDINGS=1 go test ./pkg/nvkr/factory \
  -run '^TestMappedBindingsWithHelm$' -count=1 -v
```

For the source-chart case, set `NVKR_NVKE_CHART` to the controller chart from the exact upstream revision
you intend to exercise, and run `TestNVKEDeclarativeBindingWithHelm`. Both tests render local chart bytes;
they do not build images, publish, install or verify upstream signatures.

The Linux [public disk-issuer fixture](pkg/nvkr/factory/disk_evidence_runtime_linux_test.go) uses a controlled
native ELF daemon protocol and synthetic capture/disk bytes. On a native host with sufficient observed
composition capacity, explicitly request its issuer and memory-profile checks:

```sh
NVKR_TEST_DISK_EVIDENCE_RUNTIME=1 go test -race ./pkg/nvkr/factory \
  -run '^TestDiskEvidencePublicRuntime(Issuer|RejectsMemoryProfile)$' -count=1 -v
```

An opted-in run fails on inadequate capacity rather than skipping its positive case. It checks public
owner/receipt/export boundaries, without executing Docker, Packer, guestfish, Syft or a real NVKE image.
Actual native disk build/inspection evidence remains separately scoped in the evidence index.

The independent Debian inspector API regression requires native Linux amd64 or arm64. Use the Syft
v1.51.1 archive pins and executable measurement procedure in
[`toolchains/nvkr-disk-inspect`](toolchains/nvkr-disk-inspect/README.md) as the source for the selected tools.
Export `NVKR_SBOM_SCANNER` with the selected Syft executable path and `NVKR_SBOM_SCANNER_DIGEST` with
its measured `sha256:` executable digest. `NVKR_SBOM_PACKAGE_READER_DIGEST` must match the actual
`/usr/bin/dpkg-deb` executable in the test runtime, not a compressed archive hash. The diagnostic toolchain
source is not an approved production runtime. Opting in with missing or mismatched pins fails rather than skipping:

```sh
NVKR_SBOM_DEBIAN_INTEGRATION=1 \
go test ./pkg/nvkr/sbom -run '^TestRealDebianArtifactDependencies$' -count=1 -v
```

It builds controlled gzip/xz fixture packages and inspects them through the separate
`InspectDebianArchiveMetadata` diagnostic. That API always uses the fixed private snapshot name
`subject.deb`, independent of the caller's filename. It checks exact package metadata, pre-scan digest binding,
original-fixture size stability, and rejection of non-archive bytes. The same fixture bytes supplied to ordinary
`InspectDependencies` receive no filename hint and must return `CodeProvenanceMissing`, with no accepted
inventory, under the selected Syft version. It does not test corrupted payload members. The diagnostic method
is rejected by dependency SBOM attachment and serialized BOM validation. Factory callers retain the ordinary
dependency API; no maintained factory output invokes this separate diagnostic. This is not delivered-package
inspection, payload dependency coverage, installation, signing or publication.
See [the dependency SBOM reference](docs/dependency-sboms.md) and the
[dated local fixture observation](docs/adr/reconciliation.md#external-run-references). Native amd64 execution
has not been recorded. Rerun the opt-in regression when changing either the factory catalog Syft pin or the
Syft pin in `toolchains/nvkr-disk-inspect`; record which selected native executable was exercised.

## Repository layout

- `inputs/`: distribution, demo and diagnostic Definitions, shared catalogs, Environments, policies and nightly configuration; start with the [input index](inputs/README.md).
- `docs/examples/`: retained tutorial and deploy inputs, alongside the authoring lab.
- `testdata/examples/`: multi-output, worker, and catalog contract fixtures.
- `pkg/nvkr/definition/`: authored inputs, admission and identity.
- `pkg/nvkr/factory/`: complete compilation, Run operations and provider generation.
- `pkg/nvkr/lifecycle/`: immutable attempts and policy-scoped Release consumption.
- `pkg/nvkr/delivery/`: checked publication content and bounded held-resource lifetime.
- `.github/nvkr-run/`: generated NVKE bundle; regenerate from its Definition and Environment.
- `toolchains/`: development adapter toolchains and helper pins; maintained demos use their declared utility image.

Package comments and the ownership map below identify starting operations. CLI and SDK files group operations
by family; factory execution uses `execute_` files, and `outputs_observed.go` owns the independent output join.
Catalog resolution, pin maintenance, and producer admission have distinct files within their owning package.

## Find the owner

| Work | Owner |
|---|---|
| Definition/Environment admission and identity | `pkg/nvkr/definition` |
| Immutable attempts and Release consumer workflow | `pkg/nvkr/lifecycle` |
| Complete compilation, output joins, scheduling, packaging and provider generation | `pkg/nvkr/factory` |
| Catalog/pins, resolution and canonical identity | `catalog`, `buildgraph`, `target`, `ir`, `canonical` |
| Producer execution, observations and qualified receipts | `pipeline`, `profile` |
| External test output descriptions, wrapper validation and deterministic serialization (no execution or authority) | `pkg/nvkr/testresults` |
| Configured diagnostic check selection and local upstream conformance execution | `validation`, `conformance` — delegate product/tool publication authentication and exact executable-member export to `lifecycle` |
| Bounded archive transport and descriptor-safe paths | `internal/transportarchive`, `internal/securepath` |
| Subject-bound evidence and verification | `attest`, `sbom`, `provenance`, `trust`, `sign` |
| Release authority and delivery | `release`, `status`, `delivery` |
| Public adapters | `client/v1`, `cmd/nvkr` — delegate authority |

Read the relevant current ADR and [reconciliation row](docs/adr/reconciliation.md) before changing authority.
Use package-issued opaque receipts, admit capabilities before side effects, validate the complete graph at
executable boundaries, and preserve exact subject/policy identities. Low-level diagnostic APIs cannot turn
caller-authored descriptions into authority.

[CONTRIBUTING.md](CONTRIBUTING.md) owns contribution and commit rules. [Documentation guidance](docs/README.md)
identifies active references; historical drafts and dated evidence do not override current contracts.
