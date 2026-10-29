# ADR-26 — Definition composition

- **Decision status:** Proposed (design only; pending named-decider ratification)
- **Implementation maturity:** U1–U7 implemented; U8 deferred, U9 waits on the result-reuse decision and U10
  on a hosted composite Run. `reconciliation.md` records the implemented scope.
- **Acceptance-test maturity:** Contract suites for U1–U7, including an opt-in extended identity-invariance
  contract over the four NVKE families. No hosted Run, composite publication or consumer verification
  exercises a composite.
- **Deciders:** Pending; release-engineering, publication, catalog and qualification owners must be named before
  ratification.
- **Index:** Proposed ADR-26 supplements ADR-01–22 and proposed ADR-23/24; it remains outside the consolidated
  contract set until accepted. It coordinates with the separately drafted authenticated result-reuse decision
  (backlog item 4), which is not on this branch.
- **Relates to:** ADR-01 (canonical codec and digest families), ADR-03 (coded errors), ADR-04 §5 (consumer
  verification), ADR-09 (production provenance), ADR-11 §1–§2 (direct selection, independent output uses),
  ADR-12 (target platforms), ADR-16 §5 (evidence lanes and qualification), ADR-17 §1 and §6 (public lifecycle,
  immutable attempts), ADR-18 §8 (execution graph and partitions), ADR-19 §1–§2 (output and Release membership),
  ADR-20 §6 (pipeline-attested publication), ADR-21 (generated bundles and maintenance), ADR-22 (pins).
- **Scope:** Composing complete, independently owned Definitions into one composite Definition that compiles to
  one complete Plan and produces one Run and one Release whose membership is the exact union of its members'
  named outputs, including production and development bundles. Reuse of already-published member Releases,
  nested composites, remote members and qualification of composite Runs are deferred and fail closed. Replacing
  the hand-maintained combined NVKE Definition is a separately gated cutover.

## Context

The four-family NVKE pilot (#211) split the 69-output combined candidate into core (20), services (28), telemetry
(17) and disks (4) Definitions. Each family has its own owner, Environment, generated bundle, Run and Release, and
the [partitioning strategy](../definition-partitioning-strategy.md) records why. The combined
`inputs/definitions/nvke/definition.yaml` remains a hand-maintained second copy of the same 69 output
declarations, so every family change must be repeated there. Consumers that want one product Release must
otherwise associate four family Releases themselves.

Execution partitioning (ADR-18 §8, PR #264) already runs one complete Plan as bounded same-commit worker
workflows beneath one Run and one Release, and its development Run 40727132359 completed all 69 outputs that
way. The missing piece is an authored input that produces that combined Plan from the family Definitions without
a second copy of their declarations.

ADR-11, ADR-17 and ADR-19 reject authored Recipes, reusable selection groups, inheritance, mixins and overlays.
Composition must not reintroduce them: it adds no selection, matching, override or default. It only joins
complete Definitions whose declarations are each valid on their own.

## Decision

### 1. A composite is a Definition form that pins members by exact content identity

A composite Definition is a distinct authored document kind. It remains the public **Definition** object; it is
not a seventh lifecycle object.

```yaml
apiVersion: nvkr.dev/composite-definition/v1
kind: CompositeDefinition
metadata:
  name: <canonical name>
  description: <optional>
members:
  - name: <member Definition metadata.name>
    definition: <locator relative to this file>
    digest: sha256:<nvkr.definition digest of that member>
```

- `members` is nonempty, sorted by `name` and unique. Each entry has exactly these three fields.
- `name` must equal the member document's `metadata.name`.
- `definition` is a **locator**, not identity. It is resolved relative to the composite file and must stay
  inside the admitted input root (the same root the compile-input capture of ADR-17 §1 uses). Reads use the
  existing bounded, no-follow, regular-file loader. Two entries resolving to the same file are an error.
- `digest` is the member's typed `nvkr.definition` canonical digest (ADR-01), the same value
  `Definition.Digest` already computes. It is the member's identity. A missing digest, a malformed digest or a
  digest that differs from the loaded member is a coded error. Formatting or comment edits to a member do not
  change its canonical digest; any semantic edit does and fails closed until the composite is re-pinned (§9).
- A composite carries no `catalog`, `sbomTool`, `cosignTool`, `outputs` or any other Definition field. Unknown
  fields are rejected by strict decoding.

Path inclusion without a digest is rejected: a member edit would silently change the composite's meaning, and a
moved or substituted file could not be detected. Pure digest addressing without a locator is also rejected for
v1: NVKR has no content-addressed Definition store, and inventing one adds a discovery trust problem. The
locator finds bytes; only the digest admits them.

### 2. Admission happens before any catalog, network, subprocess or output work

`pkg/nvkr/definition` owns composite loading and admission. In order, it:

1. strictly decodes the composite (4 MiB bound, as for Definitions) and validates its own schema;
2. admits at most **16** members and rejects duplicate names, duplicate resolved locators and duplicate digests;
3. loads each member through the existing strict Definition loader, so each member must be a complete, valid
   `nvkr.dev/definition/v5` Definition on its own, including its own Helm image bindings;
4. rejects any member whose document kind is `CompositeDefinition` (§8);
5. recomputes each member's canonical digest and requires equality with the pinned digest;
6. performs the merge of §3 and validates the resulting effective Definition with the ordinary
   `Definition.Validate`.

Only after all six steps may factory open catalogs. A failure at any step returns a coded error and produces no
Plan, bundle, attempt directory or partial output. Context cancellation is checked between members and maps to
`timeout`.

### 3. Merge rules: exact union, never precedence

Admission derives one **effective Definition**: an ordinary in-memory v5 Definition that the existing compiler
consumes unchanged. The derivation is a pure, deterministic function of the composite and its admitted members.

| Input | Rule | Conflict |
|---|---|---|
| Named outputs | Disjoint union. Each output declaration is copied unchanged except for catalog path rebasing below. Output names are preserved; there is no prefixing, renaming or namespacing. The union is sorted by name. | The same output name in two members is an error, even if the declarations are identical. There is no deduplication. |
| Per-output catalog | Every output receives an explicit catalog stack. An output without an override inherits its own member's top-level `catalog`. Base and overlay locators are rebased from the member's directory to the composite's directory. | A rebased locator outside the admitted input root is an error. |
| Top-level `catalog` | Every member's top-level catalog stack, after rebasing, must be identical. It becomes the effective Definition's top-level stack. | Any difference is an error. |
| `sbomTool`, `cosignTool` | Every member must select the same value (including omission). | Any difference is an error. |
| Helm image bindings | Copied unchanged. Each member was validated standalone (§2 step 3), so every binding already resolves inside its own member; preserved names keep it resolving to the same output. | A binding cannot reach another member's output by construction. |
| Platforms | Copied unchanged per output; aggregate `platforms` sets keep their exact membership (ADR-11 §2, ADR-12). | Unsupported platforms fail in the existing compiler and Environment admission, as for any Definition. |
| Producer pins | Catalog locks remain the only pin owner (ADR-22). One catalog file loads once per compile. For every `(component, target platform)` that outputs of **different** members select, the effective producer snapshot (after overlays and toolchain substitutions) must be identical. | Divergent effective producers for the same component and platform across members are an error. Within one member, existing ADR-11 semantics are unchanged. |
| Environment | Not merged. A composite Run selects exactly one Environment authored for the composite. Member Environments are not inputs and are never read. | The composite Environment is admitted against the effective Definition by the existing rules: unsupported platforms, missing execution classes and unrelated source-access entries fail as they do today. |
| Metadata | The composite's own `metadata` names the effective Definition. Member descriptions are not concatenated. | — |

The cross-member producer rule is deliberately stricter than a single Definition: two family owners releasing the
same component at different pins is exactly the silent incompatibility a combined Release must not hide. Authors
who intend two variants of one component must put both outputs in one member, where the variant is explicit. The
four NVKE families select no `(component, platform)` in common, so the rule does not affect them.

The effective Definition is a derived internal record, like a Plan. It is never written as an authored input,
cannot be selected with `--definition` and has no file. `definition inspect` may show it as a description.

### 4. Identity: the composite has its own; member identities are recorded inside it

| Identity | Family | Binds | Excludes |
|---|---|---|---|
| Composite Definition | new `nvkr.composite-definition` | composite `metadata` and the sorted `(name, digest)` member list | member locators (transport, like `DefinitionPath`) |
| Member Definition | existing `nvkr.definition` | each member exactly as today | — |
| Effective Definition | existing `nvkr.definition` | the derived v5 Definition | — |
| Plan | existing `nvkr.run-plan/v1` | as today, plus a `composition` record | — |
| Per-output Build Digest | existing `nvkr.build` | unchanged (ADR-11 §2) | output names, member attribution |
| Run, Release content, publication root | existing families | as today, plus the composition binding in their records | — |

The new family is registered in ADR-01 when implemented. A composite's Plan, Run record and publication-root
metadata carry a **composition record**: the composite digest, the effective Definition digest, every member's
`(name, digest)`, and for every named output the member that declared it. Plans, Run records and publication
roots that carry a composition record use new schema majors; records of plain Definitions keep their current
schemas and bytes. A reader that does not admit the new major rejects it. A reader that admits it requires a
complete, consistent record: every output attributed to exactly one listed member, no unlisted member, and an
effective Definition digest that recomputes from the recorded Plan inputs.

The existing `definitionDigest` field binds the composite digest for a composite and the Definition digest for a
plain Definition. Records carry an explicit definition kind so a verifier always recomputes under the right
family; a digest string alone never selects the family.

**Identity invariance.** Composition must not change what an output is. Compiling the composite produces, for
every output, the same Build Digest and native build records that compiling that output's member alone produces
with the same catalogs. Composition changes Definition, Plan, Run and Release identities; it never changes
logical Build identity.

Member attribution is descriptive membership inside an authenticated record. It establishes which authored file
declared an output. It does not establish that a member's own Run, Release or owner accepted the composite's
bytes.

### 5. One Plan, one Run, one Release; partitioning is orthogonal

A composite compiles to one complete Plan through the existing compiler. Execution partitioning (ADR-18 §8)
projects that Plan into workers exactly as it projects any other Plan. Partitions group by dependency depth and
capacity, never by member: a member is an authoring boundary, and a partition is a scheduling boundary. A worker
must not be presented as, or publish, a member Release.

The composite Run freezes the union of required output slots before execution (ADR-17 §6). Every named output is
an independent required use. Equal Build Digests across members do not merge producer jobs, results, terminal
observations or evidence, and a successful sibling never satisfies another output's slot. A failure in any
member's output fails the composite Run; there is no partial composite Release and no per-member success.

Publication is the existing ADR-20 §6 route. The Release is one flat publication root whose membership is exactly
the union of member outputs, with the composition binding of §4 in its signed metadata. It is not a parent of
member publication roots and references no member Release. Existing aggregate limits apply to the composite as a
whole (Plan reader bound, publication object and metadata budgets, workflow size before partitioning); exceeding
one is the existing coded failure, admitted before output work wherever the existing owner already admits it.

### 6. Reusing already-published member Releases: only through authenticated acceptance, a planned follow-up

Decided: composites rebuild every output until the authenticated result-reuse decision (draft ADR-25,
backlog item 4) lands. Cross-workflow reuse of member outputs is then planned as follow-up unit U9.

In this decision every composite Run builds every output. No member Release, family Run or prior publication
supplies a composite output, and no authored field selects one. A future authored reuse selection fails with
`engine-capability` before work until the conditions below are implemented.

Reuse of a member's published outputs must go through the authenticated result-reuse decision being drafted as
backlog item 4, never through a composite-specific shortcut. That draft keys reuse on the exact named output,
binds the publication repository, workflow identity and signer policy into the key, and issues an owner-held
acceptance receipt only after re-authenticating the complete payload and evidence closure. As drafted, a family
Release is therefore **incompatible** with a composite Run: its workflow, signer identity and namespace differ.
Composite reuse requires that decision to admit an explicit, authored cross-workflow origin, and it must keep:

- **Same name only.** A member output is reused only for the composite output of the same name and member. Equal
  Build Digests, equal payload digests or equal producers never authorize reuse for a sibling. Preserving output
  names (§3) is what makes this check exact.
- **Authenticated acceptance per output.** Each reused output is accepted under the composite's own policy with
  full payload verification; discovering or naming a member Release grants nothing.
- **Origin preserved.** The reused output retains its original family Run, attempt and production predicate.
  The composite Run signs no production provenance for it and records it as reused.
- **Copy, not reference.** Accepted bytes are copied by digest into the composite namespace so the composite
  Release does not depend on a family Release's retention.
- **Lane floor.** §7 applies.

### 7. Evidence lanes: no stronger than the weakest contribution

Lanes are properties of Runs and their Environments, not of Definitions. Members contribute declarations only, so
in this decision the composite Release's lane is exactly the lane of the composite Run that produced every output.
No member, family Run or family Release lane is inherited, and a stronger family Release cannot raise it.

When reuse is implemented, the composite lane is the minimum of the composite Run's lane and the authenticated
lane of every reused output, ordered `static-unqualified` < `pipeline-attested` < `qualified`. A reused output
whose lane cannot be authenticated forces refusal, not a lower guess. Lanes never substitute for one another
(ADR-16 §5), and the aggregate evidence floor still applies inside each output.

`pipeline-attested` composites are designed here. `static-unqualified` dry-run and local diagnostic composites
behave as for any Definition. Qualification of a composite Run (ADR-16 §5, M3 artifact checks, the M4 release
manifest) is deferred: those records bind one Definition digest and do not yet bind a composition record. Until
they do, qualification of a composite Run fails with `engine-capability` before work. `qualified` composites are
not designed.

### 8. Nesting, cycles and bounds

Nesting depth is **1**: a member must be a plain Definition. Because no member can reference anything, cycles
are impossible by construction, and self-reference is rejected as a nested member. Any future nesting requires
its own decision, with a depth bound, cycle detection over `(resolved locator, digest)` on the traversal stack,
and a total-member bound.

Bounds are checked during admission: at most 16 members; each member document within the existing 4 MiB bound;
the composite plus member documents within the existing 32 MiB compile-input capture aggregate. Output-count,
Plan, workflow and publication bounds stay with their existing owners (§5).

### 9. Capability admission and maintenance

| Use | Admission |
|---|---|
| `definition validate`, `definition inspect` | Supported once implemented |
| `run plan`, `run graph`, `run diff`, dry-run | Supported once implemented |
| `run local` | Static-unqualified only, as for any Definition |
| `run generate` for a production bundle, flat or partitioned | Supported once implemented; `pipeline-attested` |
| `githubDevelopment: true` | Supported by U5; signed development stays `pipeline-attested` with its own signer and disposable namespaces and grants no production acceptance. The single fixed development layout belongs to the combined candidate until cutover, so U5 admits a named development bundle for composites only: `githubBundle: <name>` with publication under `ghcr.io/<owner>/<repo>/development-<name>`, workflow `.github/workflows/nvkr-dev-<name>.yml`, its own manifest and Plan under `.github/nvkr-dev-<name>/`, and a signer identity bound to that workflow. Names that collide with the fixed development qualification files (`qualify`, `smoke`, `qualification`) are refused. A named development bundle requires that isolated signed publication; an unsigned named development bundle is refused with `engine-capability`, because an unsigned build has no namespace to isolate. The existing refusal of `githubDevelopment` with `githubBundle` for plain Definitions is unchanged and now applies at compilation, where the Definition kind is known |
| `run check-artifacts`, `run qualify`, qualification generation, release manifest issuance | Deferred; `engine-capability` before work |
| Member Release reuse | Deferred (§6); `engine-capability` |
| Nested or remote members | Rejected (§8, §1) |

Member digests are machine-maintained. A `definition pin` command, owned by `pkg/nvkr/definition`, recomputes
member digests and rewrites them through the existing byte-CAS write; `--check` reports drift without writing.
Generation and `run generate --check` fail with `unverified-ref` when a member has drifted from its pin, so a
family edit cannot silently change the composite or its bundle. A composite bundle joins the catalog-maintenance
closure (ADR-21 §3) like any other named bundle; when shared pins change, maintenance regenerates it, and when a
member changes, the review must re-pin the composite in the same change.

### 10. Consumer view

A consumer verifies a composite Release exactly like any Release: one pinned reference and one Verification
Policy (ADR-04 §5), with the same lane and scope rules. The policy schema does not change.

- Optional exact-input expectations (ADR-17 §1) may pin the composite digest, the Environment and the Plan.
  Lifecycle additionally exposes the authenticated composition record in the Release view, so a consumer can
  require an exact member `(name, digest)` set.
- An expectation prepared from a member Definition does not match a composite Release, and a composite
  expectation does not match a member Release. There is no implicit acceptance across forms.
- Verifying a composite Release establishes nothing about any family Release, and verifying all four family
  Releases establishes nothing about a composite Release. They are different Runs with different signers.
- A consumer may filter the authenticated Release view to one member's outputs. That filter is a description;
  it is not a member Release and grants no member-scoped capability.
- Release description export (ADR-19 §3) includes the composition record and per-output member attribution.

### 11. Coded errors

Decided: composition reuses existing ADR-03 codes and opens no new band. The cost is that a CI gate cannot
distinguish a composition conflict from other `invalid-catalog` failures by exit status alone; the message
and owning operation carry that detail.

| Condition | Code |
|---|---|
| Malformed composite, unknown field, empty or unsorted members, too many members, duplicate member name, locator or digest, member name mismatch, locator outside the input root, nested member | `invalid-catalog` |
| Duplicate output name across members, differing top-level catalog stacks or tool selectors, divergent cross-member effective producer | `invalid-catalog` |
| Member digest differs from the loaded member, at load, generation or `--check` | `unverified-ref` |
| Deferred use (qualification, member Release reuse) | `engine-capability` |
| Cancellation or deadline during admission | `timeout` |

## Worked example: the four NVKE families

This example is illustrative and is **not** added to `inputs/`. It assumes the composite would live beside the
families as `inputs/definitions/nvke/composite.yaml`. The digests are the actual `nvkr.definition` digests of the
family Definitions at commit `75f154fe`, computed with the existing `Definition.Digest`.

```yaml
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

apiVersion: nvkr.dev/composite-definition/v1
kind: CompositeDefinition
metadata:
  name: nvke-composite
  description: Release the four independently owned NVKE family Definitions together as one 69-output Release.
members:
  - name: nvke-core
    definition: core.yaml
    digest: sha256:896825b8d44b95479577f38d60b991f19b03f806ac7a8dcb24d2d097cebda953
  - name: nvke-disks
    definition: disks.yaml
    digest: sha256:893a845610beca933456bd7be9c0dc47439992905b8539f7f9ca367daf48256c
  - name: nvke-services
    definition: services.yaml
    digest: sha256:fce3f939afb592fabc49887d856ebb18c4ba368f1dbf908677a5647c29dc23f5
  - name: nvke-telemetry
    definition: telemetry.yaml
    digest: sha256:041225961418f7334df77541e1264ec691d6ab51701b1b7add9ac1160e0b1b65
```

The composite selects its own Environment. It would start from `inputs/environments/github-release.yaml` (which
already admits every family's platforms, execution classes, disk evidence and source access) and change only its
bundle, partitioning, namespace and signer:

```yaml
metadata:
  name: nvke-composite-github
  description: Signed NVKE composite candidate publication in its own workflow and registry namespace.
githubBundle: nvke-composite
githubExecutionPartitions: true
publication:
  repositoryPrefix: ghcr.io/dsx-ai-factory/nvkr/m2-candidate/composite
signing:
  identity: ^https://github\.com/dsx-ai-factory/nvkr/\.github/workflows/nvkr-run-nvke-composite\.yml@refs/heads/main$
```

Admission derives this effective Definition (abbreviated). Every output keeps its family name and gains an
explicit catalog; because the composite sits in the same directory, rebased locators are unchanged:

```yaml
apiVersion: nvkr.dev/definition/v5
kind: Definition
metadata:
  name: nvke-composite
  description: Release the four independently owned NVKE family Definitions together as one 69-output Release.
catalog:
  base: ../../catalogs/nvke/module-tags          # identical in all four members
sbomTool: syft                                   # identical in all four members
cosignTool: cosign                               # identical in all four members
outputs:                                         # 69 outputs, sorted by name
  - name: capi-controller-image                  # from nvke-core
    platform: linux/amd64
    catalog:
      base: ../../catalogs/nvke/release-components
    format: oci
    components: [capi-controller-image]
  # ... remaining nvke-core, nvke-disks, nvke-services and nvke-telemetry outputs, each copied unchanged;
  # outputs that used their member's default catalog now name ../../catalogs/nvke/module-tags explicitly.
```

Expected results, which unit U3 must prove rather than assume:

- 4 members, 69 named outputs, 73 native build records; no shared output name and no `(component, platform)`
  selected by two families (checked against the authored inputs at `75f154fe`).
- Every output's Build Digest and native build records equal those in the corresponding family Plan and in the
  retained combined `definition.yaml` Plan.
- One Plan with a composition record; one partitioned Run; one Release at
  `ghcr.io/dsx-ai-factory/nvkr/m2-candidate/composite/releases@sha256:…` whose 69 outputs are each attributed to
  one family.

Negative cases on the same inputs:

- Adding `definition.yaml` as a fifth member fails with `invalid-catalog`: its 69 output names collide with the
  families.
- Editing `telemetry.yaml` without running `definition pin` fails generation and `run generate --check` with
  `unverified-ref`.
- Adding a member whose top-level catalog is `../../catalogs/nvke/release` fails with `invalid-catalog`.

Decided: once the composite exists, the hand-maintained combined `definition.yaml` becomes redundant, and
retiring it is planned as a separate cutover (U10). The combined production, development and qualification
paths depend on it. The cutover is gated on a successful hosted composite Run with all 69 outputs and one
Release, followed by independent consumer verification under explicit policy. Composite qualification (U8)
must exist before the combined qualification path can retire.

## Implementation plan

Each unit is independently reviewable and lands with its contract suite. Contract tests assert the coded error,
not only failure, and are table-driven.

| Unit | Owner | Delivers | Depends on |
|---|---|---|---|
| U1 Schema and admission | `pkg/nvkr/definition`, `pkg/nvkr/canonical` | Composite type, strict decode, `nvkr.composite-definition` family, bounded no-follow member loading, digest check, §2 order | — |
| U2 Effective Definition | `pkg/nvkr/definition` | Pure §3 merge with rebasing and conflict rules | U1 |
| U3 Compilation and Plan | `pkg/nvkr/factory` | Per-output stacks from rebased locators, cross-member producer check, Plan composition record and schema major, compile-input capture and replay of member files | U2 |
| U4 Admission matrix and pinning | `pkg/nvkr/lifecycle`, `cmd/nvkr` | `--definition` dispatch by kind, §9 capability matrix, `definition inspect` view, `definition pin [--check]` | U1, U3 |
| U5 Generation, development and maintenance | `pkg/nvkr/factory`, `catalogmaintenance` | Named composite production bundles, flat and partitioned; named composite development bundles with independent manifest, signer and disposable namespaces; drift on member edit; maintenance-closure enrollment | U3, U4 |
| U6 Run and publication records | `pkg/nvkr/lifecycle`, `pkg/nvkr/delivery` | Run-record and publication-root schema majors with composition binding and per-output attribution; release description export | U3 |
| U7 Consumer view | `pkg/nvkr/lifecycle`, `client/v1` | Composite exact-input expectation, member-set requirement, cross-form refusal | U6 |
| U8 Deferred: qualification | ADR-16/21 owners | Bind the composition record into handoff, artifact checks, qualification evidence and release manifest | U6; separate decision update |
| U9 Follow-up: member Release reuse | result-reuse owner | Cross-workflow origin admission in the reuse decision, §6 and §7 | draft ADR-25 (backlog item 4) landing |
| U10 Cutover: retire combined `definition.yaml` | NVKE release owner | Move the combined production, development and qualification paths to the composite; retire the combined bundles and input | U5–U8; hosted composite Run and consumer verification |

Acceptance tests per unit:

- **U1:** valid composite admits; each `invalid-catalog` row of §11 (unknown field, empty, unsorted, 17 members,
  duplicate name/locator/digest, name mismatch, `..` escape, symlink locator, non-regular file, nested
  composite, self-reference); digest mismatch → `unverified-ref`; formatting-only member edit keeps the digest;
  pre-cancelled context → `timeout` with no member read; composite digest excludes locators (moving a member and
  updating its locator keeps the composite digest).
- **U2:** deterministic output (shuffled member order in memory yields identical bytes); duplicate output name,
  even with identical declarations → `invalid-catalog`; differing top-level stack, `sbomTool` or `cosignTool`
  → `invalid-catalog`; default-catalog outputs receive explicit rebased stacks; Helm bindings resolve to the same
  outputs; effective Definition passes `Definition.Validate`.
- **U3:** **identity invariance** — for the four NVKE families (extended, with pinned-source access like the
  existing split contract), every output's Build Digest and native records equal the family Plans and the
  combined Plan; divergent cross-member producer for one `(component, platform)` → `invalid-catalog` with no
  Plan; within-member variants still compile; captured inputs include every member file and replay without the
  checkout; plain-Definition Plans are byte-identical to before.
- **U4:** each deferred row of §9 → `engine-capability` before catalog resolution, with no attempt directory;
  `definition pin --check` reports drift without writing; pin rewrite uses byte-CAS and loses cleanly to a
  concurrent writer with `write-conflict`; CLI and SDK both reach the same owner.
- **U5:** `run generate --check` passes byte-exactly for an unchanged composite, flat and with partitions; a member
  edit without re-pin → `unverified-ref`; partition membership never follows member boundaries; maintenance
  refuses a committed composite bundle omitted from its closure. Development: a named composite development
  bundle regenerates byte-exactly beside the combined development bundle, with a distinct manifest, workflow,
  signer identity and run-attempt namespace; it cannot share or overwrite the combined layout; plain
  Definitions still refuse `githubDevelopment` with `githubBundle`; development output never satisfies a
  production expectation.
- **U6:** composition record complete and consistent; output attributed to an unlisted member, missing
  attribution, extra member or tampered effective digest → coded failure; v4 readers reject the new major;
  plain Releases keep v4 bytes; export carries attribution.
- **U7:** composite expectation matches; member expectation against a composite Release and composite expectation
  against a member Release both fail; required member set mismatch fails; a member-filtered view grants no
  capability and its description cannot restore authority.
- **Hosted:** before reconciliation may claim external exercise, one hosted Run of the NVKE composite must
  complete with 69 outputs and one root, followed by independent consumer verification under explicit policy.
  The same evidence gates U10.
- **U9 (follow-up):** the reuse decision's matrix, extended with: family output reused only for its same-name
  composite output; an origin outside the authored allowlist refused; equal Build Digest sibling refused;
  composite lane recomputed as the minimum of §7; no composite production predicate for reused outputs.
- **U10 (cutover):** before retirement, the composite's Release membership and per-output Build Digests equal
  the last combined candidate's; the combined input and bundles are retired through ADR-21's exact retirement;
  no maintained consumer, maintenance policy or qualification policy still names them.

## Consequences

**Positive**
- Family owners keep independent Definitions, Runs and Releases; the product owner gets one combined Release from
  the same declarations without a second copy.
- Exact union, preserved names and identity invariance keep every per-output guarantee intact.
- Partitioning and composition stay orthogonal: one Plan, one Run, one Release.

**Negative / costs**
- A new document kind, digest family, schema majors and a pin-maintenance command must be implemented.
- A composite Run rebuilds outputs its families already published until authenticated reuse exists.
- The cross-member producer rule can reject a composition that would be a valid single Definition.

**Neutral**
- Family Releases and the retained combined path are unchanged. Consumers that prefer to associate family
  Releases themselves still can; that association remains downstream-owned and is not a composite.

## Alternatives considered

- **Path inclusion without digests.** Rejected: silent drift, no reproducibility, substitution undetectable.
- **Composite Release referencing member Releases (parent of roots).** Rejected for this decision: it needs a
  cross-Run importer, inherits members' signer and lane differences and cannot enforce one completeness gate. Its
  useful part, reusing published outputs, belongs in authenticated reuse (§6).
- **Merging member Environments.** Rejected: credentials, signer identity and source access would be widened by
  union without an author deciding them.
- **Namespacing output names by member.** Rejected: changes membership names consumers already use and breaks
  same-name reuse; collisions are rare and better surfaced.
- **Deduplicating identical outputs across members.** Rejected: ownership becomes ambiguous and violates
  independent output uses.
- **Nested composites in v1.** Deferred: no requirement yet, and depth 1 makes cycles impossible.
- **Extending Definition v5 with a `members` field.** Rejected: Definition v5 explicitly rejects retired
  composition fields, and a distinct kind keeps older readers failing closed.

## Follow-ups

- **ADR-01:** register `nvkr.composite-definition` and the composite-bearing schema majors when implemented.
- **ADR-11 / ADR-17 / ADR-19:** on acceptance, fold §3's distinction into their rules against Recipes, selection
  groups and inheritance. ADR-17 §1 and ADR-19 §1 currently point here.
- **ADR-16:** bind the composition record into qualification and the release manifest (U8).
- **ADR-20:** record the composition binding in publication-root metadata (U6).
- **ADR-21:** enroll composite bundles in maintenance and drift checks, and admit named composite development
  bundles (U5).
- **Result reuse (draft ADR-25, backlog item 4):** once it lands, add authored cross-workflow origin admission
  for member outputs (U9). Until then composites rebuild every output.
- **NVKE cutover:** retire the combined `definition.yaml` and its bundles after a hosted composite Run and
  independent consumer verification succeed (U10).

## Decisions recorded

The questions left open by the first draft were decided on 2026-10-29:

1. Error codes: reuse `invalid-catalog`, `unverified-ref`, `engine-capability` and `timeout`; no new ADR-03 band (§11).
2. Cross-member pin divergence: rejected; the stricter rule stays (§3).
3. Cross-workflow reuse: a follow-up after draft ADR-25 lands; composites rebuild everything until then (§6, U9).
4. Combined `definition.yaml`: a separate cutover gated on a hosted composite Run and consumer verification (U10).
5. Development bundles: pulled forward into U5 (§9).

## Acceptance evidence

Contract suites only. U1–U7 are implemented with table-driven contract tests that assert coded errors;
`NVKR_TEST_EXTENDED=1` additionally compiles the worked example above against the combined Plan's resolved
catalogs and shows identical Build Digests and native build records for all 69 outputs and 73 native builds,
and identical worker partitions to the combined Definition under the same partitioned Environment. These are
local contracts. No hosted composite Run, composite publication or consumer verification exists, and no claim
follows from the family pilot's or the combined candidate's evidence.
