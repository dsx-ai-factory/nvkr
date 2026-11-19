test "$(go env GOVERSION)" = go1.26.10
export GOENV=off GOTOOLCHAIN=local GOWORK=off
before="$(sha256sum go.mod go.sum)"
go mod download
go mod verify
go mod vendor
after="$(sha256sum go.mod go.sum)"
test "$before" = "$after"
