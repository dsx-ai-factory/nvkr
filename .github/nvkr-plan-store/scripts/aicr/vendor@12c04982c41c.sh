export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly
pins="$(sha256sum go.mod go.sum)"
timeout --kill-after=10s 900s go mod download
timeout --kill-after=10s 180s go mod verify
GOPROXY=off timeout --kill-after=10s 900s go mod vendor
printf '%s\n' "$pins" | sha256sum -c -
