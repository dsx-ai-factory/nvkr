# Contributing to nkdctl

Thanks for your interest. nkdctl is an Apache-2.0 implementation of a pinned, reproducible
Definition → Run → Release architecture. This guide covers the dev loop, the
contribution mechanics, and the invariants a change must respect. See **[DEVELOPMENT.md](DEVELOPMENT.md)**
for a deeper environment walkthrough and **[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)**. The
**[ADR reconciliation](docs/adr/reconciliation.md)** is the authority for current support and evidence claims.

## Getting started

```sh
git clone https://github.com/nhensley/nkdctl && cd nkdctl
make help          # list all targets
make build         # compile bin/nkdctl (embeds the default catalog)
make check         # the full local gate: fmt-check + vet + lint + test
```

- [`.go-version`](.go-version) records the supported Go major/minor used by CI. `make` keeps
  `GOTOOLCHAIN=local` to prevent an implicit download; it does not claim an exact local compiler-byte identity.
- **`make test`** runs pure logic with the race detector — no Docker needed.
- Active local **development producer** paths such as `build` (and the build phase of an applicable `attest` or
  `bake-off`) need **Docker + git** and network for their fetch step. The ordinary `build` path is unqualified.
  The OS-image build owns the worker payload. Removed package/rootfs producer commands are not a development path.
- **Lint** uses `golangci-lint` (config in [`.golangci.yml`](.golangci.yml)) and warning-or-higher
  `shellcheck` for executable scripts; both gates are deliberately focused.

## Local development equals CI

CI (`.github/workflows/ci.yml`) runs the same format, vet, lint, and race-test gates as `make check`,
then also compiles the binary. Run `make check` and `make build` before opening a PR.

## The invariants your change must respect

These are load-bearing. The proposed decisions in [`docs/adr/`](docs/adr/) are the maintained engineering
baseline pending ratification, and [`docs/adr/reconciliation.md`](docs/adr/reconciliation.md) records the
current implementation boundary:

- **Fail closed.** An unpinned source ref, a tag-pinned toolchain, an unsupported criteria value, or a
  reserved axis must error with a coded, distinct exit status — never a silent default.
- **Deterministic IR.** `recipeDigest` is the domain-separated digest of the canonical IR. No host paths,
  clocks, UUIDs, random values, invalid UTF-8 normalization, or Go map-iteration order may feed a semantic
  identity, manifest, or signature.
- **Keep lanes explicit.** From-source builds run in pinned-by-digest toolchain containers with
  `--network=none`. Byte-preserving prebuilt acquisition is a separate, content-addressed lane and
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

Adding a component is normally a **declarative** change in the selected Definition’s catalog under `definitions/` and, where applicable,
its Recipe overlay or mixin, with every source, toolchain, vendor closure, or acquired artifact pinned by exact
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
3. **Sign off every commit** with the [Developer Certificate of Origin](https://developercertificate.org):
   ```sh
   git commit -S -s
   ```
   The `-s` adds a `Signed-off-by: Your Name <you@example.com>` line certifying you have the right to
   contribute the change under Apache-2.0. Configure `git config user.name`/`user.email` first.
4. **Update authority and docs** when behavior changes: update the owning primary ADR directly when its decision
   changes; add an ADR only for a distinct decision, and update `docs/adr/reconciliation.md` when implementation maturity, evidence, or a disabled path
   changes. The SRD, checklist, POC narrative, and design plan are historical snapshots; preserve their
   traceability without promoting them to current release authority. Don't hand-edit generated HTML or DOCX
   exports; regenerate them from their source when an export is intentionally refreshed.
5. Open the PR with a clear description of *what* and *why*; link any issue.

## Reporting bugs / proposing changes

Open a GitHub issue with repro steps and the coded error/exit status where relevant. For security
issues, follow **[SECURITY.md](SECURITY.md)** instead of filing a public issue.
