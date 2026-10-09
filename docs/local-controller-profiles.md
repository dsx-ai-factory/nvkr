# Build the local controller profiles image

The `profiles` lane builds the small ARM64 OCI image that NKE's controller
uses for node configuration. It does not build the QCOW2 or start a cluster.
For the complete build-and-deploy sequence, follow the
[local demo guide](local-nke-demo.md).

## Build just this artifact

Inside Colima, from the native source export prepared in
[step 2 of the local demo guide](local-nke-demo.md#2-copy-committed-nkd-source-into-native-vm-storage):

```sh
CONTAINER_RUNTIME=docker \
  bash scripts/nke-full-arm64-local.sh --check profiles
CONTAINER_RUNTIME=docker \
  bash scripts/nke-full-arm64-local.sh my-profiles-01 1 profiles
```

Use a fresh attempt name. Native Linux ARM64 uses the same commands.
The Mac shared mount failed source-snapshot copying during the fresh profile
build; use native ext4 storage as the full demo guide specifies.
If remote Git access is unavailable inside Colima, supply
`NKE_LOCAL_CLONE=/absolute/path/to/nke`; only the pinned committed objects are used.

## What is built

The [Definition](../definitions/nke-local-dev-arm64/definition-profiles.json)
selects one output, `controller-profiles`.
Its [catalog](../definitions/nke-local-dev-arm64/profiles-catalog/registry.yaml)
now has a short source-owned build command, not an embedded template generator:

```yaml
patches:
  - name: patches/local-profile.patch
    sha256: sha256:4a36de3fe026f3e8b1841c49ef4590dcc5fa65c1f4d463769c32d84b05455171
buildCommand: [sh, controller/test/fixtures/nkd-local/build.sh, '{{.Arch}}']
```

NKD verifies the [patch](../definitions/nke-local-dev-arm64/profiles-catalog/patches/local-profile.patch)
before applying it to a private source snapshot. The patch and prepared source
enter build identity. The developer's NKE checkout is not modified.
The [source lock](../definitions/nke-local-dev-arm64/profiles-catalog/pins.lock.yaml)
still selects `8fb2d53368823bd386b4fbd55b4ab6d3c0931345`, the site/installer
candidate, not an approved release.

The reviewed patch makes the local differences explicit:

- Freeze the Alpine base image by digest.
- Retain the source's two VM config profiles and nine runbooks unchanged.
- Supply one plain Ubuntu 26.04 ARM64 profile and its local QCOW2 URL.
- Remove the fixture's HTTP agent override and disable first-boot package refresh.
- Install from the baked file-based APT repository only; missing packages stop boot setup.
- Preserve the single observed physical uplink across the worker's reboot.

The private build context is written only under `bin`. BuildKit exports
`bin/nke-local-profiles-arm64.oci.tar`; Dockerfile steps use network mode
`none`. This is diagnostic execution, not qualified or completely hermetic
production.

## How NKE consumes it

The controller chart copies `/profiles` from this image into its profiles volume.
The deploy-only consumer imports the exact completed Run output and supplies
its image reference as `images.profiles`. It does not edit or rebuild the image.

The local profile points to
`http://172.30.0.1:18080/nke-node-image-arm64.qcow2`.
The consumer serves the held QCOW2 there, verifies its bytes and binds each real
guest to that backing disk. The EFI URL is only NICoSim's parser placeholder;
no bare-metal EFI artifact or FDE support is supplied.

## Validation limits

Tests apply the actual authenticated patch using Git. They cover local-only APT,
install-error propagation, both admitted uplink names, idempotence, foreign paths
and tampered patch rejection. NKE's own metadata/template helpers validate the
produced content through
[the downstream contract test](../scripts/testdata/nke-local-profile/contract_test.go).

The retained Colima demo passed the real tenant/workload and worker-reboot checks.
The simplified producer retains identical Dockerfile, cloud-init, iPXE and
metadata bytes. That content comparison is not a fresh end-to-end acceptance run.
Full-source OS integration, the missing DSX material and full OS smoke remain
[separate gaps](full-nke-source-build.md).
