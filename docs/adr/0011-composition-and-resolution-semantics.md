# ADR-11 — Composition & resolution semantics

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-11 in the consolidated NKD ADR set.
- **Relates to:** ADR-02 (identity over the resolved graph), ADR-12 (criteria model), ADR-07 (reproduction identity),
  ADR-05 (inventory).
- **Satisfies:** FR-01 (ties fail closed), FR-02 (layered composition), FR-21 (standard-interface enforcement).

## Context

A recipe may be authored as a `base` plus criteria-keyed `overlays` and orthogonal `mixins`. It must **flatten
deterministically into one Resolved Recipe** whose Recipe Digest is its identity (ADR-02). The SRD requires
normative merge and conflict-resolution rules and requires ties to fail closed. This ADR fixes the merge algebra,
the fail-closed conflict rule, how drift is caught, how an operator's *kind* of extension is classified, and how
standard interfaces are enforced at authoring time.

## Decision

### 1. Normative merge algebra + fail-closed conflicts

- **Selection.** All criteria-matching overlays are composed (merge-many, not pick-one); overlays that are an
  ancestor of another match are dropped (**maximal leaves**); application order is least-specific-first then name,
  base seeded first, each overlay applied once.
- **Chain.** For each leaf, resolve its `base ← overlay` chain **child-wins**.
- **Merge.** Component refs merge by name; a set `enabled` value is child-wins; `overrides` deep-merge where
  **scalars and slices replace wholesale (no concat/union)** and **maps merge key-wise**, child wins, values
  deep-copied.
- **`enabled: false`** disables a component: it stays in the inventory for provenance but is suppressed from the
  effective set, and a more-specific child may re-enable it.
- **Mixins** merge component refs by name, but **constraints do not compose**: a name collision across any
  selected layer or mixin **fails closed** at the authoritative merge seam.
- **Sibling ambiguity (new).** Two equally-specific, non-ancestor leaves that write **conflicting values on the
  same `(component, path)`** **fail closed** (`ambiguous`), extending FR-01 into the layered path. The predicate
  keys on *same component + same leaf path + differing values* only, so orthogonal overlays (for example
  `category` vs `os`) still compose cleanly. (`role` is not an overlay-selection axis; see ADR-12.)
- **Determinism.** Every output is sorted; override re-validation runs post-merge, so an override can never unpin
  a source or downgrade a builder (fail closed).

### 2. Golden-digest fixtures

Golden fixtures **pin the composed Recipe Digest** for the test catalogs across their criteria
combinations, plus the applied-layer provenance list. A bare test run never rewrites them and fails on unintended
drift; a documented regen path handles intended changes. Any change to the merge algebra or the pins appears as a
fixture diff paired with an explanatory note.

### 3. Operator-extension-surface classifier

The resolved result is classified into exactly one of three surfaces, emitted in the deterministic selection
record:

- **published-tuple** — criteria select an existing published recipe; it resolves an existing identity and mints
  no new one.
- **declared-overlay** — operator overlays, mixins, and components compose to a **new** from-source-reproducible
  Recipe Digest, still inside the publisher's trust chain.
- **fork** — `--data` supplies a catalog whose identity differs from the referenced one, and is therefore
  **outside** the publisher's chain.

### 4. Standard-interface scheme allowlist (FR-21)

Enforced **per field role**, not by a blanket regex:

- source repositories: `https`, `git`, `ssh`;
- prebuilt download URLs: `https` only;
- OCI image / toolchain refs: a well-formed OCI reference — both bare (`registry/path@sha256:…`) and an explicit
  `oci://` prefix are accepted, with the digest-pinned/tag rules applied on top.

A violation fails closed naming the field and the offending scheme.

## Consequences

**Positive**
- Layered composition is normative, deterministic, and fail-closed on genuine conflicts.
- Golden fixtures turn merge-semantics drift into a visible, reviewable diff.
- The classifier gives downstream a machine-readable kind-of-extension signal with explicit trust implications: a
  fork leaves the chain.
- Interface enforcement is standards-based but permissive to operator equivalents.

**Negative / costs**
- The sibling-conflict predicate must be precise or it over-rejects legitimate orthogonal overlays.
- Golden fixtures are brittle by design and need the documented regen discipline.

**Neutral**
- The flattened result's identity is the ADR-02 Recipe Digest; this ADR defines *how* the flattening happens, not
  the identity mechanism.

## Alternatives considered

- **Pick one most-specific recipe (like the flat path) instead of merge-many.** Rejected — abandons the layered
  composition model.
- **Slice union/concat on override.** Rejected — breaks reproducibility intuition and churns every identity.
- **A single global scheme regex.** Rejected — breaks bare OCI refs, which have no scheme.

## Follow-ups

- **ADR-12:** the criteria asymmetric-match rules that drive overlay selection.
- **ADR-02:** the flattened result's digest is the Recipe Digest, subject to the purity boundary.
- **ADR-22 §7:** a composition that consumes an upstream **curated pin set** binds the umbrella source's commit
  as `curatedBy`, so the resolved graph records which curated set it came from; the merge and conflict rules for
  a curated member are this ADR's to state normatively.

## Acceptance evidence

Current contract evidence in `pkg/nkd/compose/*_test.go`, `pkg/nkd/recipe/*_test.go`, and
`pkg/nkd/catalog/*_test.go` covers merge algebra, sibling and selected-chain conflicts, strict field-role scheme
allowlists, layered selection ambiguity, deterministic resolution, transport relocation, and golden recipe
identities. The golden corpus is an determinism gate and must change only with reviewed semantic
or pin changes.

Acceptance remains ongoing rather than final: every schema/catalog evolution must update the relevant behavior fixtures,
and public direct composition/compilation routes must reject inputs that did not pass the same validation as the
authoritative catalog resolver.
