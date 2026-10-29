# NVSentinel Ubuntu package toolchains

These candidate images provide Go 1.27.0 and Ubuntu's dpkg-deb for the NVSentinel platform-connectors catalog. Its authored build command stages the DEB under the private output directory; it does not add packaging files or scripts to upstream source. Ubuntu 24.04 and 26.04 each have AMD64 and ARM64 inputs in `toolchain.lock.json`. The Go version comes from the pinned upstream `.versions.yaml`; distribution choices come from the customer targets. The official Go image donates the SDK, its license and certificate bundle. No Debian runtime libraries or package manager are copied into Ubuntu. This first component uses CGO disabled; this image does not establish native library or GPU/DCGM compatibility for other components.

Build each lock entry on the selected platform using its exact values:

```sh
docker buildx build --load --network=none --platform linux/arm64 \
  --build-arg TARGETARCH=arm64 --build-arg UBUNTU_VERSION=24.04 \
  --build-arg GO_IMAGE=docker.io/library/golang@sha256:6c83d163c89d1dbe66f8fc466870d66fe6325d15bb5ad1730cdc0b482fab1eec \
  --build-arg UBUNTU_IMAGE=docker.io/library/ubuntu@sha256:08571ca13e00ca07a2a84eab83a959b4242e22cceb16486a11bef1428c9e93a7 \
  --iidfile /tmp/nvkr-nvsentinel-ubuntu-24.04-arm64.iid \
  toolchains/nvsentinel-debian
```

The Dockerfile asserts Ubuntu's exact version, native image architecture, Go version and the required package tools. Its final ambient environment contains only PATH. BuildKit fetches exact input manifests; build commands have no network access and perform no package installation. SDK and base image security acceptance remain separate from these tool probes.

The recorded `localImageID` values are native configuration digests measured with `docker image inspect --platform <platform>`. BuildKit iidfiles and platform-less inspection can instead return an OCI index digest; those values cannot serve as raw configuration pins. The catalog selects the actual local repository digest reported by the daemon. The lock now records each catalog-selected repository digest separately as `localRepositoryDigest`. These are historical local-daemon names, including Docker-normalized `docker.io/library` names for unpublished images; they do not claim Docker Hub availability. Their continued presence in a customer daemon must be checked before a Run. A native configuration digest alone may not be a lookup reference. A rebuild must measure both native configuration identity and the selected repository manifest, then update the authored catalog and Environment inputs. These IDs are neither immutable published repository manifests nor a byte-for-byte image reproduction claim. No toolchain publication, production signing or package execution is established here. Native AMD64 package execution remains unverified; dated ARM64 observations used retired recipes and are recorded in [reconciliation](../../docs/adr/reconciliation.md#nvsentinel-catalog-and-package-inputs). The current ordinary producer route retains only the raw package and does not inspect its dependencies or establish installation.
