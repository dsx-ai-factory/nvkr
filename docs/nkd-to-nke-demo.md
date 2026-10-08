# NKD artifacts for NKE's staged site installer

This document covers the staged-installer boundary only. It is not an analysis
of Lia's complete local Colima/NICoSim environment. See the
[whole-demo analysis](lia-local-demo-analysis.md) for the transcript, local
provider/runtime distinction, missing artifacts and exact-byte deployment tasks.

The intended developer journey is: build the ARM64 artifacts with NKD, make the
exact produced images and charts reachable by NKE, run the NKE site installer,
create a tenant and verify it, then rebuild and retest a source change.

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
| Controller chart/templates | `controller-chart` is image-bound; Lia's deploy path currently installs a chart from its NKE checkout | Decide/test use of the packaged chart through NKE's chart override, or align the installer checkout and exact built controller source. Do not claim the installer automatically consumes this packaged chart |
| CAPNICo provider | `capnico-controller-arm64`, built from pinned public Git | Reachable development image plus NKE's `CAPNICO_IMAGE=repository:tag` override. The current override is tag-shaped; do not silently turn it into a digest contract |
| Provisioned provider | `cluster-api-provider-provisioned-arm64` | Available output, but not selected by the NICo deploy path. Building it is not proof that this demo uses it |
| Operator CLI/tools | `nkectl`, acquired `operator-tools`; separate source-built `installer-tools-arm64` candidate | Five-tool Plan covers matching Helm/kubectl/clusterctl/ORAS/Kind, but its full Run is unexercised and it is not part of `all`. Python and curl remain host prerequisites; all produced tools are Linux ARM64 |
| Dependency images | Twelve acquired ARM64 image outputs | Align chart values or node mirrors with the reachable image locations; stage 4 and NKTP also need inputs outside this set |
| NKTP | Separate `definition-site.json` declares the source chart and locked dependency preparation | Four-output site Run still unexercised; runtime images are not a complete mirrored NKD set. Installer inventory rendering still prepares source-tree chart dependencies |
| Profiles content image | Operator-supplied `images.profiles` | Accessible repository/revision, producer/output or an explicit existing image. The current laptop Definition does not build it |
| Node QCOW2 | `nke-node-image-arm64` in the `all`/`os` selection | Complete the bake/boot and connect the exact disk to the infrastructure's OS profile. No automatic QCOW2-to-NICo-profile handoff is implemented here |
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
an isolated NKTP dependency/package probe pass; no complete four-output Run or
installation is recorded. This separate selection does not replace the retained
22-output Run or become part of `all` automatically.

At the same installer source, `deploy/nke-deploy.sh` already supports packaged
chart items/overrides. The separate wrapper
`deploy/stages/05-install-site-controller.sh` consumes
`CAPNICO_IMAGE=repository:tag` and turns it into a CAPNICo manifest override
without building the image. Setting that variable on the executor directly
does not select a different CAPNICo image.
These installation hooks must not be confused with the older Tilt/source helper.
However, `deploy/lib/describe_site.py` still renders source-tree charts and builds
their dependencies during inventory preparation. Passing a packaged chart to
installation alone does not establish a rebuild-free complete journey.

Tool versions need alignment too: the acquired operator bundle contains Helm
`v4.1.4`, while the installer pins `v4.2.4`, ORAS `1.3.3` and
`clusterctl v1.12.3`. Its readiness script warns on a Helm version mismatch;
that is not a hard version gate, but neither is it a matched tool pin.
The site build wrapper uses pinned Helm 4.3.0. The separate
[source-tool selection](local-development.md#build-the-installer-tools-from-source)
declares all five matching tools, with compiler/preflight evidence only. It still
needs a complete Run, verified extraction and integration into the deploy-only
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
Run was retained. A fresh non-overlapping attempt on the
preserved 24 GiB profile is preparing materials; see the
[execution evidence](evidence/2026-10-08-colima-local-os-build.md).
Passing preflight or building materials is not bake/boot evidence.

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
installer input. Repeat the same transport for each required image/chart,
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

Remaining implementation: align the demo's NKE producers/pins; add the missing
NKTP/profiles membership; implement/test the artifact-to-installer handoff and
registry transport. The requested scenario is already selected: local Colima plus
CAPNICo/NICoSim with QCOW2-backed tenant VMs, not a production NICo site or CAPD
substitute. The wrapper still needs to create/admit its owned local management
cluster, NICo endpoint, registry and bootstrap trust configuration, plus verify
source access and OS capacity. Until that implementation and real acceptance run
are complete, this is not a working copy-paste full-E2E tutorial.
