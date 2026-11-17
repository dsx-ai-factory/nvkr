test "$(getconf GNU_LIBC_VERSION)" = 'glibc 2.36' || {
  echo 'Ubuntu 24 agent requires the pinned Bookworm glibc 2.36 builder' >&2; exit 1;
}
PATH="/src/.nvkr-tools/go/bin:$PATH" GOTOOLCHAIN=local GOWORK=off GOFLAGS=-mod=vendor make -C agent deb-amd64 \
  VERSION=v0.23.0 \
  NFPM_BIN=/src/agent/nvkr-tools/nfpm \
  DEB_OUTPUT=/src/agent/bin/nvke-agent.deb
# Match the pinned source's build-guest.sh: the agent is dynamic, but
# its updater must remain static. A failed check denies the package output.
bash /src/os-image/nix/check-ubuntu-elf.sh --arch amd64 \
  /src/agent/bin/linux_amd64/nvke-agent
readonly updater=/src/agent/bin/linux_amd64/nvke-agent-updater
test -f "$updater" || { echo 'expected updater binary' >&2; exit 1; }
headers=$(readelf -lW "$updater")
[[ "$headers" != *INTERP* ]] || {
  echo 'expected a static updater, found a program interpreter' >&2; exit 1;
}
