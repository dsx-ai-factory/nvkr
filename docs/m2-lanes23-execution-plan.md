# M2 inputs and integration handoff

## Verified starting point

On October 6, 2026, remote `main` is `61ec66fa4ffaf75fee291450f7572d244f2e624f`.
PRs #91, #92, #94 and #95 are merged. Both lane worktrees start from the newer
`codex/m2-arc-validation` head `73395149a8fd3435c7cda7ba466a566bffc79b7d`, which
adds native toolchain references, the generated candidate workflow and explicit
`build-provenance-v1` disk evidence. This is a candidate branch, not reviewed main.

The authored candidate selects 34 outputs, including independent AMD64 and ARM64
non-FDE disks and 24 native third-party image outputs. Its OS, Calico and kubeadm
source remains capture-capable NKE `ff21ba8c07cafff74b68e238a8876af11e5beded`.
The separately declared SPIRE retry uses older `726f7694`, omits DSX IMDS and is
not complete intended M2 content. Do not silently homogenize these source pins.

Both disks built and passed terminal byte checks in
[run 37445475840](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37445475840).
Both deep inspections timed out. The subsequent candidate
[run 37486058412](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37486058412)
is a separate attempt, not proof of completed main delivery.

## Owners and boundaries

| Lane | Agent and isolated branch | Owned work and handoff |
| --- | --- | --- |
| 2, inputs and customer choices | Input worker, `codex/m2-inputs-customer-choices` | Authored-input and customer-selection regressions under `pkg/nkd/buildgraph`, selected Definition/catalog corrections only when source-verified, customer examples and Definition README. Supply a reviewed uncommitted diff. |
| 3, integration preparation | `m2_integration_readiness`, `codex/m2-integration-readiness` | Minimal reproduced lint fixes, evergreen `docs/m2-input-evidence-map.md`, this plan, combined validation and generator-owned `.github/nkd-run`/workflow regeneration only after input handoff. |
| Inspection dependency | Existing lane 1 session | Diagnostics, consistent bounded deadlines, material/APT reconciliation, inspector rebuilds and measured repins. Do not duplicate these changes in lanes 2/3. |
| Integration owner | Parent session | Review both diffs, compose onto the chosen branch and preserve lane 1 work. Validated signed commit/push and non-draft PR delivery are authorized; merges and workflow dispatch remain separate effects. |

The existing inspection worktree contains uncommitted factory evidence/renderer
changes and new diagnostics files. Those belong to its owner and must not be
overwritten, reset or copied as an unreviewed whole-worktree patch.

## Immediate work

1. Fix the opt-in all-output graph regression to compile through the public
   `factory.CompileDocuments` owner. It currently rejects the valid native-toolchain
   overlays instead of exercising both disks. Reuse production input and overlay
   admission, including exact predecessor and toolchain substitution checks, rather
   than maintaining a separate test-only loader.
2. Add source-verified customer catalog/producer replacement examples and negative
   tests. Use existing whole-producer replacement semantics, not a new override
   language or approximate test-only resolution path.
3. Fix reproduced lint findings without broad suppressions or weaker validation.
   Refresh stale open-stack and missing-native-configuration statements in the
   evergreen input handoff; leave dated historical evidence unchanged.
4. Review lane 2's handoff and apply only its owned diff into the integration tree.
   Regenerate using the existing `run generate` owner with the exact M2 Definition,
   Environment and canonical generator version, then repeat byte-exact drift checks.
5. Record exact passing checks and residual dependencies. No local gate result
   implies successful native inspection, hosted signing or consumer acceptance.

## Inspection scope handoff

The latest four-lane request requires inspected evidence and receipt-gated packaging
for both architectures. The current candidate explicitly selects build-provenance
evidence and defers that work. These contracts are not interchangeable.

Lane 1 must supply reviewed diagnostics/deadline changes, rebuilt native inspector
images, actual executable/image measurements, both native inspection results and
the intended strict Environment selection. Lane 3 then integrates that explicit
selection and regenerates. Until that handoff, the current candidate remains
build-provenance-only; do not claim complete disk SBOMs or strict acceptance.

The SPIRE DSX IMDS supplier must also be restored or separately accepted as an
explicit product-content exception before describing the release as the intended
complete M2 payload.

The product acceptance documents now require both native architectures and strict
inspection; the implementation record distinguishes that target from the current
build-provenance-only candidate. Dated deferral evidence remains unchanged.

The executable handoff is still incomplete. The lane 1 deadline draft currently
changes the launcher argument count without the corresponding helper positional
decoder and full-operation deadline changes. Required handoff:

1. One reviewed atomic source patch: operation ceiling, launcher/helper deadline
   mapping, helper admission, factory join and generated job budgets, with regressions.
2. Both rebuilt native inspector image references and measured helper executable
   digests. The current profile pins name old helper bytes; pushing host code cannot
   change those images.
3. An explicit admitted timeout for each profile and native measurements. The
   existing 600-second profiles failed full inspection; a larger ceiling alone is
   neither a successful scan nor a measured requirement.
4. Strict Environment selection and generator-owned regeneration, preserving the
   current capture-capable source and all 34 selected outputs.

Read source changes from the older lane 1 tree, not its rendered bundle. Its base is
`4615b3510a295207dbdbb03f9b4561a601a58512`; importing that generated workflow would
overwrite newer candidate input and evidence changes. The integration owner reviews
the source handoff, resolves conflicts, regenerates and repeats the complete gate.

There is no open PR for `codex/m2-arc-validation` in the verified starting state.
It is a 121-file dependency relative to `main`, not a seven-file integration patch.
Choose its reviewable foundation PR/base explicitly before publishing the bounded
integration diff; do not accidentally hide or duplicate the foundation in a new PR.

## Observed delivery transport failure

The candidate [release packaging job](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37486058412/job/112367504576)
failed on `ubuntu-latest` while downloading 178 required carriers, before restoration
or package execution. Its authoritative check annotation records `System.IO.IOException:
No space left on device` in the runner's worker log. The job log download returned
`BlobNotFound`; do not reinterpret missing flushed logs as a timeout diagnosis.
Attestation, publication, signing and verification were skipped in that attempt.

The release stage and lifecycle finalizer currently download the complete required
carrier set before restoring it. The integration owner must admit sufficient capacity
or implement a validated bounded transport/retention path at the owning module;
simply raising timeouts, deleting arbitrary runner software or omitting source/evidence
checks does not fix this failure. Preserve exact-subject and complete-closure admission,
test the aggregate byte footprint and rerun this stage before declaring delivery ready.

The integration patch also fixes a separate latent transport defect: the release
stage requested both raw disk carriers even though ARC deliberately uploads only
their standard wrappers. One shared projection now selects each exact wrapper for
transport while retaining the raw logical subject and producer-job dependency.
Restoration rejects a missing wrapper; publication still checks its payload bytes
against the raw producer record. This changes the candidate release batch from
178 to 176 carriers, not its required output membership or Plan identity.
The two raw carriers were already absent from the failed run, so this is a
correctness fix, not a reduction of the bytes that caused its storage failure.

Finalizer recovery now reports the stable coded error and known disk-full, quota
or permission cause without logging arbitrary archive member text. Per-carrier
statuses and completeness admission remain unchanged. Neither correction supplies
the additional capacity needed for the complete restored evidence closure.

The ordinary runner override/finalizer-routing change was rejected by the security
gate because it could move credentialed operations onto an unverified host. No
such routing edit was applied. A verified sufficient-capacity generic hosted route
and explicit admission remain required; do not move publication or source secrets
onto ARC merely to work around storage exhaustion.

## Gates and acceptance

Use the pinned Go patch and repository commands:

```sh
make check
go test ./... -count=1
go test -race ./... -count=1
git diff --check
make workflows-check
actionlint
```

Run the opt-in `TestMaintainedM2All34OutputGraphs` with actual pinned-source access,
and the customer replacement/admission regressions. Pipeline or signing changes
require Linux race execution in addition to macOS checks. Generated files are
never hand-edited. Review final source and generated diffs independently before
authorized signed commit/push and non-draft PR delivery; merging remains unauthorized.

No repository yamllint configuration defines a workflow presentation policy.
Stock yamllint currently rejects the generated workflow's long expressions under
its default 80-column rule. Report that standalone style failure separately;
do not hand-edit generated YAML or invent a new exclusion to claim a clean gate.
Actionlint and exact native regeneration remain the repository workflow checks.

Lane 3 is ready for handoff when the combined source/inputs regenerate exactly,
the full gate is clean, all review findings are addressed, and missing operational
acceptance is explicitly recorded. It is not complete merely because tests pass.

After approved integration and merge, lane 4 must run the generated main path,
deliver both disks and every required output, authenticate exact membership and
all signatures/attestations, perform independent consumption and repeat unchanged
to establish publication-idempotency evidence. Merging and dispatching are separate
approval-gated effects, not authority inferred from this plan.
