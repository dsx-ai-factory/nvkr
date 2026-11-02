# Nightly NVKR builds and NVKE end-to-end tests

The authored [workflow](../.github/workflows/nvkr-nightly.yml) runs daily at
03:17 UTC in `dsx-ai-factory/nvkr` and supports ordinary manual branch dispatch.
Forks can dispatch their branches manually; their scheduled jobs are skipped. One `ubuntu-24.04`
GitHub-hosted AMD64 job builds and tests together. It uses no NVIDIA runner,
protected environment, repository secret, signing permission or registry writer.
It uploads no Actions artifacts or caches. Binaries, OCI images, QCOW2 files and
private diagnostic files stay on the disposable runner's local disk. Build stderr,
ordinary build output and bounded host/kernel/cgroup samples enter the public job
log. Raw deployment stdout and stderr remain private.

The job checks out the dispatched NVKR commit and resolves NVKE `main` once.
It passes that exact NVKE commit to NVKR's existing `TestEndToEnd` Go test.
The local preparation owner derives temporary source overlays from the existing
AMD64 NVIDIA inputs and the existing current-main disk-material/CAPI-init
compatibility selections; committed catalogs and locks remain unchanged.
Source-built NVKE producers use the frozen main commit. Other dependency pins
retain their owners. NVIDIA OVN remains selected.

`TestEndToEnd` calls `BuildArtifacts`, then the existing native site owner.
That builds fresh local NVKR Runs and bootstraps a real site-controller KVM VM
through the temporary kind/Lima backend. Site API and Ready-node checks are
followed by a tenant with one control plane and one worker, including workload
and DNS checks. VM, backend and network cleanup remain owned and joined by Go.
The workflow does not invoke the demo's E2E CLI or NVKE's separate test harness.
The existing demo executable supplies its existing network privilege helper.

The selected installer declares the site controller's resource identity. NVKR
checks the supported legacy `nke-controller` and current `nvke-controller`
layouts against that installer's release, chart and explicit naming overrides.
The startup-probe renderer, controller-log capture and API network policy use
the same checked selection. Unsupported or inconsistent declarations fail before
those consumers run; local OCI import aliases retain their separate ownership.

## Run a workflow branch before merging

Follow [Run E2E tests for your pull request](pr-e2e.md) to trigger this workflow
on your own PR branch, watch the result and share it with reviewers. The guide
starts with running in your own fork and also covers branches in this repository.

## Resources, timeouts and evidence

The six-hour job has bounded setup steps and passes its remaining deadline to
Go. `TestEndToEnd` reserves two hours inside that deadline for independent owned
cleanup. Setup refuses a non-hosted/non-AMD64 host or missing KVM.

Hosted setup uses a persistent udev rule granting the current runner UID ownership
and the `kvm` group access with mode `0660`. It validates the rule, reloads it,
triggers device policy, waits for completion and checks an unprivileged read/write
open. This adapts [GitHub's documented KVM setup](https://github.blog/changelog/2023-02-22-hardware-accelerated-android-virtualization-on-actions-windows-and-linux-larger-hosted-runners/)
without granting world-write access. Setup and final diagnostics record identity,
device ownership/mode/inode and ACLs. [Run 41132205486](https://github.com/dsx-ai-factory/nvkr/actions/runs/41132205486)
completed the QCOW2 build, then failed the host-user KVM open before site launch
despite its earlier one-time ACL grant. Its log does not identify the event that
changed access. [Run 41141720839](https://github.com/depohmel/nvkr/actions/runs/41141720839)
subsequently completed the full build, passed the later KVM admission and set up
its owned network, then rejected NVKE's changed MachineImage image-URL placeholder.
Its owned cleanup completed and final diagnostics retained runner ownership of
KVM with mode `0660`. Boot projection now admits exactly one retained or current
source placeholder, with both source architecture fixtures covered by regression
tests. [Run 41159653883](https://github.com/depohmel/nvkr/actions/runs/41159653883)
completed the fresh build, real site VM cloud-init, kind bootstrap and healthy
controller deployment. NVKE's `site_cluster` phase then failed while waiting for
provisioning. The bootstrap guest had 3,551 MiB available memory and 10,667 MiB
free disk; sampled bootstrap pods were Ready apart from one completed job.
Owned VM/backend/network cleanup completed and Actions artifact count was zero.
The underlying provisioning error and full site/tenant acceptance remain unverified.

The Go test prints phase start/end/failure and child process start/exit
observations. Once a minute, a parent-test heartbeat reports the last started
phase, its age and how long the public child stream has been idle. That heartbeat
shows parent liveness only; it does not establish child progress or success.
Exact-source nightly builds retain machine-command stdout in their bounded
captures and private logs instead of forwarding large Plan JSON to Actions.
Build command stderr and the final build's ordinary output continue streaming.
Bootstrap stdout, arbitrary stderr and raw diagnostic files stay private. Its
source-owned JSON log records are projected live into bounded, closed phase/outcome
values, closed cluster states and static stage labels. Final installation also
reports only exact admitted item/type labels from the selected installer's stderr.
These live observations are explicitly partial and establish neither readiness
nor process joining. The existing owned stderr drain performs the projection;
there is no separate observer goroutine. Parsing is bounded to 8 MiB and 64
observations per command. Malformed or oversized records stay private, and
diagnostic loss preserves the raw drain and command result. A failed bootstrap also
observes the temporary backend's guest memory/disk and an anonymized pod table
before cleanup, through existing bounded subprocess and presentation owners.
Cluster and node conditions are read through NVKE's existing Service-proxy route
using the owned bootstrap kubeconfig. Only selected condition names/statuses and
static source failure-context labels are presented; identities and raw messages
remain private. Context labels describe literal source phrases, not a decoded
Temporal error or proven root cause. Initialization observations also select closed
nested activity labels and Kubernetes init-phase, preflight-check, wait and
client-transport labels from private condition messages and already retained owned
serial snapshots. Raw error details remain private; these literal observations may be absent for unknown
failures. Unknown failures stay unclassified.
Logging-only service drop-ins route kubelet and containerd output to the private
owned serial console when those services start or restart. Transport labels
observe fixed phrases anywhere in that snapshot; they do not establish why the
API request failed. Owned serial snapshots also yield closed Linux OOM-kill
observations, without process identities or raw kernel messages. Missing markers
do not prove the absence of memory pressure. Raw terminal errors have no public
dispatch option and remain private.
The Go E2E presenter queues these observations without blocking cleanup; it may
drop them under backpressure. Plain writers retain bootstrap diagnostics privately.
Ordinary diagnostic loss preserves the original failure; an unjoined observation
still requires retaining dependent resources. These observations grant no readiness
or qualification acceptance.

A failed site installation also projects the already captured, joined pod
observation through the existing anonymized table. It starts no additional probe;
raw pod messages and controller logs stay private. A missing or dropped summary
does not establish pod health or identify the failed installation step.

Failed tenant cleanup can project a fixed source-wrapper label from its bounded
private capture, such as cluster lookup or delete request. Unknown, changed,
oversized or ambiguous records stay unknown. These untrusted labels identify
neither the transport cause nor whether a delete reached the server. Error text,
trace IDs and response bytes remain private; command handling is unchanged.
Failure diagnostics also observe only HTTP statuses from the already selected
site endpoint's [listener and dependency-readiness paths](https://github.com/dsx-ai-factory/nvke/blob/1ce10b480cf52f955b6d018cc7ba7ad141c6faf9/controller/internal/server/health.go), with three-second
deadlines and no redirects, proxy or credentials. Management API readiness and
pod-observation availability use fixed labels. Bodies and transport errors stay
private; these probes neither retry deletion nor grant readiness acceptance.

A failed tenant cloud-init probe can project closed Pod/container reason labels,
bounded exit/signal/restart values and documented cloud-init status labels/error
counts through that same presenter. Missing, invalid and oversized JSON remain
explicitly unavailable observations. Pod/node identities, arbitrary reason
strings, error values and raw host stdout stay private. This changes neither
the exact execution/identity checks nor the requirement for zero exit and clean
completion. Contract tests cover the projection; its fresh hosted exercise is
pending.

The CI resource wrapper samples host memory/disk, cgroup memory events and Docker
usage while the Go test runs, with a 15-second pause between samples. It records
bounded kernel warnings before and after the workload. It preserves workload
failures and fails an otherwise successful invocation if the sampler cannot join. These observations can help investigate a
runner shutdown; they establish neither its sender nor minimum resource
requirements. Product files, process arguments and environments are not printed
by the sampler.

After authenticated Packer staging completes and its container is removed, the
transport-only input and work archives close before the installer VM starts.
Original material capabilities remain held through producer-consumption recording.
This avoids retaining another complete input carrier alongside the guest.

Before checkout and builds, the disposable runner removes its unused Android,
.NET and Haskell SDK directories and records free space before and after cleanup.
It avoids recursive size diagnostics that can time out before cleanup starts.
Product outputs, Docker and Go stay local and intact. The disk producer's
64-GiB free-workspace admission remains unchanged. [Run 41075323371](https://github.com/dsx-ai-factory/nvkr/actions/runs/41075323371)
reached that gate after preparing the installer ISO, with 48,748 MiB available
against 65,536 MiB required. The cleanup change still needs a complete hosted run
to establish sufficient space for disk build and deployment.

The hosted workflow explicitly selects `resources.profile: small`, a 6-GiB site
VM, 3 GiB per tenant VM, four vCPUs per guest and 1 GiB of host/QEMU headroom.
An empty profile preserves the existing demo configuration. The small profile is
a functional-test candidate; its settings are not proven hardware minima or a
performance qualification. Host admission still checks actual available capacity.
The temporary bootstrap backend uses 6 GiB and is joined before tenant creation.
Initial admission still requires 13 GiB available: the site plus the larger of
the bootstrap backend or two tenant VMs, plus headroom. After bootstrap teardown,
tenant creation rechecks 7 GiB available beside the existing site. These
observations reserve no resources.

[Run 41252923744](https://github.com/depohmel/nvkr/actions/runs/41252923744), on
NVKR `dcc9f571d5291e68affd0b3fcaa0792b0fff55d8` and frozen NVKE
`ca3f7c025c1474d9b0b9a19a830a3006168c6de7`, completed the fresh build and
NVKE bootstrap. Final stack installation then failed. The owned site VM's serial
snapshot contained a kernel OOM-kill marker; the pod snapshot showed widespread
restarts, unready containers and Pending pods. The host sample immediately before
failure still had 4,951 MiB available memory. This establishes observed guest
memory pressure, without identifying the killed process or exact failing Helm
release. VM/backend/network cleanup completed, and Actions artifact count was zero.
The 6/3-GiB allocation rebalances the previous 4/4-GiB profile while preserving
its peak admission.

[Run 41266072695](https://github.com/depohmel/nvkr/actions/runs/41266072695), on
NVKR `53ed72e8d7be9b8890ed80d521dc513c859e48fd` and the same frozen NVKE
commit, completed the fresh build, bootstrap, full stack installation and SPIFFE
setup with that allocation. It failed 6.5 seconds into the site identity handoff,
before site/tenant test acceptance. VM/backend/network cleanup joined and Actions
artifact count was zero. The public log does not identify the internal handoff
failure. This run establishes installation progress, not a successful handoff,
tenant execution, complete resource sizing or full E2E acceptance.

Source inspection found two staging incompatibilities: the current controller's
root filesystem is read-only without writable `/tmp`, and its distroless debug
base provides PATH-resolved `sh` without `/bin/sh`. The native owner now supplies
a dedicated bounded scratch volume through supported controller chart values,
uses `sh` and admits that exact mount before staging. The source's security
settings and identity proof remain unchanged. Fixed identity-stage observations
locate future failures while raw errors remain private. The existing helper's
runtime and Linux pipe-adoption guards also admit AMD64 alongside ARM64, matching
its declared producer platforms. Its source self-pin must select that fix before
building the helper. Neither incompatibility is proved to be run #16's first
failing branch.

[Run 41321949914](https://github.com/depohmel/nvkr/actions/runs/41321949914), on
NVKR `29987555e55ce2a0e83b49aa8391aaf95400f736` with the same frozen NVKE
commit, completed fresh builds, bootstrap, full stack installation, identity
context observation, helper staging and the authenticated same-boot helper
probe. Policy preparation failed after 28.88 seconds, before host
reconfiguration or the site/tenant subtests. The pre-teardown pod snapshot
showed the controller at 0/1 Ready and the main SPIRE pod at 1/2 Ready. That
snapshot does not identify the first private command error or prove the cause
of the failure. Owned cleanup joined and Actions artifact count was zero.
The successful staging/probe steps establish those integrated boundaries;
complete policy, handoff, tenant and resource acceptance still require a
successful full run.

[Run 41331140209](https://github.com/depohmel/nvkr/actions/runs/41331140209), on
NVKR `914ed406250d33e6f9f52ffbce9063bda86814a7` with frozen latest NVKE
`1ce10b480cf52f955b6d018cc7ba7ad141c6faf9`, completed fresh builds, identity-policy
creation/convergence, authenticated host handoff and final proof. The Go site
and tenant subtests passed, including real node/cloud-init readiness and the
single workload/DNS check. The parent test failed during natural tenant-deletion
cleanup when its pool-ownership query and fresh cluster-inventory confirmation
both exited with status 1. Neither absence nor retained cluster state was
observed at that failure. Simulator and network
teardown then finished; this does not prove successful natural tenant deletion
or complete joined cleanup. Actions artifact count was zero. The underlying
private query error is unknown; a successful full run remains required.

[Run 41344383365](https://github.com/depohmel/nvkr/actions/runs/41344383365), on
NVKR `0558ac3eebc6fc2ce0fa9685b9e8ab4777410a66` with the same frozen NVKE,
passed fresh builds, identity handoff/proof and both Go subtests: site (7.90
seconds) and tenant (2968.61 seconds), including workload/DNS. The deletion
command itself then exited with status 1 before deletion waiting began; the
parent failed after 8033.05 seconds. Its private cause is unknown. Simulator/VM,
DNS and network teardown finished, but natural deletion and complete joined
cleanup did not pass. Actions artifact count was zero.

[Run 41358202754](https://github.com/depohmel/nvkr/actions/runs/41358202754), on
NVKR `bca74c3e9a4e07c99503c30ea0d14d8a721d8ee2` with the same frozen NVKE,
completed fresh builds, identity handoff/proof and the site subtest (8.41
seconds). The tenant subtest failed after 2086.58 seconds at the host cloud-init
probe's terminal Pod phase/container-count check. Its public evidence does not
identify the Pod reason, command exit or host JSON status. Tenant DNS/workload
acceptance was not reached. Natural tenant deletion and deletion waiting both
finished, followed by joined owned VM/backend, DNS and network cleanup. The
parent still failed after 6693.85 seconds because tenant acceptance failed.
Actions artifact count was zero. This establishes natural deletion for this
failed-tenant attempt, not a proven cause or fix for the preceding failures or
a successful complete E2E run.

The native tenant owner uses NVKE's unfiltered JSON node-pool inventory and
selects the already-observed cluster ID. This avoids the filtered CLI's separate
cluster-name lookup during deletion. Worker identity and infrastructure checks
remain required; missing pools are allowed only after owned deletion starts,
and pool-query errors still require fresh cluster absence or remain failures.

The profile selects 256 MiB for system and Kubernetes memory reservations through
the supported NVKE request fields and the installed kubelet package's documented
operator override. The complete Kubernetes reservation map preserves the source's
CPU and ephemeral-storage entries. The operator file is checked with systemd's
own parser; a private candidate preserving its original bytes is checked before
replacement. Existing nonempty operator arguments are rejected rather than lost.
The existing demo path does not change that file when the profile is unset.

The small profile keeps every telemetry and NVIDIA OVN component, image, feature,
memory limit and replica setting. It selects 64-MiB collector and target-allocator
memory requests and 128-MiB requests for the two selected OVN containers. These
are explicit test scheduling settings; actual memory use remains to be measured.
The source-default profile cannot fit this host selection: its seven collector
main containers alone request 2.75 GiB, while the former site reservations left
less than 1 GiB allocatable. That later scheduling contradiction does not prove
the cause of the earlier kubeadm initialization failure.

The earlier [hosted experiment](https://github.com/depohmel/nvkr/actions/runs/40578231640)
demonstrated AMD64 KVM, a disk build and NVKE's separate VM test harness, but
does not prove this native NVKR topology or its cleanup. That experiment also
encountered severe disk pressure. A completed run of this exact workflow is
required before claiming hosted acceptance.

The ARM64 build/publish/consume nightly cycle is retired. Generated ARM64 producer
workflows remain, but no nightly invokes them or `TestNightlyPublishedCandidate`.
The legacy script and consumer remain uninvoked pending removal. This local hosted
nightly grants no qualified release or publication acceptance.
