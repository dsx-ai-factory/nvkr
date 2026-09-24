# NKE OS validation plan

## Goal

Exercise the maintained non-FDE NKE image through the NKD public local lifecycle on native AMD64 and ARM64 Docker/KVM
runners, verify the exact QCOW2, and boot-smoke it with the source-owned NKE check. The workflow is repository-owned
because the generated hosted adapter still rejects requirement-bearing VM jobs and never emits self-hosted labels.

## Trust boundary

The credential-free toolchain pass is manually dispatched from the trusted workflow on `main` and uses GitHub-hosted
native AMD64 and ARM64 runners. It checks out the exact selected implementation commit, has no KVM or private-source
access, and records the exact native toolchain image IDs.

Full image validation is also manual-only and never uses `pull_request_target`. Its selector authenticates either a
commit reachable from `main` or the exact tip of a named same-repository mirror, while the workflow definition itself
is dispatched from `main`. The AMD64 leg retains the NKE workflow's verified runner label. The ARM64 leg uses the
repository-specific 16-vCPU, 64-GiB KVM scale set defined by `NVIDIA-dev/nke-gha-runners` at `488424d`. Those VMs are
single-use, so full-mode preflight may hand `/dev/kvm` to the attempt's primary group.

Full validation uses the existing repository secret `NKE_SOURCE_TOKEN`, which must provide read-only Contents access
to `dsx-ai-factory/nke`. Only the trusted default-branch workflow receives it. The checkout fetches the exact
catalog-pinned NKE commit into a clean staging path before the candidate checkout, does not persist credentials, and is
followed by explicit commit, tree and credential-state checks. The selected candidate receives the private source bytes
and declared outbound build network, but not the source credential itself. NKD uses the local checkout through a
process-scoped Git transport override.

## Two-pass AMD64 gate

The source lock contains verified AMD64 inputs but `localImages.amd64` is intentionally null. The initial toolchain
dispatch therefore builds the toolchain natively and uploads its exact local image ID. A follow-up change in the same review
must record that ID, extend the shared catalog platforms and toolchain map, and add the thin AMD64 Definition,
installer supplier selection and Environment. Only then can AMD64 full validation run. Runtime mutation of authored
inputs and reuse of the ARM64 image ID are forbidden.

## Acceptance

Each full leg must pass native architecture, Docker, KVM, effective CPU, memory, storage and timeout admission; build
through `nkdctl run local`; retain one Plan and completed Run; verify exactly one QCOW2 has no backing file and record its
SHA-256 and size; and pass NKE's pinned source-owned boot smoke under KVM. Plan, Run, material-manifest and serial/boot
logs produced before a failure are uploaded. A failure can occur before one or more of those records exists. QCOW2
upload is an explicit short-retention option because the image is large. These results remain unqualified integration
evidence and do not publish or sign an image.

FDE, generated hosted execution, publication and qualified byte reproduction are outside this change.
