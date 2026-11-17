make -C os-image/packages/cni-calico bundle nfpm \
  ARCH=amd64 BIN_DIR=/usr/local/bin NFPM_BIN=/usr/local/bin/nfpm
