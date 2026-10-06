# CWE runnable index — standalone delivery contracts

On 2026-10-03 UTC (2026-10-02 Pacific), the working tree on `codex/nkd-multiarch-cwe`, based on
`d1a4ec9403a936b154b16f4c53e3d3f6daa4c739`, completed the first delivery-only integration stage.
This record covers contract tests and local filesystem/OCI-reader interoperability. It does not establish
real CWE production, Docker execution, registry publication, hosted execution, signing or release acceptance.

The new delivery `ImageIndex` captures verified native image closures into anonymous immutable files. Tests
exercise exact platform admission before input work, unchanged child blobs, deterministic canonical index
bytes, shared-blob deduplication and ordinary `go-containerregistry` index/image reading. Negative cases cover
missing/extra/duplicate platform or descriptor membership, wrong configs, manifest/config/layer integrity,
expanded layer identity, unsupported variants, artifact manifests, unreachable blobs, ambiguous JSON, symlinks
and FIFOs. Source replacement and defensive-description tests establish the held-capability boundary.

Layout tests exercise descriptor-relative writes, file/directory synchronization, atomic no-replace commit,
independent competing writers, existing-output preservation, cancellation cleanup and closing during a write.
Independent review found that a canceled writer could wait behind another long copy; the corrected operation
gate observes cancellation while queued and is covered by
`TestImageIndexCanceledWriterDoesNotWaitForActiveCopy`. Closing still joins active operations.

The final [full host gate](2026-10-03-cwe-index-delivery/final-make-check.log) passed with pinned Go 1.27.1
on macOS ARM64: formatting, vet, lint, shellcheck, race/coverage tests (71.4%) and vulnerability scan
(no vulnerabilities). The [complete uncached host suite](2026-10-03-cwe-index-delivery/final-uncached-tests.log)
and `git diff --check` also passed after the review correction.

The [native Linux ARM64 delivery race suite](2026-10-03-cwe-index-delivery/final-linux-race.log) passed on
Go 1.27.1. It ran as UID 1000 with `--init` in the local test image built from the retained
[Dockerfile](2026-10-03-cwe-index-delivery/linux-test.Dockerfile), using a read-only checkout copy without its macOS Git pointer.
The test image config was `sha256:2c63d4002cc398cac5ae1ca4c3d470841eff83d94460239720c685ef7335a05c`.
Earlier logs without the `final-` prefix predate the canceled-waiter correction and are retained separately.

The [implementation file record](2026-10-03-cwe-index-delivery/implementation-files.json) fingerprints the
exact stage-one source files. These hashes provide diagnostic integrity only, not authenticated provenance.
The [delivery design](../multiarch-cwe-delivery.md) and ADR-20 explicitly keep aggregate Definition/compiler,
per-platform evidence, registry/signing, consumer, navigation and Helm integration disabled. That document's
stage-two interfaces are an agreed implementation target, not an enabled capability.
