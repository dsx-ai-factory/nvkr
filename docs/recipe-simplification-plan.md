# Recipe removal implementation plan

Status: implemented locally, 2026-09-17. NKD Recipe authoring, criteria matching, inheritance, and mixins are
removed. The subsequent [output-only simplification](output-only-simplification-plan.md) removes deployment
views and classifications too. This document describes the resulting model; the earlier
[local build evidence](evidence/2026-09-17-direct-output-local-build.md) retains its original schema versions.

## Required outcome

The public lifecycle has six objects: Definition, Environment, Run, Artifact, Release, and Verification Policy.
Catalog components retain reusable pinned producer instructions. Definition outputs select those components
and their target platform directly. The compiler derives build graphs from those selections. No author writes
or selects an intermediate Recipe, reusable component group, mixin, or inheritance chain.

Remove Recipe catalog entries, selection and maintenance interfaces, matching, specificity, wildcard selection,
reserved axes, enumeration frames, extension classification, and Recipe-specific fixtures. Do not preserve the
concept under another authored grouping object or a compatibility alias.

Retain exact source/tool/artifact pins, canonical identity, complete graph validation, bounded context-aware
I/O, immutable attempts, subject-bound observations and provenance, required-output completeness, signed
publication, and explicit consumer policy. Unsupported execution and evidence lanes remain fail-closed.

AICR's external recipe format and command are upstream interfaces. Their pinned profile bytes and generator
identity remain verified. Historical evidence and frozen legacy digest-domain strings keep their original
meaning; removing NKD Recipes does not authorize rewriting signed history.

## Definition and catalog model

Definition `nkd.dev/definition/v4` selects producers on each output. It contains no Recipe or cluster collection.
An optional output-specific catalog stack provides whole-producer replacement with exact predecessor checks.
The maintained NKE selections are:

```yaml
outputs:
  - name: cwe
    components: [cwe]
    platform: linux/amd64
    format: oci
  - name: nke-helm
    components: [nke-helm]
    platform: linux/amd64
    format: helm-oci
    helm:
      chart: 007-controller
      imageOutput: cwe
  - name: nkectl
    components: [nkectl]
    platform: linux/amd64
    format: executable
```

Catalog `nkd.dev/catalog/v3` owns sources, components, tools/actions, AICR profiles, and lock references.
Components declare supported target platforms and their actual producer options. Categories and deployment-role
affinities are removed. Validate artifact/disk structure from producer metadata and the requested format.

Every selected component needs an explicit supported output platform and its exact pin/toolchain route. The
current NKE producers remain `linux/amd64`; removing selectors does not admit arm64 against amd64 commands.
Target platform remains distinct from execution-host requirements and VM routing. Existing lock schema and
pins are unchanged.

An AICR Helm output selects `aicr.profile` and `aicr.generator`, with optional `aicr.bundle` rendering settings.
It cannot also select ordinary components. The selected generator must match the profile's executable pin.
The AICR module validates and normalizes actual rendering inputs; default and equivalent explicit settings
produce one effective identity. Generic selection parameters and reserved-axis activation are removed.

## Generated graphs and exact output bindings

For each named output, `pkg/nkd/buildgraph` derives an internal graph from its selected producers and pinned
inputs. An AICR output derives its graph from its exact profile, generator, and effective rendering settings.
These are generated records, with no independently authored membership layer. Preserve explicit chart-to-image
packaging edges; do not infer unsupported producer dependencies.

The typed `nkd.build` family identifies these graphs. IR v3 includes the concrete target, logical nodes, and
AICR activation. Changing a selected producer, pin, command, patch, toolchain, target, or effective rendering
input changes identity. Unrelated catalog producers, output names, packaging, transport, execution requirements,
and Environment routing remain outside logical build identity and inside their owning exact records.
Complete executable-graph validation precedes identity projection.

Output names identify distinct required uses. Bind each use to its build digest, exact producer membership,
and observed subjects. Two outputs with identical graphs may share a build digest but still require independent
execution, restore, terminal, and evidence records. A same-named component in another catalog stack cannot
replace that binding. Cross-output build reuse remains deferred.

The reserved historical `nkd.recipe` family cannot mint new identities. Changed wire shapes have intentional
major versions; the current [schema inventory](output-only-simplification-plan.md#wire-and-identity-consequences)
records the follow-on migration. Old authored/executable forms fail before work. Signed bytes are never
up-converted or relabelled, and old/new identities have no alias.

## Implementation sequence and result

| Work | Result |
|---|---|
| Replace Recipe authoring with direct output selections | Definition, catalog, CLI, and SDK select components/platforms or explicit AICR inputs. |
| Remove resolution/composition machinery | `recipe`, `compose`, and `criteria` packages and obsolete APIs/flags are removed. `target` and `aicr` own concrete value validation. |
| Migrate graph identity and downstream joins | Compiler, executor, observations, SBOMs, provenance, release, and consumers use exact build/output identities. |
| Preserve strict migration boundaries | Unknown fields, old versions, legacy commands, and reserved Recipe overlay/mixin layouts are rejected. Separately referenced component catalog overlays retain exact replacement semantics. |
| Migrate inputs and regenerate CI | Maintained Definitions and fixtures use direct producers. Generated-file retirement accepts only unchanged files owned by the previous manifest. |
| Verify the implementation | Native full gates, Linux signing/pipeline race checks, both maintained generated bundles, catalog/tamper demos, and a real NKE local build passed for the initial Recipe-removal stage. |

The follow-on implementation removes cluster, placement, and staging machinery, while preserving generic
release-content identity and exact required-output/evidence membership. Signer authorization roles remain.
AICR rendering settings now belong to outputs and are bound into graph identity and execution. Current
verification for that stage is recorded in the [output-only plan](output-only-simplification-plan.md).

## Acceptance and evidence limits

Require strict legacy-input negatives; deterministic identity vectors; selected-input sensitivity and
transport/host purity; missing, swapped, duplicate, and cross-subject evidence rejection; and no-work rejection
of unsupported lanes. Preserve meaningful tests when migrating fixtures; only tests for removed behavior are
retired. Regenerate generated bundles through their owner and preserve source/tool versions.

Run full uncached tests, `make check`, Linux sign/pipeline race checks, maintained generation checks, and
`git diff --check`. A local build uses the existing pinned NKE checkout through the demo's process-scoped Git
redirection. Do not modify the source checkout or canonical catalog to make execution work.

The [initial local run](evidence/2026-09-17-direct-output-local-build.md) built CWE, Helm, and CLI outputs,
bound the produced image into Helm, generated dependency SBOMs, published to the loopback registry, and
executed the recovered CLI. Its signing was dry-run only. Hosted acceptance for the migrated model,
qualified publication, disk/VM execution, and deployment are not established by local or unit-test success.
No commits or remote publication are part of this work.
