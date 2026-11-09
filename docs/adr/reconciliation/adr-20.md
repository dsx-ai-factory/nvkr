## Support by owner

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 20 Publication | Digest-selected sibling repositories, signed evidence membership, native OCI/Docker subjects with consistent named-output aliases and independent evidence, aggregate runnable OCI indexes with exact native-child evidence, bounded metadata navigation, selected output payload-closure retrieval and per-output assembly/finalization; consumer held-closure export bound to exact-input receipts; candidate M2 hosted signing/publication completed; public-bootstrap FDE build-provenance publication supported; development attempt namespaces and scoped GHCR cleanup; hosted GHCR package privacy admission using metadata-derived destination references and an exact-root-bound push; required Environment transport selection with preserved omitted-OCI behavior, explicit OCI identity and early APT/both rejection; required S3 copies of original signed publications through a generic byte-provider interface | Package admission has local contracts, not a hosted private-publication or public-source access proof; independent grants, historical provider payloads and future visibility changes remain operational obligations. Hosted GHCR legacy layouts without the metadata contract fail closed. `release pull` shares the bounded HTTP/2 peer-reset reacquisition used by `delivery.Pull`, with local `httptest` contracts only, as recorded in [output-local publication](output-local-publication-and-metadata-assembly.md#output-local-publication-and-metadata-assembly); no external large-disk `release pull` has been re-run since the recorded peer-reset failures. Main-only Plan execution, independent M2 full-payload disk consumption, strict full-content publication and live S3/CDN operational acceptance remain unproved; registration is deferred. Direct APT service adapter, repository signing and authenticated read-back remain unimplemented. |

Native Helm publication emits standard Helm config/content media types with no custom chart `artifactType`.
Its separate evidence manifests and enclosing metadata manifest omit that field and carry their canonical
configs as `application/octet-stream`. `TestHelmStandardPublication` checks complete publication,
full and metadata readback, selected chart retrieval and evidence preservation against a local registry
that rejects unknown manifest artifact/config types, modeling GitLab's reported database restriction.
`TestHelmLegacyTypeCompatibility` reproduces the old rejection, retains old-chart reads and refuses
mismatched legacy types. Stock Helm 4.2.3 pull/show/template was exercised locally on 2026-11-09 with
`NVKR_TEST_HELM_OCI=1`; exact archive bytes and a synthetic pinned image reference survived. The attestation
case uses an encoding fixture, not real signing. No live GitLab deployment or hosted signed Run is established.
Non-Helm artifact families retain custom types; mixed publication compatibility remains separate work.
New manifest and publication identities require regenerated subject-bound evidence and updated readers,
as specified in [ADR-20](../0020-artifact-publication-and-target-configuration.md).
