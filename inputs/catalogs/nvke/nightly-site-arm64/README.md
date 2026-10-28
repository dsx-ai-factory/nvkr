# Nightly site producer catalog

This profile-owned catalog selects the CLI, controller image, controller chart and NKTP chart for both
ARM64 nightly Definitions. It follows `../local-site-arm64`; each source role keeps its distinct
vendor closure. All four roles select NVKE tag `v0.22.0`, commit
`400c24fb6465b042be883d6b62472528520d6291`, tree
`f3afc02f5c92f4aacda3a1655836a91bef093bfe` and committer epoch `1792548398`.
The test-only nightly suites retain the separate `b4f90e2c4fc4ed3d07efc18a22066a7e0b9b99c8`
entrypoint selected by the beta migration; v0.22.0 omits that entrypoint. This does not establish runtime compatibility.

The producer mappings follow the source-owned contracts at that revision:

| Output | Source contract |
| --- | --- |
| `nvkectl` | `cli/Makefile` builds `cli/bin/nvkectl` and owns `NVKECTL_VERSION`, `NVKECTL_COMMIT`, `NVKECTL_DATE` and `NVKECTL_TREE`; its build target executes the binary's version assertion |
| `cwe` | `controller/Dockerfile` uses Go 1.27.3 and the existing Alpine digest pin and its source-owned controller Make target |
| `controller-chart` | Source `Chart.yaml` version `0.0.0`, packaged as `0.22.0`, no dependencies or `Chart.lock`; the final chart remains bound to the produced controller digest |
| `nktp` | Source `Chart.yaml` version `0.0.0`, packaged as `0.22.0`; its existing `Chart.lock` selects the three captured dependency-chart archives |

CLI vendoring verifies that CLI, controller API, chart-module and management-engine module metadata
are unchanged after the standard Go download, verify and vendor commands. The chart module has no
`go.sum`; absence remains an explicit guard. No module files are rewritten to make the build pass.

Both chart sources select the Go 1.27.3 image
`docker.io/library/golang@sha256:957861185dbf29d93668ace41e011d091c6e779953d47f0316ef2cc58e28fe2b`.
Fetch acquires the existing operator catalog's official Helm 4.1.4 ARM64 archive, verifying archive
SHA-256 `13d03672be289045d2ff00e4e345d61de1c6f21c1257a45955a30e8ae036d8f1` and executable
SHA-256 `4d6e3a69e6203094d564d5d4e94325b1f7209421dc7e832a96d2510295e03f1d` before execution.
The exact executable and its archive's license enter each distinct source closure under
`nvkr-chart-tools`, with executable mode `0555`. The fetch owner authenticates the complete returned
tree and modes into its closure receipt; the build owner rechecks that closure before making the
source snapshot read-only. Build invokes `/src/nvkr-chart-tools/helm` directly after repeating its
executable checksum. No ambient `PATH` lookup, image entrypoint override or extra build environment
variable selects the chart tool. Final Helm OCI packaging uses NVKR's existing observed-archive and
image-binding owners. The installer output's separate Helm 4.2.4 selection remains unchanged.

The compiled vendor and package commands were executed against the previous NVKE source
`04a6ed26c1e51d70167669a6882a04080eefadc7` in native Linux ARM64 Docker with the previous Go 1.27.2 image
`sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c`. Fetch used a read-only root, private HOME and temporary
storage, dropped capabilities and no new privileges. NKTP's `Chart.yaml`, `Chart.lock` and three
dependency-archive checks passed. Offline packaging used network denial and read-only source, producing
`nke-controller-0.0.0.tgz` and `nktp-0.0.0.tgz`; Helm reported `v4.1.4+g05fa379`. A changed executable
failed its checksum before packaging and emitted no chart. These are container command tests, not a
complete held-source `ExecNode`, hosted producer Run, signed publication or runtime E2E result.
They do not establish a fresh source receipt or complete Run for the current v0.22.0 source and toolchain.

NKTP dependency charts remain acquired materials with their existing byte hashes; these checks do
not authenticate upstream signatures. Native profiles and rootless BuildKit retain their separate
explicit pins. No toolchain image was built or published, no nightly producer was dispatched and no
qualified release is established. The resolved release tag, commit and owner-issued tree/epoch values remain in `pins.lock.yaml`.
