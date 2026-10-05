# Native ARM64 launcher review replay

## Scope

This exercises a local patch based on `57b03cd24efdb0eb0d4f683bc42d4032b16745fc`.
The current host launcher requests `--log-driver=none`, validates the immutable
container ID returned by create, and uses that ID for attached interactive start.
Both retained-disk tests passed on the existing native ARM64 Podman VM: attach
delivered the package report with that log-driver setting. This is not acceptance
of every later committed source file, a rebuilt helper, native AMD64 or Docker
execution, an original live factory Run, full-disk CycloneDX, publication,
qualification, or NVIDIA license/security clearance.

The caller and local daemon remain trusted. The test binary ran as UID zero in
the native VM; its concrete launcher still fixed inspection UID/GID 65534,
network-none, read-only root, capability-drop ALL, no-new-privileges, init,
no requested mounts/devices/credentials, fixed resource limits and
`--pull=never`. No image was rebuilt, pulled or published. The retained disk
was read-only and its expected digest was checked by the held-file owner.

## Exact observed identities

| Measurement | Exact value |
| --- | --- |
| Native platform | `linux/arm64`, existing Podman VM |
| Runtime executable | `sha256:fea01b6be0bcc416eeb480073ea14c9a09c5f4557e1b4456957fefff2f982378` |
| Daemon identity | `sha256:7218aeb5d0da98276ca4c6783805c007ee52aa3d6a5b9a8a0c1b7a0c7f7b63df` |
| Local image | `sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c` |
| Unchanged helper ELF | `sha256:fa56af977873a433c44fd2256513b3dc27a7f1a02d1befadc59c49a6765c2046` |
| Inspector ELF | `sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b` |
| Codec ELF | `sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e` |
| Retained QCOW2 | `sha256:da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7`, 2,851,076,096 bytes |
| Pipeline native test binary | `sha256:d249377d6d3f4b08ca5af84e3dc73f98a8051fa2cfc1c47d5d4a44713ef44efa` |
| SBOM native test binary | `sha256:6a9c12159e4fa69ab5cba9ad56697588ee3a6a4e9625d98534f533f2d81ec562` |

The helper and tool hashes were rechecked in the exact local image using a
non-root, networkless, read-only, capability-dropped, no-mount container.
The helper's earlier source remains the
[Oct 2 source](2026-10-02-nke-os-inspector-runtime.md):
SBOM runtime blob `689812ec087ed19a0faebdbf0811e68e6b6f6308`,
inspector blob `dbe44f59722af9ce1d867c0a9fa6781a6feed448`, and command
blob `9a77cd9be52d61fe6b6ac158d6e0904452b01131`.
Later helper XML/package checks, subject holding and timeout classifications
did not execute inside that unchanged image.

Source blobs for the executed host runtime/integration paths:

| File | Git blob |
| --- | --- |
| `pkg/nkd/pipeline/disk_inspection_runtime.go` | `ac330c1c8ac6fd9a6c0983615f872e3dbd562a18` |
| `pkg/nkd/pipeline/disk_inspection_runtime_integration_test.go` | `fc97ba6c73c24aee9c0f9798bc2fd2aa0a488a58` |
| `pkg/nkd/pipeline/packer_container.go` | `ed0ef03d8b5a34bdb8be96af006308b846a2d89b` |
| `pkg/nkd/sbom/disk_inspection_runtime.go` | `6e8288f11a0780ab14573fe120804f285b09f778` |
| `pkg/nkd/sbom/disk_inspection_runtime_integration_test.go` | `b45e3b6d496e2b87c8d2598be18eda5ac6b00178` |

## Commands and terminal results

Using the repository's pinned Go version and `GOTOOLCHAIN=local`, compile
each test package with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c`.
Execute the measured binaries with `podman machine ssh` and these explicit
environment inputs, not ambient registry credentials:

```sh
NKD_TEST_DISK_INSPECT_RUNTIME=/usr/bin/podman
NKD_TEST_DISK_INSPECT_IMAGE=sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c
NKD_TEST_DISK_INSPECTOR_SHA256=sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b
NKD_TEST_DISK_CODEC_SHA256=sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e
NKD_TEST_DISK_SHA256=sha256:da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7
NKD_TEST_DISK_QCOW2='<nke-os-integration-worktree>/.nkd/runs/20260923-arm64-e2e-17/outputs/nke-node-image-arm64.qcow2'
```

Pipeline arguments: `-test.run '^TestDiskInspectionRuntimeReal$' -test.v
-test.timeout=12m`. SBOM arguments: `-test.run
'^TestDiskInspectionRuntimeInventoryReal$' -test.v -test.timeout=12m`.
Both test invocations exited zero without skipping:

```text
runtime-only: image=sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c subject=sha256:da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7 bytes=2851076096 packages=730 inspector=sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b codec=sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e
canceling a running inspection container: f190ac42f32df6c264b616e05dbb4f4c1614be1d97206b1a36d46580717b2302
--- PASS: TestDiskInspectionRuntimeReal (41.62s)
    --- PASS: TestDiskInspectionRuntimeReal/canceled_inspection_joins_and_removes_container (1.49s)
runtime-only SBOM inventory: selected=/usr/bin/podman image=sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c subject=sha256:da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7 bytes=2851076096 packages=730 inspector=sha256:e95525c7c92f9581c79c8ecc0893109519fbfbb4607a341bf473e942215bfd7b codec=sha256:f26da24f8a7fd880ab13f6fcabf42d099fb68ea86df5de0c55e6faadfe2d9a6e
--- PASS: TestDiskInspectionRuntimeInventoryReal (41.12s)
```

The cancellation case observed one running inspection container before canceling,
joined the operation, required a timeout code and no observation, and required no
attempt-labelled container after cleanup. The SBOM case checked exact subject,
size, image and tool bindings and caller-description mutation isolation.
This successful runtime replay does not reproduce a warning/no-ID late-registration
failure; that cleanup branch is covered separately by bounded fake-runtime
regressions, not asserted as externally exercised here.

