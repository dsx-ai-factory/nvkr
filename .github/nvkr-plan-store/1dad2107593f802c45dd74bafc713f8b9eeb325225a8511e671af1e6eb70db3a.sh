export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-mod=vendor
make -C cli build NVKECTL_TREE=clean
