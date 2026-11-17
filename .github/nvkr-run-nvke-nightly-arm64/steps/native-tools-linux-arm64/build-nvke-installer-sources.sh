set -o pipefail
printf '%s\n' "${NKECTL_COMMIT:?}" > bin/SOURCE_COMMIT
unset CAPI_VERSION
make --no-print-directory -s -f make/tool-versions.mk \
  --eval='nvkr-capi-version: ; @printf "%s\n" "$(CAPI_VERSION)"' \
  nvkr-capi-version > bin/CAPI_VERSION
tar --sort=name --format=ustar --mtime="@${SOURCE_DATE_EPOCH:?}" \
  --owner=0 --group=0 --numeric-owner --mode=u=rwX,go=rX \
  -cf - make/tool-versions.mk deploy/nvke-deploy.sh deploy/lib deploy/nico \
  cli/internal/bootstrap/sitespec/template.yaml \
  controller/deploy/charts/controller nktp/deploy/charts/nktp \
  controller/hack/apply-capi-spiffe-exec-auth.sh \
  controller/deploy/k8s/capi-spiffe-exec-auth \
  controller/test/fixtures/releases \
  controller/nico-sim/internal/seed/default-seed.yaml controller/nico-sim/ui \
  -C bin SOURCE_COMMIT CAPI_VERSION | gzip -n > bin/nvke-installer-sources.tar.gz
