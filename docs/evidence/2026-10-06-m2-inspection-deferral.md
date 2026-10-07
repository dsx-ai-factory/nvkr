# M2 disk inspection deferral

This dated record preserves the October 6 implementation and validation observations below.
The October 7 decision preserves main/#106's scope: producer provenance for M2, deep inspection and
material-to-filesystem reconciliation for M3, as recorded in [ADR-13](../adr/0013-supply-chain-evidence.md).
The subsequent [successful validation-branch run](2026-10-07-m2-validation-branch-release.md) is separate
historical execution evidence; it does not validate the later consumer-policy, timeout or current-head changes.

The user moved deep disk inspection and material-to-installed-filesystem reconciliation
to M3. M2 continues normal pipeline-attested delivery of individual artifacts as one
cohesive release, with signing, provenance, exact membership and completeness.

## Observed blocker

[Run 37445475840](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37445475840),
at commit `4615b35`, successfully built both native QCOW2 images and completed
their terminal byte checks. Both inspector pulls succeeded. Both deep inspections
then exceeded the 600-second deadline, preventing package completion and leaving
the finalizer without required carriers. The logs do not identify the internal
inspection phase that consumed the deadline. No timeout increase is claimed to fix it.

The temporary SPIRE-only older pin, internal package read grants, candidate toolchain
exception and exact validation-branch allowance remain as recorded in the
[retry record](2026-10-06-m2-branch-retry.md) and
[native preparation record](2026-10-06-native-arc.md). The older SPIRE source still
omits the DSX IMDS plugin; this change does not restore that software.

## Implemented scope

The M2 Environment explicitly selects `diskEvidence: build-provenance-v1`.
Both native ARC groups omit inspector preparation and execution. The mode requires
native ARC and publication; unknown modes are rejected. Omission retains strict
inspection behavior.

Packaging requires the original live disk and authenticated material-consumption
trace, rehashes the exported payload and constructs the existing OCI disk wrapper.
Every selected component retains its exact producer result and source-bound
CycloneDX subject document. Documents explicitly record deferred inspection and
contain no inspected dependency inventory. The signed release disk binding names
the evidence mode, and consumer verification checks matching scope and exact
subjects. Missing evidence, substituted hashes and invented inspection claims fail.

The grouped executor also persists the root producer result from its live owner;
the root bypasses the ordinary producer executor to retain its disk capability,
and previously did not write the record required by downstream publication.

The generated bundle's Plan digest is
`sha256:2c2bf59ad5b300c55da19e2068006562f8ddf60b8899444dbdfd88cc058081f2`.

## Validation boundaries

Focused Linux ARM64 race tests exercise the in-process package/evidence transaction
for both AMD64 and ARM64 output contracts, including release read-back and rejected
missing consumption, closed owners and changed producer records. These are contract
fixtures, not new native OS builds or external registry/signing evidence.

The uncached full Go test and race suites pass. Linux race suites for delivery and SBOM pass;
the factory suite rejected tampered records correctly but exposed an incorrect test
expectation for the outer error code. After correcting that assertion, focused Linux
race tests for both output architectures, admission and release verification pass.

Generation and byte-exact regeneration checks pass. Actionlint, yamllint, shellcheck,
vulnerability scanning and Linux-targeted lint pass. The macOS completion gate stops
on five previously recorded SA4023 findings in non-Linux pipeline stubs; no clean
repository-wide gate is claimed.

The new mode has not yet completed a hosted workflow run. Full M2 delivery and
independent consumer acceptance remain unproven.
