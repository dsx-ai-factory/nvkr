CNI_DOCKERHUB_MIRROR=mirror.gcr.io make -C os-image/packages/cni-calico bundle nfpm \
  ARCH={{.Arch}} BIN_DIR=/usr/local/bin NFPM_BIN=/usr/local/bin/nfpm
