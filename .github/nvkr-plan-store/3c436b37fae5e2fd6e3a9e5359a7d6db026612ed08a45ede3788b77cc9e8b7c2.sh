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
