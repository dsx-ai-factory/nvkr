mkdir -p /src/nvkr-output/work /src/nvkr-output/calico
cp -a os-image/packages/cni-common os-image/packages/cni-calico \
  /src/nvkr-output/work/
CNI=cni-calico PRODUCT=Calico CONTENT_KIND=manifests \
  CONTENT_SPEC=/src/nvkr-output/work/cni-calico/packaging/nfpm-manifests.yaml \
  NFPM_BIN=/usr/local/bin/nfpm ARCH={{.Arch}} DEB_REVISION=1 \
  /src/nvkr-output/work/cni-common/build-deb.sh v3.30.1 \
  /src/os-image/packages/cni-calico/dist/v3.30.1 \
  /src/nvkr-output/calico
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C /src/nvkr-output/calico \
  -cf /src/nvkr-output/nvke-cni-calico.tar .
