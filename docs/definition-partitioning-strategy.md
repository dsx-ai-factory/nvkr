# Definition partitioning and related Releases

The four-family NVKE pilot implements separate authored Definitions and named production bundles alongside
the combined candidate. It reuses the current lifecycle and catalog owners; it adds no cross-Release import,
automatic association file or qualification capability. Hosted family execution and consumer acceptance remain
unrecorded. The [input instructions](../inputs/definitions/nvke/README.md#four-family-pilot) own commands and
current selections; [reconciliation](adr/reconciliation.md#maintained-input-and-schema-boundaries) owns
implementation and acceptance status.

The pilot incorporates upstream `07181055` (2026-10-20), including `9857feed`'s current catalog pins,
qualification metadata sessions and budgets, development retry support, and provisional CLI validator
catalog, plus subsequent bootstrap-interface documentation and bounded formatting commands.
[ADR-24](adr/0024-imported-validator-catalog.md) now records strict discovery/loading and local-plan profile
dispatch as implemented; broader migration, published adapters and actual upstream/native-platform catalog
acceptance remain deferred. Historical tables retain their stated source revisions; they do not describe
current generated bundles.
This document changes no ADR contract or organizational acceptance decision.

## Implemented pilot and retained path

| Definition | Outputs | Native targets | Required family |
| --- | ---: | ---: | --- |
| [Core](../inputs/definitions/nvke/core.yaml) | 20 | 24 | CLI/operator tools, controller with its bound chart, providers/reference fixture, CAPI and cert-manager bootstrap images |
| [Services](../inputs/definitions/nvke/services.yaml) | 28 | 28 | SPIRE, PostgreSQL and Temporal charts and native runtime images |
| [Telemetry](../inputs/definitions/nvke/telemetry.yaml) | 17 | 17 | NKTP chart and native runtime images |
| [Disks](../inputs/definitions/nvke/disks.yaml) | 4 | 4 | AMD64/ARM64 plain and public-bootstrap FDE disks with complete material closures |
| Total | 69 | 73 | The combined candidate's complete named output set |

Each family selects `inputs/environments/github-nvke-<family>.yaml`, with
`githubBundle: nvke-<family>`. Generation owns `.github/workflows/nvkr-run-nvke-<family>.yml`
and `.github/nvkr-run-nvke-<family>/`. Explicit signer policy matches that workflow on `main`;
publication uses `ghcr.io/dsx-ai-factory/nvkr/m2-candidate/<family>`. The resulting
`<prefix>/releases@sha256:…` identifies only that family's Release.

The family Environments preserve the combined candidate's independent production registry writer.
The exact disks publication prefix also retains preparation of the existing private M2 toolchain pins
using the fixed registry reader, before source or producer execution. Namespace lookalikes, child prefixes,
other families and foreign toolchain owners receive no exception. These are credential projection
contracts, not evidence of provisioned package access or hosted disk execution.

The combined [Definition](../inputs/definitions/nvke/definition.yaml), production and development bundles,
and their qualification paths remain available. The families are a reversible pilot, not a recursive
replacement for the combined Release. Existing consumer acceptance of the combined publication cannot be
transferred to four independently produced roots.

The family-only generation/check targets are `make nvke-split-generate` and
`make nvke-split-check`; repository-wide workflow targets include them. These checks establish generation
and drift contracts, not artifact production.

### Current local validation

The integration of `9857feeda03d2e7f529e1baef2b4a331a011e54c` reproduced drift in all four family bundles
before regeneration. `make nvke-split-generate` refreshed eleven generated JSON files through the existing
owner. Family Environments, entry workflows and action templates are unchanged; the four Plans now contain
the updated catalog and generator snapshots. Independent inspection confirms the exact disjoint union of
73 native build records and 69 output records still matches the combined Plan. The extended exact regeneration
contract passed on 2026-10-20 in 4.05 seconds; the complete focused family/credential matrix passed in
32.747 seconds, including the actual disks-family check in 0.81 seconds. This run records Go 1.27.2 on
Darwin ARM64 and staged tree `e9187e1e6ce5e5fda6d26e04b912d10622f559c0`. It uses validated catalog
snapshots; it does not independently authenticate upstream sources or reproduce artifact bytes.

| Family | Outputs | Native targets | Factory nodes | Jobs | Workflow bytes | Plan bytes | Expanded YAML events |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Core | 20 | 24 | 210 | 198 | 130,455 | 659,186 | 15,247 |
| Services | 28 | 28 | 258 | 233 | 151,551 | 542,725 | 18,272 |
| Telemetry | 17 | 17 | 160 | 153 | 102,369 | 490,343 | 11,713 |
| Disks | 4 | 4 | 106 | 55 | 62,075 | 500,953 | 5,301 |
| Total | 69 | 73 | 734 | 639 | 446,450 | 2,193,207 | 50,533 |

The updated combined Plan has 716 nodes and 2,155,133 bytes. Its unchanged workflow has 580 jobs,
394,695 bytes and 47,055 expanded YAML events. Each workflow's limits apply separately. These are local
size and generation contracts, not hosted artifact acceptance.

The full local gate completed successfully at 21:54:35 UTC on 2026-10-20 on staged tree
`2ff2a400055ae9cfe429b5c0bcfeb55bfaaedb41`, with Go 1.27.2 on Darwin ARM64 and golangci-lint 2.14.0.
It ran `make check GO_TEST_FLAGS='-count=1 -json -p=1'`, `make build` and both staged/unstaged
`git diff --check` checks. All 58 default Go package suites, static/shell checks and 19 Python tests passed;
the vulnerability scan found none. Delivery took 544.323 seconds, factory 462.554 seconds and lifecycle
363.894 seconds. Extended fixtures were disabled for this gate, with the original 20-minute package
timeout and assertions unchanged; the separate focused run above covered the relevant extended contracts.
After this gate, upstream documentation and formatting-only commit `07181055` were integrated. Go sources,
tests, authored inputs and generated bundles are unchanged from the tested tree. The final formatting
commands passed the large-inventory, spaced-filename, read-only and failure fixtures plus `make fmt-check`;
the full suite was not repeated for those formatting/documentation changes.
All-owner `make workflows-check`, actionlint, relaxed yamllint,
targeted metadata/retry lifetime race checks and formatting failure/read-only fixtures also passed.
New-head hosted CI remains pending. Native Linux signing/pipeline race and pinned-cosign interoperability
were not rerun locally; the raw local logs remain unpublished.

The maintained closure inventory now checks all committed producer bundles and both qualification pairs
offline, including exact policy selections, member bytes/modes and namespace ownership. Private source
availability cannot skip that inventory; authenticated recompilation remains the separate workflow check.
The existing combined/development/nightly credential matrix declares each row's expected disk count, with
the development exemption explicit. The separate disks-family regression remains in place.
PinSet parity contracts cover registry, resolved-catalog and snapshot admission for declaration edge cases,
including the exact 1,024-member limit and NVKE accounted-member bound. The diagnostic uses the shared limit
constant. These changes add regression coverage without expanding admission or credential authority.

The integrated upstream [formatting check](https://github.com/dsx-ai-factory/nvkr/actions/runs/39494503833/job/118876005825)
failed before starting `gofmt`: the repeated filename list made its shell command 131,872 bytes, exceeding
Linux's per-argument limit. Formatting targets adopt upstream `07181055`'s recursive `gofmt` invocation on
the four module source directories, and `fmt-check` preserves formatter errors while reporting captured
unformatted paths once. The format, check and tidy targets share this bounded invocation pattern;
generated inputs and outputs are unaffected.

Combined qualification retains same-Run authentication with the upstream 59-minute operation,
60-minute step and 90-minute job budgets. Development retry reuse retains the original Run and publication
namespace. Named-family qualification, hosted family execution, independent consumer acceptance and Plan
snapshot deduplication remain deferred.

### Historical validation at 250f842c

The following measurements and local results apply to the `c01c9132` integration committed as
`250f842cb448347e2f6b1e9f2f373eb98e41e6e5`, not the current integration. Its exact-head
[hosted Merge Gate](https://github.com/dsx-ai-factory/nvkr/actions/runs/39342927832) passed on 2026-10-19
at 23:43:57 UTC. Local raw logs remain unpublished and are not independently retrievable evidence.
They record commands and results but no complete tested working-tree hash or direct toolchain dump;
they cannot independently bind the measured executions to the final commit.

After incorporating upstream `c01c9132` and retaining the review hardening, the extended split contracts passed
on 2026-10-19 in 4.25 seconds: all 73 complete native build records and 69 named output-binding records
matched the combined Plan byte-for-byte, including catalogs, producer graphs,
material selections, evidence and aggregate child bindings. Each generated family bundle matched exact
regeneration and retained independent file ownership. This comparison uses the validated combined Plan's
catalog snapshots; it does not independently fetch upstream sources or prove reproduced artifact bytes.
[Split identity and generation contract](../pkg/nvkr/factory/nvke_definition_split_test.go)

| Family | Outputs | Native targets | Factory nodes | Jobs | Workflow bytes | Plan bytes | Expanded YAML events |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Core | 20 | 24 | 210 | 198 | 130,455 | 651,282 | 15,247 |
| Services | 28 | 28 | 258 | 233 | 151,551 | 548,972 | 18,272 |
| Telemetry | 17 | 17 | 160 | 153 | 102,369 | 486,661 | 11,713 |
| Disks | 4 | 4 | 106 | 55 | 62,075 | 501,720 | 5,301 |
| Total | 69 | 73 | 734 | 639 | 446,450 | 2,188,635 | 50,533 |

Measured on 2026-10-19 with that revision’s generator, the retained combined Plan has 716 nodes and its
workflow has 580 jobs, 394,695 bytes and 47,055 expanded YAML events; the Plan is 2,153,454 bytes. The split
reduces the largest workflow to 233 jobs, 151,551 bytes and 18,272 events, while total jobs rise by 59
(10.17%) and total workflow bytes rise to 446,450. These measurements show reduced per-workflow breadth and parser demand,
with higher aggregate job and byte counts.

Each workflow's limits apply separately; the total event count is not one parsed workflow. Every helper
bootstrap now enforces the authored dispatch policy before checkout, helper compilation or private CI
storage upload, including helpers without catalog credentials. The validator rejects missing or relaxed
guards. Negative contracts cover unadmitted feature branches, tags and push events for build-only,
publication and named-runtime helpers. These generation contracts establish no hosted execution.
[Bootstrap dispatch contracts](../pkg/nvkr/factory/github_bootstrap_dispatch_test.go)

Actionlint and relaxed yamllint passed for the changed merge-gate workflow. The `c01c9132` integration changed no
product Go source, authored family input, selected catalog or generated bundle. Upstream consolidates
redundant default-test fixtures and discovers all Go packages and factory/lifecycle tests for eleven CI
shards, retaining exact terminal-result accounting and the aggregate gate. The merge preserves the actual
disks-family credential regression with four disk producers, sharing the upstream assertion helper;
combined and nightly coverage reuses its existing render matrix. Exact namespace and nondisk refusals remain.
Hosted jobs now have five-minute budgets; upstream run timings do not establish this branch's timing.

The retained production qualifier can issue a manifest for its existing publication root
using the same Run's live authenticated receipts and original deadline; it does not compose family Releases.
Development remains qualification-only. Family-generated bytes and the six selected catalogs are unchanged.
Local native-tool support grants no hosted family capability, and named family qualification remains unsupported.

`make workflows-check` passed for all family, combined, development, qualification, nightly and catalog
refresh owners after the preceding `7f67ce63` integration, reauthenticating source pins and confirming exact
generated bytes. Its generator, authored inputs and generated files are unchanged by `c01c9132`; that
all-owner result remained applicable to `250f842c`, and the focused split regeneration passed again.

The immutable [02155409-to-250f842c comparison](https://github.com/dsx-ai-factory/nvkr/compare/02155409352955b3ce69d381fcb0da399700f3b7..250f842cb448347e2f6b1e9f2f373eb98e41e6e5)
changes test/CI orchestration and prose. The following two checks return no path/mode/blob changes for
production source/resources, authored inputs and generated owners; tests and the authored merge gate are
explicitly outside those equality claims. The Makefile generation section is unchanged, although its test
configuration changed. This commit comparison does not recover the missing local execution provenance.

```sh
git diff --exit-code 02155409352955b3ce69d381fcb0da399700f3b7 250f842cb448347e2f6b1e9f2f373eb98e41e6e5 -- \
  client cmd internal pkg toolchains .go-version go.mod go.sum inputs docs/examples ':(exclude)**/*_test.go'
git diff --exit-code 02155409352955b3ce69d381fcb0da399700f3b7 250f842cb448347e2f6b1e9f2f373eb98e41e6e5 -- \
  '.github/nvkr-*' '.github/workflows/nvkr-*.yml' .github/workflows/catalog-refresh.yaml
```

The full uncached `make check GO_TEST_FLAGS='-count=1 -json -p=1'`, `make build` and diff checks passed
on 2026-10-19 at 23:32 UTC. All 58 Go package suites passed, including the new CI-contract package, along
with formatting, license, vet, lint, shell and all 19 Python CI-tool tests; the vulnerability scan found no
vulnerabilities. Packages ran sequentially, with delivery, factory and lifecycle completing in 647.945,
495.824 and 420.533 seconds. No assertions or the existing
20-minute package timeout were weakened. An earlier concurrent gate before `2f1792fc` exhausted lifecycle's
cumulative budget during fixture setup; its complete standalone rerun passed in 871.952 seconds, followed
by a successful full serialized gate. Those historical results do not establish the timeout's cause.
Before this test/CI integration, targeted qualification and release-manifest process-lifetime race tests
passed in 9.930 and 2.157 seconds against the unchanged production implementations.
This local integration validation did not rerun Linux signing race or pinned-cosign interoperability tests.
The prior head `02155409` passed the
[hosted Merge Gate](https://github.com/dsx-ai-factory/nvkr/actions/runs/39336395335) at 23:04:11 UTC; that hosted evidence
predates this upstream integration and does not establish the new merged head's CI result.

Focused source-access contracts pass for acquired-only root/output pin sets, mixed admitted/unrelated
repository refusal, ordinary unused sources, unchanged local-engine admission and snapshot substitution.
A local Git fixture now joins real pinned-tree loading to rendering: moving an upstream tag does not change
the exact locked metadata, while malformed metadata, stale versions and stale provenance return no Plan
or bundle. Catalog revalidation receives the exact repository-scoped helper and mirror only on its admitted
steps. These fixtures do not contact private servers or establish hosted source access.
[Source-access contracts](../pkg/nvkr/factory/source_access_test.go),
[real loader composition](../pkg/nvkr/factory/source_access_loader_test.go).

Resolved catalogs now share the catalog owner's intrinsic PinSet declaration checks. Malformed paths and
unsupported formats first reproduced acceptance in low-level compilation and rehashed root/output Plan
validation, then were rejected before Plan or bundle emission. Valid catalogs and explicit producer overlays
remain supported. Snapshot digests and these semantic checks do not authenticate upstream contents;
authored loading still owns reciprocal membership and exact pinned-tree derivation.
[PinSet boundary contracts](../pkg/nvkr/factory/pin_set_semantics_test.go).

The maintained credential matrix checks combined/core/services catalog credentials and their absence in
telemetry/disks, including raw-secret aliases, exact repository/helper/mirror configuration and legitimate
source-fetch scope. All three nondisk families reject the private-toolchain namespace exception and emit no
preparation or toolchain-reader inputs. The default suite compares every family's authored Definition and
Environment with its committed Plan; full compilation and bundle comparison remain extended.
The focused family matrix passed in 34.227 seconds, including the identity comparison above and the actual
disks-family credential regression in 0.83 seconds. These are local
contracts, not hosted family execution, registry access, publication, qualification or consumer acceptance.

## Why independent artifact families

Use independently useful deliverables and their byte-producing dependencies as the primary boundary.
Directory placement helps authors find related inputs; it grants no execution or Release authority.
[ADR-19](adr/0019-clusterdefinition-and-provisioning-adapter-boundary.md#1-definition-owns-required-outputs)
already permits separate Definitions, and
[ADR-11](adr/0011-composition-and-resolution-semantics.md#1-exact-producer-selection) retains shared producer
catalogs without another authored Recipe, reusable selection group or inheritance layer.

| Boundary | Benefit | Limitation |
| --- | --- | --- |
| Artifact format | Simple inventory and runner planning | Leaves 56 OCI outputs together and separates bound charts from their images |
| Catalog or source repository | Familiar pin ownership | Existing catalogs span artifact families; one repository may intentionally have several versioned source roles |
| Native architecture | Separate native retry and capacity domains | Splits aggregate images into different artifacts; shared charts/tools can be duplicated |
| Disk versus everything else | Isolates KVM, disk evidence and large payloads | Historical measurement still left 516 jobs in the other workflow |
| Four artifact families | Preserves dependency closures with fewer independent roots than seven families | Adds setup, publication and downstream association work |
| Execution-only sharding of one Definition | Could preserve one required set and consumer entry point | Needs its own scheduling/result-join contract; independent Definitions do not implement it |

The four families consolidate a finer seven-family alternative: tools, controller/providers and bootstrap
images become core; identity and data/workflow services become services; telemetry and disks remain separate.
More families are a later option when ownership or update cadence justifies them. Splitting disks by native
architecture is similarly an operational choice, not a prerequisite.

### Boundaries retained in each Definition

- The controller chart remains with `cwe`; its image slot resolves inside the same Definition.
  Cross-Release chart binding is not an admitted input shape.
- Every disk retains its one root and complete selected transitive material closure. Another Run cannot
  implicitly provide a missing material.
- Each aggregate image retains every declared native child, producer result and evidence slot.
- Every named output still requires its own evidence join. Equal Build Digests do not permit sibling or
  cross-Run reuse.
- Keeping NKTP charts and images in one family does not rewrite upstream image references or establish
  offline installation. The reference-profile image retains its fixture status.

These constraints are owned by
[Definition admission](../pkg/nvkr/definition/model.go),
[disk closure contracts](../pkg/nvkr/buildgraph/selection_test.go),
[ADR-12](adr/0012-criteria-model-and-aicr-activation.md#1-concrete-target-platforms), and the
[candidate's source and deployment limits](../inputs/definitions/nvke/README.md#additional-release-members-and-their-source-contracts).

## Shared catalog and workflow ownership

The pilot adds files under `inputs/definitions/nvke/` without moving or copying catalogs.
[Catalog ownership](../inputs/catalogs/README.md) maps the shared producers and deliberate variants.
The combined selection uses `nvke/module-tags`, `nvke/release`, `nvke/release-components`,
`tools/operator-amd64`, `runtime-dependencies` and `node-images/release` plus its native toolchain overlay.
Filesystem colocation is not Definition inclusion or Release membership.

Catalog consolidation remains separate work. Source roles may intentionally differ in version, preparation
commands or trust assumptions; moving or splitting a catalog also requires reviewing predecessor-bound
overlays. The temporary OS SPIRE packaging-source exception and incomplete DSX IMDS requirement remain
unchanged by selecting separate output families.

The [catalog-maintenance policy](../inputs/policies/nvke-catalog-maintenance.yaml) lists all four family
pairs in `additionalRuns`, alongside its existing dependent bundles. One policy checks and regenerates
the complete explicit bundle closure when shared pins change. Maintenance rejects committed bundles omitted
from that closure; it does not infer consumers from directory names. Its `sameRelease` groups coordinate
selected source roles inside the catalog transaction, not cross-Release compatibility.
[Maintenance contract](adr/0021-generated-pipeline-maintenance-and-lifecycle.md#3-churn-automation-bump--regenerate--pr-gated-by-drift)

Named production bundles own separate manifests, locks, Plan paths and retirement. The generator does not
rewrite signing identity to match a bundle name. Family Environments therefore select their own signer and
publication namespace and retain only admitted source-access/disk settings.
[Namespace contract](adr/0021-generated-pipeline-maintenance-and-lifecycle.md#1-the-emitted-pipeline-is-a-generated-artifact-not-an-owned-scaffold)

Services has no producer Git fetch, but its acquired charts still require private pin-set revalidation.
Its source credential remains admitted for that exact catalog read and scoped to the existing
bootstrap/finalization recompilation steps. Unrelated source-access entries remain errors; acquisition,
build and publication steps do not receive the revalidation credential.
[Source-access contract](adr/0020-artifact-publication-and-target-configuration.md#2-target-configuration-surface)

Production and development qualification paths exist for the retained combined candidate. They do not
qualify independently named family producers. Named producer qualification remains unsupported, as does
`githubDevelopment: true` with a nonempty `githubBundle`. The pilot preserves those admission limits.
A downstream coordinator may dispatch independent named workflows, but exact Run correlation, aggregate
success decisions and deployment scheduling remain its responsibility.

## Historical capacity investigation

The following measurements belong only to research baseline `258ab29bdfa0a6f65de9b761272f186293c6aa0e`.
The current input relocation, generator changes and pilot names change exact workflow and Plan bytes.
Do not use these values as current provider admission or current gate evidence.

That baseline selected 69 outputs and 73 native targets, producing 716 factory nodes, 1,006 planned
artifacts and 562 jobs. Its generated graph had 2,519 edges, 561 direct finalizer dependencies and
16 dependency levels. These dimensions were measurements, not published provider quotas.

| Boundary | Research-baseline measurement or implementation bound | Effect of separate Definitions |
| --- | --- | --- |
| Workflow bytes | 390,337 of the then-enforced 500,000 bytes | Reduces the largest file; total bytes can increase |
| Expanded YAML events | 42,651 against the inspected runner parser's 50,000-event bound | Reduces each parsed workflow; textual aliases do not reduce expanded structure |
| Provider graph | 562 jobs and 561 direct finalizer dependencies | Reduces each Run's graph and finalization join |
| Canonical Plan | 2,085,428 bytes against the 4 MiB reader bound | Reduces each Plan |
| Job descriptor and inline script | Largest descriptor 40,616 of 200,000 bytes; script bound 21,000 bytes | Helps aggregate data; one oversized operation still fails |
| Publication closure | 4,096 delivery objects and 512 MiB metadata/evidence per root; live usage not measured | Independent roots reduce aggregate demand; a flat parent can recreate it |
| Individual payload, scanner and runtime capacity | Separately admitted per-artifact and native resource limits | Does not shrink one image/disk/SBOM or create runner capacity |

Current code owns current admission:
[workflow size](../pkg/nvkr/factory/github_compact.go),
[Plan reader](../pkg/nvkr/factory/execute.go),
[job descriptor](../pkg/nvkr/factory/github_action_data.go),
[publication objects](../pkg/nvkr/delivery/layout.go), and
[metadata bytes](../pkg/nvkr/delivery/metadata.go).
The research used the
[pinned runner parser](https://github.com/actions/runner/blob/796cc18ffaaafd5cea525d6231f5a8c93c11c603/src/Sdk/WorkflowParser/Conversion/YamlObjectReader.cs#L733).
GitHub's documented 256-job limit applies to matrix expansion, not a claimed universal cap on explicitly
named jobs. [GitHub Actions limits](https://docs.github.com/en/actions/reference/limits)

### Historical partition measurements

| Experimental family | Outputs | Jobs | Workflow bytes | Canonical Plan bytes | Expanded YAML events |
| --- | ---: | ---: | ---: | ---: | ---: |
| Core | 20 | 171 | 127,312 | 633,788 | 12,902 |
| Services | 28 | 232 | 151,018 | 526,387 | 17,063 |
| Telemetry | 17 | 132 | 99,720 | 471,885 | 10,046 |
| Disks | 4 | 55 | 50,973 | 489,132 | 4,651 |
| Total | 69 | 590 | 429,023 | 2,121,192 | 44,662 |

The temporary names were `nvke-platform`, `nvke-services`, `nvke-telemetry` and `nvke-nodes`.
The largest workflow fell from 562 to 232 jobs and from 390,337 to 151,018 bytes; total jobs increased
by 5.0% and total bytes by 9.9%. The seven-family alternative totaled 618 jobs and 468,141 bytes,
with maxima of 152 jobs and 101,942 bytes. A disk/remainder split left 516 jobs and 350,381 bytes
in the ordinary-output workflow. The 232-job maximum was incidental, not an invented provider quota.

A temporary Go 1.27.2 harness cloned complete output objects from the baseline Plan, tailored Environments,
and called the existing compiler and renderer. All 73 native and 69 output Build Digests matched across
the four- and seven-family experiments. That proves the measured logical identity comparison, not
reproduced bytes, credential access, hosted execution or combined Release acceptance. The experiment
changed no retained authored input or generated file.

Parallel generator experiments at
`af14028f0619fac6d031d4cb34aa46bf5e8bc2dc` and
`d9de101c07408d638c19d0bb933652b9e9e0b7ed` addressed dependency fan-in and private carrier transport.
Their immutable
[graph evidence](https://github.com/dsx-ai-factory/nvkr/blob/af14028f0619fac6d031d4cb34aa46bf5e8bc2dc/docs/adr/reconciliation.md) and
[transport evidence](https://github.com/dsx-ai-factory/nvkr/blob/d9de101c07408d638c19d0bb933652b9e9e0b7ed/docs/adr/reconciliation.md)
retain those revisions' limits. They are historical evidence, not separate unmerged work assumed absent
from current main. Partitioning complements generator improvements; it does not replace artifact privacy
or exact-result accounting. More families can increase registry requests, storage and concurrency.

## Related Releases: association before composition


There are three distinct relationships. Directory placement answers where authors find related inputs;
a selection record says which exact Releases a consumer chose together; authenticated composition would
make required child membership part of an NVKR Release. None alone proves runtime compatibility.

| Option | Current status | What a consumer obtains |
| --- | --- | --- |
| Independent Releases plus downstream selection | Existing individual verification APIs; the selection convention is proposed | Exact family references, individually accepted under independently selected policies |
| Signed association document | Proposed as a downstream convention or later native capability | An authenticated statement that exact Releases belong together; children still require verification |
| Parent Release with required children or imported outputs | New lifecycle/authority contract required | One authenticated required membership boundary, with original child origins and evidence preserved |

The initial association can be small. This is a conceptual downstream document, **not accepted NVKR input
or a proposed wire schema**; the digest placeholders are deliberately invalid:

```yaml
# Illustration only. This document grants no NVKR authority.
product: nvke-candidate
selectedReleases:
  core: registry.example/nvke/core/releases@sha256:<core-digest>
  services: registry.example/nvke/services/releases@sha256:<services-digest>
  telemetry: registry.example/nvke/telemetry/releases@sha256:<telemetry-digest>
  disks: registry.example/nvke/disks/releases@sha256:<disks-digest>
```

For a complete product selection, freeze every required family and its exact publication reference. Pin the selection record itself through reviewed immutable source or an explicitly authenticated
document. Trust policies come from the consumer, not an untrusted child or a policy URL in this record.
The consumer must reject missing/failed required families and retain the exact selection used for testing.
A new telemetry Release produces a new selection; it does not mutate the old product combination. No
transaction across registry repositories or native combined acceptance receipt is implied.

Existing `authenticity` verification checks metadata/evidence membership, signatures, production provenance
and required SBOM bundles under policy, without reading payload layers or granting payload export/exact-input
authority. `artifact-publication` additionally verifies the complete held payload closure. Browsing/export
and diagnostic pulling do not replace policy verification. Neither scope grants full-release/qualified
acceptance or proves that independently accepted Releases work together.
[consumer scopes](adr/reconciliation.md#consumer-acceptance-scopes),
[publication architecture](publication-architecture.md#signing-and-publication-order),
[downstream ownership](adr/0019-clusterdefinition-and-provisioning-adapter-boundary.md#3-downstream-systems-own-deployment)

The current assembler cannot implement the parent option by accepting more references. Output-local roots
are internal handoffs bound to one Definition, Environment and Plan, and one GitHub run. Ordered earlier
attempts are allowed; another run/workflow or changed Plan is rejected. The metadata schema has one input
identity triple. Preserve these checks. A new importer needs its own authenticated admission path.
[assembly contract](adr/0020-artifact-publication-and-target-configuration.md#6-pipeline-attested-publication),
[assembly implementation](../pkg/nvkr/factory/reference_release.go),
[negative contracts](../pkg/nvkr/factory/reference_release_test.go),
[metadata model](../pkg/nvkr/delivery/release_metadata.go)

If one native entry point becomes necessary, extend the existing Definition → Run → Release lifecycle
explicitly rather than adding an authored Recipe or misusing the diagnostic `ReleaseSet`. Freeze exact
required child Releases/outputs before work, authenticate them under independent consumer policies, and
issue package-owned receipts bound to child publication, original output, platform, artifact/evidence
subjects, origin and policy. Preserve the origin Run rather than inventing a new build claim. A signed
parent does not upgrade a child's lane, prove compatibility, or authorize reuse of sibling evidence.
The current `ReleaseSet` represents component/artifact inventory for deferred promotion, not a collection
of pipeline Releases. [ReleaseSet owner](../pkg/nvkr/release/release.go),
[release gates](adr/0016-release-gates-and-digest-first-selection.md),
[public lifecycle](adr/0017-cli-sdk-foundation-and-package-topology.md)

Bound child count, nesting depth, total leaf/evidence bytes and duplicate output names; reject cycles and
ambiguous membership before output work. Flattening every child into today's root recreates its aggregate
limits. Start with one explicit association level if this capability is pursued. Existing acquired-image
or chart producers can supply pinned inputs to downstream byte-changing builds: acquisition authenticates
the pinned acquired bytes, not general upstream Release authority or inherited original-build evidence.
[acquisition contract](adr/0022-catalog-pinning-and-the-lock-contract.md),
[current publication limits](../pkg/nvkr/delivery/layout.go)

OCI can carry an association as an ordinary artifact, but transport does not define trust: its `subject`
field establishes a weak discovery association. Current NVKR Definition formats have no generic metadata
artifact route; `oci` selects an image producer. A legitimate image or chart could contain the selection
document as ordinary payload, but that is an unexercised design possibility and verification would cover
only that carrier, not recursively validate its embedded references.
[OCI manifest specification](https://github.com/opencontainers/image-spec/blob/main/manifest.md),
[Definition format admission](../pkg/nvkr/definition/model.go)

Any accepted native design must update ADR-17/19/20 and lane/policy contracts in ADR-04/16, reconciliation,
SADD and domain vocabulary. Required negative contracts include missing/extra/duplicate children,
substituted outputs, unauthorized signers, policy weakening, unsupported lanes, evidence reuse, traversal
bounds, cancellation and revoked receipts. Existing same-run assembler negatives remain. Named qualification
must bind the actual selected children and exact producer attempts; signatures on those children do not
satisfy the current single-Run M3 qualification contract.

## Cutover work and acceptance

The authored four-family pilot and maintained generation/maintenance enrollment are the first step.
Before replacing the combined path:

1. Check exact union membership, native platform sets and per-output Build identities against the combined
   candidate. Recompute current workflow, Plan and parser-event dimensions after generator changes.
2. Execute fresh family Runs and independently verify every selected publication under consumer policy.
   Keep exact Release refs and producing attempts; successful jobs alone do not identify a product selection.
3. Establish the downstream association and test missing/failed families and substituted references.
   Runtime compatibility and complete installation remain downstream acceptance.
4. Design named qualification with explicit producer identities, exact membership and original attempts;
   settle development behavior before retiring the combined development/qualification paths.
5. Retire the combined bundle only after consumer migration and recorded acceptance. Any catalog
   consolidation remains a separate change with all consumers and predecessor bindings checked.

A native parent Release would additionally require the authority/schema changes above and updated owning
ADRs, SADD, vocabulary and reconciliation. The pilot uses existing contracts and does not implement that design.

## Historical assessment validation

At the research baseline, local four/seven-family compile/render experiments and seven focused capacity tests
passed. No producer build, publication, deployment or provider dispatch was performed for that assessment.
Formatting, license, vet, lint, shell/Python checks and a separately run vulnerability scan passed there.
The complete uncached gate did not pass: delivery, factory and lifecycle hit package timeouts, and factory's
CLI artifact-test version-alias admission fixture failed an expected coded-error assertion, reproduced in
isolation. Those results belong to the old baseline only; they neither validate this pilot nor establish
that the current branch retains those failures. Current repository checks and any external evidence must
be reported at their own exact revision.
