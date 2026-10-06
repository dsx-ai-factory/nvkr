# Generated M2 candidate release-carrier failure

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

## Correction and remaining acceptance

The reviewed carrier correction excludes the two raw QCOW2 carrier selectors from aggregate release transport:
each disk's OCI wrapper already carries those exact bytes. The observed run did not upload independent raw-disk
archives, so those selectors referred to missing carriers. For this Plan, the selected count changes from 178 to
176 without removing required output or evidence membership. This fixes transport selection correctness, not
observed downloaded bytes; it does not prove the remaining carrier set fits a generic hosted runner. Per-member metadata and remaining package/evidence
carriers still need restoration, and a new complete Run must establish storage capacity and successful packaging.

No completed release publication, signing, independent consumer verification or unchanged rerun is established
by this attempt. The later proposed strict M2 engineering target requires complete inspection, acquisition/material
reconciliation and receipt-gated packaging on both native architectures, pending named-decider ratification.
The narrower producer-provenance candidate does not complete that proposed target. The earlier user-requested
inspection deferral retains its historical scope in the [dated deferral record](2026-10-06-m2-inspection-deferral.md).

## External authority still pending

The maintained authored Environment now restricts dispatch/signing to `main`. A separate October 6 API
read-back of `nkd-release` deployment policies still listed `main`, `codex/m2-arc-validation` and
`feat/nke-deploy-pipeline`. This document grants none of those branches publication authority. External-policy
alignment and fresh read-back remain pending; an authored YAML restriction is not proof of environment protection.
