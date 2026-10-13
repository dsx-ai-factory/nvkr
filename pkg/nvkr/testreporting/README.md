# Verified observation reporting

This package passively ingests independently selected digest-pinned observations through the stock
`testobservation.VerifyOCI` owner. Every input is authenticated before deduplication; exact signer, issuer,
Definition and reviewed repository admission remain caller-owned. Different sources, attempts, suites,
failures and coverage are retained. A generated discovery index describes historical signed observations;
it supplies no current-health, qualification or service-availability receipt. Closed or canceled receipts
cannot generate another projection. No cached index is accepted as authority. At most 128 inputs and
256 MiB of cumulative authenticated canonical-observation/covered-file bytes are retained. An excess
receipt is closed immediately; its already bounded individual verification fetch may temporarily add one
owning-verifier inventory before the aggregate gate rejects it.

Community paths use the exact configured Definition and an authenticated-signer-derived privacy label.
That raw SHA-256 token is a collision-checked grouping/transport label, not a semantic identity, signer
receipt or authorization. The signer is authenticated against exact independent expectations first.

## Local TestGrid export contract

`Report.TestGrid` and `Report.ExportTestGrid` generate local artifacts only. Bucket and the complete
`groups/group/dashboard/tab/build` prefix are explicitly selected; no tab, bucket or retention defaults exist.
No external uploader is implemented or implicitly authorized. `Published` remains false.

The existing Autotest publisher, inspected at `25bf36a133f5681ded8e5b341f87a2df80aca7eb`, writes
`started.json`, original JUnit, then `finished.json` at
`framework/reportingservice/destinations/testgrid_destination.py:859-887`. The generated export follows
that ordering, retains binary SUCCESS/FAILURE and exact raw JUnit bytes, and adds `artifacts/evidence.json`
with exact authenticated membership and original-to-projected filename bindings.

Multiple JUnit files retain their original bytes under `junit.<SHA-256-of-original-name>.xml` transport labels.
These labels match the TestGrid v0.0.175 basename pattern in `util/gcs/read.go:279-280`; updater source
`pkg/updater/read.go:418-419` reads all matching JUnit artifacts. The selected local TestGrid config-gen pins
v0.0.175, and its updater manifest declares `v20251223-v0.0.175-2-g0e84967e`. This source inspection proves
neither deployed image bytes nor actual ingestion. Colliding generated names fail closed.

No-result observations still generate a failure `finished.json` carrying explicit not-run/infrastructure
status and zero observed cases. They produce no fabricated JUnit cases. Whether the selected backend
renders that failure without JUnit remains an external acceptance gate.

Started/finished timestamps must be signed observation facts `testStartedUnix` (`before`) and
`testFinishedUnix` (`after`), expressed as canonical positive Unix seconds. These cover the producer
invocation interval and retain the fact's declared evidence level; they are not trusted timestamp or exact
subprocess-duration claims. Missing or contradictory times fail closed rather than using the export clock.

## Companion TestGrid work and acceptance gaps

The local TestGrid checkout at `83d7f9234d3137d36cd2ff0004d938a0dbb537ee` projects only
`release_stream`, `release_tag`, `runtime_release` and `operations_release` in
`tools/config-gen/main.go:644-648`. This exporter supplies new `nvkr_*` metadata but does not register a
TestGroup or modify that backend/UI. The [column-header example](testdata/testgrid-column-header-example.json)
is a partial field example to append to an operator-selected group, preserving existing column positions;
it is not a deployable config and supplies no destination. A companion change must render the exact
observation/evidence links, tier, execution/cleanup reason, and no-result status.

External acceptance additionally requires the explicitly selected bucket/prefix and retention policy,
service authorization, immutable/create-only object writes, original JUnit upload before the finished marker,
authenticated object readback, and registered TestGrid API/UI readback. Public index hosting/anonymous access
requires a selected target and its access/readback evidence. No local test claims any of those effects.
