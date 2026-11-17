export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=
test ! -e controller/deploy/charts/go.sum
pins="$(sha256sum cli/go.mod cli/go.sum controller/api/go.mod controller/api/go.sum controller/deploy/charts/go.mod management-engine/go.mod management-engine/go.sum)"
cd cli
timeout --kill-after=10s 540s go mod download
GOPROXY=off timeout --kill-after=10s 60s go mod verify
GOPROXY=off timeout --kill-after=10s 180s go mod vendor
cd ..
printf '%s\n' "$pins" | sha256sum --quiet -c -
test ! -e controller/deploy/charts/go.sum
