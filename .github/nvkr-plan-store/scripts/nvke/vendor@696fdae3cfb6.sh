make -C os-image/packages/spire-agent bundle nfpm \
  ARCH={{.Arch}} BIN_DIR=/usr/local/bin NFPM_BIN=/usr/local/bin/nfpm
