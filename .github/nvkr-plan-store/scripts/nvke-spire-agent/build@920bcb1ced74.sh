set -o pipefail
mkdir -p /src/nvkr-output/work/os-image/packages /src/nvkr-output/spire-agent
cp -a os-image/packages/spire-agent /src/nvkr-output/work/os-image/packages/
cp -a os-image/files /src/nvkr-output/work/os-image/
cp -a make /src/nvkr-output/work/
# The runtime authenticates this exact named producer material before
# staging it read-only and rechecks consumption after the command.
export IMDS_AGENT_FILE=/nvkr/inputs/materials/imds-agent
export IMDS_AGENT_ARCH={{.Arch}}
test -f "$IMDS_AGENT_FILE"
test ! -L "$IMDS_AGENT_FILE"
IMDS_AGENT_SHA256="$(sha256sum "$IMDS_AGENT_FILE")"
export IMDS_AGENT_SHA256="${IMDS_AGENT_SHA256%% *}"
# Reuse the verified vendor cache offline, staging IMDS only at build time.
make -C /src/nvkr-output/work/os-image/packages/spire-agent bundle \
  ARCH={{.Arch}} SPIRE_VERSION=1.14.1 TPM_PLUGIN_VERSION=1.9.0 \
  BIN_DIR=/usr/local/bin NFPM_BIN=/usr/local/bin/nfpm
SPIRE_VERSION=1.14.1 TPM_PLUGIN_VERSION=1.9.0 DEB_REVISION=5 \
  NFPM_BIN=/usr/local/bin/nfpm \
  /src/nvkr-output/work/os-image/packages/spire-agent/packaging/build-deb.sh \
  {{.Arch}} /src/nvkr-output/work/os-image/packages/spire-agent/dist/1.14.1+tpm1.9.0/{{.Arch}} \
  /src/nvkr-output/spire-agent
packages=(/src/nvkr-output/spire-agent/*.deb)
test "${#packages[@]}" -eq 1
# Fail closed if an older package recipe ignores the optional input.
dpkg-deb --fsys-tarfile "${packages[0]}" | tar -xOf - \
  ./usr/local/lib/spire-agent/dsx-imds-node-attestor-agent \
  > /src/nvkr-output/packaged-imds-agent
printf '%s  %s\n' "$IMDS_AGENT_SHA256" /src/nvkr-output/packaged-imds-agent | sha256sum -c -
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C /src/nvkr-output/spire-agent \
  -cf /src/nvkr-output/nvke-spire-agent.tar .
