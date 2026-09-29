# Development guide

Use the Go version in [.go-version](.go-version) with `GOTOOLCHAIN=local`, golangci-lint v2 built with a compatible
Go compiler, and ShellCheck. Docker or podman is needed for actual local builds; planning and generation need
neither a registry nor a source checkout. Linux is required for held-executable qualified/signing authority;
macOS supports development, planning and contract tests.

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
make vulncheck
git diff --check
```

Changes to sign/pipeline authority also need Linux tests. Containerized tests require an init process to reap
children; Linux signing fixtures build static helpers while retaining race instrumentation in the parent suite.
Passing these checks proves contracts, not CI, signing-service or registry acceptance.

## Work with product inputs

The [lifecycle guide](docs/lifecycle.md) covers Definition → Run → Release. Begin with `definition validate`,
`run plan`, or `run local --dry-run`. Actual Runs require a new attempt identifier; stored descriptions are not
resume capabilities. The [demo index](scripts/README.md) provides bounded generation, identity and local-build
journeys with their prerequisites.

Regenerate committed CI after changing Definition, Environment, catalog pins or generator code:

```sh
go run ./cmd/nkdctl run generate \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml --engine github --root .
go run ./cmd/nkdctl run generate \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml --engine github --root . --check
```

Use the same generator version and input selection for generation and checking. Generated files are owned by
the generator; edit authored inputs. Selected producer pin changes are build-identity changes; destinations and runtime/signing
configuration belong to Environment. The complete generated Plan is the executable document; there are no separately authored compiler handoffs.

## Repository layout

- `definitions/`: maintained NKE CLI Definitions, Environments, and their catalogs.
- `testdata/examples/`: multi-output, worker, and catalog contract fixtures.
- `pkg/nkd/definition/`: authored inputs, admission and identity.
- `pkg/nkd/factory/`: complete compilation, Run operations and provider generation.
- `pkg/nkd/lifecycle/`: immutable attempts and policy-scoped Release consumption.
- `pkg/nkd/delivery/`: checked publication content and bounded held-resource lifetime.
- `.github/nkd-run/`: generated NKE bundle; regenerate from its Definition and Environment.
- `toolchains/`: development adapter toolchains and helper pins; maintained demos use their declared utility image.

The [cleanup record](docs/repository-cleanup.md) explains current package responsibilities and removed material.

## Find the owner

| Work | Owner |
|---|---|
| Definition/Environment admission and identity | `pkg/nkd/definition` |
| Immutable attempts and Release consumer workflow | `pkg/nkd/lifecycle` |
| Complete compilation, output joins, scheduling, packaging and provider generation | `pkg/nkd/factory` |
| Catalog/pins, resolution and canonical identity | `catalog`, `buildgraph`, `target`, `ir`, `canonical` |
| Producer execution, observations and qualified receipts | `pipeline`, `profile` |
| Bounded archive transport and descriptor-safe paths | `internal/transportarchive`, `internal/securepath` |
| Subject-bound evidence and verification | `attest`, `sbom`, `provenance`, `vuln`, `trust`, `sign` |
| Release authority and delivery | `release`, `status`, `delivery` |
| Public adapters | `client/v1`, `cmd/nkdctl` — delegate authority |

Read the relevant current ADR and [reconciliation row](docs/adr/reconciliation.md) before changing authority.
Use package-issued opaque receipts, admit capabilities before side effects, validate the complete graph at
executable boundaries, and preserve exact subject/policy identities. Low-level diagnostic APIs cannot turn
caller-authored descriptions into authority.

[CONTRIBUTING.md](CONTRIBUTING.md) owns contribution and commit rules. [Documentation guidance](docs/README.md)
identifies active references; historical drafts and dated evidence do not override current contracts.
