readonly stage=/src/nvkr-output/stage
readonly work=/src/nvkr-output/work
mkdir -p "${stage}/charts" "${work}" /src/nvkr-output/ovn
cp -a os-image/packages/cni-common os-image/packages/cni-ovn \
  "${work}/"
tar -xf /nvkr/inputs/materials/ovn-chart -C "${stage}/charts"
gzip -n -9 -c /nvkr/inputs/materials/ovn-image >"${stage}/images.tgz"
printf '%s  ovn-images_1.4.0_linux_{{.Arch}}.tgz\n' \
  "$(sha256sum "${stage}/images.tgz" | cut -d' ' -f1)" \
  >"${stage}/images.tgz.sha256"
CNI=cni-ovn PRODUCT=OVN CONTENT_KIND=charts \
  CONTENT_SPEC="${work}/cni-ovn/packaging/nfpm-charts.yaml" \
  NFPM_BIN=/usr/local/bin/nfpm ARCH={{.Arch}} DEB_REVISION=1 \
  "${work}/cni-common/build-deb.sh" 1.4.0 "${stage}" \
  /src/nvkr-output/ovn
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C /src/nvkr-output/ovn \
  -cf /src/nvkr-output/nvke-cni-ovn.tar .
