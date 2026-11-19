export HELM_REPOSITORY_CONFIG=/tmp/nktp-repositories.yaml
export HELM_REPOSITORY_CACHE=/tmp/nktp-cache
# These are the three exact URLs in the selected source's Chart.yaml.
# Helm, not a custom parser, validates Chart.yaml against Chart.lock.
pins="$(sha256sum nktp/Chart.yaml nktp/Chart.lock)"
helm repo add nktp-otel https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo add nktp-prometheus https://prometheus-community.github.io/helm-charts
helm repo add nktp-metrics https://kubernetes-sigs.github.io/metrics-server/
helm dependency build nktp --skip-refresh
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
