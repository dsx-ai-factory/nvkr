readonly chart=/src/helm/ovn-kubernetes
readonly out=/src/nvkr-output/ovn-kubernetes
test -f "${chart}/Chart.yaml"
grep -qx 'version: 1.4.0' "${chart}/Chart.yaml"
mkdir -p "${out}"
cp -a "${chart}/." "${out}/"
cat >"${out}/values-nke.yaml" <<'EOF'
tags:
  ovs-node: false
global:
  image:
    repository: ghcr.io/ovn-kubernetes/ovn-kubernetes/ovn-kube-ubuntu
    tag: release-1.4
EOF
find "${out}" -type d -exec chmod 0755 {} +
find "${out}" -type f -exec chmod 0644 {} +
find "${out}" -exec touch -d '2026-01-01 00:00:00Z' {} +
tar --sort=name --mtime='2026-01-01 00:00:00Z' \
  --owner=0 --group=0 --numeric-owner -C /src/nvkr-output \
  -cf /src/nvkr-output/ovn-kubernetes-chart.tar ovn-kubernetes
