export GOENV=off GOWORK=off GOTOOLCHAIN=local
target_os="${GOOS:?}"
target_arch="${GOARCH:?}"
env -u GOOS -u GOARCH make -C cli build-cross DIST=bin NVKECTL_PLATFORMS="$target_os/$target_arch" \
  LEGACY_BINARY_NAME= NVKECTL_VERSION="git-${NKECTL_COMMIT}" NVKECTL_COMMIT="${NKECTL_COMMIT}" \
  NVKECTL_DATE="${NKECTL_DATE}" NVKECTL_TREE=clean
cp "cli/bin/nvkectl-$target_os-$target_arch" cli/bin/nvkectl
