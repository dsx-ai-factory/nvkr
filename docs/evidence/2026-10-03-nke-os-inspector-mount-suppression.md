# Inspection mount suppression and issued inventory replay

This records a local patch based on `26093dfd47e2f0fe665adf5c4a2323051e7340d7`.
The source blobs below identify the tested implementation, not a later committed
head or original live factory Run. The already-local image and retained QCOW2
are the same exact subjects as the [earlier runtime record](2026-10-02-nke-os-inspector-runtime.md).

## Automatic mount boundary

A native Podman 5.6.2 exercise used temporary, nonsecret `mounts.conf` inputs
and the marker `NKD_SYNTHETIC_NONSECRET_MARKER`, without changing shared
configuration or reading real credentials. Container creation reported
`Mounts=[]`, but selecting the synthetic file at start exposed the marker.
Selecting `/dev/null` at start suppressed it. This confirms that create-time
mount observation alone cannot establish this boundary.

The native implementation is documented in
[Podman root flags](https://github.com/containers/podman/blob/v5.6.2/cmd/podman/root.go)
and its [subscription mounts](https://github.com/containers/podman/blob/v5.6.2/vendor/github.com/containers/common/pkg/subscriptions/subscriptions.go).
Disk inspection now supplies the existing native global override on create,
start and owned cleanup commands. Docker and the shared Packer runtime are
unchanged. An unsupported flag fails closed, without retrying unprotected.
This suppresses `mounts.conf`, not OCI hooks, FIPS mounts or a malicious daemon;
the configured runtime, daemon and kernel remain trusted.

## Exact tested source

| File | Git blob |
| --- | --- |
| `pkg/nkd/pipeline/disk_inspection_runtime.go` | `e21409058eb4b710c4689085b37d35ddd03f5956` |
| `pkg/nkd/pipeline/disk_inspection_runtime_test.go` | `f174737bbc15e47535a08f11fc63910cc84b6d7d` |
| `pkg/nkd/sbom/disk_inspection_runtime.go` | `5ca3504fda3dab6018e3ab84da5196ba02e542e1` |
| `pkg/nkd/sbom/disk_inspection_runtime_integration_test.go` | `5857692cebe94de81c570be8c3de6595aeefcc77` |

Static native ARM64 test binaries were compiled with `CGO_ENABLED=0 GOOS=linux
GOARCH=arm64 go test -c`, using the repository's Go version. Their SHA-256 values:

- SBOM test: `0a2f56178a4ade20e0199bc86c7895fa686133b0251bc297bfa2e6ef7a5726c6`.
- Pipeline test: `13e2f73d9e4fd2972d36852be9f94b058e36ce0c56f05c92a2b7afd7a5d0d3db`.

## Actual retained-disk results

The existing native Linux ARM64 VM executed both binaries with the explicit
image, inspector, codec, QCOW2 and digest environment inputs from the earlier
record, plus `NKD_TEST_DISK_INSPECT_RUNTIME=podman`. The selected image remained
`sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c`;
the retained disk remained `sha256:da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7`,
2,851,076,096 bytes. No image was rebuilt, pulled or published and the disk was
not modified.
The selected native `/usr/bin/podman` reports version 5.6.2 and
SHA-256 `fea01b6be0bcc416eeb480073ea14c9a09c5f4557e1b4456957fefff2f982378`;
the pipeline test held that same executable and observed the daemon identity
`sha256:7218aeb5d0da98276ca4c6783805c007ee52aa3d6a5b9a8a0c1b7a0c7f7b63df`.

`TestDiskInspectionRuntimeInventoryReal` passed in 43.22 seconds, issuing an
opaque SBOM installed inventory with 730 packages. It asserts the exact subject,
size, image, inspector and codec bindings and isolation of returned package
descriptions. A separate Go-overlay mutation that replaced the runtime image
binding with the inspector binding failed the intended assertion after a
successful scan, establishing that the new check detects a field swap.

`TestDiskInspectionRuntimeReal` passed in 41.29 seconds. Its cancellation
subtest first observed one running inspection container, then canceled and joined
the operation, required a timeout code and no observation, and confirmed bounded
container cleanup; it passed in 1.45 seconds. Held runtime and daemon identities
matched the earlier record.

Native ARM64 race contracts passed: pipeline 21.170 seconds, SBOM 8.830 seconds,
in the existing pinned Go image, network disabled, UID/GID 65534, init, two CPUs,
4 GiB and read-only source/module-cache mounts. Focused normal and race contracts
also passed, including Docker argument preservation, Podman overrides on every
operation and unsupported-flag rejection before copying input.

This is retained-disk runtime and SBOM-wrapper evidence, not an original live
factory build-to-inspection join, native AMD64 or Docker execution, complete disk
SBOM, publication, qualification or NVIDIA license/security clearance.
