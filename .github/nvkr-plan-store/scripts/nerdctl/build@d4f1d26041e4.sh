export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-mod=vendor
make binaries VERSION="${BUILD_VERSION:?}" REVISION="${GIT_VERSION:?}"
