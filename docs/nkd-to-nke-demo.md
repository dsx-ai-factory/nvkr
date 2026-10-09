# NKD-to-NKE artifact handoff reference

Start with the [Ubuntu ARM64 quickstart](local-nke-demo.md). This reference
explains what the downstream adapter consumes and its safety/recovery limits;
it is not another setup guide.

NKD builds/acquires and observes artifacts. NKE owns installation, site
configuration, provisioning and tenant lifecycle. The demo calls `nkdctl`
subprocesses, not an execution SDK, and delegates installation to the exact
pinned NKE staged installer at
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345`.
See its [deployment instructions](https://github.com/dsx-ai-factory/nke/blob/8fb2d53368823bd386b4fbd55b4ab6d3c0931345/deploy/README.md).

## Inputs consumed by the demo

| Output group in the completed Run | Selected handoff |
| --- | --- |
| OS | Exact plain ARM64 QCOW2 exported by NKD; NICoSim serves these held bytes |
| Components | Dependency OCI images/charts and CAPNICo provider; provisioned provider is built but not used by this tenant path |
| Site | Source-aligned CLI, controller image, image-bound controller chart and NKTP chart |
| Tools | Verified native Helm, kubectl, clusterctl, ORAS and Kind bundle |
| Profiles | Profile files packaged as data, pulled/verified and mounted read-only into the controller |
| Simulator | NICoSim server/CLI and seed/UI archive |

The combined `all` Definition selects the complete demo artifact membership in
one Run, using the installer-aligned site and tool catalogs. The logical input
groups above select outputs from that same Run. Focused Definitions remain
available for individual developer loops. A local `lifecycle.json` is not a
signed Release.

Kind hosts management services. CAPNICo/NICoSim provisions **real libvirt
QCOW2 guests**, not CAPD containers. Node readiness, exact backing disk,
workload/DNS and cleanup are separate acceptance checks from artifact creation.
See [full-source gaps](full-nke-source-build.md) for acquired/omitted inputs.

## Prepare charts without rebuilding them

The adapter asks `nkdctl publish chart` to verify each selected Helm layout,
manifest/config and bounded archive, then exports unchanged chart bytes. It
copies only tracked deploy files from the clean pinned installer into a private
directory and supplies the same archives to inventory rendering and execution
through `DEPLOY_PATH`.

It does not rebuild dependencies during deployment, edit the NKE checkout or
mint a Release. NKE's existing deploy-item reader remains authoritative;
that reader requires Python. Chart tests are adapter contracts, not proof of
deployment:

```sh
go test ./internal/nkedemo -run 'TestChart' -count=1
```

## Registry transport

A Run's `packages/<name>/` directory is an OCI layout, not a kubelet URL.
The adapter copies selected layouts with ORAS to its private reachable
registry, reads back exact manifest digests and supplies explicit NKE image
overrides. Source-produced archive IDs are not interchangeable with observed
manifest subjects.

Profiles are data: ORAS transfers their bundle, NKD verifies/extracts it, and
the adapter supplies an owned read-only profiles volume. No runnable profiles
image or separate profiles Dockerfile is required by this route.

The controller's CAPNICo override is currently tag-shaped. The adapter checks
the exact destination subject; a tag alone is not an authenticated release
contract. Runtime image checks cover the selected dependency roles.
Kube-vip and the test workload are additional runtime acquisitions; this is
not a completely offline deployment.

Diagnostic registry copies do not equal NKD publication/signing. Hosted
Release consumption additionally requires consumer-policy verification and
the source-owned NKE converter's supported artifact mappings. A whole
disk-containing Release is not established by a local demo.

## Deploy retained completed Runs

To retest only deployment, use:

```sh
"$NKE_DEMO" run --help
```

Supply the clean matching `--nke-checkout`, native `--nkdctl`, fresh
`--out`, and explicit completed `--os-run`, `--components-run`,
`--provider-run`, `--site-run`, `--tools-run`, `--profiles-run` and
`--simulator-run` paths. For a combined build, all seven selectors use the same
completed Run directory; each consumer verifies its own required outputs.

This is a retained-artifact replay, not a fresh build. For build-plus-deploy
use `e2e --config` from the quickstart; E2E derives its own fresh paths and
rejects retained Run references.

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

First-boot APT uses the baked local repository and fails on missing packages.
The diagnostic profile preserves the physical uplink across worker reboot.
These local profile choices do not establish a general NKE offline boot
interface or qualified image. Secrets, kubeconfigs, attempt credentials and
guest logs stay in private directories. NICoSim's diagnostic Bearer check is
not production authentication.

## Recover network controls after a crash

The private `network-state.json` journal records exact attempt ownership,
supported intents, original file identities/bytes and sysctl values before
granting controls. It is a recovery record, not an NKD receipt.

After SIGKILL/OOM or VM restart:

1. Preserve the original output directory and accepted Runs. Confirm the same
   dedicated Linux host/account; do not reset Colima or prune Docker.
2. Inspect surviving simulator, disk server, DNS services and libvirt domains.
   Stop/join only independently verified owned producers. Never kill a PID
   merely because it appears in the journal. Tenant/Kind/registry recovery after
   a hard crash is a separate operator task.
3. Restore only recorded network controls:

   ```sh
   "$NKE_DEMO" restore --work /absolute/Linux/path/to/original-demo-output
   ```

4. Require success before a new attempt. Active producers, foreign resources,
   changed file/bridge identities or unexpected routes cause refusal. Inspect
   the discrepancy rather than forcing removal.

The Go command accepts only version 2 journals. A Python-era version 1 journal
must be restored **before upgrading**, using the last compatible revision
`3b24beb5cb04df352e74a8d5405bd593871412b5`. If already upgraded, preserve the
original output and use a separate checkout of that revision on the same Linux
host/account, with its Python 3.12+ prerequisites:

```sh
git worktree add --detach /absolute/Linux/path/to/legacy-recovery \
  3b24beb5cb04df352e74a8d5405bd593871412b5
python3 /absolute/Linux/path/to/legacy-recovery/scripts/nke-local-demo.py \
  restore --work /absolute/Linux/path/to/original-demo-output
```

Run `git worktree add` from a Git checkout containing that commit, not an
archive-exported directory. The old CLI uses `scripts/nke-local-network.py` from
the same revision. Current Go recovery diagnoses and refuses version 1; it does
not migrate journals or execute the removed helper automatically. Do not change
the journal's version number to bypass this boundary.

Restoration takes the same kernel-released lock, never kills serialized PIDs
and, on the same boot, waits for in-flight bounded sudo work. It can replay an
interrupted rollback and reads back removals. An older attempt without the
journal requires manual inspection. Recovery does not create E2E evidence.

## Acceptance

Require one explicit input set, exact artifact read-back, completed NKE stages,
two Ready QCOW2-backed tenant nodes, workload/DNS success and owned cleanup.
The adapter prints its final PASS only after those checks.

A separate successful deployment replay does not establish a fresh one-shot
rebuild, complete source-built distribution, full OS smoke or qualification.
Current implementation and runtime limits are recorded in
[reconciliation](adr/reconciliation.md).
