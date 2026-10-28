# Acquired native Mac tools

This focused Definition acquires the selected Darwin ARM64 `clusterctl`, Helm, kubectl, ORAS, Packer, its QEMU plugin and vmnet executables. It references the [shared native catalog](../../../catalogs/tools/native/README.md); there is no duplicate registry or lock.

For the complete source-built host bundle and Linux site companions, use `nvke-demo tools` with the [native Mac selection](../native-mac/README.md). Output verification and private extraction remain owned by NVKR. No system installation or sudoers changes occur. QEMU, firmware and ISO tooling remain separate prerequisites.

## Prepare installed Packer prerequisites

From a clean repository checkout, select the Go version in `.go-version` on
`PATH` and start Docker. The following existing commands acquire the catalog's
exact Darwin Packer and QEMU-plugin bytes. Choose fresh attempt and destination
names if any already exist:

```sh
GOTOOLCHAIN=local make build
bin/nvkr run local \
  --definition inputs/definitions/demo/native-mac-tools/definition.json \
  --environment inputs/environments/local-native-mac.json \
  --attempt mac-prerequisites-001 --jobs 2 --runtime docker --ui plain

bin/nvkr publish root \
  --bundle native-tools-darwin-arm64=.nvkr/runs/mac-prerequisites-001/packages/native-tools-darwin-arm64 \
  --out .nvkr/mac-prerequisites-001-publication
bin/nvkr publish extract \
  --layout .nvkr/mac-prerequisites-001-publication \
  --bundle native-tools-darwin-arm64 \
  --out .nvkr/mac-prerequisites-001-bin

export PATH="$PWD/.nvkr/mac-prerequisites-001-bin:$PATH"
packer plugins install \
  --path "$PWD/.nvkr/mac-prerequisites-001-bin/packer-plugin-qemu" \
  github.com/hashicorp/qemu
packer version
packer plugins installed
```

Packer's local `--path` installation places the verified plugin in its normal
user plugin directory and writes the sibling checksum file. It does not download
the plugin. Keep the extracted Packer directory on `PATH` for the subsequent
[normal demo invocation](../../../../docs/local-nvke-demo.md#native-linux-and-mac-path).
This acquisition Run is a prerequisite; it is separate from the fresh product
and host-tool Runs created by the demo. Ordinary `nvke-demo tools` does not select
Packer or this plugin.

These commands are source-verified; this complete setup sequence has not yet
been exercised as a clean customer setup. Catalog pins verify archive and
executable integrity. They do not establish publisher authentication or
vulnerability acceptance. The [Packer toolchain publication gates](../../../../toolchains/nvkr-packer/README.md)
remain unchanged; local acquisition is not redistribution approval.
