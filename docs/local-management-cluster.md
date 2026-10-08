# Use NKD-built tools for NKE's management cluster

This is a deploy-only management diagnostic, not the complete laptop demo.
It consumes the completed `installer-tools-arm64` Run, creates one private
Kind management node, runs NKE's pinned Cluster API install/check stage, then
removes that node and its anonymous data volume. It rebuilds no artifacts.
Kind hosts management only; a CAPD/container tenant is not substituted for the
requested CAPNICo/NICoSim QCOW2 tenant.

## Prerequisites

Use the [first-time build guide](local-development.md) to enter a dedicated
Linux ARM64 Colima VM and complete the separate `tools` selection first.
The diagnostic requires Python 3.12+, Git, native Docker, eight CPUs, 20 GiB
RAM and 20 GiB free on the native output filesystem. These are this step's
admission thresholds, not measured full-demo or fresh OS-bake requirements.
Mac physical free space remains a separate limit.

No host Go, Make, globally installed Helm/Kind or application source build is
needed for this consumer. `nkdctl` must already exist from the build launcher's
bootstrap. It verifies/extracts the existing five-tool bundle into a fresh
private directory. The tools must match the selected installer: Helm 4.2.4,
Kind 0.32.0, kubectl 1.36.0, clusterctl 1.12.3 and ORAS 1.3.3.

The selected installer is the inspected candidate NKE commit
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345`, not NKE main or a qualified release.
Use a clean pinned clone. The command executes private copies of tracked Git
blobs for stage 04, `deploy/lib.sh` and `make/tool-versions.mk`; it does not
execute potentially hidden working-tree modifications or edit the NKE checkout.

## Run inside Linux/Colima

From the native NKD checkout/export that contains the completed tool Run:

```sh
set -euo pipefail
NKD_TOOLS_ATTEMPT=demo-tools-01  # Your already completed tools Run.
NKD_TOOLS_RUN="$PWD/.nkd/runs/$NKD_TOOLS_ATTEMPT"
NKD_BOOTSTRAP="$PWD/.nkd/laptop-bootstrap/$NKD_TOOLS_ATTEMPT/nkdctl"
NKD_MANAGEMENT_INPUTS=$(mktemp -d "$PWD/.nkd/management-inputs.XXXXXXXX")

# Requires access to the canonical NKE repository. This is a separate clone,
# not a modification of your development checkout.
git clone --filter=blob:none --no-checkout \
  https://github.com/dsx-ai-factory/nke "$NKD_MANAGEMENT_INPUTS/nke"
git -C "$NKD_MANAGEMENT_INPUTS/nke" sparse-checkout init --cone
git -C "$NKD_MANAGEMENT_INPUTS/nke" sparse-checkout set deploy make controller/hack
git -C "$NKD_MANAGEMENT_INPUTS/nke" checkout --detach \
  8fb2d53368823bd386b4fbd55b4ab6d3c0931345

# Acquired management-node runtime, not an NKD source-built NKE output.
docker --host unix:///var/run/docker.sock pull \
  kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5

python3 -B scripts/nke-local-management.py \
  --nke-checkout "$NKD_MANAGEMENT_INPUTS/nke" \
  --nkdctl "$NKD_BOOTSTRAP" \
  --tools-run "$NKD_TOOLS_RUN" \
  --out "$NKD_MANAGEMENT_INPUTS/diagnostic"
```

Use a fresh output directory for every execution. Separate source exports may
require absolute paths to the completed Run/bootstrap and the current script;
the [recorded replay](evidence/2026-10-08-local-management-cluster.md) identifies
the actual split-source paths used. Do not rerun a tool build merely to move its
accepted outputs beside this script.

The consumer deliberately uses the native Unix Docker socket and private
HOME/kubeconfig, ignoring inherited Docker context, Kind provider, release
selection and site credentials. It retains the source Kind node pin,
dual-stack networking and ImageVolume setting, but removes the exact guarded
Docker socket mount because this management-only route has no CAPD tenant.
Kind still creates a privileged node container. Use a dedicated trusted VM,
not a shared or untrusted Docker daemon.

## What success means

Success requires a Ready management node and all of NKE stage 04's checks:
cert-manager, CAPI core, kubeadm bootstrap/control-plane Deployments Available,
the Cluster CRD Established and matching CAPI version. NKE's source check treats
a cert-manager version mismatch as a warning, not an additional hard failure;
the recorded replay reported its intended 1.19.3 version. The source
stage fetches public provider manifests and runtime images. Those are acquired
dependencies, not additional NKD source builds or a hermetic deployment.

Before success, the consumer stops/joins owned subprocess groups, validates
exact node labels/name/image and its newly created anonymous `/var` volume,
then deletes that exact node and volume and checks disappearance. It rejects
pre-existing/shared volumes. It does not prune shared Docker resources.
The shared Kind network and acquired image cache remain. The fresh private
diagnostic directory retains extracted tools, source projection, observed input
hashes, kubeconfig and phase logs. Keep it out of Git and public logs.

This does **not** install NKE, publish/sign a Release, start CAPNICo/NICoSim,
boot a tenant or pass its workload. The complete [site handoff](nkd-to-nke-demo.md)
and [real QCOW2 guest probe](local-qcow2-guest-probe.md) retain their separate
acceptance boundaries. This step is a tested building block toward their
integration, not a replacement for the requested full demo.
