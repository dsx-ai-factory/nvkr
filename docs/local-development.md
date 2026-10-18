# Build Linux ARM64 NVKE artifacts locally

For the shared native Ubuntu ARM64 or Apple Silicon build/deploy/tenant workflow,
follow the [native demo guide and current validation limits](local-nvke-demo.md#native-linux-and-mac-path).
This reference describes the focused Linux artifact launcher.

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

### Explicit legacy Colima build route

An existing Colima ARM64 Linux profile can host the legacy artifact launcher.
Keep the checkout and build workspace on its native Linux filesystem, rather
than the Mac shared mount, and run OS/build commands inside that Linux guest.
Inspect guest and physical Mac capacity separately; sparse guest storage does
not reserve host space. The [native Mac demo](local-nvke-demo.md#additional-macos-setup)
uses the Mac CLI directly.

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

The launcher retains the compiler-read input closure as `compile-inputs.json`
inside the immutable attempt, before producer execution. Generated local toolchain
references remain relative to the repository so the saved inputs can be replayed.
For disk outputs, it also retains `disk-materials.json` from the original live
producer-consumption observations before finalizing the Run. This records staged
material handoffs, with explicit limits for source vendoring and network/guest
acquisitions. Both records are descriptive; they do not authenticate the Run or
qualify its outputs. Material-retention failure prevents a complete captured Run.
Direct `nvkr run local` callers can request the same capture with
`--capture-inputs-root .` and repository-relative Definition and Environment paths.
Dry runs retain no attempt or input snapshot.

## Qualify local artifacts

On native Linux, `run qualify-local` verifies every output of a completed captured Run, signs and verifies
its build evidence, and checks each selected native tool's version in isolation. Separate keys sign the
tool-check report and qualification statement. It then publishes artifacts and signed evidence, downloads
the complete set again and independently verifies the bytes, saved inputs and signatures.
Its result remains `unqualified-local-execution`. Qualification has passed for the native ARM64 demo's
29 outputs and all 10 delivered tools. Exact artifact references and remaining checks are tracked in
[reconciliation](adr/reconciliation.md#local-qualification-demo).

For a new complete demo, add `--qualify-local` to the normal native Linux ARM64 command:

```sh
nvke-demo e2e --config /native/demo.yaml --qualify-local
# Build, sign, smoke and publish without deployment:
nvke-demo build --config /native/demo.yaml --qualify-local
```

Alternatively set `build.qualifyLocal: true` in the base YAML. Use a fresh attempt; do not combine
this option with `prepared`, `qualificationPolicy` or explicit qualification settings. The command
prepares the attempt, generates disposable trust, builds, qualifies and verifies registry read-back.
It automatically stops its qualification registry before returning build inputs, including on failure
or cancellation. A stop failure prevents deployment. Data, keys and evidence remain under
`.nvkr/laptop-bootstrap/<attempt>/qualification/`; registry references are offline after stop.
The normal demo retain/cleanup behavior is unchanged. Host prerequisites are the same as the base
demo, plus network access for the pinned signing executable and images and disk space for publication.
This one-command flow has contract coverage; it has not yet had a fresh full native build/qualify/deploy run.

For advanced use that keeps the registry running for later consumer checks, explicit setup remains:

```sh
bash scripts/nvke-full-arm64-local.sh --prepare-only first-all 2 all
export NVKE_DEMO="$PWD/.nvkr/laptop-bootstrap/first-all/nvke-demo"
mkdir -p .nvkr/qualification
"$NVKE_DEMO" qualification-setup --config /native/demo.yaml \
  --out .nvkr/qualification/first-all
"$NVKE_DEMO" build --config .nvkr/qualification/first-all/demo.yaml
```

The base YAML must match the prepared repository, attempt, jobs, source clone and network, with no
existing qualification, `qualifyLocal` or `prepared` settings.
For `run.network: upstream-ovn`, add `--network upstream-ovn` to the prepare-only command.
Build-only invocation now preserves the configuration's network selection too. A minimal base YAML is:

```yaml
version: 1
build:
  repo: /native/nvkr
  attempt: first-all
  jobs: 2
run:
  network: ovn
  out: /native/first-all-deployment
```

Setup downloads cosign v3.1.3 over HTTPS and checks its maintained ARM64 byte digest before executing
it, generates three fresh passwordless local keys, writes a public policy bound to the prepared Plan,
recompiles the inputs and runs signing preflight, pulls the pinned smoke and registry images, and starts
a dedicated registry on a dynamically allocated loopback port. Its fixed demo profile covers the exact
29 selected outputs; changed membership, unsupported tools or a mismatched prepared invocation fail.
The profile supports the maintained NVIDIA and upstream OVN selections and the explicit Calico diagnostic.
It does not learn smoke exemptions or signer trust from candidate artifacts. Generated keys all belong to
this local operator; setup does not establish independent organizational approval.

The new setup directory contains `demo.yaml`, `policy.json`, role keys, pinned `cosign`, `scratch/`,
private diagnostics and disk-backed `registry-data/`. Its parent must exist on native disk, and setup
never overwrites an existing directory. The registry stays running for qualification and later consumer
read-back; its storage is separate from the base demo's small deployment registry. Stop it explicitly:

```sh
"$NVKE_DEMO" qualification-registry-stop --out .nvkr/qualification/first-all
```

The registry runs with the Linux caller's UID and GID so it can use its private storage without
adding capabilities or widening directory permissions. To exercise setup and actual OCI write/read-back
as a non-root Linux user with Docker, run
`NVKR_TEST_LOCAL_REGISTRY=1 go test ./internal/nvkedemo -run '^TestExternalQualificationRegistryStorage$' -count=1 -timeout=10m`.
Use disk-backed `TMPDIR`; the test stops its own registry and does not touch demo registries.

Stop verifies the container ID, owner labels, pinned image, user, storage mount and loopback binding, then
stops only that container. It retains the container, keys, data and evidence. Stopping makes registry
references unavailable until the retained container is started again. Failed setup retains diagnostic
files and stops any registry it has started when cleanup can complete; an interrupted setup must not be
treated as ready. Neither setup nor stop deletes data, prunes Docker, installs host packages or changes
existing trust. Existing completed Runs continue to use the explicit `run qualify-local` interface below.

For externally managed trust or an already completed Run, use the explicit configuration below instead
of setup. Supply an independently authored `nvkr.dev/local-qualification-policy/v1` JSON policy with the exact
Definition, Environment and Plan digests, native Linux platform, pinned cosign executable digest, three
distinct P-256 public keys, pinned smoke image and complete supported tool profiles. The command accepts
explicit passwordless private-key files for those roles. It rejects unsupported or omitted smoke members;
reducing the policy scope cannot make the complete demo qualify.

Prepare the exact attempt first so the policy can bind the selected Plan, including its local
toolchain substitution. On the native Linux host, from the repository root:

```sh
bash scripts/nvke-full-arm64-local.sh --prepare-only first-all 2 all
```

Review `.nvkr/laptop-bootstrap/first-all/selected-plan.json` and author the policy with its exact
identities and complete smoke scope. Then set `build.prepared: true` in the demo YAML, or pass
`--prepared` to `nvke-demo build` or `e2e`, keeping the same repository, attempt, jobs, source clone
and network selection. This mode requires a qualification policy and an existing bootstrap with no
existing producer Run. It recompiles the current inputs through the prepared executable and checks
the policy again before producers start; the saved Plan is not a reusable verification receipt.
It does not resume a failed or completed Run. The final qualification independently checks the
captured inputs and delivered bytes. Native full-demo validation of this two-stage flow remains open.

For `nvke-demo build` or `e2e`, `--qualification-policy /absolute/path/policy.json`
(YAML `build.qualificationPolicy`) checks the prepared Plan and complete smoke scope before the
producer Run starts. Bootstrap and toolchain preparation still occur before this check. This option
only admits the intended build. To also require signing, smoke and registry read-back before deployment,
add the following fields under `build` in the demo YAML (all filesystem paths must be absolute):

```yaml
  qualificationPolicy: /trust/local-qualification.json
  qualification:
    builderKey: /trust/builder.key
    smokeKey: /trust/smoke.key
    qualifierKey: /trust/qualifier.key
    cosign: /opt/pinned/cosign
    repository: localhost:5000/local-m3
    scratch: /native/qualification-scratch
    evidenceOut: /native/qualification-scratch/first-all-evidence.json
    plainHTTP: true
    anonymous: true
```

With this configuration, both `build` and `e2e` invoke `run qualify-local` on the newly built complete
attempt. Before producers start, lifecycle preflight also checks the three private keys against their
policy roles using the pinned cosign executable, registry settings, the new evidence destination and
at least 1 GiB of preliminary disk-backed scratch headroom. Final payload-dependent capacity and all
signing checks run again after the build; preflight does not reserve capacity or verify registry access.
`e2e` reaches deployment only after the child exits successfully and reports `passed` for that
attempt in the local lane. The demo retains the child result in
`.nvkr/laptop-bootstrap/<attempt>/qualification-result.json`; it reserves that transcript before
starting qualification and refuses to reuse it. An interruption may leave an empty transcript;
choose a fresh attempt rather than treating it as successful evidence.
Private child diagnostics, including stderr, are retained in
`.nvkr/laptop-bootstrap/<attempt>/qualification-diagnostics/qualify-local.log`.
Compiler replay still authenticates upstream Git pin sets, so qualification needs the same
source access as build preparation. The demo preserves the supported credential-helper home,
SSH agent, proxy, certificate and explicit local redirect settings for that controller;
the delivered-tool smoke environment remains isolated from those credentials.
The signed evidence remains at `evidenceOut`. A failed qualification preserves the completed build for
diagnosis but prevents deployment. This adapter has process-fixture coverage. The complete qualification
command has passed against the real demo outputs; downstream cluster deployment remains a separate exercise.

Use an already running development registry and existing disk-backed scratch. Replace these example
paths with the completed attempt, approved policy, key files and pinned executable. The evidence file
must be new, and its parent directory must exist. Run from the repository root and pass the
completed workspace as a clean relative path under `.nvkr/`, as required by factory observation:

```sh
mkdir -p .nvkr/qualification
bin/nvkr run qualify-local \
  --workspace .nvkr/runs/first-all \
  --policy local-qualification.json \
  --builder-key /secure/local-builder.key \
  --smoke-key /secure/local-smoke.key \
  --qualifier-key /secure/local-qualifier.key \
  --cosign /opt/pinned/cosign \
  --repository localhost:5000/local-m3 \
  --scratch .nvkr/qualification \
  --evidence-out .nvkr/qualification/first-all-evidence.json \
  --plain-http --anonymous
```

Authenticated registries use `NVKR_REGISTRY_USER` and `NVKR_REGISTRY_PASSWORD`; explicit anonymous
access rejects those credentials when present. The command emits one canonical JSON result on stdout.
`passed` requires signing, smoke, complete registry read-back and cleanup. A failure may leave signed
evidence or remote artifacts; the result lists attempted references without claiming their acceptance.
Use a new evidence destination for a retry. This command does not provision a tenant; the configured
demo invokes it before proceeding to deployment.

For a separate consumer exercise of the complete 29-output demo, retain the successful command's
JSON stdout as a result file. On native Linux, the opt-in test below downloads its digest-addressed
publication into fresh scratch and verifies it using the independently selected public policy.
It needs no producer workspace or private signing keys. Compiler pin authentication still requires
source access. This test explicitly uses the anonymous HTTP development registry and requires
disk-backed `TMPDIR` capacity for downloads and captured payloads:

```sh
NVKR_TEST_M3_RESULT=/native/qualification-result.json \
NVKR_TEST_M3_POLICY=/trust/local-qualification.json \
NVKR_TEST_COSIGN_BINARY=/opt/pinned/cosign \
TMPDIR=/native/qualification-scratch \
go test ./pkg/nvkr/lifecycle -run '^TestExternalLocalM3Consumer$' -count=1 -timeout=2h -v
```

The exercise checks the complete positive case, invalid signatures for each role, missing signatures
or provenance, changed compiler inputs or source pins, mismatched Run evidence, a wrong policy Plan, missing or extra
transport members, and altered or removed downloaded disk bytes.
Negative cases alter only the consumer's downloaded data. A skipped test is not external evidence.

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

The bundle contains Linux ARM64 Helm 4.2.4, kubectl 1.36.0, clusterctl 1.13.6,
ORAS 1.3.3 and Kind 0.32.0, matching
[NVKE's pinned installer declarations](https://github.com/dsx-ai-factory/nvke/blob/400c24fb6465b042be883d6b62472528520d6291/make/tool-versions.mk).
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

For the retained CLI example, start from its `v0.22.0` baseline.
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
