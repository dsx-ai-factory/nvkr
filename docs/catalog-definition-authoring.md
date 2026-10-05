# Manage shared catalogs and explicit Definitions

Reuse producers and pins in catalogs, and keep each Definition's required outputs explicit. The current
catalog stack, per-output catalog selection, derived pin sets, and chart image slots already cover useful
reuse without an inheritance language. Start with those mechanisms; use the
[authoring lab](examples/authoring-lab/README.md) to see exactly how each replacement changes identity.

This analysis uses the NKE deploy/controller inputs at `d1a4ec9`. Recommendations below are authoring and
maintenance proposals, not new schema support. The lab exercises public pinning and planning commands;
it does not establish container execution, signing, publication, installation, or release qualification.

## What belongs in each file

| Concern | Owner | Reuse mechanism |
|---|---|---|
| Source repository, allowed version, vendoring and toolchain | Catalog source/producer | Reference one source from several producers; retain separate source roles when acquisition differs. |
| Exact upstream commit and acquired bytes | Machine-managed lock | Pin/bump through the catalog owner; never copy a newer version string over old digests. |
| Versions selected by an upstream release | Source pin set | Explicit members and reciprocal `pinnedBy`; update the umbrella and members together. |
| Producer command, output contract, chart digest paths | Catalog producer | Select the producer directly from as many outputs as needed. |
| Complete producer substitution | Ordered catalog overlay | Pin the predecessor catalog digest and replace the whole producer. |
| Deliverable membership, platform and chart-to-image relation | Definition output | Name each required output, its exact producers, and any per-output catalog stack. |
| Registry, credentials, runtime and runner choices | Environment | Select it independently; do not embed these settings in shared producer instructions. |

The contracts are [ADR-11](adr/0011-composition-and-resolution-semantics.md),
[ADR-17](adr/0017-cli-sdk-foundation-and-package-topology.md), and
[ADR-22](adr/0022-catalog-pinning-and-the-lock-contract.md). A shared source is not a generic dependency
language. Chart image slots and VM material contracts introduce specific edges with their own admission
rules. Equal Build Digests do not authorize reuse of another named output's execution or evidence.

## Repetition in the maintained NKE inputs

The [deploy Definition](../definitions/nke-deploy/definition.yaml) and both controller architecture
Definitions already share [one controller catalog](../definitions/nke-controller/catalog/registry.yaml).
The deploy Definition selects six outputs while the architecture Definitions select the controller image,
provider image, and controller chart. Keep this distinction: a common catalog does not require identical
output membership or a new reusable group object.

| Repetition or coordination cost | Recommendation now | Condition for a later change |
|---|---|---|
| Two controller Definitions repeat output structure across architectures. | Keep the small explicit files, sort output names, and validate both whenever the shared catalog changes. Their platform and chart bindings are reviewable without expansion rules. | Consider a deterministic authoring generator only after multiple products demonstrate the same repeated edits. It must emit ordinary complete Definitions with one owner and a drift check. |
| Deploy and older CLI inputs repeat action and utility-tool declarations. | Keep reusable producer catalogs cohesive. Select a separate catalog per output when the CLI has a different source policy; choose shared utility tools explicitly at the Definition level. | Catalog imports would require exact identities, collision rules, pin ownership, cycle/resource bounds, and deterministic regeneration. No import support is claimed here. |
| `nke` and `nke-helm` point at the same release but serve different source roles. | Keep the selected release aligned deliberately. Their toolchain and Helm vendoring declarations justify distinct roles. Update both roots in one existing lock transaction. | Derive a member only when an upstream version document actually declares it. Do not use `pinnedBy` as an invented same-repository relationship. |
| Controller chart archive/version flags contain `0.1.1`. | Review chart identity alongside the chosen NKE release. A source bump alone does not update these authored packaging instructions. | A chart metadata/version extraction feature needs an explicit input, exact pinned-tree validation, and identity contract before adoption. |
| An overlay's exact predecessor digest changes when the base catalog moves. | Review the complete new base, then intentionally reissue the overlay against it. A stale overlay is a useful refusal. | A rebase helper could summarize affected producers and emit a candidate; it must never silently refresh pins or bless an incompatible producer contract. |

Avoid copying the whole NKE catalog merely to change one supplier. A whole-producer overlay preserves the
logical component's supported platforms and output type. Use a new component name when that contract
changes. A per-output catalog override limits which named output uses the alternate supplier; it does not
change every other output's catalog stack.

## Update a coordinated release

The maintained `nke` and `nke-helm` sources currently both constrain `v0.21.0`. An ordinary bump should
remain a no-op while those constraints stay fixed. A release-selection policy must explicitly authorize
changing both constraints before automation chooses a different release. Once the reviewed registry
selects the intended release for both roots, use the existing complete-lock transaction:

```sh
nkdctl catalog bump --dir definitions/nke-controller/catalog --check nke nke-helm
nkdctl catalog bump --dir definitions/nke-controller/catalog nke nke-helm
```

The preview returns `NKD-E-DIFF` when changes are available. The four external chart members move through
`nke`'s `nke-deploy-v1` pin set. They cannot be bumped independently. Before accepting the candidate,
check that the two source roles resolve the intended matching commit, tree, and epoch; do not equate
matching tag text across unrelated repositories with matching content. Regenerate the deploy bundle and
validate both controller Definitions from the same reviewed inputs.

This is an operational coordination recommendation, not a new catalog relationship or release authority.
Both lock updates use the catalog's existing atomic transaction; a separate workflow must own the reviewed
registry edit, lock update, regeneration, and validation as one candidate change. Pin-set loading re-reads
pinned Git objects, so private-source access must be available to generation and finalization with the
existing repository-scoped credential rules. Offline pin-set transport is not implemented.

## Three kinds of replacement

| Intended change | Authoring operation | Expected identity effect |
|---|---|---|
| Upgrade an upstream source and its declared cohort. | Pin or bump the source/umbrella; retain producer and output names. | Selected graphs using changed source inputs get new Build Digests. Unrelated selected graphs retain theirs, even if the whole catalog digest changes. |
| Use another implementation or supplier for one logical component. | Replace its complete producer in a predecessor-bound catalog overlay. | Every output using that effective stack and component changes; changing from source build to acquired bytes also changes the graph's producer kind. |
| Change what one deliverable uses. | Select different producers or provide an explicit catalog stack on that Definition output. | That output's Build Digest changes; other outputs remain independently bound to their existing selection. Packaging relationships are revalidated. |

A chart's Build Digest identifies its source producer graph. Binding a different image is a packaging
relationship: the complete Plan and eventual chart package capture it. Do not infer that all downstream
Build Digests must change whenever a package dependency changes. No build-time comparison predicts future
artifact digests without executing and observing those exact outputs.

The lab separately demonstrates a global producer overlay, that same overlay scoped to one output, and
a different component selection. It also rejects an old overlay after the base source moves. It retains
full before/after Build Digests, complete plans, authored variants, and the pin preview for inspection.

## A maintained example with honest limits

Run the [local authoring lab](examples/authoring-lab/README.md) for seven output slots, seven catalog
producers, a two-member source pin set, shared source fetches, two CLI target platforms, and a source chart
bound to an image output. Its fixture scripts are intentionally small so identity changes can be understood.
The test executes the actual public CLI against a deterministic local Git repository and verifies coded
refusals, no-op lock preservation, deterministic plans, and the exact changed-output sets.

Use these complementary maintained examples for additional contracts instead of pretending one fixture
exercises all NKD capabilities:

- [NKE deploy](nke-deploy-pipeline.md): the real six-output source/acquired chart composition and current evidence limits.
- [Generic pin sets](generic-pin-set-example.md): mapping exact upstream versions to source and acquired members.
- [Declarative Helm bindings](declarative-helm-bindings.md): image slots, immutable acquired charts, and registry-independent package bytes.
- [NKE OS inputs](../definitions/nke-os/README.md): explicit VM roots/materials, architecture-specific inputs and execution requirements.
- [Disk material tracing](nke-os-material-trace.md): the internal observation contract and its remaining lifecycle/evidence gaps.

No new inheritance, mixin, catalog-import, wildcard-selection, or cross-output reuse behavior is introduced.
