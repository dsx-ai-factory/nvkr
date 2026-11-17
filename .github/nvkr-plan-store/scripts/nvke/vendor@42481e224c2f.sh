test "$(getconf GNU_LIBC_VERSION)" = 'glibc 2.36' || {
  echo 'Ubuntu 24 agent requires the pinned Bookworm glibc 2.36 builder' >&2; exit 1;
}
# Retain the source-required compiler; the Packer image owns VM tools.
case "$(uname -m)" in
  x86_64) go_arch=amd64; go_sha=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40 ;;
  aarch64) go_arch=arm64; go_sha=a06e99309dbcbdca1a8ccd2282c3ee146e55930886e26b4f1193269587165f49 ;;
  *) echo 'Unsupported Go execution architecture' >&2; exit 1 ;;
esac
mkdir -p .nvkr-tools
curl --fail --location --max-time 120 --output .nvkr-tools/go.tar.gz \
  "https://go.dev/dl/go1.27.3.linux-${go_arch}.tar.gz"
printf '%s  .nvkr-tools/go.tar.gz\n' "$go_sha" | sha256sum --quiet -c -
tar -xzf .nvkr-tools/go.tar.gz -C .nvkr-tools
export PATH="${PWD}/.nvkr-tools/go/bin:$PATH"
test "$(GOTOOLCHAIN=local go version)" = "go version go1.27.3 linux/${go_arch}"
cd agent
export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=
pins="$(sha256sum go.mod go.sum)"
# Distinct equivalent URLs avoid Go's per-proxy cached metadata errors.
# Go owns transport fallback; checksum failures never select another proxy.
# Keep direct fallback limited to a missing proxy module (404/410).
proxy=https://proxy.golang.org
GOPROXY="${proxy}|${proxy}/|${proxy}//,direct" \
  timeout --kill-after=10s 540s go mod download || {
    status=$?; echo 'agent module download failed or exceeded 540s' >&2; exit "$status";
  }
printf '%s\n' "$pins" | sha256sum --quiet -c - || {
  echo 'agent module pins changed during download' >&2; exit 1;
}
# Offline verification and vendoring run once, after acquisition succeeds.
GOPROXY=off timeout --kill-after=10s 60s go mod verify || {
  status=$?; echo 'agent module cache verification failed or exceeded 60s' >&2; exit "$status";
}
GOPROXY=off timeout --kill-after=10s 180s go mod vendor || {
  status=$?; echo 'agent offline vendoring failed or exceeded 180s' >&2; exit "$status";
}
printf '%s\n' "$pins" | sha256sum --quiet -c - || {
  echo 'agent module pins changed during vendoring' >&2; exit 1;
}
# Capture the exact packaging executable alongside the vendored source.
# The build phase remains offline and cannot bootstrap another nfpm.
readonly package_arch='{{.Arch}}'
case "$package_arch" in
  amd64) release_arch=x86_64; archive_sha=0660ca602b2d2d2ae4781a06c692b3eeb9d437ffea05b831d76e41f4a3188783; binary_sha=17133a2467ffb7cec851c2d7bae0c6098d09d7ed7d3d101a9605f6a473323936 ;;
  arm64) release_arch=arm64; archive_sha=1c0f5f2999b9a974bfb04fdb0cc3306096de530ac5dbb25d739cc5f5219c919c; binary_sha=4d7ddf169945f7f557ac5373035d373429050d00476925953560e1ac65e16c74 ;;
  *) echo 'unsupported nfpm architecture' >&2; exit 1 ;;
esac
mkdir nvkr-tools
curl --fail --location --proto '=https' --proto-redir '=https' --connect-timeout 30 --max-time 300 --max-filesize 67108864 \
  --output nvkr-tools/nfpm.tar.gz \
  "https://github.com/goreleaser/nfpm/releases/download/v2.47.0/nfpm_2.47.0_Linux_${release_arch}.tar.gz"
printf '%s  %s\n' "$archive_sha" nvkr-tools/nfpm.tar.gz | sha256sum --strict -c -
# The vendor step runs as root, where GNU tar would restore the archive's unmapped owner.
tar --no-same-owner -xzf nvkr-tools/nfpm.tar.gz -C nvkr-tools nfpm LICENSE.md
printf '%s  %s\n' "$binary_sha" nvkr-tools/nfpm | sha256sum --strict -c -
chmod 0755 nvkr-tools/nfpm
