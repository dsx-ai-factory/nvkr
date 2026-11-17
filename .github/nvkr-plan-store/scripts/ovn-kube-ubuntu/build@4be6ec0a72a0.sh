echo 'OVN IMAGE BUILD: source-owned Dockerfile acquires OVS/OVN and kubectl through APT; not hermetic or NV FIPS/STIG.' >&2
exec buildctl-daemonless.sh build --frontend dockerfile.v0 \
  --local context=/src --local dockerfile=/src/dist/images \
  --opt filename=Dockerfile.ubuntu.nv --opt platform=linux/amd64 \
  --opt build-arg:OVN_K8S_SRC_ROOT=. \
  --opt build-arg:BASE_IMAGE=docker.io/library/ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55 \
  --opt "build-arg:SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
  --opt build-arg:OVS_SOURCE=doca \
  --opt build-arg:OVN_SOURCE=uca \
  --opt build-arg:CLOUD_ARCHIVE=gazpacho \
  --opt build-arg:UCA_OVN_VER=26.03.0-2~cloud0 \
  --opt build-arg:DOCA_RELEASE=3.5.0 \
  --opt build-arg:K8S_VER=v1.36 \
  --output type=oci,dest=/src/nvkr-output/ovn-kube-ubuntu.oci.tar,oci-mediatypes=true,rewrite-timestamp=true,name=localhost/nvke/ovn-kubernetes:git-8af51273d52dfa07454435d18d37230e582d797a
