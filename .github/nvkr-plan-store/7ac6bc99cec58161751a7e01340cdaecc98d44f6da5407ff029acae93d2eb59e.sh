GOWORK=off GOFLAGS=-mod=vendor make -C agent deb-amd64 \
  VERSION="${BUILD_VERSION:?}" \
  NFPM_BIN=/usr/local/bin/nfpm \
  DEB_OUTPUT=/src/agent/bin/nvke-agent.deb
test -f "/src/agent/bin/nvke-agent.deb"
test "$(dpkg-deb --field "/src/agent/bin/nvke-agent.deb" Package)" = "nvke-agent"
test "$(dpkg-deb --field "/src/agent/bin/nvke-agent.deb" Architecture)" = "amd64"
