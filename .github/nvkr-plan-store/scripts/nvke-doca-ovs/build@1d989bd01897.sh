# NVIDIA publishes these DOCA OVS debs without source; the vendor stage
# verified each against NVKE's committed lock.
cd /src/os-image/packages/doca-ovs/nvkr-debs
packages=(*.deb)
test "${#packages[@]}" -eq 8
switch=(doca-openvswitch-switch_*_arm64.deb)
test "${#switch[@]}" -eq 1
test -f "${switch[0]}"
test "$(dpkg-deb --field "${switch[0]}" Package)" = doca-openvswitch-switch
test "$(dpkg-deb --field "${switch[0]}" Architecture)" = "arm64"
mkdir -p /src/nvkr-output
# The composer accepts only .deb archive members.
tar --format=ustar --sort=name --mtime="@$SOURCE_DATE_EPOCH" \
  --owner=0 --group=0 --numeric-owner \
  -cf /src/nvkr-output/nvke-doca-ovs.tar -- *.deb
