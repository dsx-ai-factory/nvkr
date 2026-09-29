# NKE OS validation plan

## Goal

Exercise the maintained non-FDE NKE image through the NKD public local lifecycle on native AMD64 and ARM64 Docker/KVM
runners, verify the exact QCOW2, and boot-smoke it with the source-owned NKE check. The workflow is repository-owned
because the generated hosted adapter still rejects requirement-bearing VM jobs and never emits self-hosted labels.

## Trust boundary

The credential-free toolchain pass may be manually dispatched from a PR branch at its exact head or from the trusted
workflow on `main`. It uses GitHub-hosted native AMD64 and ARM64 runners, has no KVM or private-source access, and
records the exact native toolchain image IDs. A branch dispatch must select that branch's dispatch SHA and cannot
request a full build.

Full image validation is also manual-only and never uses `pull_request_target`. It only accepts a commit reachable
from `main` while the workflow itself is dispatched from `main`. No pre-merge code receives private NKE source.
The AMD64 leg retains the NKE workflow's verified runner label. The ARM64 leg uses the
repository-specific 16-vCPU, 64-GiB KVM scale set defined by `NVIDIA-dev/nke-gha-runners` at `488424d`. Those VMs are
single-use and must expose `/dev/kvm` to the runner identity; NKD performs the runtime admission before build work.

Full validation's separate trusted fetch job references the repository-scoped `NKE_SOURCE_TOKEN`, which must provide
read-only Contents access to `dsx-ai-factory/nke`. The secret is not restricted to this workflow by GitHub policy;
other workflows with sufficient permission could reference it. The trusted selector reads the `nke-os` commit and tree
from the selected Recipe catalog lock; the workflow does not own or hardcode an NKE version. The dispatcher enters
that exact NKE commit as a second value, and the selector rejects a mismatch. This is a consistency check, not
independent human approval. The checkout fetches that commit into a clean staging path before the build job, does not
persist credentials, and is followed by explicit commit, tree and credential-state checks. The build job receives the
source bytes and declared outbound build network, but not the source credential itself. NKD uses the local checkout
through a process-scoped Git transport override.

## Attempt-bound Docker toolchain

The source lock pins the AMD64 and ARM64 toolchain materials, but its native Docker image IDs are null. The first
two native Docker diagnostic runs on 2026-09-28 did not produce identical image IDs, so the workflow must not claim
byte-reproduced toolchain images or invent stable Docker pins. The full build instead checks the source lock, builds
the toolchain natively, and freezes the observed exact image ID into an immutable attempt-specific catalog overlay
before compiling the executable Plan. NKD authenticates the loaded image against that Plan at execution. The
catalog's AMD64 baseline is a previously observed diagnostic ID, not an approved maintained toolchain identity.
The existing ARM64 Podman pin remains separate. Neither runtime's image is a published, approved toolchain.

The native AMD64 Definition, installer supplier and local Docker Environment are authored. The full workflow still
requires the trusted `main` dispatch and an exact main-reachable commit or copy-pr-bot mirror; PR-branch dispatch is
toolchain-only. A successful full run, not compilation or unit tests, is required to establish that the build works
on the selected KVM runners.

## Acceptance

Each full leg must pass native architecture, Docker, KVM, effective CPU, memory, storage and timeout admission; build
through `nkdctl run local`; retain one Plan and completed Run; verify exactly one QCOW2 has no backing file and record its
SHA-256 and size; and pass NKE's pinned source-owned boot smoke under KVM. Plan, Run, material-manifest and serial/boot
logs produced before a failure are uploaded. A failure can occur before one or more of those records exists. QCOW2
upload is an explicit short-retention option because the image is large. These results remain unqualified integration
evidence and do not publish or sign an image.

The workflow owns runner selection, credential-scoped source transport and artifact transport only. Catalog loading,
source-pin validation, runtime admission, build execution, disk observation and lifecycle completeness remain in their
owning NKD modules.

FDE, generated hosted execution, publication and qualified byte reproduction are outside this change.
