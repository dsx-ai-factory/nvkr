## S3 boot and package delivery contract

ADR-20's [specified S3 file-delivery contract](../0020-artifact-publication-and-target-configuration.md#s3-file-delivery-contract)
freezes the proposed native-file/member, typed destination, source selection, portable proof, detached
delivery-root/locator and commit/interruption boundaries. ADR-01 reserves distinct new identity families;
ADR-04/13/14/17/18 own the corresponding trust, evidence, boot, lifecycle and generated execution rules.
This is a contract-only scope extension for review, not named-decider ratification or feature enablement.
No code schema constant, decoder, credential, dependency, catalog pin or generated workflow is changed.

Current object routes still fail `CodeEngineCapability`, and Definition accepts no native `file` output.
The current whole-Plan exact-input receipt cannot authorize selected-output projection. Signature/provenance
verification records do not retain portable source bundles; proof capture and supplied-bundle verification
remain implementation work. Definition v6, Environment v2, Run Plan/artifact-graph v6, PublicationRoot v5
with matching media types, Run v5 and the new identity families are contract reservations, not runtime
registrations or accepted wire schemas. Release v4 keeps its unchanged OCI source view; S3 coordinates,
selected completion and separate policies belong to generated delivery records and receipts. Existing native
package metadata diagnostics and archive inventories cannot satisfy package payload dependency evidence.

The native-file and file-delivery canonical JSON records reserve exact media types
`application/vnd.nvkr.native-file.v1+json` and `application/vnd.nvkr.file-delivery.v1+json`; generic or swapped
record media types are rejected by the specified contract. API `readback` and URL-specific `urlReadback`
must both match the object's exact digest/size before root generation or signing. `urlReadback` contains
exactly the matching URL and digest/size observations and is present exactly with `readURL`; independent
consumers still verify the bytes. Media-type and API/URL mismatch positive/negative cases remain
implementation/acceptance work.

Every selected raw QCOW2 and projected EFI/iPXE/kernel/initrd/ISO payload requires the URL pair, including
shared boot/non-boot keys. API-only proof/control closure remains allowed unless an object is itself selected
as an installer download. Each file output binds an exact `nativeManifest` object key authenticated for its
original output/producer; a same-kind manifest is not an interchangeable choice. Direct DEB projection uses
the existing ordinary host-package producer/raw-byte/artifact-path contract, without output-to-output
selection or APT reconstruction; archive joins and hosted authority remain separately gated.
`lifecycle` exclusively owns the proposed private durable delivery-namespace store and its atomic enrollment,
overlap reservations, phase/completion transitions and retirement. The immutable local attempt store and
retained artifacts are not this cross-Run store. No backend/service is admitted; implementation and capability
admission remain outstanding, including the new URL/manifest/producer-binding positive and negative cases.

The required supported set includes selected AMD64/ARM64 QCOW2, compatible EFI/iPXE assets and original
Debian packages. The pinned NVKE source [documents](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/efi/README.md#L36)
the public/fleet imager parameter mismatch; compatibility must be resolved and exercised rather than inferred
from a build. Its [simulator fixture](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/controller/nico-sim/test/integration/testutil/flows.go#L24)
explicitly skips fetching the EFI, so that test cannot establish actual EFI-chainload/install acceptance.
Native package installation must account for conflicting version alternatives.

No S3 service/version, storage credential, boot-network access, independent S3 consumer or full boot/package
acceptance is recorded for this extension. Enablement requires the exact provider and generated execution,
independent proof/payload consumption, real EFI imaging/reboot, direct VM boot and native package checks,
including failure and indeterminate-commit cases. Existing OCI evidence, dry-runs and unit tests do not
establish these outcomes. The owning ADR states the prerequisite acceptance.
The contract requires URL-path encoding that preserves native Debian basenames containing `+`, a frozen
locator without a final-bundle version ID, and confirmed durable locator retention before the commit request.
Encoding/read-back, lost-acknowledgement recovery through version discovery or admitted exact-key
ETag-conditional full reads, and recovery after producer/runner loss remain required acceptance.
Conditional recovery must authenticate the complete bytes and closure without rewriting the locator;
changed/missing validators, incomplete/mismatched bytes and denied/failed version reads cannot authorize a
weaker read or success. These cases remain unimplemented/unexercised; the documentation requirements add
no implemented recovery or storage capability.
The contract also requires reviewed credential action/resource scopes, complete source-binding tuple
ordering, and a cleanup budget covering cancellation and worker joining without abandoning workers.
Negative boot acceptance explicitly includes mismatched EFI architecture and incompatible imager parameters.
Provider policy evidence and these failure/rejection exercises remain outstanding.
In-run retries preserve held bytes, key and conditional creation without requiring absent-object proof or
bucket-listing permission. Retryable completion failures reuse the same upload ID and frozen part list;
completion `409` first reconciles full bytes and reuses equal authenticated bytes; conflicting bytes fail.
If reconciliation cannot authenticate equal bytes and establishes no conflict, stop new work, cancel active
requests and join transfer workers before abort/list cleanup and a fresh upload. `412`/`NoSuchUpload` require
full-byte reconciliation. SDK and owner retries share request limits; whole-object PUT replays and fresh multipart
uploads also consume the upload bound. These retry and budget-exhaustion cases remain unimplemented/unexercised.
Both the state order and generated job dependencies require confirmed generation-checked `commit-authorized`
promotion after recovery retention and before final PUT. Every final-bundle dispatch/retry requires current
authorization for the exact operation/generation; retention alone cannot bypass closure or retirement.
Cross-Run signing also requires serialized predecessor reconciliation for the enrolled physical namespace; an uncertain or expired locator
cannot be bypassed by re-signing. Cleanup expiry requires separately bounded supervisor termination/reap
observation, and ordering acceptance covers unsorted and mixed-type bindings independently of the comparator.
These remain implementation and acceptance requirements, not exercised retry or process-lifetime evidence.
Using a retained job artifact additionally requires confirmed expiry covering the pre-admitted absolute
recovery deadline and independent discovery/access when no terminal Run record exists; a retention-days
setting alone establishes neither guarantee. Recovery must retain exact root/bundle/locator bytes for
same-key conditional replay, enforce a frozen terminal-publication cutoff and test expiry/discovery failures
with coded errors before commit. A new provider Run ID cannot bypass pending state; terminally reconciled
deliveries still allow later distinct versions. Persistent cross-Run pending state, exact-byte replay and
these retention/cutoff guarantees remain implementation and acceptance work; a job artifact alone supplies
neither serialization nor a persistent pending-state index. The proposed namespace identity excludes target
names and transport aliases, with explicit operator enrollment and one owning repository, workflow path,
display name/bundle, exact dispatch ref and evaluated non-canceling concurrency group.
New enrollment must exclude equal, ancestor and descendant prefixes of enrolled or retired reservations on
the same provider/resource and bucket, with serialized overlap checking and no-replace creation.
An authenticated exact retry reuses the unchanged non-retired enrollment before new-enrollment overlap
rejection; a different repository or other owner field at an equal prefix is rejected, and retirement still
blocks a same-owner retry. Required tests cover lost enrollment acknowledgements and repository-only mismatch.
Preparing-state CAS permits proven-undispatched closure; unrecoverable authorized state requires recorded
retirement and a separately enrolled disjoint prefix. Exact-bundle replay does not require joining a lost
runner, while current workers remain owned through stop/join. First-use, alias/owner collisions, overlapping
enrollment races, repository/workflow/group/cancellation mismatches, closure races,
retirement and missing/queue-expired cutoff tests are required. Independently authenticated completion must
be durably recorded and confirmed through a generation-checked `verified-complete` transition before producer
success, bound to the original operation, namespace, Run/attempt/target, exact locator/root/bundle and enforced
policies. Persistence failure, lost-acknowledgement recovery and stale/substituted transition bindings require
explicit tests; remote completion remains recoverable and later consumers still authenticate its closure.
These boundaries remain unimplemented/unexercised.
