# Local NKE demo from NKD-built artifacts

The developer workflow has two separate commands: NKD builds the artifacts;
the deploy-only consumer installs those exact outputs and exercises NKE.
No application, chart or disk build runs during deployment. Kind is the
management cluster only. Tenant nodes are real ARM64 libvirt VMs backed by the
NKD-produced QCOW2, through CAPNICo and NICoSim, not CAPD/container nodes.

The new integrated command is under real-runtime validation. Contract tests and
separate site/guest probes do not establish a completed tenant/workload result.
This is a disposable trusted-user
diagnostic, not M2, a signed Release, qualified execution or a complete
source-built distribution. The older retained disk's failed full OS smoke and
missing DSX source material remain disclosed.
Private integrated attempts have not passed; no private runtime acceptance
outcome is recorded here. The candidate projects eight-GiB guests and supplies
an exact Netplan link naming-policy drop-in. Contracts cover the emitted
configuration, not udev/reboot behavior. A complete integrated reboot, worker
join, workload and final cleanup remain acceptance requirements.
This guide is not yet a validated full-demo result.

The selected controller hardcodes `ghcr.io/kube-vip/kube-vip` and renders a
digest-pinned static Pod with `IfNotPresent`. The retained OS materials do not
include that image. Preloading the exact runtime reference into containerd's
`k8s.io` namespace is still an OS-material integration task; rebuilding it from
source also requires the NKE runtime to consume the resulting image reference
and digest. Do not silently substitute an upstream digest for rebuilt bytes.

## Start on an Apple Silicon Mac

Follow the [clean-machine Docker/Colima setup](local-development.md#apple-silicon-mac-with-colima)
first. Docker Desktop alone is not the native VM execution host for this path.
The consumer disables clusterctl's optional remote update check through its
source-owned `CLUSTERCTL_DISABLE_VERSIONCHECK` switch; exact executable version
checks remain mandatory.
Use a dedicated ARM64 Colima Docker profile with eight CPUs and 24 GiB RAM;
do not reset or change your existing default Docker context. Inside it, native
Python 3.12+, Git, Bash, curl, OpenSSL, Docker and passwordless `sudo -n` are
required. The native VM tools are listed in the
[guest prerequisite guide](local-qcow2-guest-probe.md#prepare-the-dedicated-linux-vm-once).
The integrated network guard additionally needs `dnsmasq`, `iptables`,
`iproute2` and `libcap2-bin`; the source-owned boot patch needs `debugfs`
from `e2fsprogs` and `qemu-io` from `qemu-utils`. These are explicit host
package acquisitions, not NKD-built or organizationally qualified toolchains.

Builds should use a new committed-source export on the VM's native ext4 volume,
as described in that setup guide. A fresh OS bake needs about 90 GiB free on
both the physical Mac and selected build filesystem. Native VM free space
does not create additional physical Mac capacity. Build the other lanes first,
then the OS, not concurrently with it on this laptop.

## Build once, then deploy without rebuilding

Inside the native source export, use fresh names for these seven separate Runs:

```sh
set -euo pipefail
export CONTAINER_RUNTIME=docker
# Optional: exact pinned Git objects from a local clone visible inside Colima.
# export NKE_LOCAL_CLONE=/absolute/path/to/nke
for lane in components providers site tools profiles simulator os; do
  bash scripts/nke-full-arm64-local.sh --check "$lane"
  bash scripts/nke-full-arm64-local.sh "demo-$lane-01" 1 "$lane"
done
```

For remote sources, omit `NKE_LOCAL_CLONE`; Git credentials must work inside
the VM. A Mac `gh` login alone does not prove that access. Never embed tokens in
commands, catalogs or logs. Separate successful Runs are not a combined Run or
Release. The historical `all` selection does not include all seven lanes.
PR #113's experimental direct-source OS catalog is not yet integrated into this
maintained laptop selection; the OS still uses its declared NKE material graph.
Acquired third-party charts/runtime images and unavailable-source exceptions are
not relabeled as source builds. See the [full-source gap map](full-nke-source-build.md).

The site, tools and profiles inputs target the exact staged-installer candidate
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345`, not a floating NKE branch.
Prepare a separate clean NKE clone at that commit. Keep the source clone
unchanged; the consumer projects its tracked bytes privately. Source-owned NKE
scripts render/check chart values and the built `nkectl` validates the tenant
request. No approximate YAML or iPXE parser is added in NKD.
The local profiles image adds an idempotent guest boot command for
[`net.ifnames=1`](https://github.com/systemd/systemd/blob/v259/docs/PREDICTABLE_INTERFACE_NAMES.md),
after NICoSim's source-owned GRUB setup, to preserve the first boot's physical
uplink name used by OVN's generated network configuration.
This is laptop deployment configuration, not a change to the accepted QCOW2.
The producer admits exactly one physical NIC, named `eth0` or `enp1s0`, and
configures its generated Netplan link through
`/etc/systemd/network/10-netplan-<name>.link.d/10-nkd-local-name.conf`, with
empty `NamePolicy=` and explicit `Name=<name>`. First-boot seed renaming may
succeed or remain busy; either actual uplink must survive the OVS reboot.
This uses [systemd's supported explicit Name precedence](https://github.com/systemd/systemd/blob/v259/man/systemd.link.xml),
preserving the generated MAC match and MTU. Missing, multiple or unsupported
physical NICs fail before writes. This single-NIC simulator configuration is
not a general `eth0` policy for multi-interface hosts. Different existing
content or symlinks fail the boot command. Rebuild `profiles` with a fresh attempt
and pass that exact Run; an older completed Run does not acquire this correction.
It also adds [`network-config=disabled`](https://docs.cloud-init.io/en/latest/reference/network-config.html#kernel-command-line)
to the subsequent boot's GRUB command line. First boot still consumes NICoSim's
network seed; afterward, the persistent NKE/Netplan configuration owns networking.
This prevents the seed's `set-name: eth0` from being reapplied over the OVS uplink.
Cloud-init is not disabled, and its other modules continue to run.

The laptop profile also disables first-boot `package_update` and restricts APT
installs to `/etc/apt/sources.list.d/nke-local.list`, disabling other source
parts. The pinned image provisioner prepares that `file:/usr/lib/nke/debs`
repository and its APT lists during the bake. A missing package/dependency
terminates the installation commands before service restarts or the completion
marker; it is not permission to fetch Ubuntu packages or silently skip
installation. This profile change does not rebuild the disk or establish
complete offline deployment:
kube-vip and the workload still have separate runtime acquisition requirements.
Require actual guest installation and reboot checks; shell contracts are not E2E.

Cache the pinned service images using the existing build/management setup.
The integrated adapter uses the existing `kind` Docker bridge. On a clean
dedicated VM, inspect `docker network ls` and `ip route`, then create it once if
absent and the proposed range is unused. Do not recreate an existing network:

```sh
# Once only, inside the dedicated VM; skip if kind already exists.
docker network create --driver bridge --subnet 172.18.0.0/16 \
  --gateway 172.18.0.1 kind
```

The private registry service additionally requires:

```sh
docker pull docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8
```

Set absolute paths to the completed Runs. They may be in different exports;
there is no reason to rebuild or relocate accepted artifacts merely to put them
beside the consumer. Each Run must have complete, exact expected/observed output
membership and no failure. The consumer rejects changed layout/disk bytes.
For the fresh names used in the build block above, set these inside the same
native export:

```sh
export NKD_NATIVE_BOOTSTRAP="$(pwd)/.nkd/laptop-bootstrap/demo-tools-01/nkdctl"
export NKD_TOOLS_RUN="$(pwd)/.nkd/runs/demo-tools-01"
export NKD_SITE_RUN="$(pwd)/.nkd/runs/demo-site-01"
export NKD_COMPONENTS_RUN="$(pwd)/.nkd/runs/demo-components-01"
export NKD_PROVIDER_RUN="$(pwd)/.nkd/runs/demo-providers-01"
export NKD_PROFILES_RUN="$(pwd)/.nkd/runs/demo-profiles-01"
export NKD_SIMULATOR_RUN="$(pwd)/.nkd/runs/demo-simulator-01"
export NKD_OS_RUN="$(pwd)/.nkd/runs/demo-os-01"
export NKD_DEMO_OUT="$(pwd)/.nkd/deploy-demo-01"
# Set NKE_INSTALLER_CLONE to your separate clean clone at the pin above.
```

```sh
set -euo pipefail
: "${NKD_NATIVE_BOOTSTRAP:?Existing native Linux nkdctl from a successful launcher}"
: "${NKE_INSTALLER_CLONE:?Clean exact pinned NKE installer clone}"
: "${NKD_TOOLS_RUN:?Completed tools Run directory}"
: "${NKD_SITE_RUN:?Completed site Run directory}"
: "${NKD_COMPONENTS_RUN:?Completed components Run directory}"
: "${NKD_PROVIDER_RUN:?Completed providers Run directory}"
: "${NKD_PROFILES_RUN:?Completed profiles Run directory}"
: "${NKD_SIMULATOR_RUN:?Completed simulator Run directory}"
: "${NKD_OS_RUN:?Completed OS Run directory}"
: "${NKD_DEMO_OUT:?NEW output directory outside source and all Runs}"

python3 -B scripts/nke-local-demo.py \
  --nke-checkout "$NKE_INSTALLER_CLONE" --nkdctl "$NKD_NATIVE_BOOTSTRAP" \
  --tools-run "$NKD_TOOLS_RUN" --site-run "$NKD_SITE_RUN" \
  --components-run "$NKD_COMPONENTS_RUN" --provider-run "$NKD_PROVIDER_RUN" \
  --profiles-run "$NKD_PROFILES_RUN" --simulator-run "$NKD_SIMULATOR_RUN" \
  --os-run "$NKD_OS_RUN" --out "$NKD_DEMO_OUT" --timeout 3600
```

Run that block inside `colima ssh --profile nkd-demo`, or on an equivalent
dedicated native ARM64 Linux host. Linux executables cannot run directly on macOS.
The tenant budget defaults to 60 minutes. This is a timeout bound, not a
completion estimate.
The consumer requires the disk-copy bytes plus 30 GiB free on its native output
volume and the management admission's eight CPUs/20 GiB RAM. Its output must
not already exist. This is two-VM diagnostic capacity, not fresh-bake admission.
Check physical Mac capacity separately before launching it; a sparse Colima
disk's apparent free space is not additional Mac capacity.

The trusted diagnostic guard temporarily admits guest TCP to the exact owned
management-node address on SPIRE ports 30440/30443 before Docker's raw-table
direct-routing drop. It withdraws and reads back those exact rules before
management-node deletion. No Docker daemon setting, existing Docker rule or
network is replaced. An unjoined child or failed rule withdrawal retains the
dependent controls instead of recycling the management address. This scoped
exception is specific to the disposable native demo, not production policy.
NICoSim's source-owned guest diagnostics use a random per-attempt SSH credential,
not its default password. That disposable account has passwordless sudo only in
these owned guests. Diagnostic API requests send the existing per-attempt Bearer
header through a private, owner-checked `nico-api-header` file, not process
arguments or phase logs. The pinned simulator only checks that a nonempty Bearer
header exists; this is not production authentication or token verification.
Before deletion, the adapter attempts to retain agent, kubelet and
containerd logs privately, with a 30-second/1-MiB bound per request. Credentials
and configuration files are not collected as diagnostic items. Diagnostic
failures do not prevent cleanup; an unjoined child retains dependent controls.

## What gets deployed

| Built output | Downstream use and identity check |
| --- | --- |
| Five-tool bundle | NKD verifies/extracts it; each tool's native version is checked |
| Controller, profiles, CAPNICo images | Unchanged ORAS copy into a private registry; descriptor readback and actual Pod image IDs match the accepted manifests |
| Six chart archives | NKD delivery owner validates/exports unchanged bytes; NKE installs/checks them |
| NICoSim server/CLI/assets | Verified extraction; private server copy, seed and UI; libvirt backend, containers disabled |
| Plain ARM64 QCOW2 | Independent digest/size check, read-only private copy, full HTTP byte readback; successful tenant evidence requires both VM overlays to reference byte-matching downloaded backing disks |

The authored profiles select Ubuntu 26.04, Kubernetes 1.34.2 and OVN-Kubernetes
1.4.0. This command creates one control-plane VM and one worker with the selected
source seed's four-CPU instance type, projected privately to eight GiB RAM and
a 60-GiB disk. The original seed is admitted by its exact source digest and kept
unchanged. Only the selected instance's name/memory block is replaced in a
separate runtime seed; NICoSim's source-owned decoder validates it through
`NICO_SIM_SEED_PATH`. `demo-inputs.json` records both seed digests and the sizing.
The projection selects eight GiB instead of the source's six-GiB default.
This is deployment configuration, not a rebuild
or change to the accepted simulator/disk artifact bytes. The QCOW2 bytes are the
delivery subject; mutable guest overlays naturally diverge during provisioning.
The consumer does not claim exact guest reproduction or inspect a final disk SBOM.

Success requires two ready ARM64 Kubernetes nodes, the expected VM VIP,
real owned libvirt domains/TPMs and exact backing disks, then a pinned acquired
Alpine workload that executes on ARM64 and resolves Kubernetes service DNS.
This acquisition is a workload test, not an additional source-built NKE artifact.
No success is emitted if tenant, workload, identity or cleanup checks fail.

## Ownership, diagnostics and cleanup

The command refuses existing demo bridges/QEMU policies, session VMs,
simulators, overlapping routes and occupied ports. It owns the temporary
`nkdvm0` bridge, exact commented firewall rules, DHCP/DNS, private disk HTTP
server, simulator, management node/volume and registry. It temporarily grants
only private executable copies and the byte/identity-bound QEMU bridge helper
the permissions required by this trusted native diagnostic. It does not make
the accepted Run artifacts privileged or change Docker's global daemon config.
The simulator and libvirt observations use explicit native-account HOME/XDG
paths, not ambient client settings. Admission compares a running session
daemon's selected path bindings before privileged setup; the source's TPM
seeding must target that same daemon namespace. Files/databases/cache belonging
to this demo remain in its private directory; the session daemon's TPM state
is removed through exact-domain `virsh undefine --tpm`. The source also creates
or reuses native-account `~/.config/swtpm-localca` signing-key, certificate and
serial state. That shared CA is outside the attempt directory and retained;
domain cleanup does not remove it.

The finalizer deletes only the exact NKE tenant, stops/joins producers, sweeps
only libvirt domains bound to this private simulator directory, restores exact
known BGP routes and the network guard's controls. Changed/foreign ownership or
an unjoined process fails closed and retains setup for investigation. Do not
interpret retained host controls as permission to leave independent daemons
running: the consumer attempts stop/join of simulator, HTTP and DNS even when
resource restoration is refused. A real join failure still retains dependent
host state and fails the command. Cleanup diagnostics cannot replace its error
identity, and a cleanup failure reports the original phase failure too.
Do not prune Docker or delete unknown domains to force another attempt.

### Recover network controls after a crash

New attempts persist `network-state.json` privately before granting host
privileges. It records the owner, exact permitted intents, original file
identities and bytes, original sysctl values and attempt-bound root staging
path. File and directory sync precede new privileged work. This is a
trusted-user recovery journal, not an authenticated NKD receipt or a Release.
Keep the original output directory; older attempts without this journal need
manual operator inspection and cannot use this recovery command.

After SIGKILL, OOM or a Colima restart:

1. Confirm you are in the same dedicated native VM and account. Preserve the
   output directory and accepted Runs; do not reset Colima or prune Docker.
2. Inspect the exact attempt's simulator, disk-HTTP and DNS producers. Stop and
   join any surviving owned producers using fresh runtime ownership checks,
   not PIDs copied from the journal. Inspect the explicit native libvirt session
   and remove only independently verified attempt-owned domains. Do not remove
   an unknown guest to make recovery pass. Tenant/Kind/registry cleanup remains
   a separate operator task after a hard crash.
3. Run the network-only restore command inside the native VM:

   ```sh
   python3 -B scripts/nke-local-demo.py restore --work "$NKD_DEMO_OUT"
   ```

4. Confirm it exits successfully before starting a new attempt. An active
   guard, present original PID, live private capability-bearing executable,
   disk-HTTP listener, simulator or libvirt domain prevents restoration.
   Changed bridge/file identities, capabilities or routes retain controls and
   return an error; inspect the exact discrepancy rather than forcing removal.

Recovery takes the same kernel-released lock, never kills serialized PIDs and,
on the same boot, waits 40 seconds for any in-flight bounded sudo command to
terminate. It restores only the recorded supported controls, reads back their
removal and can replay an interrupted rollback. VIP route restoration requires
recorded simulator/BGP intent. A full diagnostic volume does not prevent
withdrawing known privileges after producers have joined, but failed journal
persistence still makes the command fail. Recovery does not create tenant,
workload, signed-release or end-to-end acceptance evidence.

Private phase logs, kubeconfigs, credentials, simulator database and VM consoles
stay under the new output directory; keep it out of Git and shared logs.
`demo-inputs.json` identifies consumed outputs; `site/site-inputs.json` records
deployed site images/charts. `site/tenant-result.json` is written only after the
tenant and workload assertions. These are local diagnostics, not signed receipts.
The final PASS additionally requires owned cleanup and unchanged accepted disks.
