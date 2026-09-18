# NKE Recipes

NKE Recipes uses `nkdctl` to turn a product **Definition** into a **Run** and an immutable **Release** of artifacts and evidence.
Catalog components supply reusable producer instructions; an Environment supplies execution/publication settings; consumers
apply a Verification Policy. Downstream systems own deployment topology and node placement. Authors declare outputs once; NKD derives packages, routes and internal evidence relationships.
Each output directly selects catalog components and a concrete target, or an AICR profile and generator.
The resulting build graph is generated and has no separate authoring lifecycle.

## Start here

```sh
make build
bin/nkdctl definition validate --definition definitions/nke-v1/definition.yaml
bin/nkdctl run plan --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-local.yaml
```

Planning reads inputs without building or publishing. Follow the [lifecycle guide](docs/lifecycle.md) for local
attempts, hosted generation and policy-scoped Release verification, or choose a [runnable demo](scripts/README.md).
The [NKE inputs](definitions/nke-v1/README.md) build the CLI, CWE image, and Helm bundle; the
[AICR inputs](definitions/aicr/README.md) produce a pinned Helm OCI output. The NKE OS-image build owns
the entire worker payload; its NKD integration remains deferred.

## Current boundary

Definition/Environment compilation, immutable local attempts, GitHub generation, and
binary/APT/Helm Release-consumer contracts are implemented. Linux verification requires pinned tools and explicit
identity/issuer policy. The supported consumer scope authenticates artifact publication and build evidence.

The project is not a production-complete release system. Earlier personal-namespace hosted runs demonstrated
real keyless signing and independent consumption. The first NVIDIA Run
also passed independent signature/provenance verification, but omitted SBOMs from its signed closure. The new
[single-entry release layout](docs/publication-architecture.md) requires a fresh hosted run; organizational acceptance remains open. Full v1 release qualification/automation, wrapped OS builds and several
qualified authority adapters remain unavailable. Dry-run/local output is unqualified; a production signature
does not prove exact-byte reproduction. See the [implementation and evidence record](docs/adr/reconciliation.md).

## Contribute

Go is selected by [.go-version](.go-version). `make check` runs the local gate; see
[DEVELOPMENT.md](DEVELOPMENT.md) for setup and [CONTRIBUTING.md](CONTRIBUTING.md) for contribution mechanics.
No external-service result is inferred from unit tests.

The [documentation map](docs/README.md) routes architecture, vocabulary, milestones and recorded evidence.
The [SADD](docs/sadd.md) explains the architecture; current [ADRs](docs/adr/) own engineering contracts pending
named-decider ratification. [SECURITY.md](SECURITY.md) and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) cover reporting
and community expectations.
