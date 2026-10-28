# NVSentinel ko toolchain candidate

This supporting image supplies ko, the source-required Go 1.27.0 compiler and a complete native base-image
cache to the NVSentinel catalog. NVSentinel is built by ko through NVKR; the Dockerfile assembles tools
and cached inputs, and does not replace the component's Go image recipe. The image contains no registry
credentials or runtime socket. Both architectures are assembled locally; AMD64 assembly probes on the
recorded ARM64 machine used emulation.

NVSentinel's pinned `.versions.yaml` declares ko v0.18.0. That release's official ARM64 binary failed
the repository-pinned binary vulnerability check. This **local development candidate** instead selects
ko source commit `16c8737c7ae9c0d3c865f39d432663103c4d3cb3`, with the accompanying dependency patch
updating `golang.org/x/net` from v0.59.0 to v0.60.0. It is not an upstream release or an approved
organizational toolchain. NVSentinel's source version file is unchanged. The builder executable is
compiled with Go 1.27.2; the compiler used for NVSentinel remains Go 1.27.0.

Both compiled ko binaries passed `govulncheck -mode=binary` on October 14, 2026 with no affected symbols.
The historical scan transcript and its module-level advisory identity are not retained here, so that
advisory cannot be independently identified from this checkout. The no-affected-symbol observation is
not current security acceptance and does not establish security of NVSentinel; a fresh pinned scan and
retained advisory identifiers are required before security approval. `go mod verify` passed on the selected patched tool source.

The license report identifies Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause and MPL-2.0 dependencies.
The classifier did not identify three modules' short license notices; their exact source `LICENSE`
files explicitly name Apache-2.0. The two unmodified HashiCorp MPL-2.0 modules remain part of this
separate executable, with their notices and public source references retained. This does not claim
NVIDIA-wide legal approval or add these dependencies to NVKR's Go module.

## Reconstruct the local inputs

Use a private working directory, the exact Go 1.27.2 builder, the repository-pinned govulncheck tool and
an installed crane. The lock owns source, patch, executable and native base-image pins. Prepare the
context without changing upstream NVSentinel:

```sh
git clone https://github.com/ko-build/ko /path/to/ko-source
git -C /path/to/ko-source checkout --detach 16c8737c7ae9c0d3c865f39d432663103c4d3cb3
git -C /path/to/ko-source apply /path/to/nvkr/toolchains/nvsentinel-ko/dependency-security.patch
cd /path/to/ko-source
GOTOOLCHAIN=local go mod verify
for arch in amd64 arm64; do
  mkdir -p "/path/to/context/$arch"
  GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
    -mod=readonly -trimpath -buildvcs=false \
    -ldflags '-X github.com/google/ko/pkg/commands.Version=16c8737c7ae9-nvkr-local-net0.60.0' \
    -o "/path/to/context/$arch/ko" .
  /path/to/nvkr/bin/govulncheck -mode=binary "/path/to/context/$arch/ko"
done
crane pull --platform linux/amd64 --format=oci \
  cgr.dev/chainguard/static@sha256:a40219f3b0c2719e4e4220310e80a2230a4b95881644292c130e3a8e8287fc6c \
  /path/to/context/amd64/ko-cache/img
crane pull --platform linux/arm64 --format=oci \
  cgr.dev/chainguard/static@sha256:daed076c904e5dc26f5ff87d77aa4cea8b7ca76e906b561915dafe5b086377dc \
  /path/to/context/arm64/ko-cache/img
```

Verify executable hashes and every cached blob against the lock and its OCI descriptors before
assembly. Retain ko's license and dependency notices/source references under `context/licenses`;
the local assembly includes the classification report and the independently read short notices.
The native manifest descriptor must be present in the cache's root index, along with its complete
configuration and layer closure. A parent index alone cannot substitute for that cache shape.

```sh
docker buildx build --platform linux/amd64,linux/arm64 --network none --load \
  --file /path/to/nvkr/toolchains/nvsentinel-ko/Dockerfile \
  --tag nvkr-nvsentinel-ko:local-candidate /path/to/context
docker image inspect nvkr-nvsentinel-ko:local-candidate
```

The lock records the actual local repository digest of the assembled candidate. Reconstruction is not
a byte-reproduction claim: read back the resulting repository digest and update authored catalog
toolchain selection if it changes. No registry publication is performed by these commands.

## Offline operation

The source recipe copies the frozen cache into its private output directory, where ko can also write
its binary cache. It preserves NVKR's pinned GOOS/GOARCH and reproducibility flags; ko derives its
platform from those variables, and vendoring is appended to GOFLAGS. The selected configuration
contains only platform-connectors and linker stamps with a fixed source epoch. The source recipe emits
labels and annotations through ko’s supported command flags; per-build YAML fields cannot supply them.
It exports an OCI layout with `--push=false`; the declared archive contains only `oci-layout`,
`index.json` and `blobs`.

A generic tool fixture completed with a full pinned base cache under `network=none` and a read-only
root with private output. Removing one cached layer caused an explicit network-unreachable failure.
These are tool/cache checks, not NVSentinel execution evidence. Actual catalog Runs have their own
immutable lifecycle records. Local toolchain assembly, including emulated AMD64 probes, does not
prove native AMD64 NVSentinel execution, service readiness, signing or publication.
