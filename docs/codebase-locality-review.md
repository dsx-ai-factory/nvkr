# Code layout and change locality

Reviewed 2026-09-17 at `16b3775`. Implementation progress is tracked in the
[implementation record](codebase-locality-implementation.md). This is an architecture assessment and proposed refactoring sequence, not an
implemented contract change. Read-only traces covered Definition compilation, local/GitHub execution, publication,
Release consumption, and SDK entry points. Current capabilities remain governed by the ADRs and reconciliation.

## Assessment

The code has poor change locality in its central workflows. The public model is simpler after Recipe and
deployment-topology removal, but it still feeds much of the older generic compiler and evidence plumbing.
Maintainers repeatedly translate the same information between representations and reconcile the copies.
Package names often describe architectural mechanisms rather than telling a reader where an operation lives.

A count of Go source under `pkg/nkd`, `cmd/nkdctl`, and `client/v1` found 212 production files containing
57,394 lines, and 221 test files containing 42,048 lines. Counts include comments, blank lines and platform
variants. Factory and pipeline account for 71 production files and 17,413 lines; the command layer has 17
production files and 5,627 lines. Size alone is not a defect. The important evidence is the repeated ownership
and representation changes below. Broad schema removals naturally require broad migrations; those migrations
alone do not establish that ordinary changes are poorly localized.

## Concrete sources of complexity

### Compilation returns across its owning seam

[Lifecycle compilation](../pkg/nkd/lifecycle/compile.go), lines 47–82, lowers Definition/Environment into
`factory.CompileSpec` and `ExecutionSettings`, invokes factory compilation, then edits the returned Plan to
add the public digests and output joins, rebind evidence, and validate again. Its `lower` function, starting
at line 88, separately derives packages, producer selectors, tools, and publication routes.

[Factory's model](../pkg/nkd/factory/model.go) gives those handoffs independent schema versions, digest
methods and validators. This makes a change pass through the public model, copied compiler model, compiler,
and post-compilation enrichment. Deriving a Run plan is useful; an independent, document-shaped intermediate
model and a Plan completed by its caller add avoidable coordination.

The compiler should own the full transformation from admitted inputs to a complete Plan. Internal helper
values may remain, but should earn their existence through a distinct responsibility. Removing an intermediate
digest needs deliberate migration: compile-spec and execution-settings digests currently appear in production
provenance, so this is not merely an in-memory type cleanup.

### One producer has two execution descriptions

[Factory compilation](../pkg/nkd/factory/compile.go), starting at line 567, copies execution settings from an
`ir.Node` into a factory node. [Plan validation](../pkg/nkd/factory/validate.go), line 229, compares network,
container, platform, requirements and execution binding back to the original IR.

Logical build graphs and whole-Run dataflow answer different questions and should remain distinguishable.
However, scheduler records can reference the authoritative producer instruction instead of owning editable
copies of its execution settings. Rehydration must still verify the exact graph and all references before work.
Do not remove current comparison checks until the duplicate executable representation is gone.

Local execution also traverses the model twice: [factory local execution](../pkg/nkd/factory/local.go) executes
producer subgraphs through `pipeline.Local`, adapts their reports into factory results, and then traverses
non-IR operations. GitHub schedules the full factory graph and selects between two node command protocols.
This is a later consolidation opportunity after compiler ownership is clear.

### Release consumption depends on compiler vocabulary

[The Release consumer](../pkg/nkd/lifecycle/release.go), starting at line 224, translates delivery publication
objects into `factory.LifecycleBinding`. At line 339 it creates both expected and observed output lists from
that same binding to call a factory validator. The producer performs the reverse conversion in
[factory packaging](../pkg/nkd/factory/execute.go), starting at line 562.

This consumer-side conversion performs structural checks, but it does not compare independently frozen
expectations with independent observations. The real Plan-to-observation completeness join remains necessary.
Publication consumption should use a delivery-owned checked output view and project directly to public artifacts,
without reconstructing compiler records.

The release-content identity also has separately maintained schemas in
`factory/lifecycle_binding.go` at the baseline, line 15, and
[lifecycle content](../pkg/nkd/lifecycle/content.go), line 11. Both mint `KindReleaseContent`. One existing owner
should define its canonical shape, projection and digest for producers and consumers. Do not introduce a general
shared-types package to hold the duplicates without owning their behavior.

### Path-based interfaces make callers coordinate verification

[Delivery pull](../pkg/nkd/delivery/registry.go) verifies staging and committed layouts. Format-specific
[extraction](../pkg/nkd/delivery/inspect.go) and [image inspection](../pkg/nkd/delivery/image.go) independently
reinspect the publication. Lifecycle then checks the layout again after projecting outputs.

Those rechecks currently defend against mutable paths and caller-controlled descriptions. Their necessity
reflects the interface: callers repeatedly hand around a path plus parsed data. A deeper delivery operation
should own opening, checking and reading the exact closure, with explicit resource lifetime. Removing repeat
reads is safe only when held-byte ownership or an equivalent authenticated capability preserves those guarantees.
No performance improvement has been measured in this assessment.

### Supported and deferred workflows share the same navigation surface

Both `factory/github.go` and `pipeline/github.go` are provider renderers, but they implement different evidence
contracts. Maintained Run generation uses factory; separate qualified emitters share pipeline with the actual
execution machinery. Their names alone do not tell a reader which path to follow. Likewise, lifecycle Release
consumption, qualified release authorization and delivery layout checks are different operations spread across
similarly named files and modules.

The SDK's normal `VerifyRelease` method is surrounded by lower-level authentication and publication orchestration.
`client/v1/release.go` at the baseline exposes authorizations, transport artifact lists and target maps,
while qualified publication admission in `pkg/nkd/release/authorization.go` at the baseline, line 603, deliberately blocks the
unimplemented cross-process commit protocol. Maintained publication takes a different path. This expands the
interface a reader must learn without providing a working alternative publication workflow.

Review deferred entry points against actual consumers. Remove unused ones or make their scope explicit outside
the main workflow. Retain working lower-level authority mechanisms needed by maintained operations. Capability
gates and diagnostic callers must be accounted for before removing a surface; no qualified lane becomes a
pipeline-attested fallback.

## Recommended refactoring sequence

1. **Give compilation one owner.** It accepts admitted Definition/Environment inputs and returns a complete
   immutable Run Plan. Move derivation and completion together; eliminate the document-shaped `CompileSpec`
   handoff rather than adding a wrapper around it. Lifecycle retains attempt preparation and persistence.
   Keep build identity distinct from Run routing and evidence.
2. **Give publication membership one owner.** Consolidate the content schema and output checks, then remove
   the Release consumer's conversion into factory records. This is also a useful bounded first patch if the
   compiler migration needs more design work. Aim to preserve canonical bytes in this step.
3. **Make producer execution reference one definition.** Preserve whole-Run dataflow, but replace copied producer
   execution settings with exact references. Then simplify local/hosted scheduling around the same operations.
4. **Reduce the exposed deferred surface.** Separate or retire unavailable SDK/provider workflows after tracing
   callers and updating owning contracts. Keep the ordinary product path small and discoverable.
5. **Organize files after ownership changes.** Use coherent operation files with their focused tests, and explicit
   provider/platform adapter names. Merge files split only by historical patch or contract names where it improves
   reading. Add short package comments identifying the starting operation and owned invariants. Avoid artificial
   file-size targets, a giant replacement package, and another generic orchestration framework.

The desired reader path is admission and compilation → execution or hosted generation → observed output
finalization → publication and consumer verification. Each operation should own its transformations and expose
a small interface. Specialized canonical, trust, signing and sandbox modules continue to own their invariants.

## Success criteria

- A reader can locate the maintained entry point and follow each operation without reconstructing a parallel
  model in another module. Tests for an operation are discoverable beside its implementation.
- A representative AICR-setting or output-metadata change requires fewer independently maintained definitions
  and copy/conversion sites. Count those sites before and after; do not substitute a target file count for locality.
- Release consumers no longer need compiler binding types. One owner defines release-content canonical bytes.
- There is one authoritative producer execution description. Required versus observed output records remain
  separate, and equal Build Digests never satisfy another output's missing execution or evidence.
- Exact-byte verification, strict decoding, capability admission, held-resource ownership, signer policy and
  immutable attempts retain their negative contracts. Revalidation at real process/trust transitions remains.
- Refactoring preserves canonical output or explicitly versions changes; the full native gate, affected Linux
  race checks, generated-bundle checks and appropriate local execution evidence pass before completion claims.

No production code or ADR was changed for this review. Source references and counts were checked locally;
`git diff --check` is the proportional validation for this documentation-only assessment.
