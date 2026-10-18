# Legacy NVKR-to-NVKE artifact handoff and recovery

For the current Mac/Linux workflow, use the [native demo guide](local-nvke-demo.md).
This page describes the older staged-installer path with a Kind-hosted site and
its retained-session recovery controls. It is a reference for those existing
sessions, not the architecture or setup procedure for current `run` and `e2e`.

NVKR builds/acquires and observes artifacts. NVKE owns installation, site
configuration, provisioning and tenant lifecycle. The demo calls `nvkr`
subprocesses, not an execution SDK, and delegates installation to the exact
pinned NVKE staged installer at
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345`.
See its [deployment instructions](https://github.com/dsx-ai-factory/nvke/blob/8fb2d53368823bd386b4fbd55b4ab6d3c0931345/deploy/README.md).

That older implementation prepared this installer in a private attempt-local
directory and accepted a matching read-only `run.nvkeCheckout` override. Current
native commands consume source-built installer bundles from their selected Runs.
See [pin origins and upgrades](local-nvke-demo.md#where-the-nvke-versions-come-from)
before changing the NVKE source selection.

## Inputs consumed by the demo

| Output group in the completed Run | Selected handoff |
| --- | --- |
| OS | Exact plain ARM64 QCOW2 exported by NVKR; NICoSim serves these held bytes |
| Components | Dependency OCI images/charts and CAPNICo provider; provisioned provider is built but not used by this tenant path |
| Site | Source-aligned CLI, controller image, image-bound controller chart and NKTP chart |
| Tools | Verified native Helm, kubectl, clusterctl, ORAS and Kind bundle |
| Profiles | Native OCI content image; the chart's init container copies `/profiles` into its shared read-only controller volume |
| Simulator | NICoSim server/CLI and seed/UI archive |

The combined `all` Definition selects the complete demo artifact membership in
one Run, using the installer-aligned site and tool catalogs. The logical input
groups above select outputs from that same Run. Focused Definitions remain
available for individual developer loops. A local `lifecycle.json` is not a
signed Release.

Kind hosts management services. CAPNICo/NICoSim provisions **real libvirt
QCOW2 guests**, not CAPD containers. Node readiness, exact backing disk,
workload/DNS and cleanup are separate acceptance checks from artifact creation.
See [full-source gaps](full-nvke-source-build.md) for acquired/omitted inputs.

By default, both clusters select NVIDIA OVN. The site consumes the completed standalone
`ovn-chart-arm64` and `ovn-image-arm64` outputs through the owned registry;
the tenant uses its disk's separately produced OVN packages. Those paths do not
establish equal bytes. Kind's default CNI and kube-proxy are disabled. Its OVS
helper reuses the selected image's existing `ovs-server` entrypoint; site OVN
readiness precedes management-node readiness and Cluster API installation.
The source-owner startup fix remains required before this selection can pass
fresh E2E acceptance. Validate actual OVN-node startup and its iptables calls,
not merely the presence of `firewall-cmd`; the selected image's host wrappers
and chart mounts also need to agree. The explicit `upstream-ovn` selection uses
its distinct image, chart, disk and profiles outputs for both clusters. The
temporary `kind-calico` diagnostic keeps Kind networking and uses tenant Calico;
neither mode validates the NVIDIA fork, and neither is an automatic fallback.
The final PASS and retained tenant result name the selection. See the
[network alternatives](local-nvke-demo.md#explicit-upstream-ovn-alternative) and
[current validation status](adr/reconciliation.md).

## Prepare charts without rebuilding them

The adapter asks `nvkr publish chart` to verify each selected Helm layout,
manifest/config and bounded archive, then exports unchanged chart bytes. It
copies only tracked deploy files from the clean pinned installer into a private
directory and supplies the same archives to inventory rendering and execution
through `DEPLOY_PATH`.

It does not rebuild dependencies during deployment, edit the NVKE checkout or
mint a Release. NVKE's existing deploy-item reader remains authoritative;
that reader requires Python. Chart tests are adapter contracts, not proof of
deployment:

```sh
go test ./internal/nvkedemo -run 'TestChart' -count=1
```

## Registry transport

A Run's `packages/<name>/` directory is an OCI layout, not a kubelet URL.
The adapter copies selected layouts with ORAS to its private reachable
registry, reads back exact manifest digests and supplies explicit NVKE image
overrides. Source-produced archive IDs are not interchangeable with observed
manifest subjects.

Profiles are a native container image, not an executable bundle or a generic OCI
data artifact. The producer uses NVKE's existing pinned profiles Dockerfile with a
digest-pinned Alpine base that supplies `/bin/sh` and `cp`. ORAS copies the observed
image to the same local registry and verifies the destination manifest digest.
The adapter enables `profiles.image.*`; NVKE's standard `materialize-profiles`
init container copies `/profiles` into an `emptyDir` mounted read-only by the
controller. Deployment checks the exact init image and its successful exit.
No host-directory injection or additional profiles Dockerfile is needed.

Older Runs whose `controller-profiles` output has `kind: executable` cannot be
consumed as this native image. Do not relabel or rewrite a completed Run. Build
a fresh profiles selection and supply it with `--profiles-run`, or build a new
combined Run. A compatibility replay with the older demo binary retains its
original data-volume behavior; it is not validation of the new image handoff.

The controller's CAPNICo override is currently tag-shaped. The adapter checks
the exact destination subject; a tag alone is not an authenticated release
contract. Runtime image checks cover the selected dependency roles.
Kube-vip and the test workload are additional runtime acquisitions; this is
not a completely offline deployment.

Diagnostic registry copies do not equal NVKR publication/signing. Hosted
Release consumption additionally requires consumer-policy verification and
the downstream consumer's artifact selection and deployment mapping. A whole
disk-containing Release is not established by a local demo.

## Published release descriptions

The generic [release description](release-navigation.md#export-a-complete-release-description) exports all
published artifacts, platform metadata and exact source/evidence bindings from one immutable reference.
It contains no NVKE installer roles or release-wide source pin. NVKE owns conversion into any installer
format, source-checkout compatibility, chart values and the required artifact selection. That conversion
is not supplied by NVKR's exporter. The existing local demo above retains its separate pinned installer path.

The historical NKE consumer inspected at
[`b506565e698a3f0f1a64b2072476ba928c2e799e`](https://github.com/dsx-ai-factory/nke/tree/b506565e698a3f0f1a64b2072476ba928c2e799e)
compares a supplied source commit with the deployment checkout's HEAD and imports images named by
deployment-item metadata. Extra runtime image references alone do not cause their import or rewrite chart
values. Its [`FindLayout` implementation](https://github.com/dsx-ai-factory/nke/blob/b506565e698a3f0f1a64b2072476ba928c2e799e/cli/internal/bootstrap/nkerelease/chartlayer.go)
finds a native manifest listed in a layout's `index.json`, either directly or in a direct child directory.
An NVKR aggregate layout instead uses `index.json` as the aggregate and lists its native children;
finding the aggregate by that consumer's lookup requires consumer-owned staging that preserves its digest.
The generic published description retains the aggregate digest and exact children without staging packages.

A pre-cutover 2026-10-10 probe of that exact `FindLayout` implementation passed eleven discovery checks using
synthetic NKD native AMD64/ARM64 image layouts and a retained inspected AMD64 binary bundle. It checked direct and
parent-directory lookup plus missing/duplicate/scaffolding-digest rejection. This is discovery evidence
only, with no container import, executable runtime, Release authentication or offline installation claim.
The [full-source gap map](full-nvke-source-build.md#offline-deployment-inventory) retains the inspected
NKTP/clusterctl prerequisites and remaining offline work.

## Deploy retained completed Runs

To retest only deployment, use:

```sh
"$NVKE_DEMO" run --help
```

Supply the clean matching `--nvke-checkout`, native `--nvkr`, fresh
`--out`, and explicit completed `--os-run`, `--components-run`,
`--provider-run`, `--site-run`, `--tools-run`, `--profiles-run` and
`--simulator-run` paths. For a combined build, all seven selectors use the same
completed Run directory; each consumer verifies its own required outputs.

This is a retained-artifact replay, not a fresh build. For build-plus-deploy
use `e2e --config` from the quickstart; E2E derives its own fresh paths and
rejects retained Run references.

Keep the deployment terminal open for phase progress. In a second terminal,
set `DEMO_OUT` to the exact `--out` directory from that command. Once management
creation has produced `site/kubeconfig`, watch site installation and provisioning:

```sh
"$DEMO_OUT/site/tools/kubectl" --kubeconfig "$DEMO_OUT/site/kubeconfig" \
  get events --all-namespaces --watch
```

Inspect Cluster API objects with the same executable and kubeconfig using
`get clusters,machines --all-namespaces`. Once tenant provisioning has produced
`site/tenant-kubeconfig`, watch tenant nodes:

```sh
"$DEMO_OUT/site/tools/kubectl" --kubeconfig "$DEMO_OUT/site/tenant-kubeconfig" \
  get nodes --watch
```

`virsh -c qemu:///session list --all` shows the QCOW2 guests. Detailed private
stage logs remain under `"$DEMO_OUT/site"`; do not post kubeconfigs or logs without
checking for credentials. A failed network preflight has not created these
clusters. These watch commands are optional operator tools, not required
workarounds to discover success. At the default retained handoff, the CLI shows
actual read-only API/node checks and prints separate site/tenant `kubectl`
commands. `status --session` repeats live cluster checks. Default deployment
leaves the environment running; `--cleanup` or the separate `cleanup --session`
command performs owned teardown. See the
[retained access instructions](local-nvke-demo.md#retained-deployment-and-actual-cluster-access).

## Local safety boundary

Use a dedicated single-user Ubuntu VM. The adapter owns a private management
cluster, registry, libvirt guests, guest bridge, temporary routes/firewall rules
and service processes. It refuses occupied endpoints, nonempty libvirt sessions
and overlapping networks rather than deleting foreign resources.

The network guard temporarily grants narrowly identified private executables
and the QEMU bridge helper required privileges. It preserves process stop/join
ownership and withdraws controls only after dependent producers stop.
No global Docker daemon policy is changed. This is operator-trusted local
execution, not production isolation.

Site CNI/OVS containers are privileged within the owned Kind cluster and share
the execution host's kernel. The adapter requires the Open vSwitch/Geneve
modules to be loaded already; it does not claim this prerequisite independently
isolates privileged source code from the host. Node-local OVS processes and
paths are removed with the owned Kind node/volume; shared host kernel modules
are not unloaded.

First-boot APT uses the baked local repository and fails on missing packages.
The diagnostic profile preserves the physical uplink across worker reboot.
These local profile choices do not establish a general NVKE offline boot
interface or qualified image. Secrets, kubeconfigs, attempt credentials and
guest logs stay in private directories. NICoSim's diagnostic Bearer check is
not production authentication.

## Recover network controls after a crash

The private `network-state.json` journal records exact attempt ownership,
supported intents, original file identities/bytes and sysctl values before
granting controls. It is a recovery record, not an NVKR receipt.

After SIGKILL/OOM or VM restart:

1. Preserve the original output directory and accepted Runs. Confirm the same
   dedicated Linux host/account; do not reset Colima or prune Docker.
2. Inspect surviving simulator, disk server, DNS services and libvirt domains.
   Stop/join only independently verified owned producers. Never kill a PID
   merely because it appears in the journal. Tenant/Kind/registry recovery after
   a hard crash is a separate operator task.
3. Find the journal for the failed attempt, then select its containing directory.
   The example below matches the quickstart's `--out`; change `DEMO_OUT` if your
   attempt used another output path. Do not pass the journal file itself or
   delete its directory before recovery succeeds.

   ```sh
   find .nvkr -name network-state.json -print
   DEMO_OUT="$PWD/.nvkr/demo-e2e-01-demo"
   test -f "$DEMO_OUT/network-state.json" &&
   "$NVKE_DEMO" restore --work "$DEMO_OUT"
   ```

   In an interactive Linux terminal, recovery authenticates before opening its
   dashboard and maintains that session without mid-run password prompts.
   Redirected or unattended recovery instead requires approved
   non-interactive access; check `sudo -n -k timeout --signal=TERM --kill-after=5 25 true`.

4. This restores only recorded host-network controls; it does not delete tenants,
   VMs, Kind or registry containers. Require success before a new attempt.
   Active producers, foreign resources, changed file/bridge identities or
   unexpected routes cause refusal. Inspect the discrepancy rather than forcing
   removal.

Recovery accepts version-3 journals. Unsupported journal versions fail before host mutation.

Restoration takes the same kernel-released lock, never kills serialized PIDs
and, on the same boot, waits 40 seconds for in-flight bounded sudo work. This
safety wait may be quiet. A remaining private capability producer is reported
with its currently observed PID and matched executable path; inspect that
process before taking action. The diagnostic is not permission to kill other
services, and recovery never signals the reported process. It can replay an
interrupted rollback and reads back removals. An older attempt without the
journal requires manual inspection. Recovery does not create E2E evidence.

## Acceptance

Require one explicit input set, exact artifact read-back, completed NVKE stages,
two Ready QCOW2-backed tenant nodes, workload/DNS success and owned cleanup.
The explicit `--cleanup` route prints its final PASS only after those checks.
Default retained success is a usable deployment handoff, not completed owned
cleanup or full recorded E2E acceptance.

A separate successful deployment replay does not establish a fresh one-shot
rebuild, complete source-built distribution, full OS smoke or qualification.
Current implementation and runtime limits are recorded in
[reconciliation](adr/reconciliation.md).
