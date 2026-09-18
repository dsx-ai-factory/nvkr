# Output-only build and release model

Status: implemented and verified locally, 2026-09-17. This follows Recipe removal and removes the remaining
deployment-description layer from NKD. The user authorized this simplification after reviewing the current
cluster-view and role behavior. No deployment compatibility or provisioning capability is introduced.

## Intended model

A Definition declares the exact named artifacts to produce, their component/profile selections, target
platforms, formats, and actual packaging relationships. Environment supplies execution and publication
configuration. A Run freezes those required outputs and records exact observations; a Release authenticates
their membership and evidence. Different deliverable sets can use different Definitions.

Remove Definition cluster views, tenant/operator types, node roles and role affinity, the node/cluster category
taxonomy, cluster context/CNI declarations, dormant cluster validation requirements, generated composition and
realization records, and the unused provisioning interface. Do not replace them with a renamed grouping object.
Remove deployment-role fields in release inventory/evidence records and disk annotations. Remove the unused
role-based staging manifest. Keep signer/receipt authorization roles and their exact trust-policy checks.

Deleting cluster code must preserve the generic work currently located beside it: release-content identity,
required-output completeness, independent output-name/build-digest joins, observed payload checks, authenticated
publication metadata, and no-work admission failures. A successful sibling output must never satisfy another
output's missing job, artifact, or evidence.

## Preserve real output inputs

The former cluster AICR bundle object feeds actual generator arguments. Move those settings onto the AICR
output as `aicr.bundle`: deployer, storage class, accelerated-node selectors/tolerations, and workload selectors.
The AICR module owns typed values and canonical normalization; the factory maps bound settings to renderer
options. Omitted rendering settings normalize to the existing `helm` deployer. Reject unsupported or ambiguous
values before any generator work.

The compiler places the effective settings in AICR activation within the generated build graph. Their exact
values participate in Build identity, alongside the upstream profile and generator pins. Runtime execution
uses those bound settings directly, with no search through cluster views. Default and explicitly equivalent
settings have one identity; different effective settings have different identities. Preserve independent settings
for separately named outputs that select the same upstream profile.

Keep target platforms, actual artifact kinds, executable/archive/disk structure, Helm-to-image dependencies,
and typed producer options. Disk authoring validates its selected producer's disk metadata and supported output
kind directly. It no longer requires a classification label. Unsupported disk lowering/execution remains
fail-closed. Preserve qualified compatibility and conformance receipts as separate release-policy protocols;
deleting unimplemented cluster qualification does not weaken their signer or subject checks.

## Implementation sequence

1. Remove catalog classifications and introduce AICR-owned bundle settings. Migrate direct graph compilation and
   identity contracts; retain platform/toolchain/pin validation.
2. Remove cluster models and composition nodes from lifecycle/factory. Keep a smaller generic output-binding
   module for exact content and required/observed joins. Execute AICR using the selected output's graph settings.
3. Remove deployment fields from release, delivery and disk descriptions and their CLI surfaces. Retain signer
   roles; simplify member keys while continuing to reject duplicates and cross-subject evidence substitution.
4. Migrate maintained inputs and fixtures. Move the AI-factory examples' existing AICR bundle arguments onto
   their AICR outputs without changing their values. Recompute component-overlay predecessor identity through
   catalog loading. Preserve source/tool lock pins and producer commands.
5. Revise ADR-12/14/17/19 and affected identity, evidence and publication contracts directly. Remove cluster
   views from M2/M3 acceptance and milestone scope; keep the exact artifact matrix and evidence requirements.
   Align vocabulary, SADD, examples and repository guidance. Preserve dated evidence at its original scope.
6. Regenerate maintained workflows and run the completion gate, strict legacy-input negatives, AICR rendering
   argument/identity tests, consumer substitution tests, and a real local NKE build. Record actual results.

## Wire and identity consequences

Removed fields are rejected, including when their value is empty. Do not silently decode a topology-bearing
record as an output-only one. New schemas use explicit major versions; old signed bytes are not converted or
relabelled. Retired cluster/staging digest domains remain reserved and cannot mint new identities.

| Owner | New version |
|---|---|
| Definition | v4 |
| Catalog, catalog snapshot, catalog overlay, IR | v3 |
| Compile specification, Run plan, artifact graph, lifecycle binding | v3 |
| Public Run and Release | v3 |
| Publication root and configuration media type | v3 |
| Validation vector | v3 |
| Inventory and ReleaseSet | v4 |
| Reproduction result and evidence waiver | v3 |
| Release authorization | v9 |
| Disk manifest | v2 |

Update dependent media types and embedded descriptions consistently. Schemas whose wire meaning is unchanged
retain their versions. Build graphs keep the `nkd.build` family with the new IR schema included in identity.
The earlier Recipe-removal run remains a valid historical record of its then-current inputs.

## Completion evidence

Required checks are full uncached native tests, `make check`, Linux signing/pipeline race tests, both maintained
generated-bundle checks, and `git diff --check`. Verify old cluster/category/role inputs fail before output or
subprocesses, rendering settings change AICR Build identity and exact invocation, and output/evidence
substitution still fails. Repeat only affected checks after subsequent narrow changes.

Run the maintained NKE local demonstration using the unchanged source pins and existing process-scoped Git
redirection. Inspect all three outputs, publication records, dependency SBOMs, and recovered CLI execution.
Local dry-run signing is not hosted or authenticated-consumer acceptance.

## Implementation record

- Removed `cluster` and `placement`, cluster compilation/realization and provisioning descriptions, deployment
  fields across authoring and evidence, and unused staging manifests and related CLI flags. Reserved historical
  digest families cannot mint new identities. Old fields and schema versions fail strict admission.
- Preserved exact required-output completeness, name/build/component joins, payload and evidence verification,
  release-content identity, and signer authorization. Independent integration review found no blocking defects.
- Moved actual AICR rendering inputs onto outputs, normalized them once, and bound them into graph identity and
  execution. Tests cover independent settings, equivalent defaults, changed values, tampering and renderer arguments.
- Migrated maintained inputs and examples; updated owning ADRs, product scope, vocabulary, and guides. Source/tool
  lockfiles and producer commands are unchanged. NKE hosted output was regenerated through its owner.
- Full uncached native tests and `make check` passed, including formatting, vet, lint, ShellCheck and race/coverage
  tests. Linux sign/pipeline race tests, both maintained generation checks, catalog/tamper demos and diff checks passed.
  One stale AICR negative fixture was corrected before rerunning the full gates; no production validation was weakened.
- The real NKE local Run built all three outputs, produced dependency SBOMs, bound Helm to the observed CWE image,
  published to the loopback registry and executed the recovered CLI. See the
  [recorded evidence](evidence/2026-09-17-output-only-local-build.md) for exact identities and limits.

The new schemas have no backward-compatibility alias. Qualified publication, disk/VM execution, deployment and
new signed hosted acceptance remain outside the demonstrated scope. Changes are uncommitted for review.
