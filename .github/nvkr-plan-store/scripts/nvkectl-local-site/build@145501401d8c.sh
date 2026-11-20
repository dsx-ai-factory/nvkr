export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-mod=vendor
# Make owns the linker symbols and proves the stamp by executing version.
make -C cli build NVKECTL_TREE=clean
