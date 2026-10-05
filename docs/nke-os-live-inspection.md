# Live installed-package inspection

The local lifecycle can opt into installed-package inspection through `LocalOptions.DiskInspection`. Its caller
supplies an already admitted, concrete SBOM inspection runtime and keeps it open for the operation. Without
that capability, existing OS builds remain build-only. No default inspection or full disk SBOM is claimed.
Dry-run remains non-executing even when an inspection runtime is supplied: it does not admit or invoke that
runtime and returns only the pending, static-unqualified Run description, with no observed outputs or inventories.

The lifecycle invokes factory inspection after execution succeeds and before exporting or closing the held
QCOW2. Factory retains the original Run's disk lifetime permit and authenticates its private material trace.
The concrete runtime owns a process-wide capacity-admission permit and snapshots the originally collected inode. The permit remains
held through inspection, container cleanup and snapshot close, including failure and cancellation. Separate
runtime instances cannot allocate competing admitted snapshots in this process. This is serialization around
current host/cgroup availability, not a kernel memory allocation or a reservation against other processes or
the daemon's enclosing cgroup. Factory does not reopen a caller-selected
disk path or reconstruct producer observations from result JSON. The SBOM owner returns an opaque inventory
from the admitted runtime, and factory binds it to the original owner, named output, Plan, Build, exact disk
digest and size, and runtime/inspector/codec identities. The trace and held disk are rechecked after scanning.

Receipt validation requires the original still-open owner and same runtime. Identical disk bytes do not
authorize a sibling attempt. Run close revokes new admission, cancels active inspection and waits for its
bounded cleanup. Immutable descriptions remain readable after close; copied descriptions and reloaded Run
JSON cannot mint or restore live authority. The lifecycle returns these process-local descriptions through
`Run.InstalledDiskInventories`; they are deliberately not an independently authored or persisted lifecycle
object.

The installed-inventory receipt has no publication consumer yet. The local lifecycle validates it before
keeping only its diagnostic description; that extra live validation does not grant a stronger evidence lane.

The binding test covers AMD64 and ARM64 subjects, description isolation, serialization exclusion and
post-close rejection. Separate tests using the AMD64 fixture cover missing/substituted material observations,
changed disk or runtime/tool bindings, sibling replay, zero-value receipts, cancellation and concurrent close.
Lifecycle tests cover invocation before export/close and failure without a retained delivery payload.
These tests use controlled producer/inspection seams, not real NKE bakes.
The same-Run sibling-output test gives both outputs their own valid material trace and equal-byte held
completion before checking receipt replay. The production scan closure's concrete Packer type check and
capacity-before-snapshot wiring have separate guard and owned-operation tests. Those tests cover admission
before copying, snapshot failure, mid-copy cancellation and runtime close joining child cleanup before
snapshot closure and permit release. The private output loop has disk-only selection, ordering and
failure tests. A successful opaque inventory still requires the real runtime harness; injected tests do not
prove a live NKE bake followed by inspection. The `inspectLocalDisks` Observe/Validate/Description closure,
exported factory Observe/Validate wrappers and successful runtime-inventory binding projection remain
unexercised pending that harness.

An opted-in inspection failure fails the Run and discards uncommitted delivery bytes; it is not a warning
that leaves a successful build without its requested inventory. Failed Runs expose no installed inventories,
including when scanning succeeded but later export, subject binding or disk commit failed. Successful views
must match every observed disk's output name, Plan, Build, exact digest, size, format and encryption.

One ten-minute operation deadline covers queueing, material tracing, snapshotting, scanning and subject/tool
rechecks. The runtime's configured timeout additionally bounds its complete owned admission, snapshot and
scan operation; it does not reserve that entire duration after preparation. A shorter caller deadline and
caller cancellation remain effective.
Contention consumes that same declared budget; post-scan rechecks receive no fresh allowance. The mutex
queue has no FIFO guarantee. These limits fail closed rather than silently extending an authored timeout.

Real acceptance still requires both AMD64 and ARM64 NKE builds inspected through this same original live
lifecycle, with the exact source, Plan, attempt, image/tool identities and terminal inventory observation
recorded. Inspecting a previously retained image in a separate process does not satisfy that handoff proof.
`TestNKEOSLiveInstalledInventory` is the opt-in lifecycle-package harness, not a `client/v1`-only SDK
acceptance test. It requires
`NKD_TEST_NKE_LIVE_INSPECTION=1`, exact Definition/Environment paths, inspector image/tool pins and measured
runtime resources through its explicit environment inputs. It rejects a disk target other than the actual
native host architecture and verifies the returned original Run, output, Plan, Build, digest and size. Run it
separately on native Linux AMD64 and ARM64 with a test timeout sufficient for a complete bake; a skipped test
is not acceptance evidence.

The live-bake harness uses separate names to distinguish a new original-Run build from the existing
retained-image inspector test. Its tool inputs map as follows:

| Live-bake harness | Retained-image harness |
| --- | --- |
| `NKD_TEST_INSPECT_RUNTIME` | `NKD_TEST_DISK_INSPECT_RUNTIME` |
| `NKD_TEST_INSPECT_IMAGE` | `NKD_TEST_DISK_INSPECT_IMAGE` |
| `NKD_TEST_INSPECT_INSPECTOR_DIGEST` | `NKD_TEST_DISK_INSPECTOR_SHA256` |
| `NKD_TEST_INSPECT_CODEC_DIGEST` | `NKD_TEST_DISK_CODEC_SHA256` |

The live harness additionally requires Definition/Environment paths and numeric resource inputs. The retained
image harness instead supplies an existing QCOW2 path and digest. Those tests establish different scopes and
are not interchangeable acceptance evidence.

Acquisition reconciliation, full CycloneDX generation, publication, FDE and qualified reproduction remain
outside this change.
