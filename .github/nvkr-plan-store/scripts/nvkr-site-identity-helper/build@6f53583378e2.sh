export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-mod=vendor
test "$(go env GOVERSION)" = "go$(cat .go-version)"
go build -trimpath -buildvcs=false -o bin/nvkr-site-identity-helper ./cmd/nvkr-site-identity-helper
