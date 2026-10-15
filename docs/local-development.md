# Build Linux ARM64 NVKE artifacts locally

For your first complete build/deploy/tenant test, follow the
[Ubuntu ARM64 quickstart](local-nvke-demo.md). On macOS that guide adds a Colima
Linux VM; outputs are Linux ARM64, not Darwin binaries.

This reference covers focused artifact builds and committed source changes.
NVKR's `nvkr` builds named outputs from a Definition, pinned catalog and
Environment. A local Run is unqualified; it does not publish/sign a Release or
install a cluster. The historical launcher's `full` name does not establish a
complete source-built distribution. See [remaining source gaps](full-nvke-source-build.md).

## Start from a clean machine

Use Ubuntu ARM64 with Git, Bash, jq, GNU timeout and Docker. Follow the
[quickstart's prerequisites](local-nvke-demo.md#2-install-prerequisites-on-the-ubuntu-execution-host)
for setup. Compilation runs in the pinned utility container: no host Go, Packer
or Helm is needed. The OS/simulator/site lanes also require Docker Buildx.

The launcher checks capabilities before downloading or creating an attempt.
It does not install packages, start/resize VMs, prune caches or reset runtimes.
Git authorization must work on the Linux execution host.

Ordinary local fetch projects global `credential.helper`, `credential.username`
and `credential.useHttpPath`, plus fetch-only HOME/XDG, SSH-agent and selected
proxy/TLS-trust settings. System helpers, GH/GCM/DBus/keyring environment,
arbitrary SSH commands and other global URL rewrites are not inherited.
Use a compatible configured helper or an explicit local repository containing
the pinned objects; checkout and status receive no developer helper or HOME.

### Apple Silicon Mac with Colima

Follow [the quickstart's macOS setup](local-nvke-demo.md#additional-macos-setup)
and enter its native ext4 source export. Invoke all OS/demo commands inside
Linux, not on the Mac shared mount. Inspect physical Mac capacity separately;
a 150-GiB sparse guest disk does not reserve that much host space.

For component-only builds the launcher also supports an explicitly selected
`COLIMA_PROFILE=NAME` route. The forwarded Mac route
rejects `os` and `all`; use native Linux storage for those.

## Check before building

From the NVKR root on the execution host:

```sh
bash scripts/nvke-full-arm64-local.sh --help
bash scripts/nvke-full-arm64-local.sh --check components
bash scripts/nvke-full-arm64-local.sh --check os
```

Preflight creates no attempts and performs no pulls. It checks preliminary
runtime/tool/resources, not Git permission, future network availability,
Packer success or reserved capacity. Actual Plan and runtime admission still
run at execution. OS admission requires eight CPUs, 10 GiB available memory,
64 GiB workspace and its authored timeout; allow about 90 GiB before a bake.

## Choose a focused build

```sh
# Optional exact Git-object transport; omit for authenticated remote Git:
export NVKE_LOCAL_CLONE=/absolute/Linux/path/to/nvke
bash scripts/nvke-full-arm64-local.sh first-components 1 components
```

Use a new attempt name for every invocation. The arguments are attempt,
parallel producer jobs, and selection:

| Selection | Outputs |
| --- | --- |
| `providers` | Two source-built infrastructure-provider images |
| `components` | 22 outputs: CLI/controller/chart, providers and acquired dependencies/tools; no disk |
| `os` | Plain ARM64 QCOW2 and its material closure |
| `all` | Combined demo artifact set: OS, components, site, tools, profiles and simulator |
| `site` | Installer-aligned CLI, controller image/chart and NKTP chart |
| `tools` | Source-built native installer-tools bundle |
| `profiles` | Native OCI controller profiles image with configuration and runbooks |
| `simulator` | NICoSim server, CLI and runtime-assets bundle |

The complete demo builds `all` in one Run through `nvke-demo e2e`, then deploys
its exact completed outputs. Acquired third-party images/charts
are not source compilations. Ordinary local SBOMs are composition evidence,
not full-disk scan/qualification. AMD64, FDE, hosted signing and publication are
not selected by this example.

## Build the complete selection, including the OS

```sh
bash scripts/nvke-full-arm64-local.sh first-os 1 os
# Or the combined demo artifact set, without deployment (two concurrent operations):
bash scripts/nvke-full-arm64-local.sh first-all 2 all
```

Use a conservative jobs limit on a laptop; raise it for a larger host. Software acceleration uses
TCG, not KVM. The launcher builds/probes pinned local toolchain sources when
necessary and freezes the measured image ID in the attempt's catalog overlay.
It does not update maintained locks, publish images or confer qualification.

The older [OS-only wrapper](../inputs/definitions/diagnostics/README.md) has different
runtime/acceleration admission; its commands are not interchangeable with
the laptop route. See [toolchain inputs](../toolchains/nvkr-packer/README.md).

NVKR prepares agent/CNI/Kubernetes/SPIRE/OVN material bytes, image inputs and
installer ISO. Source-owned NVKE Packer consumes the staged manifest and
payloads to assemble the disk. NVKE Make remains the direct developer entry
point. Ubuntu autoinstall can still access APT; this is not hermetic OS or
byte-reproduction evidence.

## Build the installer tools from source

```sh
bash scripts/nvke-full-arm64-local.sh first-tools 1 tools
```

The bundle contains Linux ARM64 Helm 4.2.4, kubectl 1.36.0, clusterctl 1.12.3,
ORAS 1.3.3 and Kind 0.32.0, matching
[NVKE's pinned installer declarations](https://github.com/dsx-ai-factory/nvke/blob/8fb2d53368823bd386b4fbd55b4ab6d3c0931345/make/tool-versions.mk).
See [catalog producers](../inputs/catalogs/tools/installer-local-arm64/registry.yaml)
and generated pins for exact commits, compiler and preparation instructions.

Source preparation verifies module checksums before offline compilation.
Helm's local-only preparation removes four checked upstream negative-test
symlinks; this does not enable hosted source fetching or qualification. Compiler
tests do not establish organizational license/security approval.

The acquired `operator-tools` in `components` is a different historical
selection. The demo's combined Run includes the source-built installer-tools output.
Do not run these Linux executables directly on macOS.

## Inspect completed outputs

For `first-components`:

```sh
jq '{attempt,status,lane,expectedCount:(.expected|length),
     observedCount:(.observed|length),failure}' \
  .nvkr/runs/first-components/lifecycle.json
```

Require `complete`, equal expected/observed counts and no failure.
The lane remains `unqualified-local-execution`. Failed partial bytes are not
accepted outputs. Ordinary artifacts live in `packages/<name>/` as OCI layouts;
a completed OS Run exports `outputs/nvke-node-image-arm64.qcow2`.

Source snapshots, acquired inputs and producer results remain in the same
attempt. `.runner/plan.json` is its retained Plan. Use `run plan --plan` to
inspect it; there is no `run show` command. Keep accepted Runs private and
preserve them before deleting a VM.

With an existing native `nvkr`, extract a completed tools bundle through
the delivery owner rather than guessing OCI blob paths:

```sh
NVKR_TOOLS_RUN="$(pwd)/.nvkr/runs/first-tools"
NVKR_TOOLS_BOOT="$(pwd)/.nvkr/laptop-bootstrap/first-tools"
"$NVKR_TOOLS_BOOT/nvkr" publish root \
  --bundle "installer-tools-arm64=$NVKR_TOOLS_RUN/packages/installer-tools-arm64" \
  --out "$NVKR_TOOLS_BOOT/consumer-publication"
"$NVKR_TOOLS_BOOT/nvkr" publish extract \
  --layout "$NVKR_TOOLS_BOOT/consumer-publication" --bundle installer-tools-arm64 \
  --out "$NVKR_TOOLS_BOOT/consumer-bundle"
"$NVKR_TOOLS_BOOT/consumer-bundle/helm" version --short
```

Both destination directories must be new. This verifies local bundle bytes,
not signatures or Release authorization. OCI layouts are not registry URLs;
deployment needs the [explicit artifact handoff](nvkr-to-nvke-demo.md).

## Rebuild a committed local source change

For tight edit loops use NVKE's own Make/tests. To test NVKR packaging, commit your
change in a dedicated NVKE clone and pin the changed producer. `NVKE_LOCAL_CLONE`
changes transport only; it does not follow your branch or include dirty files.

For the retained CLI example, start from its `cli/v0.1.0-rc.2` baseline.
Use native `nvkr` built with the repository's pinned Go:

```sh
NVKE_DEV_CLONE=/absolute/Linux/path/to/nvke
NVKE_DEV_COMMIT=$(git -C "$NVKE_DEV_CLONE" rev-parse HEAD)
GIT_CONFIG_COUNT=1 \
GIT_CONFIG_KEY_0="url.$NVKE_DEV_CLONE.insteadOf" \
GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/nvke \
  bin/nvkr catalog pin \
  --dir inputs/catalogs/nvke/module-cli-arm64 \
  --commit "$NVKE_DEV_COMMIT" nvke
git diff -- inputs/catalogs/nvke/module-cli-arm64
NVKE_LOCAL_CLONE="$NVKE_DEV_CLONE" \
  bash scripts/nvke-full-arm64-local.sh cli-change-01 1 components
```

This changes the tracked example lock. Review it and preserve unrelated edits;
do not commit a development-only pin or publish an unpushed source.
A different baseline may require matching build instructions/compiler, not just
repinning. Copy shared catalogs before experimenting with controller/OS changes.
`catalog pin --check` previews the difference with exit 3 (`NVKR-E-DIFF`).

## Validation status

Unit/contract tests do not prove clean-machine setup, a fresh complete E2E run,
full-source membership, a complete disk smoke or qualification. Require terminal
Run records and real consumer checks for your own invocation. Current limits
belong in [reconciliation](adr/reconciliation.md), not a second quickstart.
