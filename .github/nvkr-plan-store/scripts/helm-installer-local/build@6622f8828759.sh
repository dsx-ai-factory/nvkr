export GOENV=off GOWORK=off GOTOOLCHAIN=local GOPROXY=off
go build -trimpath -mod=vendor \
  -ldflags "-s -w -buildid= -X helm.sh/helm/v4/internal/version.version=$BUILD_VERSION -X helm.sh/helm/v4/internal/version.metadata= -X helm.sh/helm/v4/internal/version.gitCommit=$GIT_VERSION -X helm.sh/helm/v4/internal/version.gitTreeState=clean" \
  -o /src/nvkr-output/helm ./cmd/helm
version_template="$(printf '{%s}' '{.Version}')"
test "$(/src/nvkr-output/helm version --template "$version_template")" = "$BUILD_VERSION"
