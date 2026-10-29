# NVSentinel Ubuntu package toolchains

This Dockerfile assembles Go 1.27.0 and Ubuntu's `dpkg-deb` for the NVSentinel platform-connectors
catalog. Its authored producer stages the DEB under the private output directory without adding
packaging files or scripts to upstream source. Ubuntu 24.04 and 26.04 each have AMD64 and ARM64
inputs in `toolchain.lock.json`. The Go version comes from the pinned upstream `.versions.yaml`;
distribution choices come from the customer targets. The official Go image donates the SDK, its
license and certificate bundle. No Debian runtime libraries or package manager are copied into
Ubuntu. This first component disables CGO and establishes no native library or GPU/DCGM compatibility.

Each route's `image` is an expected content-pinned local preparation reference under `localhost/`,
shared by the catalogs and Debian Environments. The four content pins are unchanged; the new lookup
names have not been exercised. `localImageID` retains historical native-platform configuration IDs,
which establish neither current availability nor byte-for-byte SDK reproduction.

To build an SDK, select the route's exact input values and a matching native host. This ARM64
Ubuntu 24.04 example creates an explicit local tag:

```sh
docker buildx build --load --network=none --platform linux/arm64 \
  --tag localhost/nvkr-nvsentinel-ubuntu-24.04-arm64:local \
  --build-arg TARGETARCH=arm64 --build-arg UBUNTU_VERSION=24.04 \
  --build-arg GO_IMAGE=docker.io/library/golang@sha256:6c83d163c89d1dbe66f8fc466870d66fe6325d15bb5ad1730cdc0b482fab1eec \
  --build-arg UBUNTU_IMAGE=docker.io/library/ubuntu@sha256:08571ca13e00ca07a2a84eab83a959b4242e22cceb16486a11bef1428c9e93a7 \
  --iidfile /tmp/nvkr-nvsentinel-ubuntu-24.04-arm64.iid \
  toolchains/nvsentinel-debian
```

Before a Run, verify that the exact locked reference resolves. A tag or image ID alone does not
establish that binding. BuildKit iidfiles and platform-less inspection can identify an OCI index
instead of the native configuration. After rebuilding, measure both the configuration and selected
manifest/index. If either differs, update the lock, catalog and Environment references and experiment
overlays together through their owners; a rebuild does not automatically reproduce the old digest.

The Dockerfile checks Ubuntu's exact version, selected architecture, Go version and required package
tools. Its final ambient environment contains only PATH. BuildKit acquires the pinned base inputs;
the Dockerfile's build commands have no network access or package installation. SDK and base-image
security acceptance remain separate from these assertions. No toolchain publication, production
signing or package execution is established here. Native AMD64 package execution remains unverified;
dated ARM64 observations used retired recipes and retain their scope in
[reconciliation](../../docs/adr/reconciliation.md#nvsentinel-catalog-and-package-inputs). The current
ordinary producer route retains only the raw package, without dependency inspection or installation proof.
