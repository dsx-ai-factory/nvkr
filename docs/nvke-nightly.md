# Nightly NVKR builds and NVKE end-to-end tests

The authored [workflow](../.github/workflows/nvkr-nightly.yml) runs daily at
03:17 UTC in `dsx-ai-factory/nvkr` and supports ordinary manual branch dispatch.
Forks can dispatch their branches manually; scheduled jobs run only upstream.
One public `ubuntu-24.04` AMD64 job builds and tests together, without NVIDIA
runners, repository secrets, signing permissions or registry writes.

The job checks out the selected NVKR commit and resolves NVKE `main` once.
The existing `TestEndToEnd` Go test uses that exact source revision to build fresh
local Runs, bootstrap a real site-controller KVM VM through kind/Lima, and test
one tenant control-plane node and one worker. Acceptance requires actual host
cloud-init completion on both tenant nodes, the selected workload image and
architecture, workload completion and cluster DNS. The parent passes only after
natural tenant deletion, deletion verification and joined VM/backend/network cleanup.

Source preparation reuses the existing AMD64 NVIDIA Definition and v0.23.0
catalogs, preserving their producer commands, material graph and toolchains. It leaves
committed catalogs and the NVKE checkout unchanged; other dependency pins retain
their owners. NVIDIA OVN remains selected. The existing native helper self-pin
has its separate maintenance owner, including its post-squash canonical-main re-pin.

## Run a workflow branch before merging

The following branch-dispatch instructions concern the unsigned NVKE E2E workflow.
The separate full GPU-site candidate is described below.

Push the branch used by your PR to your fork and enable Actions there if GitHub
prompts you. The workflow must also exist on the fork's default branch for manual
dispatch; sync an older fork with upstream after this workflow is merged.

Replace the repository and branch below with your fork and PR branch:

```sh
gh workflow run nvkr-nightly.yml --repo YOUR_GITHUB_USERNAME/nvkr --ref YOUR_PR_BRANCH
gh run list --repo YOUR_GITHUB_USERNAME/nvkr --workflow nvkr-nightly.yml \
  --branch YOUR_PR_BRANCH --event workflow_dispatch \
  --json databaseId,headSha,status,conclusion,url
gh run watch RUN_ID --repo YOUR_GITHUB_USERNAME/nvkr --exit-status
```

The Actions UI also supports **NVKR nightly NVKE end-to-end tests → Run workflow**
with the PR branch selected. No workflow inputs are required. This nightly
intentionally freezes NVKE main once per cycle; it accepts no NVKE-ref override.
The versioned catalog selections do not promise support for arbitrary
older source revisions. Contributors with
upstream write access can use `dsx-ai-factory/nvkr` instead of a fork.

Verify that `headSha` matches the PR commit and that the complete run concludes
`success`; ordinary PR CI and passing child subtests alone do not prove the full
lifecycle. The run summary records both source commits. Share the run URL and
tested NVKR commit on the PR. After pushing a fix, dispatch the branch again:
[re-running an old workflow](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs)
retains its original NVKR commit. A re-run resolves NVKE main again; compare the
NVKE commit in each run summary. GitHub documents the default-branch requirement
under [manual dispatch](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow).

## Resources and ownership

The committed [hosted configuration](../demos/nightly-nvke.yaml) owns the test's
resource selection; the workflow does not construct configuration in shell.
The standard Go `-exec` option starts the test binary in the checkout root, so
the existing loader and exact-source owner resolve these relative paths there.
A fixed local attempt/output name is safe because each job gets a fresh
workspace, runs once and publishes no build products.

The explicit `small` profile selects a 7-GiB site, 3 GiB per tenant VM, four
vCPUs per guest and 1 GiB headroom. The temporary 6-GiB bootstrap backend stops
before tenant creation. Initial admission requires 14 GiB actually available:
site plus the larger of bootstrap or two tenants, plus headroom. Tenant creation
rechecks 7 GiB beside the existing site. These checks reserve no resources.
An unset profile retains the existing scheduling defaults. These allocations
are functional-test settings, not proven hardware minima or performance qualification.
The small profile also sets the site's system reservation and the tenant's
Reserved/SystemReserved/KubeReserved memory to 256Mi, appends the guest kubelet
`--kube-reserved=cpu=1000m,memory=256Mi,ephemeral-storage=1Gi` override and restarts
it, sets OVN control-plane/node-zone memory requests to 128Mi, and adds 64Mi
memory requests to the seven site telemetry collector roles and three target
allocators. Recorded small-profile runtime acceptance covers these settings,
not the default reservations.

The six-hour job passes its remaining deadline to Go, which preserves two hours
for independently bounded cleanup. Setup requires a GitHub-hosted AMD64 machine,
KVM, unattended administrator access and sufficient disk/memory. A runner-scoped
udev rule preserves mode-0660 KVM access when device policy is reapplied; an actual
unprivileged read/write open admits the device. Unused Android, .NET and Haskell
SDK directories are reclaimed before builds; product files, Docker and Go remain.
The Packer producer still requires 64 GiB free workspace. Its authenticated
transport archives close after staging, while original material capabilities
remain held through producer-consumption recording.

Runtime compatibility changes retain the source's security and identity checks:
the selected installer owns legacy/current controller resource names; helper
staging uses source-supported `sh` and a bounded disk-backed scratch mount;
policy convergence waits only for the unchanged pinned running context to become
Ready within its existing deadline. Identity changes and unknown completion remain
terminal. Tenant pool observation uses the supported unfiltered inventory with
exact cluster/worker ownership, avoiding a separate name lookup during deletion.
After owned deletion starts, joined read-command exit-1 observations can be
retried within the existing 45-minute wait. They never establish absence or retry
the delete command. Success still requires a fresh valid inventory proving absence;
invalid JSON, changed ownership, cancellation and unjoined work remain failures.

## Evidence and privacy

The [reconciliation record](adr/reconciliation/consumer-acceptance-scopes.md#consumer-acceptance-scopes)
owns completed runtime evidence, its exact NVKR/NVKE revisions and its limits.
A passing run covers only its recorded revision; it does not prove the causes of
earlier failures, minimum hardware, conformance or qualification.

Build stdout/stderr and ordinary Go test outcomes appear in the public job log.
Raw deployment errors, host output and identity values remain in private local
captures. The workflow uploads no Actions artifacts or shared caches; binaries,
OCI images, QCOW2 files and private diagnostics remain on the disposable runner.
There is no public-error option. Trusted NVKE code retains sudo/Docker access on
that runner; these unsigned diagnostics grant no release or publication authority.

The ARM64 build/publish/consume nightly is retired. Generated ARM64 producer
workflows retain their existing admission policies, but no nightly invokes them
or the legacy published-artifact consumer.

## Dispatch a complete floating GPU-site candidate

The generated `nvkr-run-nvke-nightly-gpu-site.yml` producer uses the complete
[GPU-site composite](../inputs/definitions/nvke/gpu-site.composite.yaml) with its own
[Environment](../inputs/environments/github-nvke-nightly-gpu-site.yaml). On first dispatch
it freezes main once for `dsx-ai-factory/nvke`, `dsx-ai-factory/ovn-kubernetes`,
`dsx-ai-factory/cluster-api-provider-nico` and `NVIDIA/aicr`. Toolchains, acquired
dependencies and the Rancher source remain pinned. The production GPU-site workflow,
namespace and pins are separate and unchanged.

After maintainer configuration and merge to main, dispatch the generated producer:

```sh
gh workflow run nvkr-run-nvke-nightly-gpu-site.yml --repo dsx-ai-factory/nvkr --ref main
```

The producer is manual-only. The first bootstrap uploads `nvkr-runtime-inputs`; its
`plan.json` contains `runtimeInputs.sources` (repository/ref/commit/tree/timestamp) and
the exact captured authored input files. Every partition receives that frozen Plan.
A retry restores the original same-Run artifact; it fails if that artifact has expired.
Use a new dispatch to select newer main commits. Workflow topology or recipe changes
still require reviewed regeneration.

Publication is confined to `ghcr.io/dsx-ai-factory/nvkr/nightly/gpu-site` and its derived
output repositories. The independent
[qualification policy](../inputs/policies/nvke-nightly-gpu-site-qualification.yaml)
generates a complete-composite qualifier and native smoke pair. Finalization and
qualification replay the saved inputs and exact Git commits without re-resolving main.
Artifact checks and smoke do not establish GPU-site deployment or exact-byte reproduction.
No hosted floating-composite execution has been recorded.

Maintainers must authorize the existing scoped registry writer and independent reader
for this namespace, confirm source/private-transport readers and protected publishing/ARC
environments, and admit the new exact main-workflow signer and qualification policy pair.
Retain candidate manifests and exact source/material objects for at least the 14-day
input/handoff artifact lifetime; authenticated source mirrors may supply retained commits.
The input snapshot does not archive upstream Git objects. Missing exact inputs fail closed.
Generation does not create credentials, registry permissions, retention policies or runners.
