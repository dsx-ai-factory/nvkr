readonly chart=/src/helm/ovn-kubernetes
readonly out=/src/nvkr-output/ovn-kubernetes
test -f "$chart/Chart.yaml"
grep -qx 'version: 1.4.0' "$chart/Chart.yaml"
test ! -e "$chart/values.yaml"
mkdir -p "$out"
cp -a "$chart/." "$out/"
cp "$chart/../../LICENSE" "$out/LICENSE"
# Preserve the source-owned template: getImage uses repository:tag.
# A fixed repository@sha256 plus the authenticated hex slot gives an
# exact digest reference without changing upstream Helm templates.
test "$(grep -c '^    repository: ghcr.io/ovn-kubernetes/ovn-kubernetes/ovn-kube-ubuntu$' "$chart/values-single-node-zone.yaml")" = 1
test "$(grep -c '^    tag: master$' "$chart/values-single-node-zone.yaml")" = 1
sed -e 's|^    repository: ghcr.io/ovn-kubernetes/ovn-kubernetes/ovn-kube-ubuntu$|    repository: localhost/nvke/ovn-kubernetes@sha256|' \
  -e 's|^    tag: master$|    tag: ""|' \
  -e 's|^  enableOvnKubeIdentity: true$|  enableOvnKubeIdentity: false|' \
  -e '/^tags:$/a\
  ovnkube-control-plane: true\
  ovnkube-single-node-zone: true\
  ovnkube-identity: false\
  ovs-node: false' \
  "$chart/values-single-node-zone.yaml" >"$out/values.yaml"
tar --format=ustar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C /src/nvkr-output -cf /src/nvkr-output/ovn-kubernetes-chart.tar ovn-kubernetes
