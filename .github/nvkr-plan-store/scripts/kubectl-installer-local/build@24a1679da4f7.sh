export GOENV=off GOWORK=/src/go.work GOTOOLCHAIN=local GOPROXY=off
build_date="$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)"
go build -trimpath -mod=vendor -tags selinux,notest,grpcnotrace \
  -ldflags "-s -w -buildid= -X k8s.io/component-base/version.gitVersion=$BUILD_VERSION -X k8s.io/component-base/version.gitCommit=$GIT_VERSION -X k8s.io/component-base/version.gitMajor=$KUBE_GIT_MAJOR -X k8s.io/component-base/version.gitMinor=$KUBE_GIT_MINOR -X k8s.io/component-base/version.gitTreeState=clean -X k8s.io/component-base/version.buildDate=$build_date -X k8s.io/client-go/pkg/version.gitVersion=$BUILD_VERSION -X k8s.io/client-go/pkg/version.gitCommit=$GIT_VERSION -X k8s.io/client-go/pkg/version.gitMajor=$KUBE_GIT_MAJOR -X k8s.io/client-go/pkg/version.gitMinor=$KUBE_GIT_MINOR -X k8s.io/client-go/pkg/version.gitTreeState=clean -X k8s.io/client-go/pkg/version.buildDate=$build_date" \
  -o /src/nvkr-output/kubectl ./cmd/kubectl
