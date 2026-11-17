readonly stream=1.4.0
readonly revision=1+git.d6a3812d1bc1
readonly out=/src/nvkr-output
readonly charts="$out/charts"
readonly images="$out/images"
readonly image_dir="$images/usr/lib/nke/cni-ovn/$stream"
readonly image_name="ovn-images_${stream}_linux_arm64.tgz"
mkdir -p "$charts/DEBIAN" "$charts/etc/dgx/ovn-kubernetes" "$images/DEBIAN" "$image_dir" "$out/debs"
tar -xf /nvkr/inputs/materials/ovn-chart -C "$charts/etc/dgx"
test -f "$charts/etc/dgx/ovn-kubernetes/Chart.yaml"
grep -qx "version: $stream" "$charts/etc/dgx/ovn-kubernetes/Chart.yaml"
# Embedded chart selects the exact source-built OCI archive name.
# The standalone Helm artifact instead binds this image by digest.
cat >"$charts/etc/dgx/ovn-kubernetes/values-nke.yaml" <<'EOF'
skipCallToK8s: true
global:
  enableOvnKubeIdentity: false
  image:
    repository: localhost/nvke/ovn-kubernetes
    tag: git-d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d
    pullPolicy: IfNotPresent
EOF
for kind in charts images; do
  root="$out/$kind"
  cat >"$root/DEBIAN/control" <<EOF
Package: nke-cni-ovn-$kind-$stream
Version: $revision
Architecture: arm64
Maintainer: NKE <nke-dev@exchange.nvidia.com>
Provides: nke-cni-ovn-$kind
Conflicts: nke-cni-ovn-$kind
Description: NVKE OVN $kind from pinned upstream source
EOF
done
printf 'Depends: containerd | nke-kubeadm-distro\n' >>"$images/DEBIAN/control"
gzip -n -9 -c /nvkr/inputs/materials/ovn-image >"$image_dir/$image_name"
(cd "$image_dir" && sha256sum "$image_name" >"$image_name.sha256")
# Preserve the offline containerd import contract and fail configuration
# on checksum, readiness or import errors.
cat >"$images/DEBIAN/postinst" <<'EOF'
#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
[ "${1:-}" = configure ] || exit 0
readonly image_dir=/usr/lib/nke/cni-ovn/1.4.0
readonly image_name=ovn-images_1.4.0_linux_arm64.tgz
if [ -n "${NKE_CTR_BIN:-}" ]; then
  ctr="$(command -v "$NKE_CTR_BIN")" || { echo "OVN import: ctr is not executable: $NKE_CTR_BIN" >&2; exit 1; }
elif [ -x /usr/local/bin/ctr ]; then
  ctr=/usr/local/bin/ctr
else
  ctr="$(command -v ctr)" || { echo 'OVN import: ctr not found' >&2; exit 1; }
fi
[ -f "$ctr" ] && [ -x "$ctr" ] || { echo "OVN import: ctr is not executable: $ctr" >&2; exit 1; }
readonly ctr
readonly log=/var/log/nke-cni-ovn-postinst.log
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
    "$out/debs/nke-cni-ovn-${kind}-${stream}_${revision}_arm64.deb"
done
tar --format=ustar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C "$out/debs" -cf "$out/nvke-cni-ovn.tar" .
