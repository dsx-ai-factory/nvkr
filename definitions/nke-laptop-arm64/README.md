# ARM64 local NKE example

Start with the [first-time developer guide](../../docs/local-development.md).
This example selects the ARM64 counterpart of the combined M2 artifact selection
plus CAPNICo and the provisioned provider, without publication or signing. It is
not a full source-built NKE distribution. See the
[full-source gap map](../../docs/full-nke-source-build.md).

| Selection | Definition | Outputs |
| --- | --- | --- |
| Complete selected set | `definition.json` | 23: disk, images, charts and executable bundles |
| Faster component loop | `definition-components.json` | 22: everything above except the disk |
| OS-only loop | `definition-os.json` | One disk with its complete material closure |
| Source-built provider loop | `definition-providers.json` | Two native provider images, no disk |
| NICoSim build loop | `definition-simulator.json` | One executable bundle: source-built Linux ARM64 server, CLI and pinned seed/UI archive, not included in the 23-output set |

Use `environment-local.json` for rootless Podman, or `environment-local-docker.json`
with `CONTAINER_RUNTIME=docker` on native Linux ARM64, including execution inside
an explicitly selected `COLIMA_PROFILE` on Apple Silicon. Neither publishes or signs.
The OS selects TCG and the recorded local Packer subject through
`os-tcg.json`. The CLI catalog adds an explicit ARM64 producer; its pin starts
at the maintained CLI release and its stamps follow machine-managed pins.
The operator catalog pins native ARM64 Helm and kubectl assets.
The CAPNICo catalog builds the public v0.0.45 Git source using its source-owned
Dockerfile; the provisioned provider reuses the maintained controller catalog's
existing source producer. Neither provider is replaced with an acquired image.

```sh
bin/nkdctl definition validate --definition definitions/nke-laptop-arm64/definition.json
bin/nkdctl run local \
  --definition definitions/nke-laptop-arm64/definition.json \
  --environment definitions/nke-laptop-arm64/environment-local.json \
  --attempt arm64-preview --dry-run

NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh arm64-components-01 1 components

# No host Go, Make or installed nkdctl needed for the launcher:
bash scripts/nke-full-arm64-local.sh --check providers
CONTAINER_RUNTIME=docker bash scripts/nke-full-arm64-local.sh docker-providers-01 1 providers

# Build the missing simulator tools separately; this does not deploy a demo.
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh arm64-simulator-01 1 simulator
```

Use a fresh attempt for execution. `NKE_LOCAL_CLONE` supplies pinned Git objects,
not dirty checkout bytes; omit it for canonical remote fetch. See the guide for
commit-based development, clean-machine installation, exact outputs and resource
checks. Fresh toolchains freeze native-validated IDs in attempt-scoped overlays,
not under an older maintained ID. Docker Desktop on macOS is not admitted here.
Historical Runs do not prove the new bootstrap paths; preflight/dry-run do not
prove full-set success.

The simulator catalog pins NKE by exact commit and starts from the recorded
host-local Go/GCC image ID. The launcher supports Podman and native ARM64 Docker,
building and natively probing pinned toolchain sources when required. It binds
the measured image ID to exactly the selected simulator producers through an
attempt overlay. `--check simulator` requires no cached toolchain and does no
producer work; Docker bootstrap requires Buildx. The later committed
[Docker Run and startup probe](../../docs/evidence/2026-10-08-colima-nicosim-build.md)
passed separately; neither it nor the earlier Podman Run proves VM provisioning.
Module downloads and verification precede offline CGO compilation;
the server invokes NKE's `build-static` target. These Linux tools do not run directly
on macOS. The additional `nico-sim-runtime-assets` producer packages the pinned
diagnostic seed, UI and vendor notices in a tar+gzip archive, using deterministic
metadata. The producer writes `nicosim-runtime-assets.tar.gz`; the final bundle
member and verified extraction use `nico-sim-runtime-assets`, the component name.
Treat that member as archive data, not an executable/install target.
Its three-component Plan is contract-tested;
the earlier recorded Runs built only the server/CLI. Artifact-only runtime
consumption, the QCOW2-to-tenant handoff, profiles and the deploy-only adapter
still need validation/integration. This separate build is not a complete Colima/NICoSim demo.
