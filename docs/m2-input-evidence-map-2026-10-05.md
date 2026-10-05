# M2 maintained input evidence map

Initially checked against integration baseline `dde5fffea9e33d0922188cfb8d6019c889ae1fd9`.
The isolated D extraction is based on `e816cd255bef0926caaa3ca4ac95097a888d0572`; the current A
interface was rechecked at `e87974b830503eb3058c82808b33994301809722` plus its uncommitted trusted
source-fetch additions. The final A stack parent and whole-stack gate remain pending.
This is an input/configuration record. It grants no runtime, publication, signer or M2 acceptance claim.

The refreshed catalog image-pin owner uses the existing pinned OCI library instead of
Docker's manifest JSON projection. A temporary read-only owner diagnostic on October 5
resolved all twelve selected upstream references and both native architectures: all 24
manifest digests exactly matched the maintained lock (`TestM2LiveImagePinReadback`,
8.713 s package result). It fetched and authenticated each native manifest/config, not
layers, and wrote no lock or registry object. The diagnostic source is preserved privately
at `/private/tmp/m2-image-pin-live-diagnostic.go`; it is not a default network-dependent
test. Fixture tests cover both native media formats, ambiguity, platform/digest/size
substitution, metadata bounds and cancellation. No production runtime approval follows.

## Candidate output selections and requirement ownership

The combined Definition now selects 34 candidate outputs: the original ten below plus twelve
non-test deployment image members for each native architecture. The human-authorized image mode
is the exact pinned NKE deployment values, not unmodified chart defaults. This does not establish
runtime/publication completion or resolve the separately owned external-NKE-image checkpoint.
`docs/epics-v1-spec.md`, Epic M2 Goal/Acquisition/acceptance criteria 2–3, and
`docs/milestones.md:36–55` require external NKE and third-party image rows in addition to these packages.
The formal v1 scope is AMD64; this sprint additionally
requires a separate native ARM64 non-FDE disk. Each disk has its own execution and evidence obligations.

| Output | Authoritative authored selection | Source and scope |
| --- | --- | --- |
| `nkectl` | `definitions/nke-v1/catalog`, component `nkectl` | Source lock `nke` at `ed9fb09b04fd40bf331d25df2222206ffaca593e`; CLI release selector retained |
| `cwe` | `definitions/nke-controller/catalog`, component `nke-controller` | Source lock `nke` at `33fb71b2f4224b470ebc628a9165fc4b92870406`; wrapped OCI image |
| `controller-chart` | Same catalog, `nke-controller-chart`, controller image bound to `cwe` | Pinned chart source and final image-bound package; chart inventory does not scan its image |
| `postgresql` | Same catalog, `postgresql-chart` | Exact acquired chart 18.5.15, source-derived from the pinned deploy tree |
| `spire` | Same catalog, `spire-chart` | Exact acquired chart 0.30.1, source-derived from the pinned deploy tree |
| `spire-crds` | Same catalog, `spire-crds-chart` | Exact acquired chart 0.6.1, source-derived from the pinned deploy tree |
| `temporal` | Same catalog, `temporal-chart` | Exact acquired chart 0.74.0, source-derived from the pinned deploy tree |
| `operator-tools` | `definitions/nke-m2/catalog`, `helm` and `kubectl`, AMD64 | Helm 4.1.4 and Kubernetes 1.36.3 release assets; source-owned NKE distro lock agrees with both transport hashes |
| `nke-node-image-amd64` | `definitions/nke-m2/os-catalog`, AMD64 root and all transitive materials | Exact NKE candidate below, native AMD64 KVM requested, non-FDE QCOW2 |
| `nke-node-image-arm64` | Same catalog, ARM64 root and all transitive materials | Exact same source candidate, native ARM64 KVM requested, separate non-FDE QCOW2 |

The chart sources, exact chart bytes, CLI and controller pins remain in their existing owning catalogs.
They are independent source selections; this document does not assert that one NKE source release owns all outputs.

## Exact disk source and capture projection

NKE `b30117c25b2dcdf6c16021dae88a2eafdafcae61` has tree
`fe97cd65a5d90b3287caadd06ec929f7f1f8ec86` and committer epoch `1791144257`.
Local Git object inspection establishes that it descends from material-adapter commit
`289abbfa70f53e65788788e39c802b559b040953`. Source review/merge acceptance is separate.
Four NKE source entries were regenerated using `catalog pin --commit` against the verified local Git clone,
with a transport-only `insteadOf` mapping. No NKE checkout, source tag or source file was modified.

| Input | Authoritative source at that exact NKE commit | Compiled projection |
| --- | --- | --- |
| Guest capture opt-in | `os-image/packer/variables.pkr.hcl`, `capture_apt_acquisitions` | Public literal `"true"` for both native disk roots |
| Retained capture carrier | `os-image/packer/nke-node-non-fde.pkr.hcl`, download provisioner | `packer.acquisitionArchive: non-fde-apt-acquisitions.tar`, relative to authenticated output directory |
| Material manifest | Same HCL variables and producer consumers | Schema `nke.dev/os-image-materials/v1`; `image-inputs: archive`, `installer-iso: disk-image` |
| Image-input composition | `os-image/packer/scripts/compose-image-inputs.py` | Explicit agent, Calico, kubeadm distro, OVN and SPIRE material bindings; read-only source and staged materials |
| Agent version | `agent/Makefile` and exact-tree `git describe --tags --match agent/*` | Source-derived `v0.2.1-68-gb30117c2` |
| Kubernetes embedded payload | `os-image/packages/kubeadm-distro/lock/{amd64,arm64}/v1.36.3.lock` and peer-version locks | Source-owned Debian payload composition and exact per-architecture image/acquisition pins |
| SPIRE payload | `os-image/packages/spire-agent/lock/1.14.1+tpm1.9.0.lock` and bundle script | SPIRE, TPM plugin/helper and DSX IMDS attestor; source pin authenticates the updated lock |

The source capture contract rejects disappeared repository metadata and any matching `file:`/`cdrom:` URI.
Its installer-media behavior is explicitly unexercised in `os-image/docs/apt-acquisition-capture.md`.
The capture graph regression is held separately until the final B/C/A schema is present in this extraction.
Its asserted native target/toolchain architecture, capture opt-in, carrier path and material projection must
pass on that exact parent before handoff. Existing image/operator graph tests do not establish that capture
projection, a successful capture-enabled installer bake or a live evidence join.

## Runtime, inspector, registry and signer map

| Required field or closure | Current authoritative configuration | Missing evidence |
| --- | --- | --- |
| Disk toolchain host architecture | Separate AMD64/ARM64 source toolchain maps and native allowed-host platforms | Existing catalog digests are local identities; approved published per-native OCI manifests remain absent |
| Packer and preparation tools | `toolchains/nkd-packer/toolchain.lock.json` and Dockerfile | `images` remains null; BUSL redistribution and vulnerability review approvals remain `not-recorded` |
| Full inspector tools | `toolchains/nkd-disk-inspect/Dockerfile` and source lock | Updated helper closure, native execution, exact image identity and executable measurements required |
| Syft inspector scanner | Official v1.51.1 release checksums; schema Git blob `37a6582388f00a8dcbf9a030d227921b1322f8f1` at that tag | Both source archive digests verified; full native image execution remains separate |
| Inspector architecture | Profile key must equal native `linux/amd64` or `linux/arm64` execution host | Both measured native full profiles and capacity grants remain required |
| Scratch and memory | B's bounded private-volume inspection contract | Actual final filesystem/inventory/document sizes and host/daemon headroom must be measured; installed-package inventory is insufficient |
| Registry namespace | Existing `definitions/nke-v1/environment-github.yaml` | M2 production target preserves `ghcr.io/dsx-ai-factory/nke-recipes`; live blob limits, retention and package access not verified here |
| Registry credential | Existing `github:GITHUB_TOKEN` reference | Runtime values are not inspected or serialized; production credential admission/read-back required |
| Signing identity | Existing main `nkd-run.yml` identity regex and Actions issuer | Existing policy copied byte-for-byte; protected publication environment and current keyless run remain unverified |
| Private source transport | Existing `github:NKE_SOURCE_TOKEN` mirror mapping | Source credential must remain in trusted fetch, outside candidate build execution |
| Generated runner route | ADR-18 generic-hosted-only rule | Native KVM capability for both routes remains unresolved; historical ARC diagnostics cannot establish generated production admission |

`definitions/nke-m2/environment-github.yaml` records the existing production publication/signing target and
producer-declared build network permission. It omits unavailable native execution classes and inspector
profiles. This is intentionally incomplete for production admission; no placeholder digest or invented runner
class is supplied. Every missing field must be resolved before generation/production launch.

Runtime selection uses only the supported typed `runtime: podman` and
`buildNetworkPolicy: producer-declared` controls. No `podmanStorageDriver` field,
ambient storage variable or setup hook is supplied. Storage follows the existing
runtime owner's admission/observation semantics; the historical explicit-VFS probe
does not freeze this Environment's driver or approve its default isolation route.

### Exact native production input handoff

The input owner must receive separately measured AMD64 and ARM64 closures; an index tag or an
inherited local runtime ID is not an approved native publication pin. Consume a reviewed Packer
image through the existing catalog overlay `toolchainSubstitutions` contract: bind the exact
predecessor catalog digest, `arch`, current `from` pin, measured `to` pin and sorted explicit
producer set. The nine source-map consumers selected by each disk are `nke-agent`, `nke-cni-calico`,
`nke-cni-ovn`, `nke-image-inputs`, `nke-kubeadm-distro`, that architecture's disk root and patched
installer producer, `nke-spire-agent`, and `ovn-kubernetes-chart`. Preserve `ovn-kube-ubuntu`'s
separate pinned BuildKit image; its explicit component override is not a Packer-map consumer.
No replacement is authored until its exact measured destination and required approvals exist.

Each native Environment `executionClasses` entry needs `name`, `runsOn`, `hostPlatform`,
`resources`, `maxTimeoutSeconds` and `capabilities: [kvm]`. Provider/runtime isolation admission
is a separate execution gate; the current reviewed Class schema does not expose the older draft's
`runnerKind`/`vmIsolation` fields, and those unavailable fields must not be authored. The disk
root requires matching native host platform, at least 8000 CPU
millis, 10240 MiB memory, 65536 MiB ephemeral storage and a 21600-second timeout grant. These are
declared minima, not observed allocation or isolation proof. A runner label or historical ARC
diagnostic cannot supply the missing provider/runtime admission. Generic hosted-runner policy
remains a separate generated-execution gate; do not choose a self-hosted/trusted-host route merely
because the schema can describe one.

Each `diskInspectionProfiles[linux/amd64|linux/arm64]` entry needs its canonical digest-qualified
native `image`, actual executable SHA-256 measurements for `inspectorDigest`, `codecDigest`,
`guestfishDigest`, `scannerDigest` and `packageReaderDigest`, measured `resources` headroom,
positive `timeoutSeconds` no greater than 600, positive `maxFilesystemBytes` no greater than
16 GiB, and production `scratchStorage: private-volume`. The full image authenticates the
helper/appliance/runtime closure; package versions and helper-only binary hashes do not replace
that image pin. Resource and document bounds must admit the actual complete retained disk and
materials, not only an installed-package list. Selection follows the frozen root's native host
binding; guest-platform package metadata remains distinct even for cross-guest TCG consumers.

The current published-image fields in both toolchain source locks are null, with their owning
license/security gates `not-recorded`. The Packer lock's `validatedNKE.commit` remains historical
`726f76944404e1106626b8333d3576bdf67b575b`, not the selected capture-capable source. Before claiming
production readiness, native capture-enabled bakes must retain the selected
`non-fde-apt-acquisitions.tar`, and original live Run owners must reconcile each exact final disk
and complete material closure. Both selected NKE source PRs remain open at the recorded exact
heads; pinning them establishes bytes, not source-review or merge acceptance. Final-parent graph
tests, the full repository/security gates, full signed publication and independent consumer
verification remain required separately. No unavailable handoff value is filled by this record.

## Native acquisition, build and measurement commands

Run from the final composed repository root with its pinned Go version. These
commands use existing owners, not a new pin writer or publication workflow.
`--check` below does not write; the other catalog maintenance commands do update
their machine-managed locks and belong only in a reviewed input-maintenance step.
No such update was executed during this read-only source refresh.

```sh
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin \
  --dir definitions/nke-m2/os-catalog \
  --commit b30117c25b2dcdf6c16021dae88a2eafdafcae61 --check \
  nke-os nke-cni-calico-source nke-kubeadm-distro-source nke-spire-agent-source
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin \
  --dir definitions/nke-m2/catalog --tag v4.1.4 helm
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin \
  --dir definitions/nke-m2/catalog --tag v1.36.3 kubectl
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin-artifacts \
  --dir definitions/nke-m2/image-catalog --all --arch amd64 --arch arm64
toolchains/nkd-packer/build.sh --check
```

The source check requires authorized access to the exact NKE repository. Existing
source mirrors or local Git `insteadOf` transport overrides do not alter its pin.
Release executable selection downloads and verifies the archive/member without
executing it. Image pin maintenance uses the repository's existing manifest
acquisition owner and retains both native subjects; it is not image publication.

On an admitted native Linux AMD64 host and separately on an admitted native Linux
ARM64 host, the existing Packer owner acquires, verifies, builds and locally tests:

```sh
CONTAINER_RUNTIME=podman toolchains/nkd-packer/build.sh --arch amd64
CONTAINER_RUNTIME=podman toolchains/nkd-packer/build.sh --arch arm64
podman image inspect --format '{{.Os}}/{{.Architecture}} {{.Id}}' \
  localhost/nkd-packer-toolchain:dev-amd64
podman image inspect --format '{{.Os}}/{{.Architecture}} {{.Id}}' \
  localhost/nkd-packer-toolchain:dev-arm64
```

Run only the matching architecture's build and inspection on each host. The
script rejects a non-native runtime check unless `--cross-build-only` is explicit;
that option does not supply native proof. The source lock may deliberately reject
a rebuilt image different from the historical local pin. A rejection must be
investigated, not bypassed or overwritten. Docker remains an explicit supported
local alternative in this script, not approval for the pending generated route.
Neither build pushes, signs, edits a source lock or supplies a repository digest.

Full inspector assembly and measurement use the separate native commands in
[the source README](../toolchains/nkd-disk-inspect/README.md#local-measurement-not-publication-admission).
They build the final source-bound helper, acquire exact snapshot/Syft inputs,
inspect actual local image platform/ID and record all five executable pins plus
the helper and package inventory. Record final source bytes with every measurement;
old helper-only hashes from before admission edits are obsolete. After a separately
approved publication owner supplies an exact repository digest, `crane digest
--platform linux/amd64 <exact-digest-reference>` or its ARM64 counterpart can read
the native descriptor back; no currently missing reference is invented here.
Reviewed Packer pins are consumed through existing `ToolchainSubstitution`, and
reviewed inspector pins through the native Environment profiles described above.
The current integration diagnostic workflow
`.github/workflows/nkd-inspector-closure-diagnostic.yml` now explicitly runs
`go test ./pkg/nkd/pipeline -run '^TestDiskInspectionSelectionProjectsAuthenticatedLogicalRuntime$' -count=1`
in its native matrix. This covers the controlled accessor/held-runtime regression
that helper compilation alone omitted. It does not exercise a full SDK disk scan,
produce measured native image pins, or establish a successful workflow run.

Fresh read-only checks on October 5 found both NKE source PRs still open at the
listed heads. Run `37353594709` completed with overall failure: ARC AMD64 job
`111910268835` and ARC ARM64 job `111910268880` succeeded, while both generic hosted
jobs failed. Successful ARC host observations included 96/64 CPUs, respectively;
available memory 379292/249592 MiB and workspace space 3379904/3560851 MiB. These
are historical observations, not reservations, provider admission, current
availability or generic-runner approval. The later Docker diagnostic run
`37357713937` completed with failure on all four jobs. No completed toolchain
closure or image publication is inferred from either host probe.

Both exact native Bash descriptors were freshly read back with `crane digest
--platform linux/amd64|linux/arm64` and agree with the image lock. The other 22
descriptor measurements retain their recorded earlier scope; no fresh complete
layer acquisition or signed publication is asserted. The source locks still
contain no maintained published Packer/inspector image references; this is a
statement about the checked locks, not a claim that no registry anywhere has an
image. The October 5 installed-package inspector replay used unchanged October 2
ARM64 image `sha256:a0e238dedfca964f4474ee0d649826d03642dbb256ad8f9b3b8b4ee63a6f663c`;
its recorded tools and 730 installed packages do not satisfy the new full-content
native profile or complete capture reconciliation.

## Required embedded matrix rows

The M2 specification assigns node-agent, node-side Kubernetes, control-plane images and CNI to the disk,
and requires a recorded placement choice for SPIRE agent. Both native disk selections include the
source-owned agent, kubeadm distro, Calico, OVN and SPIRE material producers. Their material locks are
at exact NKE `b30117c25b2dcdf6c16021dae88a2eafdafcae61`:

| Required row | Authoritative source selection | Evidence still required |
| --- | --- | --- |
| Node-agent | `agent/Makefile`; exact source describe version above | Actual retained agent bytes joined to final disk |
| Node Kubernetes and control-plane images | `os-image/packages/kubeadm-distro/lock/{amd64,arm64}/{v1.34.2,v1.35.3,v1.36.3}.lock` | Every per-architecture archive/image acquisition joined to final disk |
| Calico | `os-image/packages/cni-calico/lock/{amd64,arm64}/v3.30.1.lock` | Exact manifests and images joined to final disk |
| OVN | `os-image/packages/cni-ovn/lock/{amd64,arm64}/1.4.0.lock` | Exact chart and image joined to final disk |
| Embedded SPIRE agent | `os-image/packages/spire-agent/lock/1.14.1+tpm1.9.0.lock` | Exact SPIRE 1.14.1 native binary, TPM 1.9.0 and DSX IMDS acquisitions joined to final disk |

SPIRE's chart deploys a **different** SPIRE agent container at 1.15.3; the embedded 1.14.1 executable
cannot satisfy that chart image row. A source declaration alone is not containment evidence.

## Chart image matrix from authenticated chart payloads

Each chart was acquired by the repository-native downloader and re-hashed against its existing lock:
PostgreSQL `c350143d2e21c1e1a3670dd32ab64fd11d49198a66fae87c63a3112531c0cd17`,
SPIRE `7c32126bb1909076cdf9fd0419dbf9e5b012f152bb09a96a4259917d70581fac`,
Temporal `14a8efe1cc92c91befac151db91f20f9a13962788e9d710c86655add6d966d37`.
Diagnostic `helm template` with Helm `v4.3.0+gbec5b06`, `--skip-tests`, no deployment values,
produced the following runtime/install-hook references. This renderer is diagnostic, not the pinned
M2 operator executable and not production execution. Names lacking a registry below are the exact
chart spellings, not inferred registry canonicalizations.

The following default-mode references are retained only as a comparison to the human-selected
deployment-value mode. They are **not** the required runtime set frozen for this attempt. No exact
final-disk containment evidence is yet recorded. A chart archive digest is not an image manifest digest.

| Chart | Exact default runtime/install-hook image reference | Effect of exact NKE deploy values |
| --- | --- | --- |
| PostgreSQL 18.5.15 | `registry-1.docker.io/bitnami/postgresql:latest` | Source values replace tag with `@sha256:b74b23f091dcc87041bcae557d840c36458af46b955279cb13aedec4c32d7f71` |
| SPIRE 0.30.1 | `busybox:1.37.0-uclibc` | Not rendered in non-test deploy-value mode |
| SPIRE 0.30.1 | `cgr.dev/chainguard/bash:latest@sha256:90041f375e30f41aa7e0390075d8a69dc61900771d52fa98d63ee5d03d866a58` | Still rendered |
| SPIRE 0.30.1 | `ghcr.io/spiffe/oidc-discovery-provider:1.15.3` | Still rendered |
| SPIRE 0.30.1 | `ghcr.io/spiffe/spiffe-csi-driver:0.2.7` | Still rendered |
| SPIRE 0.30.1 | `ghcr.io/spiffe/spiffe-helper:0.11.0` | Not rendered in deploy-value mode |
| SPIRE 0.30.1 | `ghcr.io/spiffe/spire-agent:1.15.3` | Still rendered; not equivalent to embedded executable |
| SPIRE 0.30.1 | `ghcr.io/spiffe/spire-controller-manager:0.7.0` | Still rendered |
| SPIRE 0.30.1 | `ghcr.io/spiffe/spire-server:1.15.3` | Still rendered |
| SPIRE 0.30.1 | `registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.15.0` | Still rendered |
| SPIRE 0.30.1 | `registry.k8s.io/kubectl:v1.37.0` | Not rendered in deploy-value mode; not the operator kubectl 1.36.3 |
| Temporal 0.74.0 | `cassandra:3.11.3` | Disabled by source values |
| Temporal 0.74.0 | `docker.io/curlimages/curl:7.85.0` | Disabled by source values |
| Temporal 0.74.0 | `docker.io/grafana/grafana:11.0.0` | Disabled by source values |
| Temporal 0.74.0 | `quay.io/prometheus-operator/prometheus-config-reloader:v0.74.0` | Disabled by source values |
| Temporal 0.74.0 | `quay.io/prometheus/alertmanager:v0.27.0` | Disabled by source values |
| Temporal 0.74.0 | `quay.io/prometheus/prometheus:v2.53.0` | Disabled by source values |
| Temporal 0.74.0 | `quay.io/prometheus/pushgateway:v1.8.0` | Disabled by source values |
| Temporal 0.74.0 | `quay.io/prometheus/node-exporter:v1.8.1` | Disabled by source values |
| Temporal 0.74.0 | `registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.12.0` | Disabled by source values |
| Temporal 0.74.0 | `docker.elastic.co/elasticsearch/elasticsearch:7.17.3` | Disabled by source values |
| Temporal 0.74.0 | `temporalio/admin-tools:1.30.3` | Still rendered |
| Temporal 0.74.0 | `temporalio/server:1.30.3` | Still rendered |
| Temporal 0.74.0 | `temporalio/ui:2.48.1` | Still rendered |

Exact NKE `33fb71b2f4224b470ebc628a9165fc4b92870406` deploy values additionally render
`ghcr.io/spiffe/spire-tpm-plugin-tpm-attestor-server:v1.9.0`. With chart tests included they also
reference `cgr.dev/chainguard/min-toolkit-debug:latest@sha256:9e45e6836c28489a6e57ca1210ec66927e88eca2409e403616a1278e210e86c9`
and `docker.io/smallstep/step-cli:0.30.2` (plus the shared busybox/bash/agent references).
These test-only rows are separate from the non-test runtime selection.

The current `Definition.HelmOutput` supports image-slot bindings only, not deployment values or
overlays. Thus the acquired charts **do not freeze** the source deploy values above. The human
selected the pinned NKE deployment-value mode for non-test runtime image membership; Definition
metadata and its 24 explicit native image outputs record that selection. The chart packages remain
configurable under their existing component contract; no unsupported values field or assertion
that the package fixes deployment topology is introduced. Optional chart-test references remain
separate, not silently promoted to mandatory runtime members.


## Native deployment image pins and media read-back

`definitions/nke-m2/image-catalog` selects the exact NKE deploy tree above, source values in
`controller/deploy/nke-sbom/{postgresql,spire,temporal}/values.yaml` and the three verified chart payloads.
The human-selected non-test runtime set is PostgreSQL (one), SPIRE (eight, including TPM server),
and Temporal (three). Each image is independently required for both native architectures; the
formal v1 AMD64 restriction is expanded here by the sprint's native runtime-image requirement.
Both upstream architectures actually resolved. The original formal-v1 AMD64 operator executable
package remains explicitly AMD64; it is not substituted for chart-side kubectl or SPIRE containers.

The source pin was generated with `catalog pin --commit 33fb71b2f4224b470ebc628a9165fc4b92870406`.
The image lock was generated atomically with `catalog pin-artifacts --all --arch amd64 --arch arm64`.
No artifact digest was hand-authored. The informational source/artifact `tag` is the source-selection
commit, not a claimed upstream image version. Registry refs below and native artifact digests own acquisition.
The Bash declaration uses its exact source-owned index digest with the informational `latest` tag omitted:
Docker's combined tag-plus-digest spelling failed manifest verification, while its canonical digest-only
spelling succeeded unchanged. The compiler uses the existing OCI reference parser to separate index
selection from native manifest transport; no second digest suffix is emitted.

Installed diagnostic `crane 0.22.1`, matching the repository's go-containerregistry dependency version,
read back each of the 24 exact native manifest refs. `Docker v2` below means
`application/vnd.docker.distribution.manifest.v2+json`; `OCI v1` means
`application/vnd.oci.image.manifest.v1+json`. Every selected layer reports its corresponding gzip media
(`application/vnd.docker.image.rootfs.diff.tar.gzip` or `application/vnd.oci.image.layer.v1.tar+gzip`).
No selected manifest reports zstd, uncompressed, or foreign layer media. These are descriptor read-backs,
not complete layer acquisition, scanning, mirroring, signing or independent consumer verification.

| Required native output | Native manifest digest | Manifest media |
| --- | --- | --- |
| `csi-node-driver-registrar-image` | `sha256:1ca23bdbb1504e41233d527e826d02dac98571c45a6cd74c5fb28be485d680fa` | Docker v2 |
| `postgresql-image` | `sha256:9ca5fe8db49a439acdfda55fcbd6ff1c44bc84facf5e775b7e170434d61ce18c` | Docker v2 |
| `spiffe-csi-image` | `sha256:b98766c90387033702ef64a3a4dce1aae252cb741b3ac2ef13f271dd7660bb1e` | OCI v1 |
| `spire-agent-image` | `sha256:0d9c792d7f409b748d3a1fc93fe70b6d1450474c177d2a731ddc36e97a17e9fe` | OCI v1 |
| `spire-bash-image` | `sha256:d49c2b6585ac8e6341adfe8cb214a656914ab5390fb7804a7ed9e90a5b785fba` | OCI v1 |
| `spire-controller-manager-image` | `sha256:fb3bfc8f98f24c769004543d6b760142380c8f2d3a0354391ee2bfdd48039e59` | OCI v1 |
| `spire-oidc-image` | `sha256:32f5e7d7b6329ed9dff97ad374e11d9156c43611ecfcb0693b7ca085b5328cdf` | OCI v1 |
| `spire-server-image` | `sha256:3aa2dce70fc1098d6718a87c629aa8cadb83e4672c71dd63c5575ea5d0603789` | OCI v1 |
| `spire-tpm-server-image` | `sha256:0ba43be0bd5cee7afc54fb351989692ddd07c106111ecda3cf6e9cbe23da137c` | Docker v2 |
| `temporal-admin-tools-image` | `sha256:f24c619953ba5724f36446cca514ba7dd865c2367873cbad70977caf58a02448` | OCI v1 |
| `temporal-server-image` | `sha256:1a71947754cd244b3e6f1beb61e2eb2408e9180f7f092fd30182849625db0ac7` | OCI v1 |
| `temporal-ui-image` | `sha256:6734b3988aa3615286ac8edc83f4d40f10fba587b7109570702efe09478807f9` | OCI v1 |
| `csi-node-driver-registrar-image-arm64` | `sha256:0898bf569379e23a39b9dbd640934c4b9e2563b2c023cbad3e8f686167fd8b99` | Docker v2 |
| `postgresql-image-arm64` | `sha256:b3dec5235745879ca095c08c230e5a53825f7cd795bcee6e6ac88eed91267d28` | Docker v2 |
| `spiffe-csi-image-arm64` | `sha256:6bfd786d4ffd3f7435659a7b923d819a7a0bcc55cd05673908915d433e86c0a2` | OCI v1 |
| `spire-agent-image-arm64` | `sha256:0c94e29f29a13c885294ae73755033040438117ca6c1e257318c5df0868290ac` | OCI v1 |
| `spire-bash-image-arm64` | `sha256:8d29611142077489b9863426dfccf935c5ee9a27a3548d726c16de32692e71e3` | OCI v1 |
| `spire-controller-manager-image-arm64` | `sha256:99dae65b7e207a7d34f9bef2aff901d6cbba40bd5ed31093ebf51e6a063783bc` | OCI v1 |
| `spire-oidc-image-arm64` | `sha256:6adc68d480f285e586edb67f4053bfb8923816a02486bf22d117e29bdee54a78` | OCI v1 |
| `spire-server-image-arm64` | `sha256:b2fb59ac8ec292b2d0e3685008b4a72726fc2298923a569ada9b2700f8b86380` | OCI v1 |
| `spire-tpm-server-image-arm64` | `sha256:d5a0bd84d06ce4a7cd8d0cc5f7f05ca041ed3e1cdd0f8fd129831d871669f2bf` | Docker v2 |
| `temporal-admin-tools-image-arm64` | `sha256:2f90b7c95151bb7a3ddf41749681918f70528fd6c90b160a89e05970b678144a` | OCI v1 |
| `temporal-server-image-arm64` | `sha256:77c887f311db18f086ce2b8ac3144e7f0ad59d73765d01c3e412dea9b8fb6e2c` | OCI v1 |
| `temporal-ui-image-arm64` | `sha256:0e7478fad0eebac6d135a9c1efcc21f50f5026e017064b32f034585b13674788` | OCI v1 |

### Actual acquisition diagnostics

The existing OCI acquisition pipeline fetched complete native Bash closures and independently re-opened
and hashed them at its terminal gate. AMD64 subject `sha256:d49c2b6585ac8e6341adfe8cb214a656914ab5390fb7804a7ed9e90a5b785fba`
had 14,946,289 total closure bytes (2.613 seconds acquisition, 155 ms terminal verification). ARM64 subject
`sha256:8d29611142077489b9863426dfccf935c5ee9a27a3548d726c16de32692e71e3` had 13,933,611 bytes
(1.806 seconds acquisition, 27 ms terminal verification). Both completed with the presentation
`verified acquired bytes, no execution qualification or reproduction claim`.

The initial concurrent local commands used the same default component fetch directory; the ARM64
attempt correctly refused the existing AMD64 closure before output. An isolated diagnostic working
directory passed the ARM64 retry. Generated factory paths are scoped by named output; the separate
native output names preserve that distinction. These macOS acquisition diagnostics do not exercise
Linux authority, container execution, SBOM scanning, publication/signing or consumer verification.

## Downstream-only and checkpoint ownership questions

The pinned controller deploy tree names CAPNICo
`ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller:v0.0.45` in
`controller/deploy/nke-sbom/capnico/capnico.yaml:1615`. That unselected manifest alone does not
establish M2 required membership. The existing catalog's `cluster-api-provider-provisioned`
builds a different NKE project; it is not evidence for that CAPNICo image. Similarly the deploy
overlay enables a fleet-profile content image but intentionally supplies no portable tag/digest:
`controller/deploy/nke-sbom/controller/overlay.yaml:234–251`. Neither value may be invented.

The M2 spec explicitly assigns the NKE build owners the checkpoint list of NKE images outside
the disk (`docs/milestones.md:63`, Epic M2 Goal). A recorded owning list is still missing here.
CAPNICo, provisioned-provider and fleet profiles remain checkpoint/scope questions, not an
automatic expansion to every image-bearing YAML file in the repository. There is no recorded
human deferral of a required row. Unresolved required chart-image rows prevent full M2 acceptance.
