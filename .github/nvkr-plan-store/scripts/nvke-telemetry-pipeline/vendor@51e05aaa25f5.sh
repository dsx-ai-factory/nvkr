case "$(uname -m)" in
  x86_64) arch=amd64; digest=c306b46f719b0a4da32d0f78ee21bf90ce8d602f15b22ab753f0674d1670a7f3 ;;
  aarch64) arch=arm64; digest=564de2191b881e9f71b5606b25345821ea1682f06ab90499d3ab22b530176da1 ;;
  *) echo 'Unsupported Helm execution architecture' >&2; exit 1 ;;
esac
curl --fail --location --max-time 120 --output /tmp/helm.tar.gz "https://get.helm.sh/helm-v4.2.4-linux-$arch.tar.gz"
printf '%s  %s\n' "$digest" /tmp/helm.tar.gz | sha256sum -c -
tar --no-same-owner -xzf /tmp/helm.tar.gz -C /tmp "linux-$arch/helm"
mkdir -p .nvkr-tools
cp "/tmp/linux-$arch/helm" .nvkr-tools/helm
export HELM_REPOSITORY_CONFIG=/tmp/nktp-repositories.yaml
export HELM_REPOSITORY_CACHE=/tmp/nktp-cache
pins="$(sha256sum nktp/Chart.yaml nktp/Chart.lock)"
timeout --kill-after=10s 120s .nvkr-tools/helm repo add nktp-otel https://open-telemetry.github.io/opentelemetry-helm-charts
timeout --kill-after=10s 120s .nvkr-tools/helm repo add nktp-prometheus https://prometheus-community.github.io/helm-charts
timeout --kill-after=10s 120s .nvkr-tools/helm repo add nktp-metrics https://kubernetes-sigs.github.io/metrics-server/
timeout --kill-after=10s 120s .nvkr-tools/helm dependency build nktp --skip-refresh
printf '%s\n' "$pins" | sha256sum -c -
set -- nktp/charts/*.tgz
test "$#" -eq 3
printf '%s\n' \
  '084e6edb680cf4e2acc30bd496568c53fdf663cbacf6e17876b25785c35b7a13  nktp/charts/metrics-server-3.13.1.tgz' \
  'e3460a7572a2b9c98ff421a29501dc26cdd09d83859f0556f6c3f5443793afda  nktp/charts/opentelemetry-kube-stack-0.25.1.tgz' \
  '70fab291e00a34c58fd943bb075fecc2e2e73fa95db1bcbbe7cdfe008795354a  nktp/charts/prometheus-node-exporter-4.56.1.tgz' \
  | sha256sum -c -
