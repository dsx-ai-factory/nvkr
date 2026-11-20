cd cli
export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=
pins="$(sha256sum go.mod go.sum ../controller/api/go.mod ../controller/api/go.sum ../controller/deploy/charts/go.mod ../management-engine/go.mod ../management-engine/go.sum)"
timeout --kill-after=10s 540s go mod download
printf '%s\n' "$pins" | sha256sum --quiet -c -
GOPROXY=off timeout --kill-after=10s 60s go mod verify
GOPROXY=off timeout --kill-after=10s 180s go mod vendor
printf '%s\n' "$pins" | sha256sum --quiet -c -
