# NVKE demo/site module

This nested Go module owns the NVKE demo CLI, VM guests, network guard, TPM
adapter, site/tenant E2E harness, diagnostic Go facade, and site-identity helper.
The operating guide remains [Build and run NVKE locally](../../docs/local-nvke-demo.md).

This is a partial extraction from core NVKR. It still imports core packages through
`replace github.com/dsx-ai-factory/nvkr => ../..`; shared internal helpers remain
in the root module. Its module path stays below NVKR so Go permits those internal
imports. A binary-only consumer boundary is deferred to a later change.

Run from the repository root with the Go version in `.go-version`:

```sh
make demo                 # bin/nvke-demo
make demo-test-bin        # bin/nvke-demo.test; does not run native E2E
make demo-check           # format, vet, build, tests, lint and vulnerabilities
# Focused tests without starting VMs:
go -C demos/nvke test ./internal/nvkedemo -run '^TestDemoQualificationGate$' -count=1
```

The nested Makefile also builds `bin/nvkr-site-identity-helper` at the repository
root. Runtime config, authored Definitions and catalog pins stay at their existing
repository paths. Native E2E remains explicitly opt-in. Catalog builds that select
older pinned NVKR source keep that source revision's original command path.

Core `go test ./...`, `go vet ./...` and `golangci-lint run` do not traverse this
module. `make ci-check` includes its separate checks; the hosted merge gate runs
them for demo/input, root-library or build-control changes and on every main
push/manual run. Root-library PRs still need that separate compatibility check
until the binary-only boundary replaces the temporary imports.
The existing shared third-party notice file covers binaries from both modules.
The demo check also runs the shared source-only context-root scanner with its own
exact exception inventory; this does not compile demo packages into core tests.

The test target downloads dependencies from both module graphs before running
the offline external-consumer and release-dependency boundary checks. For a direct
`go test` run from a fresh module cache, first run `make -C demos/nvke test-deps`.
After that preparation, the boundary test can run with `GOPROXY=off`.

External Go tests import `github.com/dsx-ai-factory/nvkr/demos/nvke/pkg/nvkr/demo`.
Until separately versioned, local consumers must replace both the root and demo
modules with their matching checkout directories; Go does not inherit a dependency's
`replace` directives. No separate repository or module release is introduced here.
