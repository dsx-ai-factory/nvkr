# Local disk tracing and inspection

This is the engineering reference for the process-local staged-material trace and optional local
installed-package diagnostic. [Node images](node-images.md) explains ordinary authoring and execution.
[ADR-13](adr/0013-supply-chain-evidence.md) owns evidence contracts; [reconciliation](adr/reconciliation.md)
owns current implementation and externally exercised scope.

Factory records staged producer/material handoffs during successful local disk execution. The optional
inspection path consumes those private records before disk export; no CLI command serializes their
live authority. These observations do not inventory every private source download or installed file,
issue publication clearance, or demonstrate qualified reproduction.

## Staged material observations

| Trace field | Owning source |
|---|---|
| Plan and Build identities, output membership | Validated factory Plan and exact lifecycle output |
| QCOW2 digest, size and successful root | Package-owned held disk completion, independently streamed by the existing disk observer |
| Material binding and producer edge | Exact selected producer IR in the Plan |
| Consumed material digest, component, kind and architecture | Held material handoff, frozen while its inputs are supplied to a successful invocation |
| Manifest digest | Exact held manifest compared with the successful producer's result before cleanup; empty when the producer declares no materials |
| Built output digest and size | Successful pipeline return and independent byte check at its Plan-selected output/result slot |
| Acquired output digest and size | Successful pipeline acquisition, checked against the IR's exact content pin |
| Coverage gaps | Always scoped to staged producer outputs; network-enabled build nodes are listed explicitly |

Only disk targets retain these private consumption records. Sibling CLI, chart and other outputs do not acquire
additional material-tracing work. Reopening or decoding a result file cannot create a missing record. Records
are synchronized with the local result's existing lifetime lock, excluded from its serialized JSON, and cleared
on close. A returned description is an independent copy, not a receipt constructor.

The internal observer follows direct and transitive material edges, compares every consumed digest
with its producer's retained output, and includes shared producers once. It fails on missing or substituted
observations and binds the observation to the exact named output, Plan, Build and QCOW2 subject. Equal-byte
sibling outputs cannot reuse each other's invocation observations. It issues no signed or
cross-process authority. A serialized local Run cannot reconstruct this observation.

The trace's immutable subject check does not prove that its issuing Run remains open, distinguish equal-byte
attempts of the same Plan, or grant access to held disk bytes. Future consumers must use the owning live-disk
capability separately, as the live inspection SDK path does. Non-VM output/result pairs are re-hashed at recording time, intentionally adding I/O
even for a multi-GB ISO; a consumer's held copy alone cannot establish that producer pairing.

## Material coverage and the inspection handoff

The validated Plan owns the selected producer graph; the source catalog owns exact material edges.
The NVKE graph includes built Debian/network packages, separate OVN image/chart outputs, a digest-verified
original ISO, its patched transform, aggregate image inputs and the final QCOW2. Trace observations
follow those edges and authenticate retained producer bytes; they do not infer package/file contents
from an archive digest or source command.

Source preparation may acquire verified leaf artifacts without exposing each through this boundary.
Base-ISO package contents, wrapped BuildKit network downloads, guest security updates and transformed
installed placement remain outside the staged-output trace. Toolchain admission is independent of
material-member observation. Built members retain their material edges rather than being relabeled
as declaration-only embedded content.

Factory supplies an owner-issued snapshot of the original held inode, not a reopened exported path.
The concrete runtime admits capacity before copying and closes the snapshot before releasing that
capacity. Factory joins the returned inspection observation to the original owner/output/Plan/Build.
Installed-only inventory plus staged-only tracing still does not establish complete acquisition
coverage or a reconciled production disk SBOM.

## Opt into installed-package inspection

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

## Receipt scope and validation

The installed-inventory receipt has no publication consumer yet. The local lifecycle validates it before
keeping only its diagnostic description; that extra live validation does not grant a stronger evidence lane.

The binding test covers AMD64 and ARM64 subjects, description isolation, serialization exclusion and
post-close rejection. Separate tests using the AMD64 fixture cover missing/substituted material observations,
changed disk or runtime/tool bindings, sibling replay, zero-value receipts, cancellation and concurrent close.
Lifecycle tests cover invocation before export/close and failure without a retained delivery payload.
These tests use controlled producer/inspection seams, not real NVKE bakes.
The same-Run sibling-output test gives both outputs their own valid material trace and equal-byte held
completion before checking receipt replay. The production scan closure's concrete Packer type check and
capacity-before-snapshot wiring have separate guard and owned-operation tests. Those tests cover admission
before copying, snapshot failure, mid-copy cancellation and runtime close joining child cleanup before
snapshot closure and permit release. The private output loop has disk-only selection, ordering and
failure tests. A successful opaque inventory still requires the real runtime harness; injected tests do not
prove a live NVKE bake followed by inspection. The `inspectLocalDisks` Observe/Validate/Description closure,
exported factory Observe/Validate wrappers and successful runtime-inventory binding projection remain
unexercised pending that harness.

## Failure and deadline behavior

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

## Exercise the original live handoff

Real acceptance still requires both AMD64 and ARM64 NVKE builds inspected through this same original live
lifecycle, with the exact source, Plan, attempt, image/tool identities and terminal inventory observation
recorded. Inspecting a previously retained image in a separate process does not satisfy that handoff proof.
`TestNVKEOSLiveInstalledInventory` is the opt-in lifecycle-package harness, not a `client/v1`-only SDK
acceptance test. It requires
`NVKR_TEST_NVKE_LIVE_INSPECTION=1`, exact Definition/Environment paths, inspector image/tool pins and measured
runtime resources through its explicit environment inputs. It rejects a disk target other than the actual
native host architecture and verifies the returned original Run, output, Plan, Build, digest and size. Run it
separately on native Linux AMD64 and ARM64 with a test timeout sufficient for a complete bake; a skipped test
is not acceptance evidence.

The live-bake harness uses separate names to distinguish a new original-Run build from the existing
retained-image inspector test. Its tool inputs map as follows:

| Live-bake harness | Retained-image harness |
| --- | --- |
| `NVKR_TEST_INSPECT_RUNTIME` | `NVKR_TEST_DISK_INSPECT_RUNTIME` |
| `NVKR_TEST_INSPECT_IMAGE` | `NVKR_TEST_DISK_INSPECT_IMAGE` |
| `NVKR_TEST_INSPECT_INSPECTOR_DIGEST` | `NVKR_TEST_DISK_INSPECTOR_SHA256` |
| `NVKR_TEST_INSPECT_CODEC_DIGEST` | `NVKR_TEST_DISK_CODEC_SHA256` |

The live harness additionally requires Definition/Environment paths and numeric resource inputs. The retained
image harness instead supplies an existing QCOW2 path and digest. Those tests establish different scopes and
are not interchangeable acceptance evidence.

## Distinguish diagnostic and production evidence

This optional installed-package diagnostic does not perform acquisition reconciliation, full-content
CycloneDX generation or publication. A separate strict full-content receipt/packaging path is implemented
and contract-tested; successful complete native NVKE discovery and original live reconciliation remain
unexercised. M2's explicit `build-provenance-v1` mode authenticates live disk/material subjects while
signing the deferred-inspection scope. [The M2 README](../definitions/nvke-m2/README.md) describes its
candidate delivery and consumer limits. Deep inspection/reconciliation moves to M3; neither diagnostic
metadata nor M2 delivery proves full-disk inventory, FDE support or qualified reproduction.
