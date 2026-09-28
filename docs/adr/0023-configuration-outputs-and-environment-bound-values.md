# ADR-23 — Configuration outputs and environment-bound values

- **Decision status:** Proposed (design draft; pending named-decider ratification)
- **Implementation maturity:** None. Tracked in `reconciliation.md`.
- **Acceptance-test maturity:** None. Tracked in `reconciliation.md`.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-23 in the consolidated NKD ADR set (adds a non-byte-reproducible output kind).
- **Relates to:** ADR-01 (canonical codec/digest families), ADR-02 (IR determinism/purity boundary), ADR-04/09/16
  (evidence lanes), ADR-17 (public objects), ADR-19 (artifact membership vs. downstream deployment ownership),
  ADR-20 (publication and target configuration, especially §1 and §6's rendered-controller pattern).
- **Satisfies:** A gap in ADR-19/20: no current output kind lets a Definition ship values that are *intentionally*
  different per Environment without perturbing Build identity or requiring a rebuild.

## Context

Every current output format (`executable`, `oci-image`, `apt`, `helm-oci`, `disk-image`) targets exact-byte
reproduction or pipeline-attested production of one logical artifact (ADR-01/02).

But a chart's *values* legitimately differ per consumer: registry mirror and other
transport-shaped settings that ADR-20 §1 already classifies as environment configuration, not build input.
Today there is no supported way to produce a per-consumer values file as a tracked, published
Definition output — authors either hand-edit it outside NKD (undocumented, unversioned, no completeness check) or
are tempted to bake one registry's values into the chart itself, which does not scale to an unbounded, unknowable
consumer set and breaks define-once/publish-many.

This ADR adds a `configuration` output format for exactly this shape: files whose content is intentionally a
function of the Environment, published as an OCI artifact, tracked as a required Definition output, but exempt
from the byte-reproducibility and cryptographic-evidence contract every other format carries.

## Decision

### 1. `configuration` is a new output format, not a new lifecycle object

A Definition output may set `format: configuration`. It is authored and required exactly like any other named
output (ADR-17/19): missing, duplicated, or substituted configuration outputs fail Release completeness the same
as any other output. It does **not** introduce a seventh public object, a second lifecycle, or a deployment-topology
concept. It remains subject to ADR-19's boundary: a configuration output may carry transport-shaped values
(registry, repository, host, namespace-default strings) and nothing that ADR-19 reserves to downstream deployment
ownership — no node roles, replica counts, cluster context, or placement. Capability admission rejects a
configuration output containing a field name that collides with a reserved topology vocabulary term before any
render or publish work.

**Example `Definition` and `SourceBuildRegistry`**
```yaml
apiVersion: nkd.dev/definition/v4
kind: Definition
outputs:
- name: nke-controller-chart
  format: helm-oci
  platform: linux/amd64
  components:
  - nke-controller-chart
- name: nke-controller-values
  format: configuration
  platform: linux/amd64
  components:
  - nke-controller-values
---
apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
components:
  nke-controller-values:
    fromSource: false
    files:
      values.yaml: |
        image:
          repository: {{ .Environment.Publication.RepositoryPrefix }}/nke-controller
```

`nke-controller-values` selects the `nke-controller-values` catalog component exactly like any other output
selects a component — no special output-level field beyond `format: configuration`. The compiler resolves that
catalog entry and renders every entry in its `files` map.

`files` is a new component field, valid only on a component with `fromSource: false`. The catalog schema already
has two `fromSource: false` lanes: `prebuilt` (byte-preserving acquisition from a pinned URL/image) and
`exclusionReason` (no artifact at all, an unsupported-platform marker). `files` is a third: catalog-inline
generated content with no upstream source, no build, and no acquisition. A component declaring `files` alongside
`source`, `buildCommand`, `prebuilt`, or `exclusionReason` is rejected before any render or publish work.

### 2. The template context is Environment-only

A configuration output's template context has no access to any other output's identity — the render context is
`.Environment` only (§3). There is no injected `.Outputs` namespace and no Definition-level declaration linking a
configuration output to the output(s) it happens to accompany.

Rendering is a pure function of the selected component's `files` map content and the exact admitted Environment
document — no network, filesystem, clock access, or any other output's identity, consistent with ADR-02's purity
boundary. It is deterministic *given* an Environment; it is not claimed to be deterministic across Environments,
which is the entire point of this output kind.

### 3. Excluded from Build identity, included in required-output membership

A configuration output's rendered bytes are excluded from Build Digest, exactly as Environment already is (ADR-20
§1). The `files` map is authored content in `registry.yaml` itself, not a pinned external source — its bytes are
captured by this repository's own commit, the same way a Definition or Environment document is, rather than
through the catalog's source-pin/lock mechanism (ADR-22). Reproducibility is preserved at the level of "which
registry.yaml commit", not "which rendered bytes". Getting a configuration artifact for a second Environment means
running the same Definition again against a different Environment (ADR-17 §6's "one Run is one immutable attempt"
is unchanged); because Environment is already excluded from Build identity, every other output in that second Run
reproduces byte-identical, and only the configuration output's content differs. This ADR adds no decoupled
"republish an existing Run under a new Environment" capability — that remains out of scope and, if ever proposed,
needs its own ADR.

A required configuration output missing from a Run's results still fails Release completeness (ADR-16) the same
as any other required output. What differs is *what* "present" means: presence is checked by successful render and
publish, not by matching an independently observed byte digest against a frozen expectation.

### 4. No signature, attestation, SBOM, or qualified-lane participation

A configuration output skips cosign signing, SLSA provenance, SBOM generation, and referrer attachment entirely —
there is no reproducible or attested claim to make about content that is expected to differ by design. It cannot
satisfy the `qualified` lane and is excluded from a Release's qualification-tier computation (ADR-04/09/16): a
Release's evidence lane is computed only from its byte-reproducible/evidence-bearing outputs. The Release view
marks each configuration output distinctly, e.g. `kind: configuration`, `evidence: none`,
`reproducibility: environment-scoped`, so a consumer can never mistake it for an evidence-bearing member by
omission. A Verification Policy (ADR-16) must explicitly admit `configuration` outputs; the default is fail-closed
— a policy that does not name `configuration` rejects a Release containing one, the same way an unrecognized
output kind fails closed today.

### 5. Publication: OCI, content-addressed like anything else, but tagged as the discoverable reference

A configuration output still publishes as an OCI artifact containing the rendered files, in its own sibling
repository under the Run's `repositoryPrefix` (ADR-20 §2), pushed by content digest like every other artifact —
OCI storage is always content-addressed; "skip the digest" never meant an unaddressed blob. What it skips is
**digest-as-identity for release/evidence purposes**: unlike other outputs, the digest is not compared against a
frozen build-time expectation, because there isn't one. Because there is no Build-Digest-keyed reference a
consumer can resolve ahead of time, the publisher additionally advances one mutable discovery tag deterministic in
the output name and the admitted Environment's identity (e.g. `<output-name>:<environment-name>`), on the same
non-authoritative-discovery-hint basis ADR-20 §2 already uses for Helm's two chart tags. Re-rendering the same
output for the same Environment (e.g. after an Environment field changes) advances that tag; it never mutates a
previously published digest's bytes.

### 6. Fail-closed capability admission

`configuration` requires the same up-front capability admission as any other format (ADR-17): unsupported engine,
a missing or unresolvable selected component, a `files`/source-lane conflict on the selected component (§1), a
template referencing an undeclared Environment path that fails to render, or an empty rendered file are coded
errors returned before any publish I/O, with no partial artifact uploaded — consistent with every other format's
"no partial output" rule.

## Consequences

**Positive**
- Closes the real gap: a Helm chart (or any paired artifact) stays byte-identical and reproducible across every
  consumer, while consumer-specific values become a tracked, required, published artifact instead of an
  undocumented manual step.
- Reuses `define-once/publish-many` (ADR-20 §1) instead of fighting it — no new Build Digest per registry.
- `files` slots into the catalog's existing `fromSource: false` lane structure (alongside `prebuilt` and
  `exclusionReason`) instead of inventing a new component kind or a parallel authoring surface.
- Zero new schema or compiler surface for v1 digest safety (§2): no `configures` field, no injected `Outputs`
  namespace, no new validation pass beyond the `files`/source-lane conflict check (§7). A configuration output
  simply never receives identity data to redirect.

**Negative / costs**
- A genuinely new class of Release member: not byte-reproducible, not signed, not evidenced. Every consumer of
  the Release model (policy, tooling, documentation) must learn to treat `configuration` outputs differently
  without accidentally treating "present" as "verified."
- Whole-Environment template exposure (§3) trades a compiler-enforced allowlist for authoring convenience; a
  careless template can publish more of the Environment document's shape than intended. This is a review-time
  risk, not a runtime one, and should be called out explicitly in any future catalog/definition review checklist.

## Alternatives considered

- **Bake per-registry values directly into the chart per Environment.** Rejected — the consumer set is not
  knowable at build time, so there is no fixed matrix to bake variants for even before considering that it would
  also multiply Build Digests for content that never actually changed and break define-once/publish-many.
- **Leave values files entirely to downstream consumers to author by hand.** Rejected as the *only* path — it does
  not scale past a couple of consumers and leaves no completeness check or discoverable published artifact; still
  valid as a fallback for a consumer who opts out of a Definition's configuration output.
- **Inject a paired output's digest into the v1 template context** (via a `configures` field or similar).
  Considered and deferred rather than rejected outright (§2) — not needed for the motivating Helm/registry-prefix
  case, since ADR-20 §6 already fixes the digest at the paired chart's own build time; deferring keeps v1's schema
  and admission surface minimal. Revisit if a future format needs a compiler-injected non-Environment value that
  its own build step doesn't already self-protect.
- **Discover the template from the paired output's own selected source tree** (a `values.yaml.tmpl` shipped
  alongside `Chart.yaml` inside `nke-helm`'s source checkout). Superseded by the inline `files` map (§1/§3) —
  scanning a built or checked-out source tree at render time would make rendering depend on that component's
  `buildCommand`/`source` having run, and would need a new path-resolution/extraction contract with no existing
  analog. Catalog-inline `files` keeps the template as plain reviewed YAML with no execution dependency.
- **A Definition-declared explicit field map instead of whole-Environment context.** Considered as the safer
  default (a compiler-checked allowlist of exactly which Environment fields a template may see). Not adopted here
  in favor of whole-document context for lower authoring overhead on arbitrary file sets; revisit if the
  information-exposure risk in §3 proves real in practice.
- **A decoupled "republish existing Run under a new Environment" operation.** Rejected for this ADR's scope — it
  would let Environment influence a *previously completed* attempt's publish step, in tension with ADR-17 §6's
  frozen-attempt model, and needs its own admission and evidence analysis if ever proposed.

## Follow-ups

- **ADR-16:** Verification Policy schema needs an explicit `configuration`-admission flag and the qualification-
  tier computation needs to exclude configuration outputs from its evidence tally.
- **ADR-20:** the publish phase needs the per-Environment discovery-tag mechanic (§6) alongside the existing Helm
  chart tag precedent.
- **Compiler:** the reserved-topology-field rejection (§1) and the `files`/source-lane conflict check (§1/§7) are
  new admission checks with no existing analog to extend from.
- **Future revision:** a deterministic, compiler-owned non-Environment value set (e.g. paired output digests, §2)
  is a tracked but undesigned extension point — no schema, field name, or injection mechanism is committed to
  here.
- **Documentation:** a catalog/definition review checklist should call out the inherited-safety limitation in §2 —
  reviewers must confirm a configuration output's paired format actually self-protects its own digest before
  treating the pairing as safe.

## Acceptance evidence

None. This ADR is a design proposal with no implementation, contract tests, or recorded run. It must not be cited
as an available capability until `reconciliation.md` records contract-tested schema validation, bounded template
rendering, and an OCI publication path with the discovery-tag mechanic — plus, before any production claim, an
actual `helm install <chart>@sha256:... -f <pulled-values>` consumer exercise against a real registry.
