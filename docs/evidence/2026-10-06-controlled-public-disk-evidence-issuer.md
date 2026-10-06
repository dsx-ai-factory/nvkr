# Controlled public disk-evidence issuer: native ARM64

## Scope and source

Native ARM64 validation passed against committed executable/test source
`7d5ec85fa88c48cf25d331dc3bf17c4798111ba5`, tree
`22a5ba2e01eb9ee604715f5ae059e3d88b8f9787`. HEAD and all 719 Git-tracked
Go/module/version files were byte-identical before and after the run. The full
manifest SHA-256 is
`a6a58288f7ff110ed7b1df3910eba2ff667ff12ebb6abc0a71c7b98e2a86849a`;
the issuer test itself is
`31038f20ee7a35ea1b345ce2836081e48597f7d81e32ceb4216c038ad016e251`.
The manifest was generated from that commit by:

```sh
git -c core.fsmonitor=false ls-files '*.go' go.mod go.sum .go-version |
  xargs shasum -a 256
```

The local diagnostic manifest is
`/private/tmp/pr91-7d5ec85-public-issuer-source.sha256`. The only untracked file
was this evidence record. A later documentation-only commit is not the
executable-source snapshot exercised here.

The target is `TestDiskEvidencePublicRuntimeIssuer` plus
`TestDiskEvidencePublicRuntimeRejectsMemoryProfile` in
`pkg/nkd/factory/disk_evidence_runtime_linux_test.go`. A controlled native ELF
implements the daemon/inspection protocol, while existing public producer,
Packer-artifact and inspector owners authenticate the process-local receipt and
export. The test does not construct a receipt. Its packages/reports/installer and
512-byte QCOW2 header are synthetic; positive export checks the original held
artifact's subject/size and every exported payload byte.

This is neither real NKE discovery nor execution of Docker, Packer, guestfish or
Syft. It does not prove native AMD64, upstream origin, publication, signing,
reproduction, qualification or production acceptance.

## Reproduction

The existing local image was inspected as Linux ARM64, image ID
`266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`.
It is not a published repository pin or approved production inspector closure.
No pulls, installation, credentials, VM reconfiguration or HOME override are
required. Source/modules are read-only; scratch/cache are container-owned.

```sh
podman run --rm --pull=never --user 502:20 --entrypoint /bin/sh \
  --volume /Users/ikislenko/Workspace/github/nke-recipes/.git/checkouts/codex/m2-publication-pr:/src:ro \
  --volume /Users/ikislenko/go/pkg/mod:/go/pkg/mod:ro --workdir /src \
  --env GOTOOLCHAIN=local --env GIT_CEILING_DIRECTORIES=/src \
  --env NKD_TEST_DISK_EVIDENCE_RUNTIME=1 \
  266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6 \
  -c 'set -eu; date -u; uname -a; id; go version;
      getconf _NPROCESSORS_ONLN;
      sed -n "/^MemTotal:/p; /^MemAvailable:/p" /proc/meminfo;
      df -Pm /var/tmp;
      issuer_tmp=$(mktemp -d /var/tmp/nkd-b-public-issuer.XXXXXX);
      export TMPDIR="$issuer_tmp" GOCACHE="$issuer_tmp/go-cache";
      go test -race ./pkg/nkd/factory \
        -run "^TestDiskEvidencePublicRuntime(Issuer|RejectsMemoryProfile)$" \
        -count=1 -v -timeout 10m;
      go test -race ./pkg/nkd/sbom \
        -run "^Test(NestedOCIClosureIsRevalidatedAfterCompletedScanOperation|NestedMaterialCumulativeAdmission|RootArchiveImageMergeKeepsExactCarrierAndBudget|RootOrdinaryCandidatesDoNotExhaustNestedImageBudgets|RootSiblingArchivesAndDebianExportsShareExpansionBudget)$" \
        -count=1 -v -timeout 10m'
```

The run started at `2026-10-06 05:17:28 UTC`, process session 54685, and
terminated with exit 0. Startup reported Linux kernel
`6.15.10-200.fc42.aarch64`, user/group `502:20`, `go1.27.1 linux/arm64`, eight
online CPUs, MemTotal `16327424 kB`, MemAvailable `15661380 kB` and overlay
scratch available `245262 MiB`. These are point-in-time observations, not
capacity reservations. Each issuer row separately observed `linux/arm64`, CPU
`8000m` and scratch `244670 MiB` through `execution.ObserveLocalHost`; the
production runtime independently applied its resource/private-volume gates.
The inspection profile requested 9000 MiB RAM. No host-capacity fact was
substituted and the explicit opt-in skipped no rows.

## Public issuer results

Factory race tests passed in 6.501s; the issuer took 5.36s and the memory-profile
guard 0.13s. Every rejection produced no reconciliation receipt.

| Row | Checked boundary | Result | Observed RAM (MiB) |
| --- | --- | --- | --- |
| issued | Original receipt subject/size and three component documents; exact export; sibling and closed-owner export refused | PASS, 2.41s | 14787 |
| closed inspector | `CodeEngineCapability` | PASS, 0.45s | 14920 |
| closed original | `CodeVerifyFailed` | PASS, 0.49s | 14934 |
| wrong capture architecture | `CodeVerifyFailed` | PASS, 0.48s | 14904 |
| substituted report subject | `CodeVerifyFailed` | PASS, 0.50s | 14987 |
| missing installed correspondence | `CodeProvenanceMissing` | PASS, 0.54s | 14980 |
| cancel during root scan | `CodeTimeout`; cancellation goroutine joined | PASS, 0.49s | 15053 |
| memory profile | Concrete admitted inspector's private disk-backed scratch consumption guard rejects with `CodeEngineCapability` before scanning | PASS, 0.13s | Not separately logged |

## Separate SBOM regression scope

The same native container then passed the five selected SBOM race tests in
4.985s, without skips: completed-scan OCI closure revalidation (unchanged and
two changed-config rows), cumulative expansion/image/member admission, exact
root OCI/Docker carrier and budget preservation, ordinary candidates not
exhausting image allowances, and shared sibling OCI/Debian expansion limits.
These controlled archive/scanner-fixture tests do not establish real Syft or
guestfish discovery. The full source manifest remained identical after both
commands; no production source change is hidden by the documentation record.
