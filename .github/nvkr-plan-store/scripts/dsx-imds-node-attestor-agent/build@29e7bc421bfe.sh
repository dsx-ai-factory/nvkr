test "$(go env GOVERSION)" = go1.26.10
test "$(go env GOOS)" = linux
test "$(go env GOARCH)" = "arm64"
export GOENV=off GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off CGO_ENABLED=0
cd /src
test -f vendor/modules.txt
exec go build -mod=vendor -trimpath -buildvcs=false \
  -o /src/nvkr-output/dsx-imds-node-attestor-agent ./cmd/dsx-imds-node-attestor-agent
