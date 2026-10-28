# Native ARM64 SBOM tool

This catalog pins the Linux ARM64 Syft release asset for local ARM64 utility execution.
The NVKE quickstart selects it with an ARM64 utility Environment. NVKE image, chart and CLI
producers retain their owning release catalog through output-scoped catalog selections.

Update the existing scanner version through the CLI, keeping it aligned with the maintained
release catalog:

```sh
bin/nvkr catalog pin --dir inputs/catalogs/tools/sbom-arm64 --tag v1.51.1 syft
```

The lock binds the source revision, release archive and executable bytes. Acquisition does not
establish a signed or qualified release.
