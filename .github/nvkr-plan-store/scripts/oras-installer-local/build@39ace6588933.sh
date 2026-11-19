export GOENV=off GOWORK=off GOTOOLCHAIN=local GOPROXY=off
go build -trimpath -mod=vendor \
  -ldflags "-s -w -buildid= -X oras.land/oras/internal/version.BuildMetadata= -X oras.land/oras/internal/version.GitCommit=$GIT_VERSION -X oras.land/oras/internal/version.GitTreeState=clean" \
  -o /src/nvkr-output/oras ./cmd/oras
