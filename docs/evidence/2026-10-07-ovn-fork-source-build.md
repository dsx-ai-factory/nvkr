# Native ARM64 DSX OVN source/package evidence

Recorded October 7, 2026. Scope: a candidate OVN option, real source-owned image
assembly and authenticated Debian packaging. This is not a full OS bake,
cluster deployment, signed Release or qualified-execution claim.

## Exact inputs

- NKD base: `d6cdf8dabb6bbda3af925ddb49eb16cde3eae8d9`; PR #113 parent
  `8d4519c2604dd13266f82f4033b9cfd8c0134840`, plus the candidate files recorded below.
- Fork: `https://github.com/dsx-ai-factory/ovn-kubernetes`, commit
  `26cc199de8e814a7fe6d899a770fc04df0bbc2f9`, tree
  `401ad2014d37d40082085b647dd4070a56042a5c`, source epoch `1791151171`.
  This is an exact-commit candidate pin, not an OVN 1.4.0 release tag.
- Catalog registry SHA-256:
  `fb781881f131b2b5b3147b17c43eb9023c77f00ec8028395071f19112d463d4f`.
- Machine-generated source lock SHA-256:
  `63eb239d3b1a7a2aac8d08ac6af076d47097a8c3c43c6ffafe15e71ef3f81a67`.
- Final integration-test source SHA-256:
  `ee2636c51f6017610b47dd2e8aafcfe3d06ebc932e000a69c83ea7d27e684a9d`.
- Rootless BuildKit:
  `moby/buildkit@sha256:80b15f0735e87bab7bf59ec4d695dfb4a7cfb25521cf56dc75d6f256285b63ef`.
- Source-owned image builder:
  `quay.io/projectquay/golang@sha256:74560d1d7488711be460297046d5b8423dc21e81f2a0953b36ef6c8ec37a15cb`.
- Source-owned Ubuntu image base:
  `docker.io/library/ubuntu@sha256:da6fc2be547864451aa253836dd926da33623312df4a9a243e35dc877c378a78`.
- Offline Debian packaging and verification toolchain:
  `docker.io/library/golang@sha256:27da97ffaed4bbb1263c44a8e423a72a1547830ee14a70fdb0cef3f6268a1b8b`.

The final render check independently built Helm 3.20.2 from the existing pinned
public-source producer, using that native compiler. It did not download a Helm
release binary. The graph/compiler tests also admitted the AMD64 compiler declared
in the catalog; this record does not establish an AMD64 live fork build.

## Execution route

The existing Apple-hypervisor Podman machine ran native Linux `aarch64`, with
8 CPUs, 16 GiB memory and SELinux enforcing. The dedicated VM workspace and
`TMPDIR` were prepared with a normal shared Podman `:z` label; no global SELinux
policy was changed. The existing rootless BuildKit adapter used its documented
fixed UID/security settings, no runtime socket and no mounted credentials.

The fork was internal at the time of the GitHub API check. Host-side authenticated
Git acquired the canonical repository and exact commit into
`/private/tmp/nkd-ovn-source-20261007.Ms3LQq/repo`. A process-local Git `insteadOf`
mapping supplied that existing clone to the VM's source fetch. The catalog URL,
commit/tree pins, source receipts and Build identity were not edited to use a local
path. Credentials were not forwarded to BuildKit or the packaging container.

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c \
  -o bin/nke-ovn-source.test ./pkg/nkd/buildgraph

# Inside the existing native VM, from the worktree's pkg/nkd/buildgraph:
GIT_CONFIG_COUNT=1 \
GIT_CONFIG_KEY_0=url.file:///private/tmp/nkd-ovn-source-20261007.Ms3LQq/repo.insteadOf \
GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/ovn-kubernetes \
TMPDIR=/var/tmp/nkd-ovn-source-20261007/tmp \
NKD_OVN_SOURCE_RUNTIME=podman ../../../bin/nke-ovn-source.test \
  -test.run '^TestNKEOVNForkLive$' -test.v -test.timeout 70m
```

The integration test compiled a complete diagnostic factory Plan, then executed
its real producer operations in dependency order. The unchanged source-owned
Dockerfile built OVN-Kubernetes, acquired its declared APT runtime inputs and
exported one OCI archive. Factory material observation held and rehashed that
exact output before the ordinary offline packaging container consumed it at
`/nkd/inputs/materials/ovn-image`.

The package command produced a flat USTAR archive containing chart and image
Debian packages for stream 1.5.18, revision `1+git.26cc199de8e8`. It never invoked
the NKE package folder.

## Terminal measurements

The final enhanced test passed in **310.90 seconds**:

| Output | Bytes | SHA-256 |
|---|---:|---|
| OVN OCI export | 351,955,968 | `dd683a5ea6438cb39961aaf3cc5d9bfe359ecffa8e0d3c794ce2bc5914ecfbe0` |
| `nke-cni-ovn.tar` | 348,784,640 | `2112c55ac351b6a0dbddfbfb8c9bd1fe9a4214911b6f3e079298b67b5997c701` |

Independent inspection ran in a read-only, network-denied packaging-tool
container. It extracted both packages through `dpkg-deb`, checked their native
architecture and package names, checked the chart/values paths and hook syntax,
validated the compressed image sidecar, and proved that decompressing the image
payload reproduced the exact retained OCI export digest above. Source-built Helm
rendered the packaged chart with fixture API/pod-network inputs. YAML decoding
checked that every rendered image field referred to the locally built image, not
the old NVCR defaults. These fixture checks are not deployment admission.

The named inspection container was force-cleaned under a separate bounded cleanup
context; temporary source/output owners were closed and joined. The original test
log remains in the dedicated VM workspace as `final-build.log`; successful test
temporary artifacts were cleaned by the test owner and were not published.

An earlier, less extensive source/package test also passed in 117.96 seconds and
produced a different package digest. No unchanged-rerun reproduction claim is made:
the source-owned Dockerfile has build stamps and live APT dependencies. The final
test adds Helm rendering and exact compressed-image correspondence; only its final
inputs/measurements are used above.

## Admission failures retained

An initial CLI invocation stopped before source work with
`NKD-E-BUILDER-NOT-PROVIDED`: the default local CLI profile provides only
`oci-hermetic`, not the declared `buildkit-rootless` producer. The test uses the
existing unqualified factory producer adapter instead. No profile was broadened,
qualified claim fabricated or admission gate bypassed.

## Remaining gates

The final `make check` passed with the repository's Go 1.27.1 and Bash 5 on
`PATH`: formatting, vet, lint, shellcheck, CI-tool tests, race/coverage tests and
the pinned Go vulnerability scan (no module vulnerabilities found). `actionlint`,
the GitHub-compatible `yamllint` profile and `git diff --check` also passed. One
intermediate full-gate attempt stopped at another session's lint lock; the
supported serial lint mode passed, then a fresh complete `make check` passed
without changing or skipping a check. This does not replace upstream
component/compiler security or license review.

- AMD64 live fork execution, Debian configuration/containerd import on an actual
  NKE node, a fresh OS bake and runtime CNI acceptance remain unexercised here.
- The fork's standard Ubuntu image acquires OVS, OVN, kubectl and their runtime
  closure through APT. It is not a fully Git-rebuilt/hermetic closure or the private
  NVIDIA FIPS/STIG image flavor.
- Optional OVS-node, identity and DPU subcharts are disabled. Enabling them or NGN
  extensions needs separate image/configuration bindings and tests.
- The fork vendors Kubernetes 1.36.2 libraries. Compatibility across all maintained
  Kubernetes versions is not established by a render or compilation.
- Internal-source access in a generated hosted pipeline, toolchain/component
  license/security review, maintained predecessor-bound catalog replacement and
  complete OS-image evidence/publication remain separate work.
- Calico and the maintained OS/M2 inputs are unchanged. No SBOM/signing/publication
  operation or Release acceptance was executed by this test.
