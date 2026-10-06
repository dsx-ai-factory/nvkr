# Native ARM64 content-reader validation

## Current OCI material correction

Current parent: `a9dfaedd861ad3f5deda6f1ecf076e4bf07ea015`. The tested
uncommitted snapshot below is identified by its unchanged pre/post-run hashes,
not by the later fix/documentation commit. It is a targeted correction, not
complete QCOW2 inspection or native AMD64 proof.

Native process 55917 reproduced the bug before the correction: the ARM64 helper
returned success for an AMD64 OCI configuration instead of
`NKD-E-VERIFY-FAILED`. The Debian fixture package's `all` architecture was not
proof of image platform. The fix reuses the existing complete nested OCI owner:
authenticate all held layout members/configurations before scanning, inspect
every native image, and revalidate the entire closure and original carrier
afterward. No new OCI parser, receipt or pathname authority was introduced.

| Source | SHA-256 |
| --- | --- |
| `pkg/nkd/sbom/disk_content_runtime.go` | `f1b7442a2e2324661b664fd4a06f829fc4e75da987e483ab4ec3ba28bf0a906f` |
| `pkg/nkd/sbom/disk_oci_material_test.go` | `87c04292c0a28a1f4d7cbf558da672cadd5f8ccbf8c2c9fac35241db2e5d0855` |
| `pkg/nkd/sbom/disk_scanner_integration_test.go` | `729483e34b864c1f05c7b89709855b2dee56150b01ae5fd2883829174efc9914` |
| `pkg/nkd/sbom/disk_content_runtime_integration_test.go` | `fa9ddd0b919987d509ac3c00de7a3b7e41943f745dc7e06a06ec1429e873475f` |

The build/run command below was repeated using
`content_root=/private/tmp/nkd-b-content-validation.AQ6yWF`. Build process 82877
and race process 95404 exited 0. Race binary SHA-256:
`f48ae3836c9401da5d75d2254149ba2cbce3af29145144ff752946dc34521ee6`.
The final run additionally selected `TestDirectOCIMaterialAuthenticatesClosureBeforeScanner`
and supplied these measured helper tool pins:

```sh
--env NKD_SBOM_INSPECTOR_DIGEST=sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b \
--env NKD_SBOM_CODEC_DIGEST=sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e \
--env NKD_SBOM_GUESTFISH_DIGEST=sha256:db1f245febce9e588902858806fc63b7878cc2c02ad9651e9a87102bbebe2feb
```

Exact final test selector:

```sh
-test.run '^(TestDiskContentPinnedScannerReal|TestDiskCapturedPackagesPinnedToolsReal|TestDirectOCIMaterialAuthenticatesClosureBeforeScanner)$' -test.v -test.timeout 5m
```

All selected rows executed without skips in the existing native ARM64 image,
with the same read-only/nonroot/network-disabled/4-GiB limits documented below:

| Scope | Result | Seconds |
| --- | --- | --- |
| Both declared platforms: native/multiple native/foreign/mixed membership, missing/corrupt/extra blobs, mixed transports (16 controlled rows) | PASS | 0.10 |
| Generated Docker transport, actual Syft | PASS | 0.64 |
| Held Docker transport, actual Syft | PASS | 0.63 |
| Plain nested image, actual Syft | PASS | 0.62 |
| Gzip nested image, actual Syft | PASS | 0.64 |
| Native ARM64 OCI helper, exact package/class/carrier/tool/platform bindings | PASS | 0.74 |
| Foreign AMD64 configuration rejected without a report | PASS | 0.11 |
| Actual two-package Debian capture and metadata join | PASS | 2.03 |
| Substituted sibling package metadata rejected | PASS | 2.01 |

Controlled AMD64 configuration rows are not native AMD64 execution. The native
OCI success row retains exact `nkd-scanner-fixture` version `2:1.0-1ubuntu4`,
architecture `all`, container class and original held subject.

`TestDiskContentRuntimeReconciliationReal` is a separate, unexercised opt-in
through `NKD_TEST_DISK_CONTENT_MANIFEST`. Its bounded exact JSON selection has
`runtime`, native `platform`, existing typed `profile`, `buildDigest`, `output`,
and `inputs` containing `kind`, `path`, `digest`. It requires a root, installer,
complete capture and staged inputs; uses the existing public admitted runtime
and held-content APIs; scans twice; reverses material order; compares canonical
CycloneDX; and rejects missing evidence, cancellation and closed runtime.
Run separately on each genuinely native Linux host with a complete selection:

```sh
NKD_TEST_DISK_CONTENT_MANIFEST=/absolute/complete-selection.json \
  go test -race ./pkg/nkd/sbom -run '^TestDiskContentRuntimeReconciliationReal$' -count=1 -v -timeout 3h
```

The explicit test deadline budgets two complete matrix passes; it does not
extend any authenticated per-operation profile limit (at most 600 seconds).
It is a manual diagnostic budget, not proof that every possible matrix fits it.

No complete retained matrix was supplied or run. Caller paths/pins cannot
restore original live PackerArtifact/Run or publication authority.

## Historical nested-reader measurement

Observed October 6, 2026, before 15:16 UTC. This historical run is targeted content correctness evidence,
not complete QCOW2 inspection, original-Run reconciliation, hosted acceptance or publication approval.

## Exact source and runtime

Base source: `4615b3510a295207dbdbb03f9b4561a601a58512`. The only Go delta is the
plain/gzip nested-image regression in `pkg/nkd/sbom/disk_scanner_integration_test.go`;
its SHA-256 was unchanged before and after execution:
`fcd83f8d9205662dd05fb89ce2e6fb69e618939fc1f550b8879d35dca4d2f2e4`.
This identifies an uncommitted test snapshot, not a later commit.

The existing local inspector image ID was
`6fe5b341957b02b266610fdec2b7fdb11403a8f0160d7b5b03fcda8c76d8e15d`,
observed as `linux/arm64`. Actual Podman host information reported Linux ARM64,
8 CPUs and 16,719,282,176 bytes total memory. These are observed local facts, not
an issued hosted execution class or a resource reservation. The image contains
an older helper; these tests call the current test binary and selected tools,
not that helper's full-content entrypoint.

Measured tool SHA-256 values from that local image:

| Executable | SHA-256 |
| --- | --- |
| `/usr/local/bin/syft` | `617cef943234e0cae184b4901414ca4059ad6c6923a9c9decb46f49f6a2c6573` |
| `/usr/bin/dpkg-deb` | `3cf93403af757e938bda51322774697b8ff1e1d030b333008b2308e0b79a8dd6` |

These match the selected ARM64 profile tool fields. A local image ID does not
establish equality with a published repository manifest or authenticate the
complete profile.

## Repeated native real-tool runs

The existing local Go image
`266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`
built the race binary natively with Go 1.27.1, read-only source/module mounts,
user 502:20, network disabled and private scratch. No image pull, installation,
HOME override or VM configuration change was performed.

```sh
content_root=/private/tmp/nkd-content-validation.U4DERz
content_source=/Users/ikislenko/Workspace/github/nke-recipes/.git/checkouts/codex/disk-inspection-validation
podman run --rm --pull=never --network none --read-only \
  --user 502:20 --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,nosuid,nodev,size=2g \
  --volume "${content_source}:/src:ro" \
  --volume /Users/ikislenko/go/pkg/mod:/go/pkg/mod:ro \
  --volume "${content_root}:/validation:rw" --workdir /src \
  --env GOTOOLCHAIN=local --env GIT_CEILING_DIRECTORIES=/src \
  --env GOCACHE=/validation/go-cache --entrypoint go \
  266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6 \
  test -race -c -o /validation/sbom-race.test ./pkg/nkd/sbom

podman run --rm --pull=never --network none --read-only \
  --cap-drop ALL --security-opt no-new-privileges --user 65534:65534 \
  --cpus 2 --memory 4g --pids-limit 256 --tmpfs /tmp:rw,nosuid,nodev,size=1g \
  --volume "${content_root}/sbom-race.test:/nkd-validation/sbom-race.test:ro" \
  --env NKD_SBOM_SCANNER=/usr/local/bin/syft \
  --env NKD_SBOM_SCANNER_DIGEST=sha256:617cef943234e0cae184b4901414ca4059ad6c6923a9c9decb46f49f6a2c6573 \
  --env NKD_SBOM_PACKAGE_READER_DIGEST=sha256:3cf93403af757e938bda51322774697b8ff1e1d030b333008b2308e0b79a8dd6 \
  --entrypoint /nkd-validation/sbom-race.test \
  6fe5b341957b02b266610fdec2b7fdb11403a8f0160d7b5b03fcda8c76d8e15d \
  -test.run '^(TestDiskContentPinnedScannerReal|TestDiskCapturedPackagesPinnedToolsReal)$' \
  -test.v -test.timeout 5m
```

Race binary SHA-256:
`e79d8f0d748347c21a6ad98748cfc7f17c1dd588bff912b4939b13a1ea392e88`.
Native race execution (process 91497) exited 0 with no skipped cases:

| Case | Result | Seconds |
| --- | --- | --- |
| Generated Docker transport, actual Syft | PASS | 0.53 |
| Held Docker transport, actual Syft | PASS | 0.54 |
| Plain nested image, exact carrier and container class | PASS | 0.54 |
| Gzip nested image, exact carrier and container class | PASS | 0.53 |
| Actual Debian package capture and metadata join | PASS | 1.57 |
| Substituted sibling package metadata rejected | PASS | 1.57 |

The first four cases retain package `nkd-scanner-fixture`, exact version
`2:1.0-1ubuntu4`, architecture `all`. The plain carrier was 4,096 bytes,
SHA-256 `ea671f461292ca15681c208167fa7c6c5f858ba2aa1f05230be7ed8e0c091591`;
the gzip carrier was 749 bytes,
SHA-256 `b9a47ef3f0c99fbed535249f254619916fd22dadac82a14f9db47a7e43409875`.
The nested owner reported container package class, exact scanner/carrier binding
and no invented root-file inventory. Both captured Debian packages were built
and read with actual dpkg-deb. A preceding non-race native run (79663) also
passed these six cases; this is not deterministic full-document reproduction evidence.

Separately, the existing both-architecture exact-correspondence, unknown-version,
host/container separation, missing/substituted evidence and contained-carrier
contract tests passed with host `go test -race` (1.336 seconds). These are modeled
receipt tests, not native AMD64 runtime proof. Scoped `go vet` and `git diff --check`
passed; the integrated repository gate remains a separate requirement.

## Retained inputs and remaining acceptance

The documented retained ARM64 disk was historically rehashed from
`.git/checkouts/codex/nke-os-integration/.nkd/runs/20260923-arm64-e2e-17/outputs/nke-node-image-arm64.qcow2`:
2,851,076,096 bytes, SHA-256
`da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7`.
Its original live Run ownership is not available through that pathname.

The documented 30,720-byte diagnostic APT carriers were historically rehashed:
ARM64 `684ea432b9002ebb14fefcf81920e644dbd92d3afa7de8b24636f203d9e5504c`;
AMD64 `4a23f41797a0a762a410f45d5725ca65e76377face354d8e62a67d82b05cc0f5`.
Each covers only the earlier `hello` acquisition fixture; neither is established
as the complete retained disk's acquisition archive.

The current full-inspection profiles request 28 GiB memory, above the observed
local 16 GiB VM. No full-profile admission or complete root/ISO/SquashFS scan is
claimed. The documented AMD64 host `omnistation-omni-lsn-h712d` did not resolve
under a current BatchMode SSH probe; native AMD64 access remains unverified.
Both native full-content runs, complete original material/capture correspondence,
repeated canonical evidence, original-owner receipt-gated packaging, fresh helper
image measurement and authorized hosted validation remain required by
[ADR-13](../adr/0013-supply-chain-evidence.md). Retained sizes were stat-checked
again in the current task; the historical hashes above are not freshly
measured current pins. The opt-in harness and controlled tests do not prove
that remaining acceptance.
