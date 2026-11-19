export GOENV=off GOWORK=off GOTOOLCHAIN=local GOPROXY=off
go build -trimpath -mod=vendor \
  -ldflags "-s -w -buildid= -X sigs.k8s.io/kind/pkg/cmd/kind/version.gitCommit=$GIT_VERSION" \
  -o /src/nvkr-output/kind .
test "$(/src/nvkr-output/kind version -q)" = "$BUILD_VERSION"
