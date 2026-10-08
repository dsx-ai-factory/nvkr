# NKD artifacts for NKE's staged site installer

This document covers the staged-installer boundary only. It is not an analysis
of Lia's complete local Colima/NICoSim environment. See the
[whole-demo analysis](lia-local-demo-analysis.md) for the transcript, local
provider/runtime distinction, missing artifacts and exact-byte deployment tasks.

The intended developer journey is: build the ARM64 artifacts with NKD, make the
exact produced images and charts reachable by NKE, run the NKE site installer,
create a tenant and verify it, then rebuild and retest a source change.

The exact packaged simulator also passed a later
[native libvirt-backed startup probe](evidence/2026-10-08-native-nicosim-libvirt-startup.md)
after explicit VM prerequisite installation. That probe created no guests and
does not establish tenant provisioning, networking or workload acceptance.

The later [real QCOW2 guest probe](evidence/2026-10-08-native-nicosim-qcow2-guest.md)
passed NICoSim VM creation/readiness, authenticated SSH, cloud-init, software TPM,
agent and staged Kubernetes-package checks, then deleted the owned guest and
stopped/joined the simulator. Its [runnable command](local-qcow2-guest-probe.md)
consumes completed Runs without rebuilding. This is not a site/tenant install
and does not supersede the failed complete OS smoke or missing DSX input.

The separate [management diagnostic](local-management-cluster.md) now consumes
the completed five-tool Run for real Kind management/CAPI installation and
owned node/volume cleanup. It uses pinned NKE stage bytes without rebuilding
artifacts. It does not deploy the NKE site or create a CAPD/container tenant;
the full CAPNICo/QCOW2 path still needs integration.

The later [deploy-only site consumer](local-site-integration.md) passed a
[real Colima replay](evidence/2026-10-08-local-site-integration.md): six prepared
charts, exact controller/profiles/CAPNICo image handoff and runtime image-ID
checks, site health, registration and API access, then owned cleanup. This
supersedes the earlier site-installation gap, not the tenant/workload gap.

The internal downstream [network guard](evidence/2026-10-08-local-network-guard.md)
also passed native setup/restoration and a deliberate consumer-failure replay.
It is not yet connected to the site entrypoint, simulator/guest lifetime or
disk serving. Its running DHCP/DNS process does not establish guest delivery,
BGP routes or tenant provisioning.

**The artifact-build part is tested; the complete NKD-to-demo E2E is not yet
validated.** Do not use the completed 22-output local Run as proof that a site or
tenant was installed. The [first-time build guide](local-development.md) is the
working build entry point; this document records the exact downstream handoff
and blockers rather than inventing a completed installation recipe.

## Which demo

This targets the staged NICo site-controller installer introduced in
[NKE PR #1160](https://github.com/dsx-ai-factory/nke/pull/1160), inspected at
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345` on October 7, 2026. The PR is open,
based on `deploy/nico-layout`; its install scripts must not be assumed present on
NKE `main` or on the older artifact pins. Its authoritative instructions are
[deploy/README.md](https://github.com/dsx-ai-factory/nke/blob/8fb2d53368823bd386b4fbd55b4ab6d3c0931345/deploy/README.md)
and the [site-controller guide](https://github.com/dsx-ai-factory/nke/blob/8fb2d53368823bd386b4fbd55b4ab6d3c0931345/docs/site-bootstrap/install-site-controller.md).

It takes an existing Kubernetes site kubeconfig, NICo endpoint and bootstrap
trust material. That is the installer's starting boundary, not proof that Lia's
demo used remote hardware. Her local environment is Colima plus NICoSim, as the
user clarified. Kind hosting the management cluster does not imply a CAPD
infrastructure provider; CAPNICo can use NICoSim. The exact simulator backend
used in the demonstrated session is not established by the meeting transcript.
The requested acceptance target is now explicit: tenant nodes use the existing
NKD-built ARM64 QCOW2 through a real NICoSim VM backend. A newly built tenant-node
OCI image is neither required nor a substitute for this target.

## Responsibility and input map

NKD owns build/acquisition, observation, packaging and, on an admitted hosted
route, publication/signing. NKE owns site configuration, install order, chart
values, infrastructure provisioning and tenant lifecycle.

| Demo input | Existing local NKD output or source | Remaining handoff |
| --- | --- | --- |
| Controller image | `cwe`, built from NKE Git | Copy unchanged manifest/layers to a registry reachable by the nodes; bind the new repository and digest |
| Four external charts | `postgresql`, `spire-crds`, `temporal`, `spire` | Publish/copy their OCI layouts without rewriting bytes; use the matching versions in NKE's manifest |
| Controller chart/templates | `controller-chart` is image-bound; the separate `site` selection matches the installer source | Prepared local chart inputs now cover inventory rendering and the executor's local-path branch; complete staged installation and image override still need validation |
| CAPNICo provider | `capnico-controller-arm64`, built from pinned public Git | Reachable development image plus NKE's `CAPNICO_IMAGE=repository:tag` override. The current override is tag-shaped; do not silently turn it into a digest contract |
| Provisioned provider | `cluster-api-provider-provisioned-arm64` | Available output, but not selected by the NICo deploy path. Building it is not proof that this demo uses it |
| Operator CLI/tools | `nkectl`, acquired `operator-tools`; separate source-built `installer-tools-arm64` | Five-tool Run, verified extraction and native version probes passed; integration into the complete journey and `all` remains. Python and curl remain host prerequisites; all produced tools are Linux ARM64 |
| Dependency images | Twelve acquired ARM64 image outputs | Align chart values or node mirrors with the reachable image locations; stage 4 and NKTP also need inputs outside this set |
| NKTP | Separate four-output `site` Run completed the source chart and locked dependency preparation | Packaged chart handoff has real rendering/dry-run evidence, but runtime images are not a complete mirrored NKD set and staged installation remains unexercised |
| Profiles content image | Separate `profiles` Run builds `controller-profiles` from the staged-installer source | Real Docker build/content tests passed. The diagnostic ARM64/26.04 plain profile still needs exact held-disk serving, reachable registry, and real provisioning; it is not included in `all` |
| Node QCOW2 | `nke-node-image-arm64` in the `all`/`os` selection; Run 04's bake completed and a copied disk reached SSH/cloud-init | Full smoke failed in SPIRE checks; retained material lacks DSX IMDS. The new diagnostic plain 26.04 profile has content tests only. Its exact disk handoff/provisioning and the complete OS contract remain unvalidated |
| Site cluster, NICo access, SPIRE bootstrap CA, tenant login | Operator's authorized test configuration | Not build artifacts. Keep secrets and kubeconfig outside Git; do not invent credentials, site identity or trust roots |

The artifact catalog currently builds the controller from NKE `v0.21.0`, commit
`33fb71b2f4224b470ebc628a9165fc4b92870406`, and CLI from `cli/v0.1.0-rc.2`,
commit `ed9fb09b04fd40bf331d25df2222206ffaca593e`. Those are not PR #1160's
source revision. Its readiness gate checks the installer checkout against the
manifest's source pin. Merely repinning an older catalog can also fail when the
source-owned build or pin-set paths have changed. Matching build instructions,
compiler, pin-set mapping and installer inputs must be tested together.
The additional `site` Definition aligns four candidate producers to PR #1160's
exact commit: CLI, controller image, image-bound controller chart and NKTP chart.
It uses the source-owned CLI Make build and controller Dockerfile, rather than
assuming the older linker/module contracts still work. Compiler contracts and
an isolated NKTP dependency/package probe pass. The later Colima
[four-output site Run](evidence/2026-10-08-colima-site-and-tools-build.md) completed;
no installation is recorded. This separate selection does not replace the retained
22-output Run or become part of `all` automatically.

At the same installer source, `deploy/nke-deploy.sh` already supports packaged
chart items/overrides. The separate wrapper
`deploy/stages/05-install-site-controller.sh` consumes
`CAPNICO_IMAGE=repository:tag` and turns it into a CAPNICo manifest override
without building the image. Setting that variable on the executor directly
does not select a different CAPNICo image.
These installation hooks must not be confused with the older Tilt/source helper.
With the default deploy path, `deploy/lib/describe_site.py` still renders
source-tree charts and builds their dependencies during inventory preparation.
The prepared-input adapter below gives both rendering and the executor's
local-path branch the same exact archives, without changing the NKE checkout.
This has real render and executor dry-run evidence, not a completed staged install.

Tool versions need alignment too: the acquired operator bundle contains Helm
`v4.1.4`, while the installer pins `v4.2.4`, ORAS `1.3.3` and
`clusterctl v1.12.3`. Its readiness script warns on a Helm version mismatch;
that is not a hard version gate, but neither is it a matched tool pin.
The site build wrapper uses pinned Helm 4.3.0. The separate
[source-tool selection](local-development.md#build-the-installer-tools-from-source)
declares all five matching tools. Its later Run, verified extraction and native
version probes passed; it still needs integration into the deploy-only
journey; it does not replace the build wrapper automatically.

## Build and inspect today

Follow [clean-machine setup](local-development.md#start-from-a-clean-machine),
then, from the NKE Recipes implementation checkout:

```sh
bash scripts/nke-full-arm64-local.sh --check components
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh demo-components-01 1 components
jq '{attempt,status,lane,expectedCount:(.expected|length),
     observedCount:(.observed|length),failure}' \
  .nkd/runs/demo-components-01/lifecycle.json
```

Use a fresh attempt name. Omit `NKE_LOCAL_CLONE` for canonical remote Git, provided
private-source authorization works inside the execution host. Require `complete`,
22/22 and no failure before reusing any output. This selection still acquires
third-party images/charts; it is not a full source-built distribution.

The [recorded container-bootstrap Run](evidence/2026-10-07-local-container-bootstrap.md)
passed these artifact gates. The OS requires its separate capacity gate and real
bake/boot. On October 8, user-authorized failed-attempt cleanup recovered enough
workspace for the real Colima prerequisite check to pass; see the
[storage and preflight evidence](evidence/2026-10-08-local-storage-cleanup.md).
The first fresh Docker disk attempt failed before Packer during SPIRE snapshot
preparation. The native-filesystem retry reached kernel installation but failed
after Colima's global OOM killer terminated QEMU. It was canceled and recorded failed
with zero accepted disks. Its run-owned build container was cleaned up; the failed
Run was retained. The fresh non-overlapping attempt on the preserved 24 GiB
profile completed one disk build and exported the independently hash-checked
2,851,733,504-byte non-FDE QCOW2; boot smoke remains a separate check. See the
[execution evidence](evidence/2026-10-08-colima-local-os-build.md).
The observed installer is Ubuntu 26.04.1, not the fixture profile's Ubuntu
24.04 FDE choice. The exact disk/profile handoff still needs to honor that
distinction. Passing preflight or building materials is not bake/boot evidence.

## Prepare charts without rebuilding them

`scripts/nke-prepare-chart-inputs.py` accepts all six exact Helm OCI layouts
needed by the pinned installer. NKD verifies the selected manifest, all layout
blobs, chart/config identity and bounded archive structure, then exports the
unchanged `.tgz` bytes. The adapter copies only tracked deploy files into a new
private directory, replaces each chart source with its prepared local archive,
and revalidates the result with NKE's own deploy-item reader. It does not run
Make, fetch chart dependencies, modify NKE, install a site or mint a Release.

Prerequisites: Python 3.9 or newer, Git, a native `nkdctl` built from this change,
and a clean NKE Git checkout at
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345`. Linux ARM64 tools from a Colima Run
must execute inside Colima, not directly on macOS. For a host-side handoff,
`make build` creates the native `bin/nkdctl` using the repository's pinned Go.

The following prepares inputs after the separate `site` and `components` Runs
complete. Two separate Runs are recorded; this is not a combined Run or a
completed deployment:

```sh
# From the NKE Recipes checkout; choose existing, completed attempts.
set -euo pipefail
: "${NKD_SITE_ATTEMPT:?Set the completed site attempt}"
: "${NKD_COMPONENTS_ATTEMPT:?Set the completed components attempt}"
: "${NKE_INSTALLER_CHECKOUT:?Set the clean, exact pinned NKE checkout}"
: "${NKD_CHART_INPUTS:?Set a NEW output directory outside NKE and all OCI layouts}"
make build

# These are diagnostic observations, not signed Release authority.
jq -s -e 'length == 2 and all(.[];
       .status == "complete" and .failure == null and
       (.expected|length) == (.observed|length))' \
  ".nkd/runs/$NKD_SITE_ATTEMPT/lifecycle.json" \
  ".nkd/runs/$NKD_COMPONENTS_ATTEMPT/lifecycle.json" >/dev/null

chart_args=()
for item in controller nktp postgresql spire-crds temporal spire; do
  case "$item" in
    controller) attempt="$NKD_SITE_ATTEMPT"; output=controller-chart ;;
    nktp) attempt="$NKD_SITE_ATTEMPT"; output=nktp ;;
    *) attempt="$NKD_COMPONENTS_ATTEMPT"; output="$item" ;;
  esac
  manifest=$(jq -er --arg name "$output" \
    '[.observed[] | select(.name == $name and .kind == "helm-oci")] |
     select(length == 1) | .[0].digest' \
    ".nkd/runs/$attempt/lifecycle.json")
  chart_args+=(--chart "$item=$(pwd)/.nkd/runs/$attempt/packages/$output"
               --manifest "$item=$manifest")
done
python3 scripts/nke-prepare-chart-inputs.py \
  --nke-checkout "$NKE_INSTALLER_CHECKOUT" --nkdctl "$(pwd)/bin/nkdctl" \
  "${chart_args[@]}" --out "$NKD_CHART_INPUTS"
export DEPLOY_PATH="$NKD_CHART_INPUTS/deploy"
```

Run the block in Bash. Use absolute paths for the three path variables. Versions
must match NKE's selected items, including controller `0.0.0` and NKTP
`0.1.0-rc.2`; an older chart fails rather than being silently substituted.
The output is private configuration with `charts/<item>.tgz` and otherwise
unchanged deploy semantics. Existing destinations, dirty/wrong source pins,
missing or extra items, wrong manifests, unsafe archives and output paths inside
NKE or a producer layout fail without leaving prepared output.

NKE's renderer consumes these archives through `DEPLOY_PATH`. Its executor also
consumes them through the local-path branch, but **a release-manifest chart
reference takes precedence over that path** unless explicitly overridden.
Do not claim setting `DEPLOY_PATH` alone forces the complete staged wrapper to
use local archives when `RELEASE_FILE` supplies different chart references.
Preserve the wrapper's source/readiness checks; do not disable them to make the
demo pass. Registry transport, a consistent manifest/override selection and the
real installation remain separate handoff work.

The [real six-chart handoff probe](evidence/2026-10-08-local-chart-handoff.md)
passed native NKE rendering and executor dry-run without fetching or rebuilding
chart dependencies. It deliberately records four completed-Run charts separately
from two manual packaging probes. A later
[completed-Run handoff](evidence/2026-10-08-colima-site-and-tools-build.md) repeated
both probes with all six charts from completed NKD Runs. Neither is deployment.

## Registry and release-manifest handoff

`packages/<name>/` contains an OCI layout, not a URL the kubelet can pull. Loading
an image into Podman on the build host does not stage it into the site nodes.
Lia's stage 2 pulls through each node's containerd from a registry.

For development, ORAS supports an unchanged OCI-layout copy. The following is a
handoff pattern for an explicitly authorized, reachable development registry,
not a registry-copy test already completed by this guide:

```sh
: "${NKE_DEV_REGISTRY:?Set the authorized registry host/path reachable by site nodes}"
NKE_DEV_ATTEMPT=demo-components-01
NKE_DEV_LAYOUT="$(pwd)/.nkd/runs/$NKE_DEV_ATTEMPT/packages/cwe"
NKE_DEV_DIGEST=$(jq -er '.manifests | select(length == 1) | .[0].digest' \
  "$NKE_DEV_LAYOUT/index.json")
oras cp --from-oci-layout "$NKE_DEV_LAYOUT@$NKE_DEV_DIGEST" \
  "$NKE_DEV_REGISTRY/cwe:$NKE_DEV_ATTEMPT"
```

The destination must serve the same manifest digest before it becomes an
installer input. A later [real loopback registry probe](evidence/2026-10-08-local-registry-handoff.md)
copied the completed site Run's controller and bound chart and matched both
manifest digests on registry read-back. That two-output test does not prove a
complete dependency mirror, node access or installation. Repeat the same transport for each required image/chart,
verify read-back digests, configure node authentication/mirrors and use new
attempt-specific tags. Do not call this diagnostic transport NKD release
publication, signing, qualification or release verification.

NKE's [release-manifest example](https://github.com/dsx-ai-factory/nke/blob/8fb2d53368823bd386b4fbd55b4ab6d3c0931345/deploy/release-manifest.example.yaml)
names the controller as `controller`, not NKD's `cwe`, and the four charts by
matching deploy-item names. It also carries source and chart versions. For a
manual local manifest, preserve the actual unqualified lane and identify it as
diagnostic; never write `pipeline-attested` simply to suppress a warning.

For an actual signed NKD Release, the installer supports `RELEASE_REF` and runs
`nkdctl release verify` plus NKE's converter. The caller still supplies the exact
source pin and chart versions. A local `lifecycle.json` is not a Release and must
not be passed off as verified release output. At the inspected PR head, the
converter rejects `disk-image`; feeding an entire OS-containing Release is not
an established handoff. Its executable mappings alone also do not install tools.

## Run the NKE installer once the handoff is ready

These are NKE's existing commands, conditional on an authorized test site,
source-aligned manifest, reachable artifacts and a completed site configuration:

```sh
# In the exact NKE checkout named by the release manifest:
export SITE_KUBECONFIG=/absolute/path/to/authorized-test-site.kubeconfig
export RELEASE_FILE=/absolute/path/to/release-manifest.yaml
export SITE_CONFIG=/absolute/path/to/private-site-config.yaml
deploy/stages/00-check-readiness.sh check
deploy/install.sh --dry-run
# Review the chosen site and printed mutations before the real install:
deploy/install.sh
deploy/stages/05-install-site-controller.sh check
deploy/stages/06-secure-and-register.sh check
```

Use NKE's site-config example to populate the real site name/trust domain/VIP,
architecture, existing bootstrap CA files, NICo credentials, registry access and
tenant credentials. Set `NODE_EXEC`/`NODE_MAP` for how to reach the actual nodes;
without `NODE_EXEC`, node commands run locally. Admit only the intended API
client networks. The inspected reference leaves API authentication disabled;
do not expose it as a production service without the NKE-owned security setup.

`--dry-run` is a rehearsal, not complete offline validation: registry verification
and input resolution may still occur. `nico CONFIGURED` checks objects and manager
availability; it does not establish that NICo credentials can provision hardware.
Tenant creation must use that matched CLI's actual interface and an approved
test site's available capacity. No verified tenant-create command or site inputs
are available for this NKD E2E trial yet, so none are invented here.

## E2E completion criteria

A full result requires all of the following recorded against one explicit input set:

1. Complete required artifact Run, including the disk if testing OS provisioning.
2. Registry read-back agrees with the produced image/chart subjects; every deployed
   repository/digest is traceable to that Run or an explicit external input.
3. NKE installer passes every real stage/check, including controller readiness,
   dependency health, provider readiness and API reachability.
4. The intended provider creates a tenant; its nodes and Kubernetes workload become
   ready. OS E2E additionally proves those nodes use the newly built disk/profile.
5. A committed source change is repinned, rebuilt, deployed and observed with the
   new identity. Test resources are cleaned up through the authorized NKE workflow.

Remaining implementation: combine the completed selections with the required
membership, including PR #113's direct-source OS graph where applicable. The
tested site adapter already joins six chart inputs with source-owned stage
selection and reachable controller/profiles/provider registry transport. The requested
scenario is already selected: local Colima plus
CAPNICo/NICoSim with QCOW2-backed tenant VMs, not a production NICo site or CAPD
substitute. The full wrapper still needs owned NICoSim routing/DHCP/BGP and
exact disk serving, guest/bootstrap trust compatibility, tenant creation and
workload checks, plus verify source access and OS capacity. The current site
CA and successful standalone guest do not prove that joint trust/bootstrap path.
Until that implementation and real acceptance run
are complete, this is not a working copy-paste full-E2E tutorial.
