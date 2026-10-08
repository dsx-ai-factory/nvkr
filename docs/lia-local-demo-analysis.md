# Lia's local NKE demo and the NKD handoff

The requested journey is **NKD builds the selected artifacts, a separate script
deploys those exact outputs into the local Colima/NICoSim environment, then the
developer creates and tests a tenant through NKE**. CAPD is not a replacement for
CAPNICo/NICoSim. The local demo does not require access to a production NICo site.

The user clarified the acceptance target: **tenant nodes boot the QCOW2 OS disk
already selected by NKD**. A custom tenant-node OCI image is not an additional
required deliverable or a substitute for that disk. The remaining OS work is
laptop execution, exact-disk handoff and real guest/tenant validation, not a new
OS producer.

This is an analysis, not a completed executable tutorial. No local site or tenant
was deployed during this analysis.

Subsequent implementation added a separate source-built Linux ARM64 NICoSim
server/CLI and pinned seed/UI archive bundle. The [three-component Colima Docker
Run](evidence/2026-10-08-colima-nicosim-assets.md) completed 1/1, and NKD delivery
verified/extracted its final members. An offline mock-runtime probe served exact
packaged UI bytes without NKE source mounted. This closes the simulator's asset
delivery/startup gap, not real VM provisioning, the missing combined artifact set
or the Colima-to-tenant demo.

## Sources and evidence limits

Checked on October 7, 2026:

- The entire 46-minute **NKE Journey**, October 5, meeting transcript from the
  [supplied Teams recap](https://teams.microsoft.com/l/meetingrecap?threadId=19%3Ameeting_ODExZTIzYTAtNGJjNS00MzRmLThmNmUtODUxNmQxYjVjYTAw%40thread.v2&organizerId=6a696423-f301-4849-ad19-0ed45ddd00be&tenantId=43083d15-7273-40c1-b7db-39efd9ccc17a&callId=9272922a-a8b9-4a4b-9e3f-f5925bd93703).
  Its calendar iCalUId matches the supplied recap. This is the planning/install
  discussion, not a terminal-by-terminal demonstration of the Colima setup.
  The video itself was not inspected.
- [NKE PR #1160](https://github.com/dsx-ai-factory/nke/pull/1160), open at
  `8fb2d53368823bd386b4fbd55b4ab6d3c0931345`, and its actual installer sources.
  It is stacked on `deploy/nico-layout`, not merged into `main`.
- NKE `main` at `6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4`: Colima setup,
  NICo provider, simulator, custom node builder, Tilt and backend E2E scripts.
- NKD local-build branch at `91a8f7e348d82bc3ac6e1bdec61747d342d9328e`:
  [laptop Definition](../definitions/nke-laptop-arm64/definition.json), catalog
  pins and the retained complete 22-output component Run.

The user identifies Lia's environment as local Colima plus NICoSim. Neither the
meeting transcript nor PR #1160's test summary specifies her exact simulator
backend, resource settings or runtime commands. Do not invent those details.
The simulator's default Tilt selection is container-backed; its VM backend is a
separate explicit selection. The requested demo selects real QCOW2-backed VMs,
not that default. This user-selected target does not establish Lia's exact
recorded runtime settings.

## What Lia explained

| Transcript time | What it establishes |
| --- | --- |
| 01:06–02:26 | The problem is turning an initial vanilla Kubernetes cluster into a complete NKE site controller, including building, locating and installing artifacts |
| 09:18–17:26 | Charts and images are insufficient: install templates, values and a supported configuration must also be supplied |
| 25:13–27:45 | The initial OSS install story includes NICo; the build/install starting point and NKD interaction still need clarification |
| 31:06–33:38 | NKE owns how its outputs are built and installed; NKD wraps source-owned producers and handles graph, packaging and evidence |
| 33:41–40:00 | Lia deliberately leaves NKD out of the planned demo, hand-constructs a release manifest and splits the existing install script into stages |
| 40:20–43:23 | NKD should supply an aggregate artifact conforming to NKE's installer format, rather than making the installer depend on NKD internals |
| 43:29–46:15 | The demo combines deploy templates/order and the handwritten release manifest. Lia reports that her PR works; the discussion does not establish a final customer-facing install contract |

Meeting proposals are not ratified engineering decisions. Lia's report and the
PR author's runtime summary are not an independently reproduced NKD E2E result.

## The complete local system

```text
NKD Definition and pinned catalogs
  -> source builds / explicitly declared acquisitions
  -> exact image, chart, executable and configuration outputs
  -> local transport and NKE installer input
  -> Colima Docker + Kind management cluster + NICoSim
  -> NKE stack + CAPNICo
  -> NKE tenant request -> CAPNICo -> NICoSim -> tenant nodes
  -> agent/SPIRE/Kubernetes/CNI readiness -> workload test -> tenant deletion
```

Colima's Linux VM supplies Docker and the kernel used by its containers.
It is not itself proof that NICoSim is provisioning separate tenant VMs.
The source-owned [Colima setup](https://github.com/dsx-ai-factory/nke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/hack/colima-start.sh)
starts/configures Docker, checks capacity, tunes limits and enables the vTPM proxy.
These host changes need explicit preflight and ownership in a developer wrapper.

[NICoSim](https://github.com/dsx-ai-factory/nke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/nico-sim/README.md)
exposes a NICo-compatible API with two distinct backends:

- **Container backend:** NICoSim invokes Docker to create systemd node containers.
  It uses the same custom Kind node image builder as CAPD, but the infrastructure
  provider and API remain CAPNICo/NICoSim. An OCI node container is not a QCOW2.
- **VM backend:** NICoSim can use real QEMU guests on macOS or KVM/libvirt on
  Linux, but it also supports an in-memory mock runtime. The
  [runtime configuration](https://github.com/dsx-ai-factory/nke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/nico-sim/internal/config/config.go)
  admits `NICO_VMS_VM_BACKEND=mock` and `mock://` libvirt URIs. A new QCOW2 needs
  matching boot/profile, networking, trust and architecture inputs, plus observed
  execution by a real VM runtime. Neither a container-backed demo nor selection
  of a mock VM runtime proves that the newly built OS disk boots.

The [NICo Tilt provider](https://github.com/dsx-ai-factory/nke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/hack/nico-provider/Tiltfile)
starts NICoSim when no external NICo endpoint is configured. Its default simulator
backend selection is `container`. `NicoCluster.spec.siteID` selects a simulator
site; CAPD uses a different infrastructure contract.

The existing source-built **Linux ARM64** simulator can also be used for a
candidate VM-backed route inside Colima. NKE's
[libvirt adapter](https://github.com/dsx-ai-factory/nke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/nico-sim/internal/vm/libvirt/virsh.go)
selects a `qemu` domain when `/dev/kvm` is absent; its
[domain template](https://github.com/dsx-ai-factory/nke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/nico-sim/internal/vm/libvirt/domain_xml.go)
selects the ARM64 `virt` machine with `cortex-a57` for that software route.
Therefore a native macOS simulator producer or nested KVM is not inherently
required to try this route. The real candidate must use
`NICO_VMS_VM_BACKEND=libvirt`, `NICO_CONTAINERS_ENABLED=false` and
`NICO_VMS_BASE_IMAGE` pointing to the exact completed NKD disk export. Colima
still needs libvirt/QEMU, firmware, cloud-init seed tooling, bridge/DHCP, metadata
reachability and the chosen trust/attestation configuration. The source code
establishes the route, not a completed Colima guest or tenant test; software
execution performance and readiness budgets remain unmeasured.

## The installer Lia adds

[PR #1160's installer](https://github.com/dsx-ai-factory/nke/blob/8fb2d53368823bd386b4fbd55b4ab6d3c0931345/deploy/README.md)
consumes three separate inputs:

| Input | Owner and content |
| --- | --- |
| Deploy path | NKE's `deploy/nico`: order, manifests, templates, chart locations/pins and base values |
| Release manifest | Exact reachable artifact references/digests and matching source/chart metadata. Lia assembles it manually for the demo |
| Site configuration | Operator-specific endpoint, architecture, trust/CA, profiles reference and private credentials. Keep outside Git |

It does not create the Colima VM, run NICoSim or build a tenant node image.
It takes a usable site kubeconfig and runs seven stages:

1. Validate manifest, source checkout, chart versions, tooling and cluster readiness.
2. Prepare scheduling and a default StorageClass.
3. Stage the controller image into each node's containerd.
4. Render site values, bootstrap configuration and inventories.
5. Install cert-manager, Cluster API core and kubeadm providers.
6. Install CAPNICo and the six Helm releases: NKTP, PostgreSQL, SPIRE CRDs,
   Temporal, SPIRE and controller.
7. Patch CAPI SPIFFE exec-auth, register the site in `nkectl` and check API access.

The installer repeats image staging after rendering and before chart installation.
Its default controller and NKTP charts come from the source checkout; the four
external charts come from the release manifest. The executor supports chart
overrides, but the staged wrapper does not automatically select NKD's packaged
`controller-chart`. The source readiness gate accepts a matching supplied commit;
without a commit it requires the matching exact tag.

The generated `version-bom.cdx.json` is a manifest-item inventory, not a full
inspection-derived disk SBOM or proof of all installed runtime dependencies.

## Required artifact handoff

| Required piece | Current NKD laptop support | Concrete gap |
| --- | --- | --- |
| Controller image | Source-built `cwe` | Align source/build contract with the selected demo revision; import/copy without rebuilding and verify deployed identity |
| Controller chart | Image-bound `controller-chart` | Feed the packaged chart to the source-owned installer rather than silently using a different source-tree chart |
| CAPNICo | Source-built ARM64 provider image | Chart/manifests and install override must consume that image; the current source install helper builds it again |
| NICoSim and simulator CLI | Separate `definition-simulator.json` selects source-built Linux ARM64 server/CLI and pinned seed/UI archive; a three-component Docker Run and verified artifact-only mock startup passed. The launcher supports fresh pinned-toolchain bootstrap | Integrate into the demo selection and real VM process/runtime handoff. The separate bundle is not part of the 23-output `all` set; real VM/site/tenant deployment remains unvalidated |
| Tenant node disk | Existing `nke-node-image-arm64` output with its OS material graph | Make the existing ARM64 TCG build complete on the Mac, then bind the exact QCOW2 to the NICoSim VM profile; do not add a substitute tenant-node container build |
| Profiles/runbooks image | No selected output | Build the explicitly selected local demo fixture, label it diagnostic, bind the exact tenant QCOW2/OS choices; do not call it production fleet profiles |
| NKTP chart and runtime closure | No NKTP chart output | Package its locked chart dependencies and enumerate its selected runtime images |
| External charts and runtime images | Four acquired charts and twelve acquired runtime images | Demonstrate that every deployed reference resolves to the selected bytes. This is not all-public-components-from-Git support |
| CLI and operator tools | Linux ARM64 `nkectl`, Helm/kubectl bundle | Linux binaries cannot run directly on macOS. Run them in the chosen Linux environment or add native-host producers; add matching installer tools |
| BGP and any selected identity-exchange helpers | Not a complete selected set | Model required helper images/configuration from the chosen scenario. Do not silently enable or omit them |
| Installer/templates/configuration contract | No complete delivered deploy bundle | Preserve NKE's exact source revision and installation format; map Run outputs to it, not arbitrary hand-entered refs |
| QCOW2 boot evidence | Disk output declared; the 16 GiB native trial reached QEMU kernel installation but failed after global host OOM killed QEMU. A fresh 24 GiB, non-overlapping attempt began | Complete the bake, boot the newly produced disk through the real VM backend and observe the tenant. The failed trial, component Run and a node container do not validate this disk |
| E2E suite | No selected test executable | Produce the matching test binary before deployment, or explicitly separate an independently built test harness from the artifact Run |

The current retained component Run is complete **22/22** in its declared
`unqualified-local-execution` lane. It is not the complete membership above and
does not establish any deployed site or tenant. See the
[recorded evidence](evidence/2026-10-07-local-container-bootstrap.md).

## Existing reuse hooks and their limits

Current NKE sources already offer useful entry points:

- Tilt's `cwe_image` input bypasses controller compilation/image building.
- `CAPD_KINDEST_NODE_IMAGE_PREBUILT=true` bypasses the NICo provider's custom
  node image build; `CAPD_KINDEST_NODE_IMAGE` names it.
- `NICO_SIM_BINARY_PREBUILT=true` bypasses Tilt's simulator build. Tilt currently
  expects a specific host path; the binary still needs the proper platform/assets.
- `CWE_PROFILES_PRELOADED=true` skips the fixture image build, but currently
  uses a fixed image name.
- The [node-image builder](https://github.com/dsx-ai-factory/nke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/hack/capd-kindest-node/build-image.sh)
  accepts prepared agent/SPIRE bundles and CNI/Helm packages. The agent bundle
  branch requires disabling local agent rebuilding.
- The [simulator launcher](https://github.com/dsx-ai-factory/nke/blob/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4/controller/hack/nico-provider/run-nico-sim.sh)
  consumes a prebuilt Linux binary on the container-backed deploy route.

These are not a complete deploy-only mode. The CAPNICo source installer still
does `docker build`; Tilt independently fetches/builds charts; the backend E2E
entrypoint builds controller/profiles/node images and compiles the simulator and
test suite. Calling those scripts as-is cannot prove that NKD built everything.

## Concrete implementation order

1. **Make the existing QCOW2 build run on the Mac.** Keep the authored ARM64 TCG
   producer and admission requirements. October 8 failed-attempt cleanup recovered
   sufficient workspace and the real Colima preflight passed. The shared-mount OS
   build failed during SPIRE preparation; the native-ext4 retry reached autoinstall
   but then lost QEMU to global OOM in the 16 GiB VM. A fresh attempt on the
   preserved 24 GiB profile is running without concurrent builds; see the
   [execution evidence](evidence/2026-10-08-colima-local-os-build.md)
   and [storage evidence](evidence/2026-10-08-local-storage-cleanup.md).
   Complete the bake rather than treating preflight or materials as acceptance.
   Use the real NICoSim VM backend for tenant nodes,
   not CAPD or the default container backend. Record matching architecture,
   attestation/CNI choices and exact install source.
2. **Complete the NKD Definition/catalog membership.** Integrate the separate simulator bundle; add
   local profiles, NKTP, deploy assets and selected helpers. Reuse the existing
   OS output, NKE-owned producers and the existing graph/executors. Public application
   components requested from source remain source builds; retained binary
   exceptions must be explicit.
3. **Implement the exact-byte deployment adapter.** Read a completed Run, admit
   the whole required set, verify/copy images and charts into a local reachable
   registry/daemon, and create NKE's installer input. Never turn a local Run into
   a claimed signed Release. Use `local-assembly`/`none` for a handwritten local
   NKE release manifest, as NKE's guide specifies.
4. **Add missing NKE-owned prebuilt handoffs.** CAPNICo installation must accept
   the produced image/chart without another build. The staged installer must
   consume the produced controller/NKTP charts and matching templates. Own
   NICoSim process start/readiness/stop, attempt paths and local kubeconfig;
   never mutate an unrelated cluster or silently reset a VM.
5. **Run the actual local E2E and document it.** Start/check Colima, create the
   management cluster, start NICoSim, install/check the stack, create a tenant,
   prove the control-plane/worker boot the produced QCOW2, wait for CNI, run a workload, delete it, then
   rebuild a source change and show the newly deployed identity. Retain sanitized
   diagnostics and a produced-to-deployed artifact map.

The result must be a tested developer command sequence, not another build script
that calls a rebuilding installer. Building the artifacts, successfully running
the installer and completing the tenant lifecycle are three separate proof gates.

## Security and completion boundaries

PR #1160 documents a demo posture: API authentication off by default, plain HTTP
NodePort, root controller, local-path storage and operator credentials outside
Git. Its stage named secure-and-register does not itself make that production
safe. A laptop wrapper must limit exposure and keep all trust/secret material local.

The PR author reports a successful seven-stage installation and tenant
create/delete; no independently reproduced NKD-to-NICoSim run was obtained here.
The transcript proves what Lia planned and reported, not her terminal settings.
Exact reproduction of the video remains unverified until those settings are
provided or visible recording evidence can be inspected.
