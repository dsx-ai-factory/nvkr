# Build your first NVKE artifacts

> **Preview:** NVKR is available as a preview. Commands, input schemas and behavior can change between releases, and we do not recommend NVKR for production use. See [Preview status and known limitations](overview/preview-status.md).

Build NVKR from source, then use it to build the NVKE CLI, controller container image and Helm chart.
All three artifacts and their dependency SBOMs stay on your machine.

**NVKR runs on macOS or Linux. The NVKE CLI and container image produced here target Linux.**
The commands select ARM64 on Apple Silicon and ARM64 Linux, or AMD64 on Intel Macs and AMD64 Linux.

## Before you start

You need:

- Git 2.38 or later and Bash. Go is required to compile NVKR; if it is missing, the launcher asks before installing it locally.
- Docker Desktop running on macOS, or Docker running on Linux, accessible to your user.
- Internet access to download source code, compilers, container images and packages.

## Build the artifacts

Open a terminal in a directory that does not already contain an `nvkr` folder, then copy this whole block.
The launcher checks Docker, compiles NVKR from this checkout and builds all three NVKE artifacts.
It checks prerequisites before building. If Go is missing, it offers a verified installation inside this checkout;
confirm with `y` to install and continue. No Make or host Python is required. Supported Git/Docker package installs
are also shown for approval; Docker must be running and accessible to your user before the build can start.
The launcher runs in a child shell. A failed command returns an error without changing your SSH shell settings.

```sh
git clone https://github.com/dsx-ai-factory/nvkr.git &&
cd nvkr &&
bash scripts/nvkr-quickstart.sh
```

The first build downloads dependencies. Wait for NVKR to report:

```text
status: complete
observed outputs: 3
```

The launcher lists the three payload files and prints the full path to their build directory.
NVKR runs directly on your host; Docker runs the NVKE build tools. The native CLI is `bin/nvkr`.
The checkout and results are inside the new `nvkr` folder. To build again from there, run
`bash scripts/nvkr-quickstart.sh`; it selects a fresh build directory and retains earlier results.
You do not need a separate NVKE checkout, registry or cluster.

## Your files

Open the build directory printed at the end. These paths are relative to that directory:

| File or directory | What you built |
|---|---|
| `nvkectl/out/nvkectl/nvkectl` | NVKE CLI executable for Linux on your CPU architecture. |
| `nvke-controller/out/nvke-controller/*.oci.tar` | Controller container image archive. |
| `nvke-controller-chart/out/nvke-controller-chart/*.tgz` | Source Helm chart archive; the image-bound chart is in `packages/nvke-controller-chart/`. |
| `*/sbom/*.cdx.json` | Dependency SBOMs in CycloneDX JSON format. |
| `packages/` | Packaged artifacts in OCI format. |
| `lifecycle.json` | Build status and artifact digests. |

Keep the build directory together. This local build is unsigned; deployment and qualification are separate steps.

## Next steps

- **Choose or preview outputs:** see the [tutorial inputs](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/examples/nvke-tutorial/README.md#preview-the-native-build). The preview needs no Docker.
- **Build the larger NVKE selection, including OS images:** follow the [maintained NVKE Definition](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/nvke/README.md). OS-image builds require native Linux KVM runners.
- **Publish:** follow [pipeline generation](generate-pipeline.md) for hosted build, signing and publication. Uploading this completed local build without rebuilding is not yet supported.
- **Deploy the demo:** follow the [native demo guide](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/local-nvke-demo.md).

If Docker's check fails, start Docker and check access with `docker info` before trying again.
If `git clone` reports that `nvkr` already exists, choose another parent directory; do not overwrite your checkout.
For a later build failure, retain its directory and terminal output when [reporting the issue](https://github.com/dsx-ai-factory/nvkr/issues).
