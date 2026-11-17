target_arch={{.Arch}}
helm_digest() {
  case "$1" in
    amd64) printf '%s' c306b46f719b0a4da32d0f78ee21bf90ce8d602f15b22ab753f0674d1670a7f3 ;;
    arm64) printf '%s' 564de2191b881e9f71b5606b25345821ea1682f06ab90499d3ab22b530176da1 ;;
    *) echo 'Unsupported Helm architecture' >&2; exit 1 ;;
  esac
}
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) echo 'Unsupported Helm execution architecture' >&2; exit 1 ;;
esac
digest=$(helm_digest "$arch")
curl --fail --location --max-time 120 --output /tmp/helm.tar.gz "https://get.helm.sh/helm-v4.2.4-linux-$arch.tar.gz"
printf '%s  %s\n' "$digest" /tmp/helm.tar.gz | sha256sum -c -
tar --no-same-owner -xzf /tmp/helm.tar.gz -C /tmp "linux-$arch/helm"
# The vendor sandbox's /tmp is noexec; only the held source volume executes.
host_tools=$(mktemp -d .nvkr-helm-host.XXXXXX)
trap 'rm -rf -- "$host_tools"' EXIT
cp "/tmp/linux-$arch/helm" "$host_tools/helm"
export HELM_REPOSITORY_CONFIG=/tmp/nktp-repositories.yaml
export HELM_REPOSITORY_CACHE=/tmp/nktp-cache
# These are the three exact URLs in the selected source's Chart.yaml.
# Helm, not a custom parser, validates Chart.yaml against Chart.lock.
pins="$(sha256sum nktp/Chart.yaml nktp/Chart.lock)"
"$host_tools/helm" repo add nktp-otel https://open-telemetry.github.io/opentelemetry-helm-charts
"$host_tools/helm" repo add nktp-prometheus https://prometheus-community.github.io/helm-charts
"$host_tools/helm" repo add nktp-metrics https://kubernetes-sigs.github.io/metrics-server/
"$host_tools/helm" dependency build nktp --skip-refresh
printf '%s\n' "$pins" | sha256sum -c -
# The captured source bytes and vendor identity bind the build target,
# independently of the utility host used to resolve chart dependencies.
if [ "$target_arch" != "$arch" ]; then
  digest=$(helm_digest "$target_arch")
  curl --fail --location --max-time 120 --output /tmp/helm-target.tar.gz "https://get.helm.sh/helm-v4.2.4-linux-$target_arch.tar.gz"
  printf '%s  %s\n' "$digest" /tmp/helm-target.tar.gz | sha256sum -c -
  tar --no-same-owner -xzf /tmp/helm-target.tar.gz -C /tmp "linux-$target_arch/helm"
fi
mkdir -p .nvkr-tools
cp "/tmp/linux-$target_arch/helm" .nvkr-tools/helm
set -- nktp/charts/*.tgz
if [ "$#" -ne 3 ]; then
  echo 'Expected exactly three pinned NKTP dependency-chart archives.' >&2
  exit 1
fi
# The reviewed release and current main both select OTEL chart 0.25.1.
# Helm validates Chart.lock; only these independently reviewed archives pass.
set -- nktp/charts/opentelemetry-kube-stack-*.tgz
if [ "$#" -ne 1 ]; then
  echo 'Expected exactly one reviewed NKTP OpenTelemetry archive.' >&2
  exit 1
fi
case "${1##*/}" in
  opentelemetry-kube-stack-0.25.1.tgz) otel_digest=e3460a7572a2b9c98ff421a29501dc26cdd09d83859f0556f6c3f5443793afda ;;
  *) echo 'Unreviewed NKTP OpenTelemetry dependency archive.' >&2; exit 1 ;;
esac
printf '%s\n' \
  '084e6edb680cf4e2acc30bd496568c53fdf663cbacf6e17876b25785c35b7a13  nktp/charts/metrics-server-3.13.1.tgz' \
  "$otel_digest  $1" \
  '70fab291e00a34c58fd943bb075fecc2e2e73fa95db1bcbbe7cdfe008795354a  nktp/charts/prometheus-node-exporter-4.56.1.tgz' \
  | sha256sum -c -
