# Inspector review-fix validation

This exercise validates an uncommitted local patch based on
`ea6d3df94014d01a98757ddf2ee98dea6ead1e49`. It is not acceptance of a new
committed PR head. The original review and historical runtime measurements
remain separate.

The patch fixes attempt-owned container cleanup and timeout-code preservation,
revalidates helper package metadata on the host using the existing XML rules,
and adds report-binding regressions. The strengthened real cancellation test
observes a running inspection container before canceling and joining cleanup.

## Host-side and contract-test source

| File | Git blob |
| --- | --- |
| `pkg/nkd/pipeline/disk_inspection_runtime.go` | `52334a7275503097f2364844a302b1c4b5816bbf` |
| `pkg/nkd/pipeline/disk_inspection_runtime_integration_test.go` | `f08af7f22df27646762340cd32ad7bca7ee1bbbe` |
| `pkg/nkd/pipeline/disk_inspection_runtime_test.go` | `d852e87460f508761618f397263ed417387c5212` |
| `pkg/nkd/sbom/disk_inspection_runtime.go` | `46f44574ae81362797910e791eee86213c3291f6` |
| `pkg/nkd/sbom/disk_inspection_runtime_test.go` | `bef9acd1e2cc944d79992279100abb3dd316b124` |
| `pkg/nkd/sbom/disk_inspect.go` | `7080ec6acb18f4d5aa9bb16eb0ff15dc7f739a93` |

These are the source fingerprints reported for the local patch and contract
tests. They do not identify the helper in the reused image. The SBOM runtime
blob `46f44574ae81362797910e791eee86213c3291f6` is not recoverable from the
repository object store at this correction, so its contents cannot now be
independently checked against that historical fingerprint.

The native ARM64 test binary was built with
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c ./pkg/nkd/pipeline`.
Its SHA-256 is `ac94cb69374c417d3bfc5e31912987c30f271cb34f4a08206a7e5ed235628ca6`.

## Actual runtime result

The existing native Linux ARM64 Podman VM and exact already-local inspector
image `sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c`
were used. The retained read-only QCOW2 was
`sha256:da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7`,
2,851,076,096 bytes. Inspector, codec, held runtime and daemon identities match
the [historical closure record](2026-10-02-nke-os-inspector-runtime.md).

No inspector image was rebuilt. The executed ARM64 helper ELF remained
`sha256:fa56af977873a433c44fd2256513b3dc27a7f1a02d1befadc59c49a6765c2046`,
with the earlier helper sources:

| In-image helper source | Git blob |
| --- | --- |
| `pkg/nkd/sbom/disk_inspection_runtime.go` | `689812ec087ed19a0faebdbf0811e68e6b6f6308` |
| `pkg/nkd/sbom/disk_inspect.go` | `dbe44f59722af9ce1d867c0a9fa6781a6feed448` |
| `cmd/nkd-disk-inspect/main.go` | `9a77cd9be52d61fe6b6ac158d6e0904452b01131` |

The real pipeline test exercised host-side runtime ownership and cancellation
against this older helper. It did not exercise the later in-image XML/package
validation or helper timeout classification. Local and native contract tests of
those functions are not proof that the changed helper ran in the inspector image.
This historical run also predates `--log-driver=none` and attached start by the
validated create ID. Their separate native ARM64 replay is recorded on
[Oct 5](2026-10-05-nke-os-inspector-launcher.md).

`TestDiskInspectionRuntimeReal`, with its explicit pinned environment inputs
and `-test.v -test.timeout=12m`, completed with exit zero:

```text
runtime-only: packages=730 bytes=2851076096
canceling a running inspection container: 4ef63e8bf0fe295549ef97fd737f0622d458e5f9bf6ef367908d8cb71f15fc43
--- PASS: TestDiskInspectionRuntimeReal (37.98s)
    --- PASS: TestDiskInspectionRuntimeReal/canceled_inspection_joins_and_removes_container (1.33s)
PASS
```

The cancellation subtest required an initially isolated inspector daemon,
observed one running container, canceled its owned inspection context, joined
the inspection goroutine, required a timeout code and no observation, and
queried the daemon with a bounded context to confirm no inspection container
remained. No guest image was modified or rebuilt.

## Contract gates and limits

The local `make check` and `make build` passed: formatting, vet, lint with zero
issues, ShellCheck, the full race suite with 70.8% statement coverage, and
vulnerability scanning with no vulnerabilities found. Focused report/cleanup/
timeout regressions passed.

Native Linux ARM64 pipeline and SBOM race suites passed in the existing pinned
Go image `sha256:266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`,
with network disabled, an init reaper, UID/GID 65534, two CPUs and 4 GiB memory:
pipeline `10.625s`, SBOM `9.494s`.

This is retained-disk runtime evidence, not an original live factory Run join,
a native AMD64 exercise, full-disk CycloneDX, publication, license/security
clearance or qualification. Host-side package validation rejects malformed
metadata; it cannot independently prove package-manager truth after a
compromised parser.
