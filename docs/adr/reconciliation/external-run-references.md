## External run references

The references above identify exact Runs and archived observations with their source, lane, and limits.
Raw records belong with CI or release artifacts; earlier investigations remain in Git history.
Immutable source links intentionally pin different commits to preserve each observation's historical snapshot.
Run `37533806963` at `625980e` records the 34-output candidate, including both disk outputs and aggregate
finalization. Its branch exception and `build-provenance-v1` mode do not establish independent full-payload
disk consumption, strict inspection, qualification, or organizational acceptance. Historical layout or
source success never establishes a current source, policy, full inventory, or qualified result.

On 2026-10-12, a local controlled Debian inspector API fixture run used the implementation at
[source commit `28443004ad73ac6c69df60e10e032f6ca0cb209c`](https://github.com/dsx-ai-factory/nvkr/blob/28443004ad73ac6c69df60e10e032f6ca0cb209c/pkg/nvkr/sbom/debian_dependencies_integration_test.go).
This is a local diagnostic observation, not an authenticated factory Run or approved toolchain. The native
Linux ARM64 test executable was built with Go 1.27.2. The container used
`ubuntu@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3`,
UID/GID `65534:65534`, `--read-only --network none --cap-drop ALL --security-opt no-new-privileges`,
a read-only tool/test mount, and `--tmpfs /tmp:rw,nosuid,nodev,exec,size=256m`.
Syft v1.51.1 ARM64 archive integrity matched the repository disk-inspector source pin
`sha256:a7fd2b784e6664acd44719270574f6cd8c6864fc2b1700bf9099bd1cccda7d7f`;
its measured executable was `sha256:617cef943234e0cae184b4901414ca4059ad6c6923a9c9decb46f49f6a2c6573`.
The runtime's `dpkg-deb` 1.22.6 executable was
`sha256:3cf93403af757e938bda51322774697b8ff1e1d030b333008b2308e0b79a8dd6`.
These native diagnostic measurements are separate from the factory's scanner pin.

| Controlled fixture | SHA-256 of archive bytes | Length |
|---|---|---|
| gzip | `ebf6ec1ed3830fe24ffdc9bd2339d8f427a5dc597ad4295224ac9da8586ef49a` | 612 bytes |
| xz | `ee9bc9e457951f3510c72a564ff09e31095ac187b10e3e935dc5380d8a6f2563` | 748 bytes |

Both fixtures yielded `pkg:deb/nvkr-debian-artifact-fixture@1.0.0?arch=arm64`. Pre-scan digest binding,
original-fixture size stability, and non-archive bytes under a `.deb` hint were checked. This does not
establish corrupt-payload rejection, complete embedded dependencies, source-created package delivery,
signing, publication or installation. This historical revision exercised the filename-selected branch of
`InspectDependencies`; it did not exercise the current separate `InspectDebianArchiveMetadata` API,
the later diagnostic method, same-byte no-hint control or dependency BOM rejection guards.
Native amd64 execution has not been recorded.

On 2026-10-12, a separate local native Linux ARM64 run exercised the exact implementation at
[source commit `8659cd28e5e615a8f490d0a302a419f329edc902`](https://github.com/dsx-ai-factory/nvkr/blob/8659cd28e5e615a8f490d0a302a419f329edc902/pkg/nvkr/sbom/debian_dependencies_integration_test.go).
The Go 1.27.2 test executable was compiled before subsequent API edits. This run used the same Ubuntu image,
Syft archive/executable and `dpkg-deb` executable identities, isolation flags and temporary-filesystem bound
listed above, plus a read-only repository mount at `/workspace` with working directory
`/workspace/pkg/nvkr/sbom` for catalog test fixtures. Both gzip/xz fixture digests, lengths and PURLs matched
the earlier observation exactly.

At this revision, filename-selected `InspectDependencies` returned `syft-debian-archive-metadata/v1`,
with component type `library`, for both controlled packages. The same archive bytes scanned without the hint
returned `CodeProvenanceMissing` and no accepted inventory. Actual fixture results were rejected by
`WithDependencies` with `CodeUnsupported`. The separate
[serialized BOM guard test](https://github.com/dsx-ai-factory/nvkr/blob/8659cd28e5e615a8f490d0a302a419f329edc902/pkg/nvkr/sbom/dependencies_test.go)
rejected an injected diagnostic method with the then-current `CodeProvenanceMissing`; that test used a
controlled component rather than the real scanner result. Scanner protocol, OCI isolation, filename-predicate
and snapshot-mutation contract tests also passed in that runtime. Non-archive bytes were rejected.
This observation does not exercise the subsequent separate `InspectDebianArchiveMetadata` API or establish
corrupt-payload rejection, complete payload dependencies, source-created package delivery, signing,
publication or installation. Native amd64 execution remains unrecorded.

Current contract tests separately assert rejection of `syft-debian-archive-metadata/v1` by dependency SBOM
attachment and serialized BOM validation. These tests do not retroactively strengthen the dated observation
above. The [opt-in regression](../../../pkg/nvkr/sbom/debian_dependencies_integration_test.go) must be rerun when
changing either the factory catalog Syft pin or the Syft pin in `toolchains/nvkr-disk-inspect`.
