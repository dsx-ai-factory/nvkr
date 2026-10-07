# Historical M2 validation-branch release

## Exact successful attempt

Read-only GitHub retrieval on October 7, 2026 confirmed [run 37533806963](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963),
attempt `1`, completed successfully after manual dispatch of `.github/workflows/nkd-run.yml` at
`625980e30b1a9f5ef27a9b46534de65eefff385a` on `codex/m2-arc-validation`.
It was created on October 6 at `21:25:39Z` and the run metadata was last updated at `23:03:19Z`.
[Retrieved run metadata](2026-10-07-m2-validation-branch-release/run.json) and
[selected job metadata](2026-10-07-m2-validation-branch-release/jobs.json) bind these observations to that exact attempt.

| Historical operation | Job | Conclusion |
|---|---|---|
| Assemble release from verified output references | [112534817109](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963/job/112534817109) | success |
| Publish release | [112536227308](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963/job/112536227308) | success |
| Sign and attest release and deliverables | [112536512974](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963/job/112536512974) | success |
| Verify release and deliverable signatures | [112539131258](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963/job/112539131258) | success |
| Finalize Run and check completeness | [112541818732](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963/job/112541818732) | success |

The [retrieved log excerpts](2026-10-07-m2-validation-branch-release/job-log-excerpts.txt) report the immutable release reference:

```text
ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases@sha256:292524ba607d5a45326ae4801e81867f5358921824df3e12c59d274360baaf0e
```

The finalizer reports `hosted Run 37533806963-1: complete (34 observed outputs)`.
The downloaded GitHub carriers `nkd-publication-release` (artifact `11448359352`) and
`nkd-verification-release` (artifact `11448904167`) contain the same
[publication reference](2026-10-07-m2-validation-branch-release/publication.txt) and
[verification record](2026-10-07-m2-validation-branch-release/verification.json).
The latter records `signatureVerified: true`, `attestationVerified: true`, `dryRun: false`,
the SLSA production predicate type, and the observed signing identity
`https://github.com/dsx-ai-factory/nke-recipes/.github/workflows/nkd-run.yml@refs/heads/codex/m2-arc-validation`
under issuer `https://token.actions.githubusercontent.com`.

## Historical scope and present verification limits

The exact authored Environment at `625980e30b1a9f5ef27a9b46534de65eefff385a` selected
`diskEvidence: build-provenance-v1`, `publication.mode: per-output-v1`, and the explicit
`codex/m2-arc-validation` dispatch/signing exception. The disk evidence implementation at that commit
emitted `nkd:diskInspection: deferred-m3`, describing producer-only evidence rather than inspection.
This successful run predates the subsequent `not-performed` token, consumer minimum-evidence policy,
timeout changes and the present PR integration. It is external execution evidence for the historical
producer-provenance M2 route, not validation of those later changes or the current head.

Aggregate verification authenticated signatures, production provenance and evidence through the producing
workflow. In per-output mode, aggregate verification and finalization use metadata and exact output references;
the current [verification owner](../../pkg/nkd/factory/execute_sign.go),
[reference graph](../../pkg/nkd/factory/reference_graph.go) and
[finalization owner](../../pkg/nkd/factory/reference_release.go) preserve that distinction.
Downloaded workflow records do not establish independent full-payload consumer acceptance.
No independent consumer accepting the complete signed disk payload closure is recorded here.

On October 7, fresh `crane digest` and `crane manifest` requests for the exact release reference using
the existing credential configuration returned `404` / `MANIFEST_UNKNOWN`. The successful historical
publication and workflow verification are supported by the logs and carriers; current registry availability,
current registry read-back and independent consumer acceptance remain unverified. This response alone
does not establish why the manifest could not be retrieved.

## Deployment-policy authority remains separate

October 7 read-back of the deployment branch policies still listed `main` and `codex/m2-arc-validation`
for [nkd-arc](2026-10-07-m2-validation-branch-release/arc-branches.json), and those two branches plus
`feat/nke-deploy-pipeline` for [nkd-release](2026-10-07-m2-validation-branch-release/release-branches.json).
The PR #97 integration head at `a54f6a2dea2fb32b4eae8014af2d6adc802f667d` has main-only authored signing.
The local PR integration proposes the separate `codex/m2-native-arc-foundation` exception; neither external
environment's retrieved policy admits that branch. No deployment-policy mutation or workflow dispatch
was performed while collecting this evidence. The historical exception and success do not authorize a new branch.

## Retrieval sources

The stored API files are JSON projections of the exact read-only endpoints below; the failure annotation is
retained in full. Credentials, signed download URLs and unrelated logs are excluded.

```sh
gh api repos/dsx-ai-factory/nke-recipes/actions/runs/37533806963
gh api repos/dsx-ai-factory/nke-recipes/actions/jobs/<job-id>
gh api --allow-escape-sequences repos/dsx-ai-factory/nke-recipes/actions/jobs/<job-id>/logs
gh run download 37533806963 --repo dsx-ai-factory/nke-recipes \
  --name nkd-publication-release --name nkd-verification-release --dir <private-temporary-directory>
gh api repos/dsx-ai-factory/nke-recipes/environments/nkd-arc/deployment-branch-policies
gh api repos/dsx-ai-factory/nke-recipes/environments/nkd-release/deployment-branch-policies
```

The earlier failed attempt's [job metadata](2026-10-07-m2-validation-branch-release/failed-carrier-job.json)
and [ENOSPC annotation](2026-10-07-m2-validation-branch-release/failed-carrier-annotations.json)
were retrieved from job `112367504576` and its check-run annotations. Its full job-log endpoint returned
`404 BlobNotFound`; no missing transcript or measured disk usage is inferred.
