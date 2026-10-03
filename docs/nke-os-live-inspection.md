# Live installed-package inspection

The local lifecycle can opt into installed-package inspection through `LocalOptions.DiskInspection`. Its caller
supplies an already admitted, concrete SBOM inspection runtime and keeps it open for the operation. Without
that capability, existing OS builds remain build-only. No default inspection or full disk SBOM is claimed.

The lifecycle invokes factory inspection after execution succeeds and before exporting or closing the held
QCOW2. Factory retains the original Run's disk lifetime permit, authenticates its private material trace,
admits snapshot capacity, and snapshots the originally collected inode. It does not reopen a caller-selected
disk path or reconstruct producer observations from result JSON. The SBOM owner returns an opaque inventory
from the admitted runtime, and factory binds it to the original owner, named output, Plan, Build, exact disk
digest and size, and runtime/inspector/codec identities. The trace and held disk are rechecked after scanning.

Receipt validation requires the original still-open owner and same runtime. Identical disk bytes do not
authorize a sibling attempt. Run close revokes new admission, cancels active inspection and waits for its
bounded cleanup. Immutable descriptions remain readable after close; copied descriptions and reloaded Run
JSON cannot mint or restore live authority. The lifecycle returns these process-local descriptions through
`Run.InstalledDiskInventories`; they are deliberately not an independently authored or persisted lifecycle
object.

The contract tests cover AMD64 and ARM64 subjects, missing/substituted material observations, changed disk or
runtime/tool bindings, sibling replay, zero-value receipts, copied descriptions, cancellation and concurrent
close. Lifecycle tests cover invocation before export/close and failure without a retained delivery payload.
These tests use controlled producer/inspection seams, not real NKE bakes.

Real acceptance still requires both AMD64 and ARM64 NKE builds inspected through this same original live
lifecycle, with the exact source, Plan, attempt, image/tool identities and terminal inventory observation
recorded. Inspecting a previously retained image in a separate process does not satisfy that handoff proof.
`TestNKEOSLiveInstalledInventory` is the opt-in public SDK harness. It requires
`NKD_TEST_NKE_LIVE_INSPECTION=1`, exact Definition/Environment paths, inspector image/tool pins and measured
runtime resources through its explicit environment inputs. It rejects a disk target other than the actual
native host architecture and verifies the returned original Run, output, Plan, Build, digest and size. Run it
separately on native Linux AMD64 and ARM64 with a test timeout sufficient for a complete bake; a skipped test
is not acceptance evidence.
Acquisition reconciliation, full CycloneDX generation, publication, FDE and qualified reproduction remain
outside this change.
