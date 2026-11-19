export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-mod=vendor
make -C controller/nico-sim build-static \
  BUILD_VERSION="${NICO_SIM_VERSION:?}" GIT_COMMIT="${NICO_SIM_COMMIT:?}" GIT_TREE_STATE="${NICO_SIM_TREE:?}"
