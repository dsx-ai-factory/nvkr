echo 'OVN IMAGE BUILD: source-owned Dockerfile acquires OVS/OVN and kubectl through APT; not hermetic or NV FIPS/STIG.' >&2
exec buildctl-daemonless.sh build --frontend dockerfile.v0 \
  --local context=/src --local dockerfile=/src/dist/images \
  --opt filename=Dockerfile.ubuntu --opt platform=linux/arm64 \
  --opt build-arg:OVN_KUBERNETES_DIR=. \
  --opt build-arg:BUILDER_IMAGE=quay.io/projectquay/golang@sha256:d5acf07940fb5e6e2d07542abd853cece623d95ffd364769ec825aab5471d3b8 \
  --opt build-arg:KUBECTL_MINOR=1.34 \
  --opt build-arg:UBUNTU_BASE_IMAGE=docker.io/library/ubuntu@sha256:da6fc2be547864451aa253836dd926da33623312df4a9a243e35dc877c378a78 \
  --opt "build-arg:SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
  --output type=oci,dest=/src/nvkr-output/ovn-kube-ubuntu.oci.tar,oci-mediatypes=true,rewrite-timestamp=true,name=localhost/nvke/ovn-kubernetes:git-"${NVKR_SOURCE_COMMIT:?}"
