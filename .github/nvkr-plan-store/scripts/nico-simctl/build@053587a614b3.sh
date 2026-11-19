cd controller/nico-sim
export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-mod=vendor
go build -trimpath -o bin/nico-simctl ./cmd/nico-simctl
