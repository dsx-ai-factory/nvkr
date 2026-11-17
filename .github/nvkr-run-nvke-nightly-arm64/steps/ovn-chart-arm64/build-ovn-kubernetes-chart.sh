readonly chart=/src/charts/ovn-kubernetes
readonly out=/src/nvkr-output/ovn-kubernetes
readonly stream="${NVKR_SOURCE_STREAM:?}"
[[ "$stream" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]]
test -f "$chart/Chart.yaml"
grep -Fqx "version: $stream" "$chart/Chart.yaml"
mkdir -p "$out"
cp -a "$chart/." "$out/"
# The pinned chart aliases upstream's values filename onto values.yaml.
test "$(readlink "$out/values-single-node-zone.yaml")" = values.yaml
rm "$out/values-single-node-zone.yaml"
test ! -e "$out/values-single-node-zone.yaml"
tar --format=ustar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 \
  --numeric-owner -C /src/nvkr-output -cf /src/nvkr-output/ovn-kubernetes-chart.tar ovn-kubernetes
