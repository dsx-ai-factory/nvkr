## Support by owner

The native scanner adapter admits Syft JSON `16.1.10` and `16.1.13`, preserving
the retained image schema while the inspector source advances to Syft v1.57.1.
Contract tests cover exact schema admission and rejection of nested package
databases through `path`, `accessPath` and the new archive `fullPath` coordinates,
including traversal across archive boundaries. On 2026-11-09, the upstream Linux
ARM64 Syft executable (`sha256:b0b37d4e18dbb923c9321a332c2f5b1ef2db13f599e58d6f3aee3a1ba3545365`)
passed `TestRealDebianArtifactDependencies`, `TestDiskCapturedPackagesPinnedToolsReal`
and the generated/plain/gzip rows of `TestDiskContentPinnedScannerReal` in an
offline native Linux container. These controlled fixtures do not exercise the
new full inspector image, libguestfs, the OCI helper rows, AMD64 execution,
complete NVKE inspection or publication.

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 13 Evidence | Executable-target SBOMs select architecture-specific toolchain pins for Linux and Darwin ARM64 while preserving native Go compiler identities, without widening execution admission; exact offline binary/image dependency scanning, separate `InspectDebianArchiveMetadata` diagnostics and bounded Helm metadata inventories, including acquired raw charts; separately scoped archive member/hash inventories for source tarball assets in executable bundles; stage-bound signed CycloneDX bundles; declared upstream evidence intake for CycloneDX 1.4–1.6 or SPDX 2.3 JSON SBOMs and in-toto v0.1/v1 SLSA v1 provenance, read from `file://`, `https://` or a pinned release archive member, with Sigstore v0.3 provenance bundles verified by a digest-pinned cosign under a declared signer identity and issuer; M2 live disk producer/material evidence with explicit deferred inspection; guarded installed-inventory and full-content export receipt APIs | Archive inventories describe files, not package dependencies; their hosted producer path remains unexercised. Raw Debian outputs preserve their source-created bytes and ordinary producer/result subject; publication adds a standard carrier and selected scanners use the bounded pinned-reader payload path. Factory callers retain ordinary hint-free binary inspection; no maintained factory output invokes the separate Debian metadata diagnostic. Dependency SBOM attachment and serialized BOM validation reject its method. Raw-Debian carrier, graph and payload-inspection contracts are local evidence; no hosted signed raw-Debian Run or real package-install acceptance is recorded. Real native NVKE full-content inspection/reconciliation and original-live-Run joins remain unexercised. Upstream-signature contract tests use the AICR v1.1.2 linux/amd64 attestation and SPDX SBOM with a fake cosign runner; one opt-in local run of cosign v3.0.6 on macOS verified that bundle online. No maintained catalog declares signed upstream evidence yet, the stock cosign path is Linux-only, and offline/air-gapped trust roots are unsupported. Vulnerability scanning and qualified signed evidence publication are outside v1 |
