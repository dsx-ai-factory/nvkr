# Local NKE development on ARM64

Start with the [first-time developer guide](../../docs/local-development.md).
`nke-local-dev-arm64` names the development workflow and artifact architecture,
not the host operating system. On Apple Silicon macOS, execution runs inside an
explicitly selected Colima Linux VM; native Linux ARM64 is also supported. Every
selected artifact targets `linux/arm64`, not Darwin.
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
| Staged-installer site loop | `definition-site.json` | Four source-aligned candidates: CLI, controller image, image-bound controller chart and NKTP chart; not included in the 23-output set |
| Source-built installer tools | `definition-tools.json` | One five-component Linux ARM64 executable bundle; separate from the acquired `operator-tools` in `all` |
| Diagnostic controller profiles | `definition-profiles.json` | One plain ARM64/Ubuntu 26.04 profiles OCI image; separate from `all`, not fleet profiles or deployment proof |

Use `environment-local.json` for rootless Podman, or `environment-local-docker.json`
with `CONTAINER_RUNTIME=docker` on native Linux ARM64, including execution inside
an explicitly selected `COLIMA_PROFILE` on Apple Silicon. Neither publishes or signs.
The OS selects TCG and the recorded local Packer subject through
`os-tcg.json`. The CLI catalog adds an explicit ARM64 producer; its pin starts
at the maintained CLI release and its stamps follow machine-managed pins.
Its compiler subject `golang@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9`
deliberately differs from the `nke-v1` compiler subject
`golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6`.
The common build command does not imply identical release compiler or bytes.

The launcher builds NKD with Go 1.27.2 from the Environment's digest-pinned
utility image. `make build` checks that SDK against `.go-version`; the launcher
does not download a second Go SDK. This does not repin the NKE producer images
or claim that existing Packer/inspection images were rebuilt with Go 1.27.2.

The combined, components and OS selections reuse catalogs under `definitions/nke-m2`.
In particular, `os-tcg.json` binds its override to that OS catalog's exact
`requiresCatalogDigest`. An M2 catalog repin can therefore invalidate this local
example. Review the changed source/material/toolchain contract before updating
that binding; do not bypass it. Default tests check exact provider membership
and compile the actual CAPNICo catalog and OS selection without remote Git.
`NKD_TEST_EXTENDED=1 go test ./pkg/nkd/lifecycle -run '^TestLaptopARM64CatalogCompilation$' -count=1`
compiles the complete, unmodified two-provider and 22/23-output catalogs. Those
integration tests require authenticated NKE Git access or an exact local Git
mirror: the shared controller catalog validates its derived chart pin set against
the locked NKE tree even when only its provider is selected. No source build or
chart download runs during compilation, but compilation is not network-free
without that mirror. It is not a runtime build or acceptance run.
The operator catalog pins native ARM64 Helm and kubectl assets.
The CAPNICo catalog builds the public v0.0.45 Git source using its source-owned
Dockerfile; the provisioned provider reuses the maintained controller catalog's
existing source producer. Neither provider is replaced with an acquired image.

```sh
bin/nkdctl definition validate --definition definitions/nke-local-dev-arm64/definition.json
bin/nkdctl run local \
  --definition definitions/nke-local-dev-arm64/definition.json \
  --environment definitions/nke-local-dev-arm64/environment-local.json \
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
producer work; Docker bootstrap requires Buildx. Require a completed Run and
artifact-only startup checks; neither establishes VM provisioning.
Module downloads and verification precede offline CGO compilation;
the server invokes NKE's `build-static` target. These Linux tools do not run directly
on macOS. The additional `nico-sim-runtime-assets` producer packages the pinned
diagnostic seed, UI and vendor notices in a tar+gzip archive, using deterministic
metadata. The producer writes `nicosim-runtime-assets.tar.gz`; the final bundle
member and verified extraction use `nico-sim-runtime-assets`, the component name.
Treat that member as archive data, not an executable/install target.
Its three-component Plan is contract-tested. No private simulator-build or startup
acceptance outcome is recorded here. A mock-runtime probe is not acceptance of
the QCOW2-to-tenant handoff, profiles or deploy-only adapter. This build is not a
complete Colima/NICoSim demo.

The separate `site` lane pins four producers to NKE PR #1160's exact candidate
commit `8fb2d53368823bd386b4fbd55b4ab6d3c0931345`, rather than combining older
controller and CLI release pins. It uses the source-owned CLI Make build and
controller Dockerfile, preserves chart versions `0.0.0` and `0.1.0-rc.2`, and binds
the controller chart to the produced image. Its local Helm wrapper registers
NKTP's three repositories before fetching locked chart dependencies and verifies
the three exact archive SHA-256 pins before offline packaging. A missing,
additional or republished same-version archive fails preparation. Those
dependency archives are not independent source builds
or upstream-signature-verified inputs.

```sh
CONTAINER_RUNTIME=docker COLIMA_PROFILE=nkd-demo \
  bash scripts/nke-full-arm64-local.sh --check site
CONTAINER_RUNTIME=docker COLIMA_PROFILE=nkd-demo \
  NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh arm64-site-01 1 site
```

Docker/Podman compiler contracts cover the selected inputs. Require a complete
four-output Run, native controller help and unchanged chart handoff/rendering
before attempting deployment. These checks do not install NKE. The wrapper uses the existing
pinned Helm 4.3.0, not the installer's preferred 4.2.4. Its readiness script warns
on this mismatch; a warning is not a matched pin. Read the
[handoff gaps](../../docs/nkd-to-nke-demo.md) before treating these inputs as a
working deployment. Do not run another large build alongside the OS bake.

The separate `tools` selection compiles Helm 4.2.4, kubectl 1.36.0, clusterctl
1.12.3, ORAS 1.3.3 and Kind 0.32.0 from their machine-pinned upstream Git trees.
It reuses PR #113's preparation/offline-build approach and does not change the
older acquired `operator-tools` output or the OS graph. See the
[source-tool instructions and limits](../../docs/local-development.md#build-the-installer-tools-from-source).
Compiler and preflight contracts do not prove a real tools build or installer
integration. Require a completed Run, verified extraction and all five native
version probes. These are Linux binaries, not Mac binaries.

The separate `profiles` lane packages pinned NKE VM config profiles/runbooks
and a diagnostic ARM64/Ubuntu 26.04 plain OS profile. Require a completed Run and
offline content/NKE-owned template checks; those are not provisioning or tenant acceptance.
See the [profiles guide](../../docs/local-controller-profiles.md) before using
its local endpoint and TPM configuration; it does not authenticate a disk or
replace production fleet profiles.
