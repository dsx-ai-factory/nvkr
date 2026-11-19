readonly stream=2.4.2
readonly revision=1+git.8af51273d52d
readonly out=/src/nvkr-output
readonly charts="$out/charts"
readonly images="$out/images"
readonly image_dir="$images/usr/lib/nvke/cni-ovn/$stream"
readonly image_name="ovn-images_${stream}_linux_amd64.tgz"
mkdir -p "$charts/DEBIAN" "$charts/etc/dgx/ovn-kubernetes" "$images/DEBIAN" "$image_dir" "$out/debs"
tar -xf /nvkr/inputs/materials/ovn-chart -C "$charts/etc/dgx"
test -f "$charts/etc/dgx/ovn-kubernetes/Chart.yaml"
grep -qx "version: $stream" "$charts/etc/dgx/ovn-kubernetes/Chart.yaml"
# Fork-specific keys: the old upstream global.image override does not apply.
cat >"$charts/etc/dgx/ovn-kubernetes/values-nke.yaml" <<'EOF'
global:
  skipCallToK8s: true
  imagePullSecretName: ""
  useImageDigest: false
ovnkube-control-plane:
  ovnkubeControlPlane:
    image: &built_image
      repository: localhost/nvke/ovn-kubernetes
      tag: git-8af51273d52dfa07454435d18d37230e582d797a
      sha256: ""
ovnkube-single-node-zone:
  ovnkubeSingleNodeZone:
    image: *built_image
  ovnkubeSingleNodeZoneNbdb:
    image: *built_image
  ovnkubeSingleNodeZoneSbdb:
    image: *built_image
  ovnkubeSingleNodeZoneNorthd:
    image: *built_image
  ovnkubeSingleNodeZoneOvnHost:
    image: *built_image
EOF
for kind in charts images; do
  root="$out/$kind"
  cat >"$root/DEBIAN/control" <<EOF
Package: nvke-cni-ovn-$kind-$stream
Version: $revision
Architecture: amd64
Maintainer: NVKE <nke-dev@exchange.nvidia.com>
Provides: nvke-cni-ovn-$kind, nke-cni-ovn-$kind, nke-cni-ovn-$kind-$stream
Conflicts: nvke-cni-ovn-$kind, nke-cni-ovn-$kind
Description: NVKE OVN $kind from pinned DSX source
EOF
done
printf 'Depends: containerd | nvke-kubeadm-distro | nke-kubeadm-distro\n' >>"$images/DEBIAN/control"
gzip -n -9 -c /nvkr/inputs/materials/ovn-image >"$image_dir/$image_name"
(cd "$image_dir" && sha256sum "$image_name" >"$image_name.sha256")
# Preserve the offline containerd import contract and fail configuration
# on checksum, readiness or import errors.
cat >"$images/DEBIAN/postinst" <<'EOF'
#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
[ "${1:-}" = configure ] || exit 0
readonly image_dir=/usr/lib/nvke/cni-ovn/2.4.2
readonly image_name=ovn-images_2.4.2_linux_amd64.tgz
if [ -n "${NKE_CTR_BIN:-}" ]; then
  ctr="$(command -v "$NKE_CTR_BIN")" || { echo "OVN import: ctr is not executable: $NKE_CTR_BIN" >&2; exit 1; }
elif [ -x /usr/local/bin/ctr ]; then
  ctr=/usr/local/bin/ctr
else
  ctr="$(command -v ctr)" || { echo 'OVN import: ctr not found' >&2; exit 1; }
fi
[ -f "$ctr" ] && [ -x "$ctr" ] || { echo "OVN import: ctr is not executable: $ctr" >&2; exit 1; }
readonly ctr
readonly log=/var/log/nvke-cni-ovn-postinst.log
cd "$image_dir"
sha256sum --strict -c "$image_name.sha256"
ready=false
for ((n=0; n<50; n++)); do
  if timeout --kill-after=1s 2s "$ctr" version >/dev/null 2>&1; then
    ready=true; break
  fi
  sleep 0.2
done
"$ready" || { echo 'OVN import: containerd readiness timed out' >&2; exit 1; }
gzip -dc "$image_name" | timeout --kill-after=5s 300s "$ctr" -n k8s.io images import - >>"$log" 2>&1 \
  || { echo "OVN import failed; see $log" >&2; exit 1; }
EOF
chmod 0755 "$images/DEBIAN/postinst"
for kind in charts images; do
  root="$out/$kind"
  find "$root" -print0 | xargs -0 touch -h -d "@$SOURCE_DATE_EPOCH"
  dpkg-deb --root-owner-group --uniform-compression -Zxz -z6 --build "$root" \
    "$out/debs/nvke-cni-ovn-${kind}-${stream}_${revision}_amd64.deb"
done
packages=("$out/debs"/*.deb)
test "${#packages[@]}" -eq 2
test -f "$out/debs/nvke-cni-ovn-images-${stream}_${revision}_amd64.deb"
test "$(dpkg-deb --field "$out/debs/nvke-cni-ovn-images-${stream}_${revision}_amd64.deb" Package)" = "nvke-cni-ovn-images-$stream"
test "$(dpkg-deb --field "$out/debs/nvke-cni-ovn-images-${stream}_${revision}_amd64.deb" Architecture)" = "amd64"
test "$(dpkg-deb --field "$out/debs/nvke-cni-ovn-images-${stream}_${revision}_amd64.deb" Provides)" = "nvke-cni-ovn-images, nke-cni-ovn-images, nke-cni-ovn-images-$stream"
test "$(dpkg-deb --field "$out/debs/nvke-cni-ovn-images-${stream}_${revision}_amd64.deb" Conflicts)" = "nvke-cni-ovn-images, nke-cni-ovn-images"
test -f "$out/debs/nvke-cni-ovn-charts-${stream}_${revision}_amd64.deb"
test "$(dpkg-deb --field "$out/debs/nvke-cni-ovn-charts-${stream}_${revision}_amd64.deb" Package)" = "nvke-cni-ovn-charts-$stream"
test "$(dpkg-deb --field "$out/debs/nvke-cni-ovn-charts-${stream}_${revision}_amd64.deb" Architecture)" = "amd64"
test "$(dpkg-deb --field "$out/debs/nvke-cni-ovn-charts-${stream}_${revision}_amd64.deb" Provides)" = "nvke-cni-ovn-charts, nke-cni-ovn-charts, nke-cni-ovn-charts-$stream"
test "$(dpkg-deb --field "$out/debs/nvke-cni-ovn-charts-${stream}_${revision}_amd64.deb" Conflicts)" = "nvke-cni-ovn-charts, nke-cni-ovn-charts"
tar --format=ustar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C "$out/debs" -cf "$out/nvke-cni-ovn.tar" .
