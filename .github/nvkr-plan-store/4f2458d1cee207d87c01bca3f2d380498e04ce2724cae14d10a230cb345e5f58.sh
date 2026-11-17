mkdir -p /src/nvkr-output/work /src/nvkr-output/calico
cp -a os-image/packages/cni-common os-image/packages/cni-calico \
  /src/nvkr-output/work/
CNI=cni-calico PRODUCT=Calico CONTENT_KIND=manifests \
  CONTENT_SPEC=/src/nvkr-output/work/cni-calico/packaging/nfpm-manifests.yaml \
  NFPM_BIN=/usr/local/bin/nfpm ARCH=amd64 DEB_REVISION=1 \
  /src/nvkr-output/work/cni-common/build-deb.sh v3.30.1 \
  /src/os-image/packages/cni-calico/dist/v3.30.1 \
  /src/nvkr-output/calico
packages=("/src/nvkr-output/calico"/*.deb)
test "${#packages[@]}" -eq 2
test -f "/src/nvkr-output/calico/nvke-cni-calico-images-3.30.1_1_amd64.deb"
test "$(dpkg-deb --field "/src/nvkr-output/calico/nvke-cni-calico-images-3.30.1_1_amd64.deb" Package)" = "nvke-cni-calico-images-3.30.1"
test "$(dpkg-deb --field "/src/nvkr-output/calico/nvke-cni-calico-images-3.30.1_1_amd64.deb" Architecture)" = "amd64"
test -f "/src/nvkr-output/calico/nvke-cni-calico-manifests-3.30.1_1_amd64.deb"
test "$(dpkg-deb --field "/src/nvkr-output/calico/nvke-cni-calico-manifests-3.30.1_1_amd64.deb" Package)" = "nvke-cni-calico-manifests-3.30.1"
test "$(dpkg-deb --field "/src/nvkr-output/calico/nvke-cni-calico-manifests-3.30.1_1_amd64.deb" Architecture)" = "amd64"
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C /src/nvkr-output/calico \
  -cf /src/nvkr-output/nvke-cni-calico.tar .
