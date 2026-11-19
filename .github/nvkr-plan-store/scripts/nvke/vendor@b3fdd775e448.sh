test ! -e os-image/packages/doca-ovs/nvkr-debs
test ! -L os-image/packages/doca-ovs/nvkr-debs
# The source Makefile owns the DOCA version, package set and committed
# per-deb SHA-256 lock. Stage beside the checkout; protected vendors
# mount /tmp with noexec.
bundle_root="$(mktemp -d "$(dirname "$PWD")/nvkr-doca.XXXXXXXX")"
trap 'rm -rf -- "$bundle_root"' EXIT
timeout --kill-after=10s 540s make -C os-image/packages/doca-ovs debs \
  ARCH=arm64 UBUNTU_SERIES=26.04 \
  DIST_DIR="$bundle_root/dist" OUT_DIR="$bundle_root/debs"
mkdir os-image/packages/doca-ovs/nvkr-debs
cp "$bundle_root/debs/"*.deb os-image/packages/doca-ovs/nvkr-debs/
