test ! -e nvkr-chart-tools
mkdir -m 0755 nvkr-chart-tools
archive=/tmp/nvkr-chart-helm-arm64.tar.gz
timeout --kill-after=10s 240s curl --fail --silent --show-error --location \
  --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 120 \
  --retry 2 --retry-max-time 180 --max-filesize 268435456 \
  https://get.helm.sh/helm-v4.1.4-linux-arm64.tar.gz --output "$archive"
printf '%s  %s\n' \
  13d03672be289045d2ff00e4e345d61de1c6f21c1257a45955a30e8ae036d8f1 "$archive" \
  | sha256sum --quiet -c -
tar -xOzf "$archive" linux-arm64/helm > nvkr-chart-tools/helm
tar -xOzf "$archive" linux-arm64/LICENSE > nvkr-chart-tools/LICENSE
printf '%s\n' \
  '4d6e3a69e6203094d564d5d4e94325b1f7209421dc7e832a96d2510295e03f1d  nvkr-chart-tools/helm' \
  | sha256sum --quiet -c -
chmod 0555 nvkr-chart-tools/helm
export HELM_REPOSITORY_CONFIG=/tmp/nktp-repositories.yaml
export HELM_REPOSITORY_CACHE=/tmp/nktp-cache
# These are the three exact URLs in the selected source's Chart.yaml.
# Helm, not a custom parser, validates Chart.yaml against Chart.lock.
pins="$(sha256sum nktp/Chart.yaml nktp/Chart.lock)"
timeout --kill-after=10s 60s ./nvkr-chart-tools/helm repo add nktp-otel https://open-telemetry.github.io/opentelemetry-helm-charts
timeout --kill-after=10s 60s ./nvkr-chart-tools/helm repo add nktp-prometheus https://prometheus-community.github.io/helm-charts
timeout --kill-after=10s 60s ./nvkr-chart-tools/helm repo add nktp-metrics https://kubernetes-sigs.github.io/metrics-server/
timeout --kill-after=10s 180s ./nvkr-chart-tools/helm dependency build nktp --skip-refresh
printf '%s\n' "$pins" | sha256sum -c -
set -- nktp/charts/*.tgz
if [ "$#" -ne 3 ]; then
  echo 'Expected exactly three pinned NKTP dependency-chart archives.' >&2
  exit 1
fi
printf '%s\n' \
  '084e6edb680cf4e2acc30bd496568c53fdf663cbacf6e17876b25785c35b7a13  nktp/charts/metrics-server-3.13.1.tgz' \
  'e3460a7572a2b9c98ff421a29501dc26cdd09d83859f0556f6c3f5443793afda  nktp/charts/opentelemetry-kube-stack-0.25.1.tgz' \
  '70fab291e00a34c58fd943bb075fecc2e2e73fa95db1bcbbe7cdfe008795354a  nktp/charts/prometheus-node-exporter-4.56.1.tgz' \
  | sha256sum -c -
