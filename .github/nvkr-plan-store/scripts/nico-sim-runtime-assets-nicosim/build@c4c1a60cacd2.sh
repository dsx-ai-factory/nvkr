set -o pipefail
cd controller/nico-sim
# The source pin owns this diagnostic seed, UI and vendor notices.
# No production profiles, private credentials or VM state are included.
tar --sort=name --format=ustar --mtime="@${SOURCE_DATE_EPOCH:?}" \
  --owner=0 --group=0 --numeric-owner --mode=u=rwX,go=rX \
  -cf - internal/seed/default-seed.yaml ui | \
  gzip -n > bin/nicosim-runtime-assets.tar.gz
