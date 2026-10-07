# PR #97 Linux review-fix contracts

On October 7, 2026, the review-fix working tree based on the previous PR #97 head
`a54f6a2dea2fb32b4eae8014af2d6adc802f667d` passed the following native Linux ARM64
race tests. These local results accompany those exact source changes; they do not establish
a GitHub workflow run or a real NKE disk build. The later hosted attempt is recorded separately below.
The separate `main` baseline was `d6cdf8dabb6bbda3af925ddb49eb16cde3eae8d9` (main/#106).

The existing local Podman VM reported `linux/arm64`. The test container used the
already-cached image `docker.io/library/golang@sha256:f44f6e88636cfb311f9ebace870ded69d943f227bb3cb27d32ffd84ea18c43ea`,
whose runtime reported `go version go1.27.1 linux/arm64`. It ran with `--init`,
read-only source/module mounts, `GOTOOLCHAIN=local`, and a separate build cache.

```sh
go test -timeout 10m -race ./pkg/nkd/factory \
  -run 'TestGitHubDiskCoordinator|TestGitHubDiskBudgetRejectsSummedAndInspectionBounds|TestGitHubCarrierPatternOrphan' \
  -count=1 -v
# PASS: factory 11.340s

NKD_TEST_DISK_EVIDENCE_RUNTIME=1 go test -timeout 10m -race ./pkg/nkd/factory \
  -run '^TestCompleteHostedDiskPublicationStrictPublicRuntime$' -count=1 -v
# PASS: test 1.13s; factory 2.148s
```

The mandatory Linux coordinator test exercises the original concrete Packer
diagnostic sink and controlled public producer owner: deadline and ordinary
failure publish the exact sealed route; successful completion does not publish
failure-only diagnostics. Portable cases cover cancellation, coincident failures,
already-coded timeouts, unavailable routes, summed budgets and orphan carrier patterns.

The separately enabled strict-completion test exercises receipt-gated packaging
over controlled bytes and checks the exact QCOW2 subject and component evidence.
Its ELF protocol is not Docker, Packer, guestfish, Syft or a real NKE bake.
Neither suite proves native AMD64 execution, full NKE inspection, registry
publication, hosted finalization or independent consumer acceptance.

## Later hosted attempt

October 7 GitHub read-back confirmed [run 37661490433](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37661490433),
attempt `1`, was manually dispatched at PR #97 head `562a609cb3939097fb6a6ad89a77f25aa739a0d5`
on `codex/m2-native-arc-foundation`, and completed with conclusion `cancelled`.
The [Environment at that exact source commit](https://github.com/dsx-ai-factory/nke-recipes/blob/562a609cb3939097fb6a6ad89a77f25aa739a0d5/definitions/nke-m2/environment-github.yaml)
temporarily admitted that exact dispatch branch and signing identity in addition to `main`.
Those source declarations did not change external deployment protection.

The full paginated jobs API reports all 26 jobs named `Generate SBOMs: ...` succeeded.
This is evidence for those generation jobs, distinct from the protected SBOM attestation jobs.
Both native disk jobs, [ARM64](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37661490433/job/112932239664)
and [AMD64](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37661490433/job/112932457193),
report `failure` with empty `steps` arrays. The [ARM64 check annotations](https://api.github.com/repos/dsx-ai-factory/nke-recipes/check-runs/112932239664/annotations)
state that `codex/m2-native-arc-foundation` is not allowed to deploy to `nkd-arc`.
The [SPIRE CRDs SBOM attestation job](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37661490433/job/112931037919)
and [finalizer](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37661490433/job/112933977061)
likewise report `failure` with no steps; their
[attestation](https://api.github.com/repos/dsx-ai-factory/nke-recipes/check-runs/112931037919/annotations) and
[finalizer annotations](https://api.github.com/repos/dsx-ai-factory/nke-recipes/check-runs/112933977061/annotations)
identify the same branch rejection on `nkd-release`.

Aggregate assembly, publication, signing and signature verification report `cancelled` with no executed steps.
The run therefore proves neither a native disk build at this head nor completed release delivery, signing,
hosted finalization or independent consumer acceptance. The current authored Environment removes the temporary
branch exception and restores main-only dispatch/signing; the `562a609...` declarations remain historical source
provenance for this rejected attempt. The Linux contract results above retain their original local scope.

The exact read-only evidence endpoints were:

```sh
gh api repos/dsx-ai-factory/nke-recipes/actions/runs/37661490433
gh api 'repos/dsx-ai-factory/nke-recipes/actions/runs/37661490433/jobs?per_page=100' --paginate
gh api repos/dsx-ai-factory/nke-recipes/check-runs/112932239664/annotations
gh api repos/dsx-ai-factory/nke-recipes/check-runs/112931037919/annotations
gh api repos/dsx-ai-factory/nke-recipes/check-runs/112933977061/annotations
```
