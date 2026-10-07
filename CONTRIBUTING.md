# Contributing to NKE Recipes

Thanks for your interest. NKE Recipes is an Apache-2.0 implementation of a pinned, verifiable
Definition → Run → Release architecture. Exact-byte reproduction is a separate qualified claim for an exact
subject; it is not inherited by every pipeline or aggregate artifact. This guide covers the dev loop, the
contribution mechanics, and the invariants a change must respect. See **[DEVELOPMENT.md](DEVELOPMENT.md)**
for a deeper environment walkthrough and **[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)**. The
**[ADR reconciliation](docs/adr/reconciliation.md)** is the authority for current support and evidence claims.

## Getting started

```sh
git clone https://github.com/NVIDIA/nke-recipes && cd nke-recipes
make help          # list all targets
make build         # compile bin/nkdctl; catalogs are supplied explicitly
make check         # the full local gate, including the Go vulnerability check
```

- [`.go-version`](.go-version) pins the exact Go patch used by CI. The local
  build and vulnerability gates require the same patch, while `GOTOOLCHAIN=local`
  prevents an implicit toolchain download.
- **`make test`** runs the fast default suite without race/coverage instrumentation.
  **`make test-race`** adds race detection and coverage; **`make test-full`** also
  enables expensive capacity and full-product integration fixtures. Direct
  `go test ./...` uses the fast default too.
- Active local **development producer** paths such as `build` (and the build phase of an applicable `attest` or
  `bake-off`) need **Docker + git** and network for their fetch step. The ordinary `build` path is unqualified.
  The OS-image build owns the worker payload. Removed package/rootfs producer commands are not a development path.
- **Lint** uses `golangci-lint` (config in [`.golangci.yml`](.golangci.yml)) and warning-or-higher
  `shellcheck` for executable scripts; both gates are deliberately focused.

## Local development equals CI

CI (`.github/workflows/merge-gate.yaml`) runs the same format, vet, lint,
shellcheck, fast tests, and Go vulnerability checks as `make check`, then also
compiles the binary. Run `make check` and `make build` before opening a PR. The
vulnerability check needs access to the Go vulnerability database; see the
[maintainer procedure](docs/cve-management.md) for triage and scope.

Expensive capacity and full-product integration fixtures require
`NKD_TEST_EXTENDED=1`; `make test-full` sets it for the race-and-coverage run.
Enable them selectively when changing large-document limits, attestation or
publication readback, catalog serialization limits, maintained workflow
generation, or the authoring lab. Go's `-short` flag keeps them skipped even when
the environment variable is set. Ordinary contract tests and fast byte-admission
checks still run by default. See [the timing investigation](docs/go-test-performance.md)
for the exact opt-in cases, measurements, and remaining bottlenecks.

For concurrency or process-lifetime changes, run targeted race tests for the
affected packages. Whole-repository race and coverage runs are opt-in rather
than a cost paid on every PR or agent turn.

## The invariants your change must respect

These are load-bearing. The proposed decisions in [`docs/adr/`](docs/adr/) are the maintained engineering
baseline pending ratification, and [`docs/adr/reconciliation.md`](docs/adr/reconciliation.md) records the
current implementation boundary:

- **Fail closed.** An unpinned source ref, a tag-pinned toolchain, an unknown component or unsupported target platform must error with a coded, distinct exit status — never a silent default.
- **Deterministic IR.** `buildDigest` identifies the validated graph's logical projection under `nkd.build`.
  Transport and execution metadata remain in the full graph and Plan. No host paths,
  clocks, UUIDs, random values, invalid UTF-8 normalization, or Go map-iteration order may feed a semantic
  identity, manifest, or signature.
- **Keep lanes explicit.** Hermetic source builds run in pinned-by-digest toolchain containers with
  `--network=none`; admitted wrapped builds record their declared network and privilege. Byte-preserving
  prebuilt acquisition is a separate, content-addressed lane and
  in the qualified lane must carry typed external SBOM/provenance evidence or an exact authenticated waiver; v1 acquisition records its pin without authenticating upstream signatures. Acquisition must never
  be relabeled as a source build.
- **One IR → many adapters.** A new CI engine is one adapter; a new component is one catalog YAML entry,
  no code. Adapters map topology/transport only, never build semantics. An adapter must transport every
  declared input, output, and receipt and enforce the declared isolation; if it cannot, emission for that lane
  fails with a capability error instead of producing a partial pipeline.
- **Apache-clean.** No copyleft/BUSL dependency **linked** into the binary (subprocess invocation of
  GPL tools like `go`/`gcc` is fine). Keep linked deps permissive and minimal — reuse patterns from
  other projects (e.g. AICR) by reading/copying, not importing.

## Contributing a catalog / component

Adding a component is a **declarative** change in the selected Definition’s catalog under `definitions/`.
Select it directly in an output with a concrete platform, with every source, toolchain, vendor closure, or acquired artifact pinned by exact
content identity. A qualified prebuilt entry must also declare digest-pinned typed evidence or an exact authenticated
waiver; v1 acquisition uses the separate production-evidence contract. Run `make build` and validate the changed Definition with `bin/nkdctl definition validate --definition <path>`. A source bake-off is useful contract evidence, but
release qualification also requires the authenticated producer, terminal, evidence, policy, and status chain
described in the ADR reconciliation. Operators may ship an entirely separate catalog via `--data` (see
`testdata/examples/operator-catalog/`).

Catalog diagnostics require an explicit `--data` path. There is no embedded worker catalog or backward-compatibility
requirement. Worker binaries belong to the NKE OS-image build; add product outputs through a Definition.

## Pull request process

1. **Branch** off `main`; keep the change focused.
2. **`make check` is green** and new behavior has a test (pure-logic where possible).
3. **Use Conventional Commits** as described below, cryptographically sign every commit, and sign off with the [Developer Certificate of Origin](https://developercertificate.org):
   ```sh
   git commit -S -s -m "fix(pipeline): resolve action pins from the catalog"
   ```
   The `-s` adds a `Signed-off-by: Your Name <you@example.com>` line certifying you have the right to
   contribute the change under Apache-2.0. The `-S` cryptographically signs the commit. See
   [Signing your commits](#signing-your-commits) for one-time setup — both are required.
4. **Update authority and docs** when behavior changes: update the owning primary ADR directly when its decision
   changes; add an ADR only for a distinct decision, and update `docs/adr/reconciliation.md` when implementation maturity, evidence, or a disabled path
   changes. Current product requirements belong in the milestone specification; engineering contracts belong
   in the owning ADR. Superseded plans and requirement exports are available in Git history.
5. Open the PR with a clear description of *what* and *why*; link any issue.

## Signing your commits

Every commit needs two independent things: a **DCO sign-off** (`-s`, a text trailer asserting you have the
right to contribute the code) and a **cryptographic signature** (`-S`, proving the commit came from you).
They are not substitutes for each other.

### One-time setup

Your signing identity must match the email you commit with, and that email must be verified on your GitHub
account, or GitHub will show the commit as `Unverified`.

```sh
git config --global user.name  "Your Name"
git config --global user.email "you@example.com"
```

**With GPG:**

```sh
gpg --full-generate-key                      # use the email configured above
gpg --list-secret-keys --keyid-format=long   # copy the key ID after `sec   rsa4096/`
git config --global user.signingkey <KEY_ID>
git config --global commit.gpgsign true      # sign every commit without passing -S
gpg --armor --export <KEY_ID>                # add at GitHub -> Settings -> SSH and GPG keys
```

**With SSH** (simpler if you already push over SSH):

```sh
git config --global gpg.format ssh
git config --global user.signingkey ~/.ssh/id_ed25519.pub
git config --global commit.gpgsign true
```

Add the same public key to GitHub a second time, as a **Signing Key** — an Authentication Key alone does not
verify commits.

Confirm it works with `git log --show-signature -1` locally and a **Verified** badge on GitHub.

### If you forgot

```sh
git commit --amend -S -s --no-edit                               # most recent commit
git rebase --exec 'git commit --amend -S -s --no-edit' -i main   # a whole branch
git push --force-with-lease
```

## Commit messages

Use Conventional Commits for every new commit and for PR titles that become squash commit subjects:

```text
type(optional-scope): imperative description
```

Use `feat` for new behavior, `fix` for corrections, `refactor` for structural changes without a behavior
change, `docs` for documentation, `test` for tests, `perf` for performance, `build` for build tooling or
dependencies, `ci` for CI configuration, and `chore` for other maintenance. Choose a short scope when useful,
such as `catalog`, `pipeline`, or `lifecycle`. Describe the resulting change; explain its reason in the body
when the subject is insufficient.

Mark breaking changes with `!` before the colon and describe the migration in a `BREAKING CHANGE:` footer.
Keep the DCO `Signed-off-by:` trailer. Examples:

```text
feat(catalog): support pinned GitHub action dependencies
fix(lifecycle): reject incomplete publication evidence
docs(demos): clarify local execution and M1 acceptance
refactor(environment)!: derive publication routes from one prefix
```

## Reporting bugs / proposing changes

Open a GitHub issue with repro steps and the coded error/exit status where relevant. For security
issues, follow **[SECURITY.md](SECURITY.md)** instead of filing a public issue.
See the [GitHub issue and PR management procedure](docs/github-issue-pr-management.md)
for triage, priority ownership, and Discussions routing.
