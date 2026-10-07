# PR #97 Linux review-fix contracts

On October 7, 2026, the review-fix working tree based on
`a54f6a2dea2fb32b4eae8014af2d6adc802f667d` passed the following native Linux ARM64
race tests. This record accompanies those exact source changes; it is not evidence
for a GitHub workflow run or a real NKE disk build.

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
