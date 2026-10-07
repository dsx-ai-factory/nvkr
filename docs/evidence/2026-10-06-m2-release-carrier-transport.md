# Generated M2 candidate release-carrier failure

## Historical scope

This record describes the aggregate-payload transport at `73395149a8fd3435c7cda7ba466a566bffc79b7d`
and its then-reviewed 178-to-176 carrier correction. Main/#106 subsequently introduced per-output publication
and metadata-only aggregate assembly/finalization; the current
[reference graph](../../pkg/nkd/factory/reference_graph.go) replaces aggregate payload edges with authenticated
output-local metadata and limits finalizer carriers to aggregate metadata, publication/signature/predicate/verification
records and the cryptographic tool. The carrier counts below are historical facts, not the current transport contract.
The [successful historical validation-branch run](2026-10-07-m2-validation-branch-release.md) is recorded separately.

## Verified attempt

GitHub read-back on October 6 confirmed that [run 37486058412](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37486058412)
was manually dispatched at commit `73395149a8fd3435c7cda7ba466a566bffc79b7d` and completed with failure.
Both native producer-provenance disk jobs succeeded: [ARM64](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37486058412/job/112348913943)
and [AMD64](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37486058412/job/112349253762).
Those jobs did not perform deep inspection despite their then-current display names.

The [aggregate release job](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37486058412/job/112367504576)
selected `ubuntu-latest` and failed while downloading 178 required input carriers. Its check annotation records
`System.IO.IOException: No space left on device` while writing the runner's worker log. Restore and aggregate
packaging remained pending; publication was skipped and finalization failed. The full job-log endpoint was
unavailable at the earlier read-back, so the annotation and terminal step states, rather than an invented
download transcript or storage measurement, establish the observed failure boundary.
October 7 read-back retained the [exact job states](2026-10-07-m2-validation-branch-release/failed-carrier-job.json)
and [raw failure annotation](2026-10-07-m2-validation-branch-release/failed-carrier-annotations.json).
The full job-log endpoint again returned `404 BlobNotFound`.

## Correction and remaining acceptance

The reviewed carrier correction excludes the two raw QCOW2 carrier selectors from aggregate release transport:
each disk's OCI wrapper already carries those exact bytes. The observed run did not upload independent raw-disk
archives, so those selectors referred to missing carriers. For this Plan, the selected count changes from 178 to
176 without removing required output or evidence membership. This fixes transport selection correctness, not
observed downloaded bytes; it does not prove the remaining carrier set fits a generic hosted runner. Per-member metadata and remaining package/evidence
carriers still needed restoration at that correction stage; that stage had no completed Run establishing storage capacity or successful packaging.

No completed release publication, signing, independent consumer verification or unchanged rerun is established
by this attempt. The October 6 proposed strict M2 target was subsequently superseded by the October 7
decision to preserve main/#106: producer provenance for M2 and inspection for M3, recorded in
[ADR-13](../adr/0013-supply-chain-evidence.md).
The [dated deferral record](2026-10-06-m2-inspection-deferral.md) retains its original attempt and validation scope.

## External authority still pending

The main baseline at `a54f6a2dea2fb32b4eae8014af2d6adc802f667d` restricts authored dispatch/signing to `main`.
October 7 read-back still listed `main` and `codex/m2-arc-validation` for
[nkd-arc](2026-10-07-m2-validation-branch-release/arc-branches.json), and those two plus
`feat/nke-deploy-pipeline` for [nkd-release](2026-10-07-m2-validation-branch-release/release-branches.json).
The local integration's proposed `codex/m2-native-arc-foundation` exception is not admitted by either retrieved
external policy. This document grants no branch publication authority. External-policy alignment remains pending;
an authored YAML restriction or local exception is not proof of environment protection.
