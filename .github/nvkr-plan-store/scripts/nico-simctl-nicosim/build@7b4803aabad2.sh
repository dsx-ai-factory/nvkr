cd controller/nico-sim
export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-mod=vendor
CGO_ENABLED=1 go build -trimpath -tags 'netgo osusergo' \
  -ldflags "-X github.com/dsx-ai-factory/nvke/controller/nico-sim/internal/version.BuildVersion=${NICO_SIM_VERSION:?} -X github.com/dsx-ai-factory/nvke/controller/nico-sim/internal/version.GitCommit=${NICO_SIM_COMMIT:?} -X github.com/dsx-ai-factory/nvke/controller/nico-sim/internal/version.GitTreeState=${NICO_SIM_TREE:?} -linkmode external -extldflags -static" \
  -o bin/nico-simctl ./cmd/nico-simctl
