# Local controller profile data

Profiles are configuration, not a runnable container image. The `profiles`
lane selects `controller-profiles`: an ordinary NKD OCI bundle containing
the deterministic `nke-local-profiles` tarball. It needs neither a profiles
Dockerfile nor BuildKit. ORAS is supplied by the shared toolchain.

For the complete build-and-deploy sequence, follow the
[local demo guide](local-nke-demo.md). To build this output alone on Linux ARM64:

```sh
CONTAINER_RUNTIME=docker bash scripts/nke-full-arm64-local.sh --check profiles
CONTAINER_RUNTIME=docker bash scripts/nke-full-arm64-local.sh my-profiles-01 1 profiles
```

Use a fresh attempt name. As with the other lanes, NKD owns source admission,
artifact observation and OCI bundle packaging; direct `oras push` does not
replace those checks.

## Content and delivery

The [catalog](../definitions/nke-local-dev-arm64/profiles-catalog/registry.yaml)
selects pinned NKE config profiles and runbooks. Its authenticated
[local data overlay](../definitions/nke-local-dev-arm64/profiles-catalog/patches/local-profile.patch)
**only adds** a plain Ubuntu 26.04 ARM64 profile: cloud-init, metadata and iPXE
configuration. It changes no upstream NKE file and adds no build script.

The demo transfers the exact OCI bundle through ORAS, reads back its digest,
and uses `nkdctl publish root` / `publish extract` to verify and extract it.
ORAS rewrites the local index wrapper, so the importer retains the original
bounded bootstrap index bytes after matching the observed manifest. NKD then
independently verifies the imported blob closure and semantic binding.
The existing bounded archive reader validates the profile data before copying
it into the owned Kind node. The node archive is rehashed before extraction.

NKE's controller chart reads the prepared directory through `profiles.path`
and a read-only volume. `profiles.image.enabled=false`, so there is no
profiles-image initContainer. The pinned installer still requires an
`images.profiles` reference: the actual data bundle reference satisfies that
legacy configuration field, but is **not** run as an image.

## Explicit NKE gaps and validation

The local cloud-init profile retains local-only APT installation and uplink
naming across reboot; these are declared local profile behavior, not fixes to
upstream NKE. A first-class data-profile installer input and general offline
first-boot/network behavior remain NKE-owned integration gaps.

Factory tests authenticate the additive overlay, compile the tarball output
and exercise its local APT/uplink behavior. The source-owned pinned controller
chart was rendered with its NICo overlay and the data-volume override:
no profiles image is rendered. This proves chart compatibility, **not a fresh
end-to-end deployment**. Earlier image-based demo results do not validate
this changed data-artifact delivery path.
