export GOENV=off GOWORK=off GOTOOLCHAIN=local GOPROXY=off
build_date="$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)"
go build -trimpath -mod=vendor \
  -ldflags "-s -w -buildid= -X sigs.k8s.io/cluster-api/version.gitVersion=$BUILD_VERSION -X sigs.k8s.io/cluster-api/version.gitCommit=$GIT_VERSION -X sigs.k8s.io/cluster-api/version.gitReleaseCommit=$GIT_VERSION -X sigs.k8s.io/cluster-api/version.gitTreeState=clean -X sigs.k8s.io/cluster-api/version.gitMajor=$KUBE_GIT_MAJOR -X sigs.k8s.io/cluster-api/version.gitMinor=$KUBE_GIT_MINOR -X sigs.k8s.io/cluster-api/version.buildDate=$build_date" \
  -o /src/nvkr-output/clusterctl ./cmd/clusterctl
