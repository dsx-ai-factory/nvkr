# M2 metadata publication contracts

This records local contract validation and subsequent hosted validation for `feat/m2-native-arc`.
The initial local results below are based on `73395149a8fd3435c7cda7ba466a566bffc79b7d`;
the successful hosted run and its narrower acceptance scope are recorded at the end.

The M2 Environment selects `publication.mode: per-output-v1`. Its generated Plan has
34 named outputs and 35 publication routes: one per output and one aggregate release.
Each output retains payload verification. Aggregate assembly and finalization consume
authenticated metadata and evidence; the finalizer restores six carriers. The
main-only dispatch/signing policy remains in force.

## Generated workflow

- Plan digest: `sha256:aa0d12191f31fb5daf4ac42041fea8b9abd7d63362475cf2ca567c1b7c772c77`.
- Workflow size: 465,535 UTF-8 bytes, below the generator's 500,000-byte limit.
- `run generate --check` with the M2 Definition and GitHub Environment passed.
- `actionlint` passed. `yamllint` passed with generated-format allowances for line
  length, the `on` key and the optional document-start marker.
- Size tests cover one byte below, exactly at and one byte above the limit, multibyte
  characters, and rejection before any generated output is written. Compaction checks
  deterministic output and exact equivalence of the expanded YAML documents.

## Publication contracts

Loopback-registry tests cover AMD64 and ARM64 disk outputs: metadata pull and push
perform zero payload GETs, while the existing full consumer pull retrieves and checks
the payload. Metadata inspection cannot issue the full-payload validation result.

Factory tests cover exact output selection, shared evidence, six-carrier finalization,
complete provider-job outcomes, wrong-run rejection, signature and SBOM rejection,
and dry-run rejection. Cryptographic adapters in these factory tests are explicit test
doubles; they do not demonstrate Fulcio, Rekor or GHCR operation.

## Check status

Focused contracts, formatting, diff checks, Go vet, Linux-targeted lint, shellcheck and
the vulnerability scan passed with Go 1.27.1. The full non-race run found one
formatting-dependent credential-test mutation, corrected to mutate parsed YAML and
then recompact; the corrected test passed. Linux race tests passed for `sign`,
`delivery`, `factory` and `lifecycle` with bounded package parallelism. The full
macOS race gate (`go test -race -p 2 ./... -count=1`) passed.

macOS lint reports five existing staticcheck SA4023 findings in platform-dependent
pipeline stubs. Linux-targeted lint reports zero findings. Initial overloaded test
runs timed out; an initial Linux run also used a read-only source mount incompatible
with a lifecycle test. The replacement Linux run uses a writable isolated source copy.

Hosted storage peaks, execution time, internal-registry permissions and live signing
for this publication mode were unmeasured at the initial local checkpoint. Per-output jobs still need sufficient
space for their individual payloads.

## Hosted validation failures

[Run 37504454272](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37504454272)
executes validation commit `5e218774f145256b8ea48a35be0ae5924910e9b8` with the exact
`codex/m2-arc-validation` config exception. Three independent failure causes were observed:

- [OIDC-image verification](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37504454272/job/112411567985)
  rejected the production predicate before cryptographic verification. Comparing the saved predicate
  carrier with the registry DSSE payload at
  `ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases-spire-oidc-image-arm64@sha256:1f425f902c5057f90d0d0575abd58cb6bd2e21afbf9a369aeddebc1d4a9e10ab`
  showed exactly one difference: `finishedOn` changed from
  `2026-10-06T17:37:51.08561615Z` to `2026-10-06T17:37:51.085616150Z`.
  Cosign's SLSA serialization uses protobuf timestamp precision. The emitter now uses
  [protobuf JSON's 0/3/6/9 fractional digits](https://protobuf.dev/programming-guides/json/)
  without rounding; the exact predicate comparison remains unchanged. Regression cases
  cover every fractional precision and timezone normalization, including the captured failure.
- [Controller-chart publication](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37504454272/job/112413523662)
  failed independent reproduction because the output-local publication omitted the bound
  image package. The declared dependency closure now includes that package and its producer
  evidence. Collection keeps it out of the chart route's output membership. A regression
  reproduces the failure with only declared publication inputs visible, then checks that
  successful collection includes only the chart. Underlying reproduction errors are now retained.
- Disk publication preparation exhausted hosted-runner storage while restoring producer materials:
  [ARM64](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37504454272/job/112422529892)
  and [AMD64](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37504454272/job/112428935919).
  The ARM64 job downloaded approximately 13 GB of carriers before restoration. Both OS build jobs
  completed successfully. Offline publication preparation now runs in the existing ARC disk group,
  reusing its material files. Hosted attestation, publication and signing consume the prepared layout;
  their credential boundary is unchanged. Successful restoration also removes its exact carrier file.
  Linux race contracts exercise held completion and prepared publication for both platform fixtures;
  generated-graph tests reject hosted disk-material downloads and credentialed ARC operations.

The run completed with failure. The finalizer rejected the incomplete provider-job closure. These fixes
were committed as `b294906` and included in validation commit `42d5ee0`. The subsequent hosted run is
described below. The earlier full-gate results above apply to `c049b36`; follow-up validation is separate.

Follow-up validation: provenance/signing race tests and the targeted factory Helm,
publication, metadata and generated-workflow race contracts passed. Linux-targeted
lint, changed-package vet, actionlint, yamllint and exact regeneration passed. The
corrected main-only workflow is 461,745 bytes with Plan digest
`sha256:6cfd876ea915b6b3f42de7b697306dc4f138d31cfafb8773b486eb611090f391`.
After the disk storage fix, full uncached macOS tests passed. Linux race tests passed for
`provenance`, `sign`, `factory` and `lifecycle`, including the new held disk preparation
contracts. Linux-targeted lint, full vet, shellcheck, vulnerability scanning, formatting,
diff checks, workflow linters and byte-exact regeneration passed. The full uncached macOS
race gate also passed; the previously recorded macOS lint limitation still applies.

## Partial retry admission

[Run 37517716998, attempt 2](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37517716998/attempts/2)
reached [release assembly](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37517716998/job/112496174271),
which rejected a production predicate as belonging to another workflow Run. The implementation required
every output's preparation and signing invocation to equal the consuming attempt, preventing reuse of
successful earlier work. The run completed with failure and its monitor was paused.

The local retry change admits only current or earlier attempts of the same exact GitHub server,
repository, run and workflow builder, retaining exact Plan/input, named-output, signature and evidence
checks. Preparation must precede or equal signing, which must precede or equal consumption. The aggregate
records its current assembly invocation and each output's original digest-pinned publication reference,
preparation invocation and signing invocation. Original output roots and predicates are not rewritten.

The regression reproduced the old rejection before the fix. Focused contracts cover earlier-attempt
reuse, mixed-attempt assembly, re-signing an older package and retrying finalization; negative cases cover
foreign runs/workflows, future or malformed attempts, changed predicates and invalid signatures/evidence.
These use explicit signing adapters, not a live registry/signing-service acceptance run. Full uncached
macOS tests and full Linux race tests initially found one negative-fixture error: its changed Plan digest
disagreed with its own dependency record, so validation correctly rejected it earlier than the asserted
error code. The fixture now constructs an internally consistent wrong-Plan predicate. Its focused tests
and the full factory package reruns passed on both platforms; every other package passed in the original
full runs. Linux-targeted lint, full vet, shellcheck, vulnerability scanning, formatting, diff checks and
exact M2 workflow regeneration also passed. The subsequent full hosted run includes this retry change;
mixed-attempt reuse remains locally contract-tested only.


## Successful hosted validation

[Run 37533806963](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963)
completed successfully on October 6, 2026, at 23:03:19 UTC, on attempt 1 of validation commit
`625980e30b1a9f5ef27a9b46534de65eefff385a`. It contains clean-branch implementation commit
`1b89d656` and all preceding publication fixes. All jobs succeeded: native AMD64 and ARM64
OS builds, all 34 output publications and their signing/verification, metadata-only release
assembly, aggregate publication/signing/verification, and
[Run completeness finalization](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963/job/112541818732).

The validation Environment admitted only the additional exact `codex/m2-arc-validation` branch.
Its Plan digest is `sha256:4a7215ab77d219d2695e2eae15d6546c102d808de8b4f229b52868935e98ba63`.
The PR branch retains main-only dispatch/signing and Plan digest
`sha256:6cfd876ea915b6b3f42de7b697306dc4f138d31cfafb8773b486eb611090f391`.
Branch comparison confirmed no implementation differences: only the exception's authored
Environment, generated bundle, policy regression and explanatory README differed before this
evidence update. The main-only configuration itself has not yet run on main.

This establishes hosted pipeline-attested candidate delivery with live signing and finalization.
It does not establish mixed-attempt retry acceptance, independent full-payload consumer acceptance,
M3 deep disk inspection, organizational toolchain approval, or a measured storage-capacity bound.
The temporary older SPIRE-agent source pin and internal candidate publication namespace remain.


## Inspection deferral and temporary source selection

[Run 37445475840](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37445475840)
built both native disks, but both deep inspections exceeded the 600-second deadline. M2 therefore
explicitly selects `diskEvidence: build-provenance-v1`; deep disk inspection and filesystem
reconciliation are deferred to M3. Original live disk/material bindings, exact producer subjects,
signed deferred-inspection scope and completeness checks remain required. Strict inspection remains
available; successful candidate delivery does not claim inspected dependency inventories.

The SPIRE-agent source remains pinned to `726f76944404e1106626b8333d3576bdf67b575b`,
omitting the DSX IMDS plugin whose download returned HTTP 403 in
[run 37443755541](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37443755541).
Other OS sources retain the capture-capable pin. Package preparation selects the immutable
toolchain's existing nFPM executable. These candidate concessions remain in the authored inputs.
