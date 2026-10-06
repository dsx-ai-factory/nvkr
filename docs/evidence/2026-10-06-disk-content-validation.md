# Native ARM64 OCI material content validation

Observed October 6, 2026. This is targeted content correctness evidence, not
complete QCOW2 inspection, native AMD64 execution, original-Run reconciliation,
hosted acceptance or publication approval. It supersedes this file's earlier
unretained run narrative; no historical RED transcript is reconstructed.

The later [parent integration record](2026-10-06-disk-content-parent-integration.md)
binds the combined diagnostics/OCI snapshot and its new native runs separately.

## Exact tested source and retained output

Base commit: `edc57d067d26d8b5681cd69f4cb0a58103028067`, with the review-fix
snapshot below. These ten Go-file hashes cover every changed package source/test
compiled for this PR, including the three callers omitted from the prior record.
They were equal before and after the native race run; a later documentation
commit is not presented as tested executable source.

```text
e2de9f8ebb2d24cbac09c5e2a0d74050cadbbd7cb2f1455af2aa460bd2bc2aad  pkg/nkd/sbom/disk_content_runtime.go
a8169ad5e64055a7c44ee93c3ad21a437aabfe6a725f306d3eff40e6379b9902  pkg/nkd/sbom/disk_material_oci.go
cfacee701b9fa9a9333a56c1e4ebe5013fba24107950f32cf06a5ea8efce3efe  pkg/nkd/sbom/disk_materials.go
d2ea5785bc01239b9f3f2a43efad1cff28d205b39f67d3ad4d5a04cb572f2fef  pkg/nkd/sbom/inspect.go
b1073f13c7e4ae963cd9d71da0326fead839dc838a9f80750d6e1343552312e7  pkg/nkd/sbom/disk_oci_material_test.go
310c4a6fd935d24f99670cc3e68b3eecd07f375d978ffe73e1e721ddebc4fe53  pkg/nkd/sbom/disk_progress_test.go
8560d77157f5c7c29c8ae0e1eefd65f5e3bd3fec2c865735231a577c88c8c07c  pkg/nkd/sbom/disk_scanner_integration_test.go
510aa41cbcfcb964fff8539479ea765b756512ce31cd46fdd38b3dc7d04a5681  pkg/nkd/sbom/disk_scanner_location_test.go
bbaeebbb2c96c39c5313d5e3d522c2229cd3cadffe815e6aabdbd50ca3ff2e89  pkg/nkd/sbom/disk_materials_test.go
79d42e5fad1b4627b8eee8b04bfe688517dcd0318acb1f0688f76e02cd1f1985  pkg/nkd/sbom/disk_content_runtime_integration_test.go
```

The native race binary was built with `-trimpath`, Go 1.27.1 and SHA-256
`cedcc7d3af281c74ca08dfaa7a056a5db75132ff29d1834e0819d69d1003c818`.
The exact commands below built and ran that binary; a repeat of the parameterized
build returned the same binary hash without changing source.
The [verbatim native output](2026-10-06-disk-content-validation.log) ends in
`PASS`; SHA-256:
`2549447775fcfaf5b1491a5b3131b59d4c294477b995ce91b550725d3ea4678f`.
It contains every selected row and startup facts. Build/run exited 0. No selected
test was skipped. Historical process IDs and unretained logs are not current
acceptance evidence.

## Runtime and selected tools

Actual Podman host observation: Linux ARM64, 8 CPUs, 16,719,282,176 bytes memory.
The test image reported `Linux aarch64`, UID/GID 65534:65534 and visible leaf
cgroup values CPU `200000 100000`, memory `4294967296`, PIDs `256`.
These are observations, not a provider reservation or authenticated hosted
execution class. No image pull, installation, HOME override or VM configuration
change occurred.

Both already-local images were observed `linux/arm64`:

- Go image: `266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`.
- Inspector image: `6fe5b341957b02b266610fdec2b7fdb11403a8f0160d7b5b03fcda8c76d8e15d`.

The inspector contains an older helper. The tests execute current compiled Go
code with these pinned tools, not that older helper executable. Local image
identity does not prove equality with a published inspection profile closure.

| Tool | Executable SHA-256 |
| --- | --- |
| virt-inspector | `e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b` |
| qemu-img | `f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e` |
| guestfish | `db1f245febce9e588902858806fc63b7878cc2c02ad9651e9a87102bbebe2feb` |
| Syft | `617cef943234e0cae184b4901414ca4059ad6c6923a9c9decb46f49f6a2c6573` |
| dpkg-deb | `3cf93403af757e938bda51322774697b8ff1e1d030b333008b2308e0b79a8dd6` |

## Exact native commands and outcomes

Executed from the inspection-validation checkout:

```sh
content_source="$(pwd -P)"
content_modules="$(go env GOMODCACHE)"
content_root=/private/tmp/nkd-b-review-validation.PV8Ygz
podman run --rm --pull=never --network none --read-only --user 502:20 --cap-drop ALL --security-opt no-new-privileges --tmpfs /tmp:rw,nosuid,nodev,size=2g --volume "${content_source}:/src:ro" --volume "${content_modules}:/go/pkg/mod:ro" --volume "${content_root}:/validation:rw" --workdir /src --env GOTOOLCHAIN=local --env GIT_CEILING_DIRECTORIES=/src --env GOCACHE=/validation/go-cache --entrypoint go 266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6 test -race -trimpath -c -o /validation/sbom-race.test ./pkg/nkd/sbom
```

```sh
podman run --rm --pull=never --network none --read-only --cap-drop ALL --security-opt no-new-privileges --user 65534:65534 --cpus 2 --memory 4g --pids-limit 256 --tmpfs /tmp:rw,nosuid,nodev,size=1g --volume /private/tmp/nkd-b-review-validation.PV8Ygz/sbom-race.test:/nkd-validation/sbom-race.test:ro --env NKD_SBOM_SCANNER=/usr/local/bin/syft --env NKD_SBOM_SCANNER_DIGEST=sha256:617cef943234e0cae184b4901414ca4059ad6c6923a9c9decb46f49f6a2c6573 --env NKD_SBOM_PACKAGE_READER_DIGEST=sha256:3cf93403af757e938bda51322774697b8ff1e1d030b333008b2308e0b79a8dd6 --env NKD_SBOM_INSPECTOR_DIGEST=sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b --env NKD_SBOM_CODEC_DIGEST=sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e --env NKD_SBOM_GUESTFISH_DIGEST=sha256:db1f245febce9e588902858806fc63b7878cc2c02ad9651e9a87102bbebe2feb --entrypoint /bin/sh 6fe5b341957b02b266610fdec2b7fdb11403a8f0160d7b5b03fcda8c76d8e15d -c 'uname -sm
id
cat /sys/fs/cgroup/cpu.max /sys/fs/cgroup/memory.max /sys/fs/cgroup/pids.max
exec /nkd-validation/sbom-race.test -test.run "^(TestDirectOCI.*|TestOCIMarker.*|TestContainerScanProgress|TestDiskContentPinnedScannerReal|TestDiskCapturedPackagesPinnedToolsReal|TestNestedOCI.*)$" -test.v -test.timeout 5m'
```

| Selected scope | Observed result |
| --- | --- |
| Top-level OCI closure table: 13 cases for each declared platform | All 26 PASS, 0.11 s |
| Nil/empty/size/budget/platform guard table | All 8 PASS; no progress or scanner child |
| Copy/marker/extraction/per-image sealing cancellation | All 4 PASS with `CodeTimeout` |
| Marker cancellation and inner/caller timeout mapping | PASS |
| Scanner-ready cancellation, success/failure/admission diagnostics | All 4 PASS, 0.03 s |
| Existing nested OCI platform/membership/dedup/closure-revalidation contracts | All selected rows PASS |
| Actual Syft: generated transport / original held carrier / plain nested / gzip nested / native OCI helper | Five PASS, 0.72 / 0.84 / 0.99 / 0.73 / 0.78 s |
| Foreign AMD64 configuration on ARM64 helper | Pre-scan rejection PASS, 0.15 s; no report |
| Actual dpkg/Syft capture join / sibling metadata substitution | Two PASS, 1.97 / 1.97 s |

There are **seven real-tool execution rows plus one helper rejection**, not eight
real-tool executions. The OCI helper success asserts original subject/size,
operation, native platform, all five tool pins, filesystem bound and exact
`nkd-scanner-fixture` version `2:1.0-1ubuntu4`, architecture `all`, container
class. Declared AMD64 rows are protocol cases on ARM64, not native AMD64 proof.
The foreign configuration is rejected before any scanner, and role-selected
manifest/config mutations, mixed plain markers, Docker-only and gzip inputs
assert the owning rejection message rather than only its error code.

## Repository check outcomes

The same frozen executable snapshot passed the complete native Linux ARM64
race/coverage suite (exit 0), using the already-local complete-tools image
`1f87906be577585bff2bc2bf05662972bd9ddd2ddae29975f05e0501090d3abb`.
Actual startup reported Go 1.27.1, jq 1.7 and Linux aarch64, UID/GID 502:20.
Scratch was disk-backed overlayfs; source/modules remained read-only. Only the
ignored `.nkd` output directory was mounted writable for the lifecycle fixture.
No tool installation, pull, network access or test deadline change was used.

```sh
content_source="$(pwd -P)"
content_modules="$(go env GOMODCACHE)"
content_outputs="$(mktemp -d /private/tmp/pr103-native-ignored-output.XXXXXX)"
podman run --rm --pull=never --network none --user 502:20 --entrypoint /bin/sh --volume "${content_source}:/src:ro" --volume "${content_modules}:/go/pkg/mod:ro" --volume "${content_outputs}:/src/.nkd:rw" --workdir /src --env GOTOOLCHAIN=local --env GIT_CEILING_DIRECTORIES=/src 1f87906be577585bff2bc2bf05662972bd9ddd2ddae29975f05e0501090d3abb -c 'validation_root=$(mktemp -d /var/tmp/pr103-complete-tools.XXXXXX) || exit; export TMPDIR="$validation_root" GOCACHE="$validation_root/cache"; uname -sm; id; go version; jq --version; stat -f -c "scratch-type=%T blocksize=%S availableblocks=%a" "$validation_root"; go test -race -covermode=atomic -coverprofile="$validation_root/coverage.out" ./... -count=1'
```

The [complete native output](2026-10-06-disk-content-validation-full-race.log)
retains every package result; SHA-256
`40a8376f276607dc9602bd8d028ec984ae8251d13d2378b83d02e20021cbdab6`.
Factory / lifecycle / CLI / SBOM / pipeline completed in
218.268 / 44.183 / 57.221 / 80.267 / 25.890 seconds. This is the ordinary
contract suite; opt-in external tests are not implied by package PASS.

The full tracked Go/module/version manifest was equal before and after:
`329134e8cd3424535c42ec5e52cf0e3a26c43c91519bd7338139866ba26f945d`.
It was computed with:

```sh
git -c core.fsmonitor=false ls-files '*.go' go.mod go.sum .go-version | xargs shasum -a 256 | shasum -a 256
```

Host `make check` passed formatting, vet, lint (0 issues) and shellcheck, but its
factory race package reached the default 10-minute whole-package timeout.
The running publication test had taken 28 seconds; its stack was at Darwin
`File.Sync`. This does not prove that Sync blocked for 10 minutes or establish a
root cause. The isolated publication race test subsequently passed (67.27
seconds); the independent native full factory run passed (198.376 seconds).
The first native all-package attempt failed only on missing `jq` in the smaller
Go image and read-only `.nkd` output. Those harness prerequisites, not tests or
production code, changed for the successful complete-tools run above.

Host `make vulncheck` exited 0 with no vulnerabilities found. Host
`make build workflows-check` and `git diff --check` passed; the generated Run
remained `sha256:2c2bf59ad5b300c55da19e2068006562f8ddf60b8899444dbdfd88cc058081f2`.
These separate results are not described as a macOS `make check` PASS.

## Contract and operational limits

Top-level disk `oci-image` materials admit only bounded **plain OCI** layouts.
Docker/gzip/`./`-prefixed/foreign or unknown-platform attestation/malformed
carriers fail closed with no partial inventory. Mixed OCI/Docker markers return
`CodeEngineCapability`, matching nested discovery. Every descriptor/config is
checked before each unique image is scanned through a bounded private Docker
projection; complete closure and original held bytes are rechecked afterward.
The original carrier remains the observation subject.

Copy, extraction, closure verification and per-image transport diagnostics are
distinct from actual scanner execution. Per-image `scanner-transport` records
carry an ordinal; `root-scan` remains ordinal 0 and describes actual scanner
work only. Instrumentation grants no authority. Cancellation retains
`CodeTimeout`; the synchronized scanner row observes child readiness before
canceling and joins its test-owned goroutine. Private scanner admission uses
`CodeUnverifiedRef` and tests its exact authentication reason. The public helper
authenticates all five tool pins before content work.

The direct lane's byte ceiling applies **per sequential copy/projection**, not
as cumulative expansion accounting. Shared member/image limits still apply.
The 16,384-component report limit is enforced before public deduplication; a
shared-base layout can fail even when its unique inventory would fit.
Multi-GB material timing remains **unmeasured**. KB-scale fixtures do not prove
completion within the authenticated per-operation deadline. No performance
redesign, package removal or partial report was used to bypass these limits.

## Full-content harness and remaining acceptance

`TestDiskContentRuntimeReconciliationReal` is separate and **unexercised** here.
Its explicit `NKD_TEST_DISK_CONTENT_MANIFEST` selection has native `platform`,
`runtime`, typed `profile`, `buildDigest`, `output`, and input
`kind/path/digest` descriptions. It requires a root, installer, capture and
staged inputs, uses existing public admitted runtime/held-content APIs, scans
twice, reverses material order and compares canonical CycloneDX. Its cancellation
row is **pre-operation admission**, not in-flight helper cancellation.

Every input is snapshotted and held across both passes. A diagnostic host needs
memory for the aggregate retained input bytes **in addition to** the runtime's
admitted profile; profile capacity does not cover these caller-owned snapshots.
Current selected full-inspection profiles request 28 GiB, above the observed
local host memory. No complete original capture/material matrix or native AMD64
runtime was established by this record.

```sh
NKD_TEST_DISK_CONTENT_MANIFEST=/absolute/complete-selection.json \
  go test -race ./pkg/nkd/sbom -run '^TestDiskContentRuntimeReconciliationReal$' -count=1 -v -timeout 3h
```

The test deadline budgets two matrix passes, not a relaxation of the authenticated
per-operation limit (at most 600 seconds), nor a guarantee that all matrices fit.
Caller paths/pins cannot restore original PackerArtifact/Run/publication authority.
Both native full-disk runs, complete material/capture correspondence, original
live joins, fresh helper-image measurement and authorized hosted validation
remain unproven under [ADR-13](../adr/0013-supply-chain-evidence.md).
