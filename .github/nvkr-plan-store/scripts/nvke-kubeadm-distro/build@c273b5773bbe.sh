mkdir -p /src/nvkr-output/work /src/nvkr-output/kubeadm-distro
cp -a os-image/packages/kubeadm-distro /src/nvkr-output/work/
for version in v1.36.3 v1.35.3 v1.34.2; do
  PEER_VERSIONS='v1.36.3 v1.35.3 v1.34.2' DEB_REVISION=1 \
    NFPM_BIN=/usr/local/bin/nfpm ARCH=amd64 \
    /src/nvkr-output/work/kubeadm-distro/packaging/build-deb.sh \
    "$version" "/src/os-image/packages/kubeadm-distro/dist/$version" \
    /src/nvkr-output/kubeadm-distro
done
packages=("/src/nvkr-output/kubeadm-distro"/*.deb)
test "${#packages[@]}" -eq 3
test -f "/src/nvkr-output/kubeadm-distro/nvke-kubeadm-distro-1.36.3_1_amd64.deb"
test "$(dpkg-deb --field "/src/nvkr-output/kubeadm-distro/nvke-kubeadm-distro-1.36.3_1_amd64.deb" Package)" = "nvke-kubeadm-distro-1.36.3"
test "$(dpkg-deb --field "/src/nvkr-output/kubeadm-distro/nvke-kubeadm-distro-1.36.3_1_amd64.deb" Architecture)" = "amd64"
test -f "/src/nvkr-output/kubeadm-distro/nvke-kubeadm-distro-1.35.3_1_amd64.deb"
test "$(dpkg-deb --field "/src/nvkr-output/kubeadm-distro/nvke-kubeadm-distro-1.35.3_1_amd64.deb" Package)" = "nvke-kubeadm-distro-1.35.3"
test "$(dpkg-deb --field "/src/nvkr-output/kubeadm-distro/nvke-kubeadm-distro-1.35.3_1_amd64.deb" Architecture)" = "amd64"
test -f "/src/nvkr-output/kubeadm-distro/nvke-kubeadm-distro-1.34.2_1_amd64.deb"
test "$(dpkg-deb --field "/src/nvkr-output/kubeadm-distro/nvke-kubeadm-distro-1.34.2_1_amd64.deb" Package)" = "nvke-kubeadm-distro-1.34.2"
test "$(dpkg-deb --field "/src/nvkr-output/kubeadm-distro/nvke-kubeadm-distro-1.34.2_1_amd64.deb" Architecture)" = "amd64"
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C /src/nvkr-output/kubeadm-distro \
  -cf /src/nvkr-output/nvke-kubeadm-distro.tar .
