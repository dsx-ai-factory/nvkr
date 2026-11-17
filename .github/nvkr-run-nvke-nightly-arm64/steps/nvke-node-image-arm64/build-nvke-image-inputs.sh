# Keep DHCP identity stable when NVKE moves L3 to its OVS bridge.
# Stage authored VM configuration; never modify the pinned source tree.
readonly files=/src/nvkr-output/native-image-files
mkdir -p "$files"
cp -a /src/os-image/files/. "$files/"
cat >"$files/00-dhcp-all.yaml" <<'NETPLAN'
network:
  version: 2
  renderer: networkd
  ethernets:
    all-en:
      match:
        name: "e*"
      dhcp4: true
      dhcp6: true
      dhcp-identifier: mac
      optional: true
NETPLAN
readonly out=/src/nvkr-output
readonly materials="$out/materials"
mkdir -p "$materials"
# The composer refuses symlinks. Copy into this private writable view while
# preserving every original read-only material unchanged.
for binding in agent calico kubeadm-distro ovn spire-agent; do
  cp --reflink=auto "/nvkr/inputs/materials/$binding" "$materials/$binding"
done
# The composer accepts only .deb archive members, not the Make-generated index.
(cd /src/os-image/packages/doca-ovs/nvkr-debs && \
  tar --format=ustar --sort=name --mtime="@$SOURCE_DATE_EPOCH" \
    --owner=0 --group=0 --numeric-owner \
    -cf "$materials/doca-ovs" -- *.deb)
exec python3 /src/os-image/packer/scripts/compose-image-inputs.py \
  --architecture arm64 \
  --materials-dir "$materials" \
  --source-files-dir "$files" \
  --output "$out/image-inputs.tar" \
  --source-date-epoch "$SOURCE_DATE_EPOCH"
