printf '%s\n' \
  '4d6e3a69e6203094d564d5d4e94325b1f7209421dc7e832a96d2510295e03f1d  /src/nvkr-chart-tools/helm' \
  | sha256sum --quiet -c -
exec timeout --kill-after=10s 60s /src/nvkr-chart-tools/helm package controller/deploy/charts/controller --version=0.23.0 --app-version=v0.23.0 --destination=charts/
