# Local controller profiles image

The `profiles` lane selects `controller-profiles`: a native OCI container image
containing profiles and runbooks at `/profiles`. The pinned NVKE controller chart
uses this image in its existing init container to copy the files into a shared
volume. The main controller reads that volume; it does not run the profiles image.

For the complete build-and-deploy sequence, follow the
[local demo guide](local-nvke-demo.md). To build this output alone on Linux ARM64:

```sh
CONTAINER_RUNTIME=docker bash scripts/nvke-full-arm64-local.sh --check profiles
CONTAINER_RUNTIME=docker bash scripts/nvke-full-arm64-local.sh my-profiles-01 1 profiles
```

Use a fresh attempt name. As with the other lanes, NVKR owns source admission,
artifact observation and native OCI image packaging; direct `oras push` does not
replace those checks.

## Content and delivery

The [catalog](../inputs/catalogs/controller-profiles/nvidia-arm64/registry.yaml)
selects pinned NVKE config profiles and runbooks. Its authenticated
[local data overlay](../inputs/catalogs/controller-profiles/nvidia-arm64/patches/local-profile.patch)
**only adds** a plain Ubuntu 26.04 ARM64 profile: cloud-init, metadata and iPXE
configuration. It changes no upstream NVKE file and adds no build script.

The producer stages those files and builds the source-owned
`controller/profiles/example/Dockerfile` through pinned rootless BuildKit. A named
build context supplies the pinned Alpine base without modifying the Dockerfile.
No separate demo Dockerfile or host profile extraction is needed.

The demo copies the accepted OCI image to its local registry through ORAS and
reads back the exact manifest digest. `profiles.image.enabled=true` selects that
repository and digest. NVKE's `materialize-profiles` init container runs the image's
shell and copies `/profiles` into the chart's `emptyDir`; the controller mounts it
read-only. Observation requires the selected image, successful init completion
and the expected shared volume. Kubernetes' additional API-token mount is allowed;
mounts masking the profile source or destination are rejected.

Older completed Runs whose `controller-profiles` output is an executable data
bundle cannot be relabeled as images. Build a fresh focused `profiles` Run and
pass it with `nvke-demo run --profiles-run`, or build a fresh combined Run. The
other accepted outputs do not need rebuilding merely to replace this output.

## Explicit NVKE gaps and validation

The local cloud-init profile retains local-only APT installation and uplink
naming across reboot; these are declared local profile behavior, not fixes to
upstream NVKE. A first-class data-profile installer input and general offline
first-boot/network behavior remain NVKE-owned integration gaps.

[Factory tests](../pkg/nvkr/factory/nvke_laptop_profiles_test.go) authenticate the
additive overlay, compile the native image output and check staging/build inputs.
[Consumer tests](../internal/nvkedemo/site_test.go) check exact image selection,
init completion and shared mounts. The source-chart rendering test is opt-in and
skipped without its chart input. An isolated native ARM64 test built the pinned
Dockerfile with rootless BuildKit, copied its OCI layout through ORAS, read back
manifest `sha256:197fe058d0fbcff5d7841abfee58067aa05f7cdd21040642fe7e327096b574a0`
and fetched that manifest from a container on the Kind network. Running the
image's init copy command reproduced the staged profiles and runbooks exactly.
This tests image construction, transport and file copying, not a completed NVKR
Run or Kubernetes init container. Those integrated checks and a fresh tenant
deployment remain unverified; the older data-bundle recording does not prove them.

## Dual-platform fixture image

The [controller profiles Definition](../inputs/definitions/diagnostics/controller-profiles.yaml)
packages the pinned NVKE test fixtures for both Linux AMD64 and ARM64,
with native image members collected into one OCI index. The
[deploy Definition](examples/nvke-deploy/definition.yaml) selects this image
alongside the controller and charts. It preserves the upstream fixture profiles,
including placeholder deployment URLs; it is not a fleet profile configuration.
The local Ubuntu 26.04 diagnostic profile remains ARM64-only.

[Image producer contracts](../pkg/nvkr/factory/nvke_profiles_image_test.go)
compile both native fixture image producers and bind the exact source patches.
These tests establish catalog and packaging declarations, not a completed NVKR
build, deployed chart, boot, or offline-install acceptance.
