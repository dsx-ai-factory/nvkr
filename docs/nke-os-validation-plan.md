# NKE OS validation plan

## Goal

Exercise the maintained non-FDE NKE image through the NKD public local lifecycle on native AMD64 and ARM64 Docker/KVM
runners, verify the exact QCOW2, and boot-smoke it with the source-owned NKE check. The workflow is repository-owned
because the generated hosted adapter still rejects requirement-bearing VM jobs and never emits self-hosted labels.

## Trust boundary

Every push to `main`, including a merged PR, selects the exact pushed commit and runs full AMD64 and ARM64
Docker/KVM image builds and boot smoke. Both QCOW2s are uploaded after successful validation. No path filter
skips merges, and newer pushes do not cancel earlier runs. The automatic post-merge run at `156d32c` passed
both full builds; the following run at `53ba88b` passed AMD64 but failed ARM64 during module acquisition.
Those runs do not validate the reliability edits below at a new head.

Manual dispatch from the trusted workflow on `main` remains available for either architecture or both, in
toolchain-only or full mode. The credential-free toolchain pass uses GitHub-hosted native AMD64 and ARM64
runners, has no KVM or private-source access, and records the exact native toolchain image IDs.

The secret-free `preflight` mode can be dispatched from a branch at that workflow's exact commit. It runs the
same no-chmod admission step as full validation on either ARC architecture or both, without fetching NKE,
building an image or exposing a source credential. This permits pre-merge runner-permission validation;
the selection step requires toolchain and full modes to run from `main`. This is a workflow-local self-check,
not GitHub-enforced authorization. The recorded branch probe reached both ARC KVM pools; branch-authored
workflows can omit the selection check and reference the repository-scoped source secret. Restricting the
runner group to selected workflows/refs and moving the secret into a main-restricted environment remain
configuration follow-ups, not controls established by this PR.

Full image validation never uses `pull_request_target`. Manual dispatch accepts an exact main commit or a
copy-pr-bot mirror matching the current, open, non-draft same-repository PR
head. Maintainers must vet the exact head before `/ok to test`; the workflow does not authenticate that comment
or require PR approval. Pre-merge candidate code receives the pinned private
NKE source contents and a network-capable KVM runner, but not the source credential.
The AMD64 leg retains the NKE workflow's verified runner label. The ARM64 leg uses the
repository-specific 16-vCPU, 64-GiB KVM scale set defined by `NVIDIA-dev/nke-gha-runners` at `488424d`. Single-use
runner isolation remains a provider fact to verify; `/dev/kvm` must be available to the runner identity. NKD
performs runtime admission before build work.
The workflow requires existing read/write access to `/dev/kvm` and does not change host device permissions.
Runner identity and device ownership/mode are printed before the access check. Each full leg also runs the
Packer dispatcher exit-status regression test with the admitted real KVM device and retains its output.
The step sets `NKD_REQUIRE_KVM_TEST=1`, so unavailable KVM fails rather than skipping the guarded cases.
That test uses a fake container runtime; it proves error classification, not a QEMU bake.
The scale set must supply KVM group or device access. Local macOS/Podman TCG development remains a parallel
supported path and is not selected by this native KVM validation workflow.

Full validation's separate trusted fetch job references the repository-scoped `NKE_SOURCE_TOKEN`, which must provide
read-only Contents access to `dsx-ai-factory/nke`. The secret is not restricted to this workflow by GitHub policy;
other workflows with sufficient permission could reference it. The trusted selector reads the `nke-os` commit and tree
from the selected catalog lock and compiles every selected architecture's complete Plan with trusted main code,
rejecting any selected NKE source node with a different commit or tree. The workflow does not own or hardcode an NKE
version. A new input schema must land in main before this trusted reader can validate a candidate using it.
On main pushes, the merged catalog and validated Plans select that exact NKE commit. For manual full runs,
the dispatcher enters it as a second value, and the selector rejects a mismatch. This is a consistency check,
not independent human approval. The checkout fetches that commit into a clean staging path before the build job, does not
persist credentials, and is followed by explicit commit, tree and credential-state checks. The build job receives the
source bytes and declared outbound build network, but not the source credential itself. NKD uses the local checkout
through a process-scoped Git transport override.

## Attempt-bound Docker toolchain

The source lock pins the AMD64 and ARM64 toolchain materials, but its native Docker image IDs are null. The first
two native Docker diagnostic runs on 2026-09-28 did not produce identical image IDs, so the workflow must not claim
byte-reproduced toolchain images or invent stable Docker pins. The full build instead checks the source lock, builds
the toolchain natively, and freezes the observed exact image ID into an immutable attempt-specific catalog overlay
before compiling the executable Plan. NKD checks the loaded image against that Plan at execution. That identity
bind is not provenance authentication. The
catalog's AMD64 ID is a substitution-only baseline, not an approved maintained toolchain identity.
The existing ARM64 Podman pin remains separate. Neither runtime's image is a published, approved toolchain.

The native AMD64 Definition, installer supplier and local Docker Environment are authored. The full workflow still
requires the trusted `main` workflow, automatically at the pushed commit or manually at an exact main-reachable
commit or copy-pr-bot mirror. A successful full run, not compilation or unit tests, is required to establish
that the build works on the selected KVM runners.

## Acceptance

### Dependency preparation and early failures

The agent fetch command uses Go's existing module downloader and checksum verification. Three entries for the
same public module proxy use Go's `|` transport fallback; checksum verification happens after transport and
does not select another supplier. The existing direct-source fallback remains limited to proxy 404/410 responses.
The complete download is bounded to 540 seconds with a 10-second kill grace. Completed modules remain in the
same attempt-local container cache, not a shared Actions cache. Verification is bounded to 60 seconds and
offline vendoring to 180 seconds, each with the same grace; neither command is retried. `GOWORK=off`,
`GOTOOLCHAIN=local` and readonly module flags are explicit. Pinned `go.mod` and `go.sum` must remain unchanged
after acquisition and vendoring. No Packer or component build is retried.

The existing source-closure receipt is issued after successful vendoring and hashing. Source commit/tree and
material checks remain unchanged. Architecture jobs still acquire separate closures: their source receipts bind
the exact architecture-selected vendor specification and image. Sharing a vendored closure requires a new
authenticated transport contract, not merely copying one job's directory or adding an Actions cache.

The workflow records its attempt name before starting the lifecycle and preserves the primary exit status with
`pipefail`. Toolchain recording runs only after an executed lifecycle step. A failed lifecycle without a Plan
reports an unavailable toolchain observation rather than a spurious missing-file failure. A malformed existing
Plan, mismatched image ID, or successful lifecycle without a Plan remains an error. Available logs are retained.
The regression suite exercises the actual authored Go command against a fixture module proxy and executes the
authored workflow snippets; it does not prove ARC availability or full NKE acquisition.

Acceptance for these reliability edits requires a fresh AMD64 and ARM64 full build and boot smoke at the exact
reviewed head, then one clean rerun. Contract tests alone cannot satisfy that external gate.

### Full build

Each full leg must pass native architecture, Docker, KVM, effective CPU, memory, storage and timeout admission; build
through `nkdctl run local`; retain one Plan and completed Run; verify exactly one QCOW2 has no backing file and record its
SHA-256 and size; and pass NKE's pinned source-owned boot smoke under KVM. Plan, Run, material-manifest and serial/boot
logs produced before a failure are uploaded. A failure can occur before one or more of those records exists. QCOW2
upload is enabled automatically on main pushes and remains an explicit short-retention option for manual runs
because the image is large. These results remain unqualified integration evidence and do not publish or sign
an image.

Unavailable or oversized build logs produce a bounded `logs-incomplete.json` marker without discarding a
separately verified QCOW2. Unsafe paths, links, devices, duplicate entries or replacement attempts remain errors.
The boot smoke selects the exact toolchain ID recorded in the executable Plan, not the mutable development tag.
The [2026-10-01 evidence](evidence/2026-10-01-nke-arc-os-builds.md) records both successful native full builds;
each subsequent execution-changing fix requires its own full-run validation.

The workflow owns runner selection, credential-scoped source transport and artifact transport only. Catalog loading,
source-pin validation, runtime admission, build execution, disk observation and lifecycle completeness remain in their
owning NKD modules.

FDE, generated hosted execution, publication and qualified byte reproduction are outside this change.
