# NVSentinel catalog candidate

This catalog starts the native package route with the real NVSentinel platform-connectors component. Ubuntu 24.04 and 26.04 each have explicit `linux/amd64` and `linux/arm64` routes from the same source and packaging patch. Separate Definitions and Environments select each distribution and native architecture.

The candidate source is `NVIDIA/NVSentinel` at `c1593477357299ab93dd5246f5b2cd236f8f07c8`. This is a development input, not an approved release. The source lock must be generated through NVKR:

```sh
bin/nvkr catalog pin --dir definitions/nvsentinel/catalog \
  --commit c1593477357299ab93dd5246f5b2cd236f8f07c8 --timeout 5m nvsentinel
for target in ubuntu-24.04-amd64 ubuntu-24.04-arm64 ubuntu-26.04-amd64 ubuntu-26.04-arm64; do
  bin/nvkr run plan --definition "definitions/nvsentinel/debian-$target.yaml" \
    --environment "definitions/nvsentinel/environment-$target.yaml" --json
done
```

The four measured local repository digest references select the Ubuntu toolchains built from the exact native Ubuntu base and official Go SDK manifests in `toolchains/nvsentinel-debian/toolchain.lock.json`. Go 1.27.0 comes from the pinned NVSentinel `.versions.yaml`. The SDK is copied into the selected Ubuntu base; dpkg and system tools come from Ubuntu. The toolchain build checks the exact Ubuntu version, architecture, Go version and required tools with networking disabled. ARM64 image probes run on ARM64; historical AMD64 image probes on an ARM64 host used emulation, which does not establish native AMD64 package acceptance. These local repository digest references require the matching images on the execution daemon; they are not published registry manifests or portable hosted pins. See the toolchain README for reconstruction.

Fetch-time vendoring uses the existing authenticated source-closure owner. The producer builds offline from that prepared closure and creates the final archive with Ubuntu's dpkg-deb. Its exact route argv checks the expected Ubuntu distribution before Go or package output work. Ubuntu distribution, package Version and package Maintainer are independent inputs; no package version or contact is inferred from an OS release.

The digest-pinned patch adds the packaging recipe, a systemd unit without start/enable maintainer scripts and native control
files for both architectures. It does not modify the inspected upstream checkout. The human selected
`Maintainer: Ilya Kislenko <ikislenko@nvidia.com>` and `Version: 0~git20261013.c159347735` for this local
development package. These are local packaging metadata, not an upstream release or maintainer claim.
The Version is an explicit candidate policy and stays distinct from the Ubuntu release and source-tag
metadata. Editing the recipe or controls requires updating the catalog patch digest; prepared-source and
Build identities change with those bytes. The `.in` files remain examples for other package policies.

The proposed package stages the platform-connectors executable and its unit. It supplies no credentials, node identity or runtime configuration, and has no maintainer scripts that start or enable the service. The proposed unit needs operator-provisioned `/etc/nvsentinel/node.env`, component configuration and an authenticated API kubeconfig before starting. This does not prove readiness, healthy-node behavior, least privilege or upgrades. The installation/removal checks below have narrower scope. Preserve node-local socket authentication and the existing datastore/API contracts. The candidate
unit retains `WantedBy=multi-user.target` and has no package-owned disable preset or baseline sandbox.
A later blanket systemd preset can enable it once its configuration conditions are met. First-boot preset,
least-privilege and live-service validation remain open; the installation checks establish only the
observed dpkg-install behavior.

This first component is not the complete node bundle: metadata collector, syslog and GPU monitor still need their native tool/library and Python/DCGM dependency contracts. Source `make lint-test-all`, native AMD64 execution, live service behavior and remaining component runs remain acceptance gates. Host-service chart/RBAC migration, authenticated OS-recipe materials and ABI binding, exact-byte OCI/APT publication and service-owned signing remain unfinished.

The local standalone scheduler now connects the original catalog producer to retained-content inspection,
terminal evidence and transactional package/evidence retention. Both architecture Environments select measured,
digest-pinned inspection images and matching catalog-pinned Syft scanners. Their private-volume profile bounds extracted content to 256 MiB and
requires 12 GiB memory and 8 GiB scratch, independently admitted before production. Historical AMD64 image probes on
an ARM64 host used emulation and do not establish native AMD64 package execution. All four
explicitly select Docker; local control fields are supplied. Plans remain distinct from execution.

Historical developer-local observations (the original standalone payloads and staged copies are no longer retained):

the catalog/Definition Runs `ubuntu24-arm64-local6` and `ubuntu26-arm64-local1` completed on native Linux
ARM64 for Ubuntu 24.04 and 26.04 respectively. Each retained its source-created package (33,924,156 bytes,
SHA-256 `b2525c4c0ddd895cbb0a0acf1a153e1a7ff90838b79a894239a6f2770d86e8b2`), a pinned-reader/Syft
CycloneDX content document containing 103 components, and unsigned local provenance. Their producer
Build identities differ by target; the observed equality of these two package payloads is not a qualified
reproduction or general ABI claim. Independent Ubuntu dpkg-deb read-back confirmed the exact package
hash/length and approved Package/Version/Architecture/Maintainer fields. The control archive contains
only `control`, without maintainer scripts.

Independent native containers for both Ubuntu releases installed the exact retained package, ran the installed
binary's help command, checked the unit was present but not enabled, removed the package, and checked its
binary and unit were absent. Runtime dependencies came from the signed pinned Ubuntu snapshot. Ubuntu
26.04's APT bootstrap required an explicit path to the copied trusted CA bundle; TLS and archive signature
verification stayed enabled. These checks started no NVSentinel service and prove no API/GPU operation,
healthy-node behavior or upgrade. This is an unqualified local platform-connectors package, without a signed
release or full node bundle. The separate OS recipe run below records its narrower image result;
native AMD64 execution remains unproved. These standalone payloads and their staged copies were removed
during the authorized local cleanup; their earlier records describe historical evidence.

On a matching native Linux ARM64 caller with the local pinned toolchains and inspector available, execute
through NVKR (use a new attempt name):

```sh
bin/nvkr run local --definition definitions/nvsentinel/debian-ubuntu-24.04-arm64.yaml \
  --environment definitions/nvsentinel/environment-ubuntu-24.04-arm64.yaml \
  --attempt ubuntu24-arm64-example --runtime docker --timeout 30m
```

The default attempt root is `.nvkr/runs`. A complete Run retains `outputs/platform-connectors.deb`,
`outputs/platform-connectors.cdx.json` and `outputs/platform-connectors.production-predicate.json` beside
its lifecycle record. The fixed image references are local assembly results, without registry publication.

## Place the DEB in existing OS inputs

The four `nvsentinel-platform-connectors-os-ubuntu2404-{amd64,arm64}` and
`nvsentinel-platform-connectors-os-ubuntu2604-{amd64,arm64}` components use the existing
`host-package` material route used by the NVKE agent. Each invokes the same pinned
NVSentinel Debian recipe with its explicit Ubuntu target and toolchain. Select the
matching component as the existing `nvke-image-inputs` producer's `nvsentinel`
material; it supplies the raw `.deb` through the existing held material transfer.
No new scheduler, disk lifecycle or publication path is required. This is a separate ordinary
host-package build, not reuse of a standalone Debian Run or its receipt, SBOM or predicate.
The later installed-byte comparison proves only the recorded local bake's bytes.
An authenticated general consumer target/evidence join remains deferred.

[The source patch](nvke-os-package-input.patch), based on NVKE development commit
`115d07c8bf671f8e8f9c8e630fb02c8d0ffcfb17`, adds one optional composer argument:

```sh
--nvsentinel-package /nvkr/inputs/materials/nvsentinel
```

The existing composer copies those exact bytes into
`debs/nvsentinel-platform-connectors.deb`, checks architecture and applies its
existing package byte limits and local APT indexing. The existing guest provisioner
installs that file during the bake. This provisioner does not start or enable package-owned units.
The composer also includes the source-owned cloud-init network override and netplan
DHCP configuration required by that provisioner, preserving their bytes and modes.
Missing either configuration fails composition before replacing an existing archive.
The patch is not an adopted NVKE release or proof of a completed OS build.

The `os-ubuntu2404-{amd64,arm64}.yaml` and `os-ubuntu2604-{amd64,arm64}.yaml`
Definitions connect the matching host package to the existing `nvke-image-inputs`
material slot and composer argument, then select the existing non-FDE Packer
output. `os-catalog` retains the main NVKE OS suppliers and their pins; the
existing DSX OVN selections remain in the target overlays. Only the composer and
selected non-FDE root use the explicitly pinned NVKE development source
`115d07c8bf671f8e8f9c8e630fb02c8d0ffcfb17` and package intake patch. Their Packer
build name, material schema and output path match that source. The consumer
copies the raw package without rebuilding or repacking it.

The OS consumer and kubeadm supplier select the upstream OS download/kernel
recovery fix [NVKE #1588](https://github.com/dsx-ai-factory/nvke/pull/1588). The
intake patch applies to that source. Other supplier pins
and the NVSentinel source/package stay unchanged. This source update does not
change NVKR's separately locked Packer-toolchain Ubuntu snapshot or establish
that its download failures are resolved.

```sh
bin/nvkr run plan --definition definitions/nvsentinel/os-ubuntu2604-arm64.yaml \
  --environment definitions/nvsentinel/environment-os-arm64.yaml --json
```

All four OS selections compile. Local native ARM64 Run `ubuntu26-arm64-native6`
completed the Ubuntu 26.04 bake using the existing toolchain substitution overlays
for the measured local Packer and matching Ubuntu SDK images. It retained a
2,885,085,696-byte QCOW2 with SHA-256
`4600f8364d75f0869ea0c94e067a685f26067da99266346944576e0145aee807`;
its independent hash and length match the completed lifecycle's observed disk.
The Run's Plan digest is
`8bf4f5c0741835e5f7778076340d7a7886bc3bae8a9fd0f44bcca737d9dcb96e`.
Read-only libguestfs inspection and Debian's package reader independently confirmed
`nvsentinel-platform-connectors` version `0~git20261013.c159347735`, architecture
`arm64`, with status `install ok installed`. The installed binary and unit match
the fresh source DEB byte-for-byte. Both required network configurations match the
source/material bytes and modes, and the QCOW hash remained unchanged afterward.
This is unsigned, unqualified local execution; independent boot, service/API/GPU
behavior and upgrades remain unverified.

The Ubuntu 24.04 selections retain the baseline
NVKE-agent CGO builder from Ubuntu 26.04; compatibility of that separate agent
with the Ubuntu 24.04 guest is unresolved. Use a compatible existing package
builder before executing those two OS candidates. NVSentinel's independent
Ubuntu 24.04 package installation does not establish the agent's compatibility.
The AMD64 Environment requires a genuine native AMD64 host with KVM. The ARM64
Environment uses the existing native ARM64 TCG development route.

A fresh customer Run uses the selected source producer and existing material owner;
earlier staged files or serialized records do not restore producer authority.
Native AMD64 packages and the other OS target bakes remain unproved.

## Select the platform-connectors image

The separate `nvsentinel-platform-connectors-image` catalog component uses the same pinned source and
authenticated vendor closure. Its source patch selects ko, a native digest-pinned base and an offline OCI
export. It preserves the upstream Go image recipe; the supporting Dockerfile assembles tools and cached
inputs. The local ko candidate differs from the source-declared version for the security checks described
in [the toolchain README](../../toolchains/nvsentinel-ko/README.md). It is not an approved release toolchain.

```sh
bin/nvkr run plan --definition definitions/nvsentinel/image-arm64.yaml \
  --environment definitions/nvsentinel/environment-image-arm64.yaml --json
bin/nvkr run local --definition definitions/nvsentinel/image-arm64.yaml \
  --environment definitions/nvsentinel/environment-image-arm64.yaml \
  --attempt image-arm64-example --runtime docker --timeout 30m
```

The matching `image-amd64.yaml` and `environment-image-amd64.yaml` compile the AMD64 selection; no native
AMD64 execution is recorded. These inputs require the measured local toolchain on the execution daemon.
The native ARM64 catalog Run `image-arm64-local3` completed with image manifest
`sha256:a5f1e54737a74db6b78fab0ba4f2eda9ccec11ca777eb76d6e74d4afb22023b6` and a pinned Syft
CycloneDX document containing 103 components. Its source-created OCI archive is 20,183,040 bytes with
SHA-256 `5375bf3248ae4fc2a3e608c2c466ab6c2e54681fdf777c6e0b9abdc0fb5f1b70`; that archive checksum
is separate from the native image manifest identity. Image labels bind the pinned source revision and
explicit version; ko receives these through its supported image-label flags. Independent OCI import and the image's help command
passed on native ARM64 with networking disabled and default UID 65532. No service started, and this Run
establishes no API/GPU behavior, qualification, signature, publication or complete NVSentinel bundle.

## Select the image-bound Helm chart

`helm-arm64.yaml` and `helm-amd64.yaml` select the source `nvsentinel-chart` and its named
`platform-connectors-image` output. The existing Helm binder takes the observed image digest from that
same Run and writes only `platformConnector.image.digest` in the private chart tree. The source patch
adds this optional value to both platform-connectors workload templates; an empty value retains the
upstream tag behavior. No authentication, RBAC, deployment mode or service setting changes.

```sh
bin/nvkr run plan --definition definitions/nvsentinel/helm-arm64.yaml \
  --environment definitions/nvsentinel/environment-image-arm64.yaml --json
bin/nvkr run local --definition definitions/nvsentinel/helm-arm64.yaml \
  --environment definitions/nvsentinel/environment-image-arm64.yaml \
  --attempt helm-arm64-example --runtime docker --timeout 30m --jobs 2
```

Native ARM64 Run `helm-arm64-local1` completed with chart OCI manifest
`sha256:2e8a7ec444fd795d2b8cf516d4b38e1b7b3ad19aa2d48e2340e61a4099febec6`, a 31-component
source-chart inventory and the 103-component image inventory. Its final chart archive is 557,469 bytes,
SHA-256 `5c6c2acbb8071f73e61b2a3e2df95089a881e60f9949278cddbd02f442234d08`; chart transport,
archive and image identities remain distinct. Independent export through NVKR's existing chart reader
and rendering with Helm 4.3.0 confirmed the platform-connectors image digest
`sha256:a5f1e54737a74db6b78fab0ba4f2eda9ccec11ca777eb76d6e74d4afb22023b6`. The 398 bundled
subchart files stayed byte-identical to the pinned source. Rendering the source patch with the new value
empty also matched the original chart's default render byte-for-byte.

This Run builds the platform-connectors image and packages the full upstream chart. Other images remain
upstream declarations; it does not build or authenticate the complete deployed image closure. The preserved
default repository does not establish that the local image digest is published there. Consumers must select
their actual published repository separately. These are local unsigned, unqualified artifacts and read-only
rendering checks, without a Helm install, live service, host migration or acceptance with the source-declared
Helm 3.19.2. Native AMD64 remains unverified. The separate Ubuntu 26.04 ARM64 OS
Run above records package consumption through the existing image recipe; it does
not establish deployment of this chart.
