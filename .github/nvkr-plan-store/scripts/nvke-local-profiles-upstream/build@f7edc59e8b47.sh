: "${SOURCE_DATE_EPOCH:?}"
# Exact pinned source bytes keep the named Alpine context exhaustive.
# This is blob integrity, not Dockerfile parsing or Release authority.
test -f controller/profiles/example/Dockerfile
test ! -L controller/profiles/example/Dockerfile
printf '%s\n' '789f7ad1e406b82f0f55a52152eff7b08846b4122582145fade588c7d931cc3e  controller/profiles/example/Dockerfile' \
  | sha256sum -c -
mkdir -p bin/profiles-context/profiles/configProfiles
cp -R nvkr-local-profiles/osProfiles bin/profiles-context/profiles/
cp -R controller/test/fixtures/runbooks bin/profiles-context/
cp controller/test/fixtures/profiles/configProfiles/nico-vms-cp.yaml \
  controller/test/fixtures/profiles/configProfiles/nico-vms-worker.yaml bin/profiles-context/profiles/configProfiles/
buildctl-daemonless.sh build --frontend dockerfile.v0 \
  --local context=/src/bin/profiles-context \
  --local dockerfile=/src/controller/profiles/example \
  --opt platform=linux/{{.Arch}} \
  --opt context:alpine:3.24.1=docker-image://docker.io/library/alpine@sha256:260479a1cfaf304c4c20da7f8405d3ce313513dcd534bb743257bdd2fe0f3e2d \
  --opt "build-arg:SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
  --output type=oci,dest=/src/bin/nvke-local-profiles-{{.Arch}}.oci.tar,oci-mediatypes=true,rewrite-timestamp=true
