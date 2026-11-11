# ADR-26 — Definition composition

- **Decision status:** Proposed (design only; pending named-decider ratification)
- **Implementation maturity:** U1–U8 implemented; U9 waits on the result-reuse decision and U10
  on a hosted composite Run. `reconciliation/README.md` records the implemented scope.
- **Acceptance-test maturity:** Contract suites for U1–U8, including an opt-in extended identity-invariance
  contract over the four NVKE families. No hosted Run, composite publication or consumer verification
  exercises a composite.
- **Deciders:** Pending; release-engineering, publication, catalog and qualification owners must be named before
  ratification.
- **Index:** Proposed ADR-26 supplements ADR-01–22 and proposed ADR-23/24; it remains outside the consolidated
  contract set until accepted. It coordinates with the separately drafted authenticated result-reuse decision,
  proposed [ADR-30](0030-authenticated-result-reuse.md) (backlog item 4).
- **Relates to:** ADR-01 (canonical codec and digest families), ADR-03 (coded errors), ADR-04 §5 (consumer
  verification), ADR-09 (production provenance), ADR-11 §1–§2 (direct selection, independent output uses),
  ADR-12 (target platforms), ADR-16 §5 (evidence lanes and qualification), ADR-17 §1 and §6 (public lifecycle,
  immutable attempts), ADR-18 §8 (execution graph and partitions), ADR-19 §1–§2 (output and Release membership),
  ADR-20 §6 (pipeline-attested publication), ADR-21 (generated bundles and maintenance), ADR-22 (pins).
- **Scope:** Composing complete, independently owned Definitions into one composite Definition that compiles to
  one complete Plan and produces one Run and one Release whose membership is the exact union of its members'
  named outputs, including production and development bundles. Reuse of already-published member Releases,
  nested composites and remote members are deferred and fail closed. Hosted pipeline-attested qualification
  covers the complete composite; local qualification rehearsal and qualified-lane composites remain deferred. Replacing
  the hand-maintained combined NVKE Definition is a separately gated cutover.

## Context

At launch, the four-family NVKE pilot (#211) split the then 69-output combined candidate into core (20), services
(28), telemetry (17) and disks (4) Definitions. Each family has its own owner, Environment, generated bundle, Run and Release, and
the [partitioning strategy](../definition-partitioning-strategy.md) records why. The combined
`inputs/definitions/nvke/definition.yaml` remains a hand-maintained second copy of the families' shared
output declarations, currently 69, so every family change must be repeated there. Consumers that want one product Release must
otherwise associate four family Releases themselves.

Execution partitioning (ADR-18 §8, PR #264) already runs one complete Plan as bounded same-commit worker
workflows beneath one Run and one Release, and its development Run 40727132359 completed all 69 outputs that
way. The missing piece is an authored input that produces that combined Plan from the family Definitions without
a second copy of their declarations.

ADR-11, ADR-17 and ADR-19 reject authored Recipes, reusable selection groups, inheritance, mixins and selection
overlays. Composition must not reintroduce them: it adds no selection, matching, override or default. It only
joins complete Definitions whose declarations are each valid on their own. Component catalog producer overlays
(ADR-11 §3) are a separate, retained feature; §3 rebases them unchanged.

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
variants:                     # optional; see §3
  - component: <catalog component name>
    members: [<member name>, <member name>]
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
- `variants` is optional. It is sorted by `component`, with at most one entry per component. Each entry has
  exactly these two fields, a canonical component name and at least two members that are sorted, unique and
  listed in `members`. It is the only authored input that admits cross-member producer divergence (§3). It
  selects, overrides and defaults nothing.
- A composite carries no `catalog`, `sbomTool`, `cosignTool`, `outputs` or any other Definition field. Unknown
  fields, including unknown fields inside a variant, are rejected by strict decoding.

Path inclusion without a digest is rejected: a member edit would silently change the composite's meaning, and a
moved or substituted file could not be detected. Pure digest addressing without a locator is also rejected for
v1: NVKR has no content-addressed Definition store, and inventing one adds a discovery trust problem. The
locator finds bytes; only the digest admits them.

### 2. Admission happens before any catalog, network, subprocess or output work

`pkg/nvkr/definition` owns composite loading and admission. In order, it:

1. strictly decodes the composite (4 MiB bound, as for Definitions) and validates its own schema, including the
   shape of any declared variants;
2. admits at most **16** members and rejects duplicate names, duplicate resolved locators and duplicate digests;
3. loads each member through the existing strict Definition loader, so each member must be a complete, valid
   `nvkr.dev/definition/v5` Definition on its own, including its own Helm image bindings;
4. rejects any member whose document kind is `CompositeDefinition` (§8);
5. recomputes each member's canonical digest and requires equality with the pinned digest;
6. performs the structural merge of §3 (every rule except the cross-member producer comparison), validates
   the resulting effective Definition with the ordinary `Definition.Validate`, and requires every declared
   variant member to select its component in one of its own outputs.

Only after all six steps may factory open catalogs. The cross-member producer rule of §3 needs resolved catalogs
and overlays, so compilation (U3) checks it after opening them and before it returns any Plan, bundle or output.
Both phases are required: a failure in either returns a coded error and produces no Plan, bundle, attempt
directory or partial output. Context cancellation is checked between members and maps to `timeout`.

### 3. Merge rules: exact union, never precedence

Admission derives one **effective Definition**: an ordinary in-memory v5 Definition that the existing compiler
consumes unchanged. The derivation is a pure, deterministic function of the composite and its admitted members.

| Input | Rule | Conflict |
|---|---|---|
| Named outputs | Disjoint union. Each output declaration is copied unchanged except for catalog path rebasing below. Output names are preserved; there is no prefixing, renaming or namespacing. The union is sorted by name. | The same output name in two members is an error, even if the declarations are identical. There is no deduplication. |
| Per-output catalog | Every output receives an explicit catalog stack. An output without an override inherits its own member's top-level `catalog`. Base and overlay locators are rebased from the member's directory to the composite's directory. Member locators must be relative; absolute locators are rejected, not rebased, matching compile-input capture. | An absolute base or overlay locator, or a rebased locator outside the admitted input root, is an error. |
| Top-level `catalog` | Every member's top-level catalog stack, after rebasing, must be identical. It becomes the effective Definition's top-level stack. | Any difference is an error. |
| `sbomTool` | Every member that selects one must select the same value; it becomes the effective `sbomTool`. A member that omits it inherits that shared value, so its outputs get SBOMs inside the composite while its standalone Runs stay unchanged. | Two different selected values are an error. |
| `cosignTool` | Every member must select the same value (including omission). | Any difference is an error. |
| Helm image bindings | Copied unchanged. Each member was validated standalone (§2 step 3), so every binding already resolves inside its own member; preserved names keep it resolving to the same output. | A binding cannot reach another member's output by construction. |
| Platforms | Copied unchanged per output; aggregate `platforms` sets keep their exact membership (ADR-11 §2, ADR-12). | Unsupported platforms fail in the existing compiler and Environment admission, as for any Definition. |
| Producer pins | Catalog locks remain the only pin owner (ADR-22). One catalog file loads once per compile. For every `(component, target platform)` that outputs of **different** members select, the effective producer snapshot (after overlays and toolchain substitutions) must be identical unless the composite declares both members as variants of that component. | Divergent effective producers for the same component and platform across members are an error unless both members are listed in that component's declared variant. A declared member that selects the component in none of its outputs is an error. Within one member, existing ADR-11 semantics are unchanged. |
| Environment | Not merged. A composite Run selects exactly one Environment authored for the composite. Member Environments are not inputs and are never read. | The composite Environment is admitted against the effective Definition by the existing rules: unsupported platforms, missing execution classes and unrelated source-access entries fail as they do today. |
| Metadata | The composite's own `metadata` names the effective Definition. Member descriptions are not concatenated. | — |

The cross-member producer rule is deliberately stricter than a single Definition: two family owners releasing the
same component at different pins is exactly the silent incompatibility a combined Release must not hide. An
author who intends different producers of one component in different members declares that in the composite. A
`variants` entry names the component and the members that may diverge on it. The declaration lives in the
composite, so review sees it, and the composite digest binds it (§4). It permits divergence only between the
listed members for that component, on every platform they select. Any other divergence still fails, including
one between a declared member and an unlisted member that selects the same component. Every declared member must
select the component in at least one of its outputs, so admission refuses a misspelled or stale declaration. A
declaration does not require the producers to differ, so catalog maintenance that converges the pins does not
break the composite. Authors may still put both outputs in one member, where ADR-11 already admits the variant.

The core, services, telemetry and Ubuntu 26.04 disks families select no `(component, platform)` in common and
declare no variants. The Ubuntu 24.04 disks family replaces the same disk, agent, installer and DOCA OVS
components through its overlay, so it is a separately released sibling that the combined Definition omits. A
composite that holds both Ubuntu series either selects both in one member or lists both disk families as members
and declares each of those components as a variant of the two; the worked example below composes the other four
families.

The effective Definition is a derived internal record, like a Plan. It is never written as an authored input,
cannot be selected with `--definition` and has no file. `definition inspect` may show it as a description.

### 4. Identity: the composite has its own; member identities are recorded inside it

| Identity | Family | Binds | Excludes |
|---|---|---|---|
| Composite Definition | new `nvkr.composite-definition` | composite `metadata`, the sorted `(name, digest)` member list and the declared variants (omitted when none, so a composite without variants keeps its digest) | member locators (transport, like `DefinitionPath`) |
| Member Definition | existing `nvkr.definition` | each member exactly as today | — |
| Effective Definition | existing `nvkr.definition` | the derived v5 Definition | — |
| Plan | existing `nvkr.run-plan/v1` | as today, plus a `composition` record | — |
| Per-output Build Digest | existing `nvkr.build` | unchanged (ADR-11 §2) | output names, member attribution |
| Run, Release content, publication root | existing families | as today, plus the composition binding in their records | — |

The new family is registered in ADR-01 when implemented. A composite's Plan, Run record and publication-root
metadata carry a **composition record**: the composite digest, the effective Definition digest, every member's
`(name, digest)`, the declared variants, and for every named output the member that declared it. Plans, Run
records and publication roots that carry a composition record use new schema majors; records of plain
Definitions keep their current schemas and bytes. A record without declared variants omits the field and keeps
its bytes. A reader that does not admit the new major rejects it. A reader that admits it requires a complete,
consistent record that recomputes the composite digest: every output attributed to exactly one listed member, no
unlisted member, and declared variants that name only listed members. Run-record and publication readers stop
there; they establish the record's identity, membership and attribution and have no effective Definition. Plan
validation has the effective Definition, so it also requires the effective Definition digest to recompute, every
declared variant member to select its component, and the §3 cross-member producer comparison to hold over the
Plan's resolved producers; a saved Plan cannot drop a declaration it needs. A consumer that needs those checks
binds the Plan through an exact-input expectation (§10).

Each variant producer is attributed to the member that selected it. Every native build belongs to one named
output, the record attributes that output to one member, and the Plan records the build's resolved producers.

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

Decided: composites rebuild every output until the authenticated result-reuse decision (draft ADR-30,
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
behave as for any Definition. Hosted qualification of a composite Run (ADR-16 §5) covers the complete composite, never separate member
Runs. Its original handoff replays the captured composite and member inputs. Exact-input verification compares
the composition record and every output against authenticated publication metadata. Artifact checks, native
worker reports, qualification evidence and the production release manifest retain that complete record and
composite Definition digest. Missing, substituted or extra output attribution fails closed. Independent policies
select exact producer, qualifier and smoke workflows; family evidence is not inherited. Development qualification
uses its own source namespace and cannot issue a production manifest. Local qualification rehearsal remains
deferred for composites; `qualified` composites are not designed.

The GPU-site policy reuses fixed executable version profiles and explicitly classifies each AICR `bundle-oci`
output as `bundle-archive`. Delivery checks its exact OCI wrapper, digest, size and complete bounded gzip/tar
archive before issuing artifact checks. This rule excludes loose-tool execution for that exact output; it does
not skip signatures, provenance, evidence or membership, and makes no deployment or application-compatibility claim.
Unsupported output kinds and missing independent applicability rules refuse qualification.

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
| Dispatch-frozen composite runtime Plan | Requires ADR-18's explicit `githubRuntimeSources` allowlist and captured replay inputs; plain runtime refresh and legacy single-source composite selection remain refused. Composition, declared variants and complete output membership survive source replacement and qualification |
| `githubDevelopment: true` | Supported by U5; signed development stays `pipeline-attested` with its own signer and disposable namespaces and grants no production acceptance. A composite may own the single fixed development layout (`.github/workflows/nvkr-dev.yml`, `.github/nvkr-dev/`, publication under `ghcr.io/<owner>/<repo>/development` and the `nvkr-dev.yml` signer), exactly as a plain Definition may; one Definition owns that layout at a time. The maintained repository gives it to the GPU site composite, so the combined candidate has no development bundle, and U8 generates its independent fixed development qualification pair. U5 also admits a named development bundle for composites only: `githubBundle: <name>` with publication under `ghcr.io/<owner>/<repo>/development-<name>`, workflow `.github/workflows/nvkr-dev-<name>.yml`, its own manifest and Plan under `.github/nvkr-dev-<name>/`, and a signer identity bound to that workflow. Names that collide with the fixed development qualification files (`qualify`, `smoke`, `qualification`, and names starting with those words followed by a hyphen) or the authored registry-cleanup workflow (`cleanup`) are refused. A named development bundle requires that isolated signed publication; an unsigned named development bundle is refused with `engine-capability`, because an unsigned build has no namespace to isolate. The existing refusal of `githubDevelopment` with `githubBundle` for plain Definitions is unchanged and now applies at compilation, where the Definition kind is known |
| Hosted `run check-artifacts`, `run qualify`, qualification generation, release manifest issuance | Complete-composite pipeline-attested qualification with independently selected policy; development cannot issue a production manifest |
| Local qualification rehearsal | Deferred; `engine-capability` before work |
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
  require an exact member `(name, digest)` set. A member-set requirement does not constrain declared variants;
  a consumer that cares reads them from the same record, and a composite-digest expectation binds them.
- An expectation prepared from a member Definition does not match a composite Release, and a composite
  expectation does not match a member Release. There is no implicit acceptance across forms.
- Verifying a composite Release establishes nothing about any family Release, and verifying all four family
  Releases establishes nothing about a composite Release. They are different Runs with different signers.
- A consumer may filter the authenticated Release view to one member's outputs. That filter is a description;
  it is not a member Release and grants no member-scoped capability.
- Release description export (ADR-19 §3) includes the composition record and per-output member attribution.
- A `per-output-v1` output-local publication carries the complete composition record with its one output;
  it is not a Release. A consumer accepts a composite Release only when the record attributes exactly its
  outputs, and assembly restores that exact membership before Release issuance.

### 11. Coded errors

Decided: composition reuses existing ADR-03 codes and opens no new band. The cost is that a CI gate cannot
distinguish a composition conflict from other `invalid-catalog` failures by exit status alone; the message
and owning operation carry that detail.

| Condition | Code |
|---|---|
| Malformed composite, unknown field, empty or unsorted members, too many members, duplicate member name, locator or digest, member name mismatch, locator outside the input root, absolute member catalog or overlay locator, nested member, malformed variant (non-canonical, unsorted or repeated component; fewer than two, unsorted, repeated or unlisted members) | `invalid-catalog` |
| Duplicate output name across members, differing top-level catalog stacks or tool selectors, divergent cross-member effective producer without a declared variant covering both members, declared variant member that does not select its component | `invalid-catalog` |
| Member digest differs from the loaded member, at load, generation or `--check` | `unverified-ref` |
| Deferred use (local qualification rehearsal, member Release reuse) | `engine-capability` |
| Cancellation or deadline during admission | `timeout` |

## Worked example: the four NVKE families

This example is illustrative and is **not** added to `inputs/`. It assumes the composite would live beside the
families as `inputs/definitions/nvke/composite.yaml`. The digests are the actual `nvkr.definition` digests of the
family Definitions at commit `75f154fe`, computed with the existing `Definition.Digest` and the Go toolchain in
`.go-version`. Until `definition pin` exists, a short program against the existing API reproduces them:

```go
for _, path := range os.Args[1:] { // inputs/definitions/nvke/{core,disks,services,telemetry}.yaml
	def, err := definition.LoadDefinition(ctx, path)
	if err != nil {
		return err
	}
	digest, err := def.Digest()
	if err != nil {
		return err
	}
	fmt.Println(def.Metadata.Name, digest)
}
```

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
  - name: capi-controller-image                  # from nvke-core; explicit override copied unchanged
    platform: linux/amd64
    catalog:
      base: ../../catalogs/nvke/release-components
    format: oci
    components: [capi-controller-image]
  # ...
  - name: nvkectl                                # from nvke-core; member default made explicit
    platform: linux/amd64
    catalog:
      base: ../../catalogs/nvke/module-tags
    format: executable
    components: [nvkectl]
  # ... remaining nvke-core, nvke-disks, nvke-services and nvke-telemetry outputs, each copied unchanged;
  # every output that used its member's default catalog now names ../../catalogs/nvke/module-tags explicitly.
```

Every output now carries its own stack, so the effective top-level `catalog` no longer selects any output's
producers. It is retained because the compiler still resolves Definition-wide tools, such as the `syft` and
`cosign` selectors, from it.

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
retiring it is planned as a separate cutover (U10). The combined production and qualification paths depend
on it; the fixed development bundle now belongs to the GPU site composite. The cutover is gated on the Hosted
acceptance criterion below, followed by U10's output equivalence. The gate names no output count: a composite
may deliver a combined output under a declared rename and may add outputs its families own, so equivalence is
checked per output rather than by total. Composite qualification (U8) must exist before the combined
qualification path can retire.

## Implementation plan

Each unit is independently reviewable and lands with its contract suite. Contract tests assert the coded error,
not only failure, and are table-driven.

| Unit | Owner | Delivers | Depends on |
|---|---|---|---|
| U1 Schema and admission | `pkg/nvkr/definition`, `pkg/nvkr/canonical` | Composite type and declared variants, strict decode, `nvkr.composite-definition` family, bounded no-follow member loading, digest check, §2 order | — |
| U2 Effective Definition | `pkg/nvkr/definition` | Pure §3 merge with rebasing and conflict rules | U1 |
| U3 Compilation and Plan | `pkg/nvkr/factory` | Per-output stacks from rebased locators, cross-member producer check with declared variants, Plan composition record and schema major, compile-input capture and replay of member files | U2 |
| U4 Admission matrix and pinning | `pkg/nvkr/lifecycle`, `cmd/nvkr` | `--definition` dispatch by kind, §9 capability matrix, `definition inspect` view, `definition pin [--check]` | U1, U3 |
| U5 Generation, development and maintenance | `pkg/nvkr/factory`, `catalogmaintenance` | Named composite production bundles, flat and partitioned; named composite development bundles with independent manifest, signer and disposable namespaces; drift on member edit; maintenance-closure enrollment | U3, U4 |
| U6 Run and publication records | `pkg/nvkr/lifecycle`, `pkg/nvkr/delivery` | Run-record and publication-root schema majors with composition binding and per-output attribution; release description export | U3 |
| U7 Consumer view | `pkg/nvkr/lifecycle`, `client/v1` | Composite exact-input expectation, member-set requirement, cross-form refusal | U6 |
| U8 Hosted qualification | ADR-16/21 owners | Bind complete composition into handoff, artifact checks, native smoke, qualification evidence and release manifest; explicit per-run policies and isolated workflow pairs | U6 |
| U9 Follow-up: member Release reuse | result-reuse owner | Cross-workflow origin admission in the reuse decision, §6 and §7 | draft ADR-30 (backlog item 4) landing |
| U10 Cutover: retire combined `definition.yaml` | NVKE release owner | Move the combined production, development and qualification paths to the composite; retire the combined bundles and input | U5–U8; hosted composite Run and consumer verification |

Acceptance tests per unit:

- **U1:** valid composite admits; each `invalid-catalog` row of §11 (unknown field, empty, unsorted, 17 members,
  duplicate name/locator/digest, name mismatch, `..` escape, symlink locator, non-regular file, nested
  composite, self-reference); digest mismatch → `unverified-ref`; formatting-only member edit keeps the digest;
  pre-cancelled context → `timeout` with no member read; composite digest excludes locators (moving a member and
  updating its locator keeps the composite digest); each malformed-variant row of §11, and a declared member
  that selects its component in none of its outputs, → `invalid-catalog`; declared variants change the
  composite digest and an empty declaration does not.
- **U2:** deterministic output (shuffled member order in memory yields identical bytes); duplicate output name,
  even with identical declarations → `invalid-catalog`; differing top-level stack, selected `sbomTool` or `cosignTool`
  → `invalid-catalog`; a member that omits `sbomTool` inherits the shared one; absolute member base or overlay locator → `invalid-catalog`, matching captured
  compile-input replay; default-catalog outputs receive explicit rebased stacks; Helm bindings resolve to the same
  outputs; effective Definition passes `Definition.Validate`.
- **U3:** **identity invariance** — for the four NVKE families (extended, with pinned-source access like the
  existing split contract), every output's Build Digest and native records equal the family Plans and the
  combined Plan; divergent cross-member producer for one `(component, platform)` → `invalid-catalog` with no
  Plan; a declared pair compiles, records its variants, keeps identity invariance and attributes each producer
  only to declared members; a declaration on the wrong component or member → `invalid-catalog`; converged
  producers under a declaration still compile; a record with an added or unlisted-member variant, or a compiled
  Plan stripped of a needed declaration with a recomputed digest, fails Plan validation; within-member variants
  still compile; captured inputs include every member file and replay without the checkout; plain-Definition
  Plans are byte-identical to before.
- **U4:** each deferred row of §9 → `engine-capability` before catalog resolution, with no attempt directory;
  `definition pin --check` reports drift without writing; pin rewrite uses byte-CAS and loses cleanly to a
  concurrent writer with `write-conflict`; CLI and SDK both reach the same owner.
- **U5:** `run generate --check` passes byte-exactly for an unchanged composite, flat and with partitions; a member
  edit without re-pin → `unverified-ref`; partition membership never follows member boundaries; maintenance
  refuses a committed composite bundle omitted from its closure. Development: a composite generates the fixed
  development layout with the fixed workflow, namespace and signer, and its Plan keeps the composition record; a
  named composite development bundle regenerates byte-exactly beside the fixed development bundle, with a distinct
  manifest, workflow, signer identity and run-attempt namespace, and cannot share or overwrite the fixed layout;
  plain Definitions still refuse `githubDevelopment` with `githubBundle`; development output never satisfies a
  production expectation.
- **U6:** composition record complete and consistent; output attributed to an unlisted member, missing
  attribution, extra member or tampered effective digest → coded failure; v4 readers reject the new major;
  plain Releases keep v4 bytes; export carries attribution.
- **U7:** composite expectation matches; member expectation against a composite Release and composite expectation
  against a member Release both fail; required member set mismatch fails; a member-filtered view grants no
  capability and its description cannot restore authority.
- **Hosted:** before reconciliation may claim external exercise, one hosted Run of the NVKE composite must
  complete with every output of its members and one root, followed by independent consumer verification under
  explicit policy. The same evidence gates U10.
- **U9 (follow-up):** the reuse decision's matrix, extended with: family output reused only for its same-name
  composite output; an origin outside the authored allowlist refused; equal Build Digest sibling refused;
  composite lane recomputed as the minimum of §7; no composite production predicate for reused outputs.
- **U10 (cutover):** before retirement, every output of the last combined candidate is present in the composite
  Release under the same name or under a rename the cutover declares, with an equivalent selection and an equal
  per-output Build Digest. The mapping is one-to-one: each combined output maps to a distinct composite output, so
  two combined outputs with equal Build Digests cannot both be satisfied by one, and no composite output satisfies
  more than one rename. The combined `nico-reference-profiles-fixture` is delivered as the `nvke-core` member's
  `controller-profiles` and is recorded as that declared rename, not as a missing output. Every composite output
  beyond the combined candidate is listed explicitly as a family-only output and attributed to its member through
  the Release's composition record. The combined input and bundles are retired through ADR-21's exact retirement;
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
- The cross-member producer rule can reject a composition that would be a valid single Definition until its
  author declares the variant in the composite.

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
- **Result reuse (draft ADR-30, backlog item 4):** once it lands, add authored cross-workflow origin admission
  for member outputs (U9). Until then composites rebuild every output.
- **NVKE cutover:** retire the combined `definition.yaml` and its bundles after a hosted composite Run and
  independent consumer verification succeed (U10).

## Decisions recorded

The questions left open by the first draft were decided on 2026-10-29:

1. Error codes: reuse `invalid-catalog`, `unverified-ref`, `engine-capability` and `timeout`; no new ADR-03 band (§11).
2. Cross-member pin divergence: rejected unless the composite declares a variant naming the component and both
   members (§1, §3); every undeclared divergence fails.
3. Cross-workflow reuse: a follow-up after draft ADR-30 lands; composites rebuild everything until then (§6, U9).
4. Combined `definition.yaml`: a separate cutover gated on a hosted composite Run and consumer verification (U10).
5. Development bundles: pulled forward into U5 (§9). On 2026-11-04 the fixed development layout was opened to
   composites so the GPU site composite can use the already registered `nvkr-dev.yml` workflow (§9).

## Acceptance evidence

Contract suites only. U1–U7 are implemented with table-driven contract tests that assert coded errors. Declared
variants are contract-tested at admission (U1), compilation and Plan validation (U3), in the inspect view (U4),
in prepared Run records and publication metadata (U6), and in the verified consumer view (U7). The opt-in
extended contract also compiles the 26.04 and 24.04 disk families as separate members: refused without
declarations and admitted with the eight components the 24.04 overlay replaces declared.
`NVKR_TEST_EXTENDED=1` additionally compiles the worked example above against the combined Plan's resolved
catalogs and shows that every composite output and native build keeps the Build Digest and native build records
of the combined Definition and the four families, including family-only outputs the combined Definition lacks,
and that its worker partitions equal those of its effective Definition compiled plainly under the same
partitioned Environment. These are local contracts. No hosted composite Run, composite publication or consumer verification
has been exercised or recorded, and no claim follows from the family pilot's or the combined candidate's evidence.
