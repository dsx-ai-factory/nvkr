test ! -e os-image/packages/kubeadm-distro/dist
test ! -L os-image/packages/kubeadm-distro/dist
# Stage beside the checkout; protected vendors mount /tmp with noexec.
bundle_root="$(mktemp -d "$(dirname "$PWD")/nvkr-kubeadm.XXXXXXXX")"
trap 'rm -rf -- "$bundle_root"' EXIT
make -C os-image/packages/kubeadm-distro bundle nfpm \
  DIST_DIR="$bundle_root/dist" \
  ARCH={{.Arch}} BIN_DIR=/usr/local/bin NFPM_BIN=/usr/local/bin/nfpm
cp -a "$bundle_root/dist" os-image/packages/kubeadm-distro/dist
