mkdir -p /src/nvkr-output/work/os-image/packages /src/nvkr-output/spire-agent
cp -a os-image/packages/spire-agent /src/nvkr-output/work/os-image/packages/
cp -a os-image/files /src/nvkr-output/work/os-image/
SPIRE_VERSION=1.14.1 TPM_PLUGIN_VERSION=1.9.0 DEB_REVISION=4 \
  NFPM_BIN=/usr/local/bin/nfpm \
  /src/nvkr-output/work/os-image/packages/spire-agent/packaging/build-deb.sh \
  amd64 /src/os-image/packages/spire-agent/dist/1.14.1+tpm1.9.0/amd64 \
  /src/nvkr-output/spire-agent
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C /src/nvkr-output/spire-agent \
  -cf /src/nvkr-output/nvke-spire-agent.tar .
