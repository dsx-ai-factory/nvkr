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
pins="$(sha256sum controller/deploy/charts/controller/Chart.yaml)"
timeout --kill-after=10s 120s "$host_tools/helm" dependency build controller/deploy/charts/controller
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
