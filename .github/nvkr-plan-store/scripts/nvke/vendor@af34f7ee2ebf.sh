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
pins="$(sha256sum controller/deploy/charts/controller/Chart.yaml)"
timeout --kill-after=10s 120s ./nvkr-chart-tools/helm dependency build controller/deploy/charts/controller
printf '%s\n' "$pins" | sha256sum --quiet -c -
