GOWORK=off GOFLAGS=-mod=vendor make -C agent deb-{{.Arch}} \
  VERSION="$BUILD_VERSION" \
  NFPM_BIN=/usr/local/bin/nfpm \
  DEB_OUTPUT=/src/agent/bin/nvke-agent.deb
