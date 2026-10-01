# Declarative Helm binding validation

On 2026-10-01, the working implementation on `feat/declarative-helm-bindings`, rebased onto upstream/main
`53ba88b` (the merged #67), passed the checks below. Scope: contract tests, real chart
acquisition, static compilation and Helm rendering. No new CWE build, publication, installation or
qualification was performed.

## Real NKE inputs

- NKE controller tag: `controller/v0.1.1-rc.9`.
- Commit: `340009f1e15d8e26940f1e03c9696f1ad251c0ae`.
- Chart: `controller/deploy/charts/controller`, extracted with `git archive`; the NKE checkout was unchanged.
- Host: macOS ARM64; Go 1.27.1; Helm `v4.2.3+g43e8b7f`.

`TestNKEDeclarativeBindingWithHelm` joined the simple `controller: cwe` relationship from the
[six-output Definition](../examples/nke-controller-bindings/definition.yaml) with the chart mappings from its
[catalog producer](../examples/nke-controller-bindings/registry.yaml). It bound a **fixture** image digest
(`sha256:` followed by 64 `a` characters), then rendered the real chart with both repositories:

- `registry.operator-a.test/nke/cwe`
- `mirror.operator-b.test/platform/cwe`

Both renders used the digest written to `image.digest`, with different repository overrides. The complete
upstream templates directory was byte-identical before and after binding. The chart tree was unchanged by
either render. Repository and tag defaults were preserved. These repositories were strings supplied to Helm;
no push/pull or image availability check was performed. Normal Helm digest overrides remain possible.

## Pin and compile

The example catalog resolved that controller tag using the existing local NKE clone through a process-local
Git `insteadOf` override. SPIRE 0.30.1, SPIRE CRDs 0.6.1, Temporal 0.74.0 and PostgreSQL 18.5.15 were acquired
from their real HTTPS/OCI repositories. The temporary lock contained the umbrella source and four acquired
chart pins. `nkdctl run plan` successfully compiled all six named outputs; the controller chart package
explicitly depends on the CWE package. The simplified Definition and catalog were recompiled against those
existing pins, and real Helm rendering was repeated successfully. Temporary inputs and plans were kept
outside the repository.

## Contract checks

The package test builds a real local fixture OCI image and compares the complete Helm OCI layout bytes
under two different Environment publication prefixes. Lifecycle observation independently reproduces the
package. Separate tests exercise multiple slots, shared list selectors, missing mappings,
duplicate targets, unsafe source entries, typed dependencies and template preservation using real Helm.
Catalog admission and identity tests cover producer-owned mappings. Relationship tests reject unknown or
unbound slots and prove that cloned mappings cannot be changed through the original input maps.

`make check notices-check workflows-check build` passed, including race tests, lint, vet, shellcheck,
vulnerability scanning and generated-workflow drift checks. Uncached `go test ./... -count=1` also passed. The maintained controller pin was advanced to rc.9 through `catalog pin` so its producer can package the
source chart with existing digest support; the CLI pin was unchanged. Generated files were regenerated. No hosted execution was triggered.
