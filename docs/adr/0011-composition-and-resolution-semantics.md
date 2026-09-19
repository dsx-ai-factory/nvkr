# ADR-11 — Direct producer selection & build graph derivation

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-11 in the consolidated NKD ADR set.
- **Relates to:** ADR-02 (identity over the resolved graph), ADR-12 (target and AICR inputs), ADR-07 (reproduction identity),
  ADR-05 (inventory).
- **Scope:** both evidence lanes share direct selection and graph admission; neither selection nor compilation
  grants execution, reproduction or release authority.

## Context

A Definition already names its required outputs and their packaging relationships. Requiring a separately
authored component group adds a second place to maintain membership without establishing compatibility.
Reusable catalog producers supply the build instructions and pins. The compiler can derive each output's
complete required build graph directly from those producers.

## Decision

### 1. Exact producer selection

Each Definition output declares a concrete target platform and either a nonempty explicit component selection
or one explicit AICR profile (ADR-12). The output uses the Definition's catalog stack unless it supplies its own.
Catalog sources, tools, components and AICR profiles are reusable. There is no authored Recipe, reusable build
group, criteria query, wildcard, specificity rule, inheritance chain, mixin, enable/disable merge, or reserved-axis
activation. Removed fields and reserved legacy `overlays/` and `mixins/` directory layouts fail closed; they
must not become ignored inputs.

Admission checks the complete catalog, unique selected IDs, supported target declarations, exact source/artifact/
tool pins, and the producer route before graph generation or external work. Unknown components and unsupported
platforms are coded failures, never empty selections. Ordinary outputs cannot combine their component set with
an AICR profile. For a disk output, resolution admits exactly one VM-image root and its complete selected material
closure. Compilation lowers the root's typed command or Packer contract and exact producer edges. Execution,
observation and publication remain unavailable and fail capability admission before source or output work.

`pkg/nkd/buildgraph` owns selection and compilation. Its resolved value describes validated inputs and owns
copies of them; it does not grant authority. Direct compilation revalidates that description. CLI and SDK callers
use this same owner rather than implementing another resolver.

### 2. Generated closure and identity

The compiler selects exactly the requested producers and their pinned inputs. It lowers supported
fetch/build/acquire/terminal-verification relationships plus declared VM material edges; it does not infer an
arbitrary producer dependency language. Duplicate fetches of the same pinned source can be shared within one graph. Every selected producer
must be covered by the terminal node, and the full graph is validated before identity or execution.

The generated graph uses `nkd.dev/ir/v4`; its transport-independent logical projection is identified under
`nkd.build` (ADR-01/02). An unrelated catalog producer cannot change the selected graph's identity. Changing
selected membership, target, pins, AICR rendering settings or output-affecting instructions must change it.
Output labels, packaging and Environment settings remain outside build identity and inside their owning records.
Deployment categories, node roles and cluster composition are removed rather than transported as unused metadata.

Each output remains independently accountable even if two output graphs have identical Build Digests. Initial
execution may produce shared components twice. Cross-output reuse requires a separate authenticated reuse
contract; a digest match alone cannot satisfy another output's required execution or evidence slot.

### 3. Component catalog overlays and trust

A separately referenced component catalog overlay is retained (ADR-22). It replaces a complete producer against
an exact predecessor catalog digest and is applied in explicit order. It does not reintroduce Recipe
inheritance, component-field deep merging or wildcard membership selection. Effective catalog snapshots bind
all admitted producer declarations and supported platforms. Catalog and overlay v3 strictly reject removed
classification and role fields.

The former extension classifier based on authoring shape is removed. A catalog path, an overlay declaration,
or a claim that a selection is published cannot establish trust. Consumer policy authenticates the exact
published subjects, identities and evidence. A recomputed build digest is content identity, not publisher or
release authority.

### 4. Standard-interface field roles

Scheme admission remains specific to each field:

- source repositories admit `https`, `git` and `ssh`;
- prebuilt download URLs admit `https`;
- image/toolchain fields admit validated OCI references, including the supported explicit `oci://` form,
  subject to their exact digest-pin requirements.

Failures identify the offending field and use the stable coded-error boundary. Pin and transport validation
are retained when authoring composition is removed.

## Consequences

Authors specify membership once, at the output that requires it. Pinned producer instructions remain reusable;
there is no second authored lifecycle object to keep synchronized. The cost is an explicit migration of graph,
plan, observation and evidence identities. A simple name substitution is insufficient: the old grouping and
matching implementation must disappear.

Multiple output uses sharing content still require distinct completeness checks. Exact required-output
membership and evidence joins remain independent of deployment topology.

## Alternatives considered

- **Retain flat named Recipes.** Rejected for the current product: no demonstrated reuse justifies an authored
  grouping between output selection and reusable producers.
- **Keep a compatibility resolver or rename groups to builds.** Rejected because it preserves the removed
  authoring model and creates two execution paths.
- **Inline producer instructions in every Definition.** Rejected because catalog producers already provide
  the useful reuse without selector complexity.
- **Infer arbitrary producer dependencies.** Deferred until a concrete product requirement and explicit
  graph/admission contract exist.

## Acceptance evidence

The migration requires direct-selection contracts in `pkg/nkd/buildgraph`, strict catalog loading contracts,
and identity/full-graph tests in `pkg/nkd/ir`. Preserve source pins, complete terminal coverage, builder/capability
admission, source deduplication within a graph, deterministic ordering, transport purity and target sensitivity.
Deleted matching/merge mechanisms need rejection coverage, not replacement golden inheritance fixtures.

Focused IR vectors and producer-selection tests exercise these contracts. AICR bundle tests cover equivalent
defaults, every rendering setting, owned input slices and malformed or caller-mutated descriptions. The completion
gate, generated-bundle checks and external evidence limits remain recorded in reconciliation.
