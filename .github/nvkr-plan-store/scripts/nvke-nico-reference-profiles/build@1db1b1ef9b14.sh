mkdir -p bin/profiles-context/profiles/configProfiles
cp controller/test/fixtures/profiles/configProfiles/nico-vms-cp.yaml \
  controller/test/fixtures/profiles/configProfiles/nico-vms-worker.yaml bin/profiles-context/profiles/configProfiles/
cp -R controller/test/fixtures/runbooks bin/profiles-context/
buildctl-daemonless.sh build --frontend dockerfile.v0 \
  --local context=/src/bin/profiles-context \
  --local dockerfile=/src/controller/test/fixtures \
  --opt 'platform=linux/{{.Arch}}' \
  --opt context:alpine:3.24.1=docker-image://docker.io/library/alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 \
  --opt "build-arg:SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
  --output 'type=oci,dest=/src/bin/nico-reference-profiles-fixture-{{.Arch}}.oci.tar,oci-mediatypes=true,rewrite-timestamp=true'
