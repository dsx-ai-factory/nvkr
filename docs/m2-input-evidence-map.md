# M2 maintained input evidence map

The source candidate is under review as PR #95, stacked on source-fetch PR #94,
publication PR #92 and evidence PR #91. Those open, under-review dependencies supply
the capture schema and scoped trusted source-fetch interfaces;
the native capture regression runs against those inherited interfaces. Exact-head local
validation is recorded in the pull request separately from production acceptance.
This is an input/configuration record. It grants no runtime, publication, signer or M2 acceptance claim.
It is an evergreen handoff; dated source/admission observations belong in the separate
[October 5 diagnostic record](evidence/2026-10-05-m2-input-acquisition.md).

The refreshed catalog image-pin owner uses the existing pinned OCI library instead of
Docker's manifest JSON projection. Fixture tests cover both native media formats,
ambiguity, platform/digest/size substitution, metadata bounds and cancellation.
External live acquisition/read-back is not recorded in this repository and is not
claimed here. The maintained native digests below are lock data, not a retained
live diagnostic or production runtime approval.

The source-owned PostgreSQL index `sha256:b74b23f091dcc87041bcae557d840c36458af46b955279cb13aedec4c32d7f71`
and SPIRE Bash index `sha256:90041f375e30f41aa7e0390075d8a69dc61900771d52fa98d63ee5d03d866a58`
differ from all four selected native child pins. The generic binding owner now retains
both exact parent digests in the machine-managed lock and freezes parent/native/platform
constraints in full IR and logical content identity. Acquisition authenticates actual
parent-to-child membership and consumes the original selected native image before output.
Missing or substituted descriptions still fail `CodeUnverifiedRef`; no source index is
discarded or replaced by a child. `TestMaintainedM2DeploymentImageMembership` exercises
public catalog loading and Resolve/Compile for all 24 native selections. The separate
34-output Git integration requires explicit opt-in and actual pinned-source access.
These contracts do not supply missing production execution classes or inspector profiles.

## Candidate output selections and requirement ownership

The combined Definition now selects 34 candidate outputs: the original ten below plus twelve
non-test deployment image members for each native architecture. The human-authorized image mode
is the exact pinned NKE deployment values, not unmodified chart defaults. This does not establish
runtime/publication completion or resolve the separately owned external-NKE-image checkpoint.
`docs/epics-v1-spec.md`, Epic M2 Goal/Acquisition/acceptance criteria 2–3, and
`docs/milestones.md:36–55` require external NKE and third-party image rows in addition to these packages.
The formal v1 scope is AMD64. The additional ARM64 disk and runtime-image rows are
out-of-spec sprint candidates, not a ratified change to M2 acceptance. Their inclusion
preserves the requested candidate matrix; each disk still has independent execution
and evidence obligations. Deployment-value membership is human-transcribed from the
pinned tree, not derived or machine-verified by the literal membership regression.

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
`TestMaintainedM2NativeCaptureGraph` asserts both native target/toolchain architectures,
the capture opt-in, carrier path, exact source-owned material kinds and native producer bindings
against the inherited B/C/A schema. This graph test does not establish a successful
capture-enabled installer bake or a live evidence join.

## Runtime, inspector, registry and signer map

| Required field or closure | Current authoritative configuration | Missing evidence |
| --- | --- | --- |
| Disk toolchain host architecture | Separate AMD64/ARM64 source toolchain maps and native allowed-host platforms | Existing catalog digests are local identities; approved published per-native OCI manifests remain absent |
| Packer and preparation tools | `toolchains/nkd-packer/toolchain.lock.json` and Dockerfile | `images` remains null; BUSL redistribution and vulnerability review approvals remain `not-recorded` |
| Full inspector tools | `toolchains/nkd-disk-inspect/Dockerfile` and source lock | Updated helper closure, native execution, exact image identity and executable measurements required |
| Syft inspector scanner | Source lock selects v1.51.1 and schemaVersion `16.1.10` | Native image execution and a retained external acquisition record remain separate |
| Inspector architecture | Profile key must equal native `linux/amd64` or `linux/arm64` execution host | Both measured native full profiles and capacity grants remain required |
| Scratch and memory | B's bounded private-volume inspection contract | Actual final filesystem/inventory/document sizes and host/daemon headroom must be measured; installed-package inventory is insufficient |
| Registry namespace | M2 candidate Environment explicitly authors `ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate` | Separate from production `cwe`/`nkectl` routes; live blob limits, retention and package access not verified here |
| Registry credential | Existing `github:GITHUB_TOKEN` reference | Runtime values are not inspected or serialized; production credential admission/read-back required |
| Signing identity | Existing main `nkd-run.yml` identity regex and Actions issuer | Existing policy copied byte-for-byte; protected publication environment and current keyless run remain unverified |
| Private source transport | Exact `https://github.com/dsx-ai-factory/nke` CLI/controller and `https://github.com/dsx-ai-factory/nke.git` OS keys, sharing the existing `github:NKE_SOURCE_TOKEN` mirror mapping | Source credential must remain in trusted fetch, outside candidate build execution |
| Generated runner route | ADR-18 generic-hosted-only rule | Native KVM capability for both routes remains unresolved; historical ARC diagnostics cannot establish generated production admission |

`definitions/nke-m2/environment-github.yaml` records the separate candidate publication prefix, existing signing policy and
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
and complete material closure. The dated snapshot records NKE #1211 as open at `b30117c`;
the deployment selection `33fb71b` is on NKE main through merged #1096. Pinning establishes
bytes, not source-owner release acceptance. Final-parent graph
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
  nke-os
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin \
  --dir definitions/nke-m2/os-catalog \
  --commit b30117c25b2dcdf6c16021dae88a2eafdafcae61 --check \
  nke-cni-calico-source
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin \
  --dir definitions/nke-m2/os-catalog \
  --commit b30117c25b2dcdf6c16021dae88a2eafdafcae61 --check \
  nke-kubeadm-distro-source
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin \
  --dir definitions/nke-m2/os-catalog \
  --commit b30117c25b2dcdf6c16021dae88a2eafdafcae61 --check \
  nke-spire-agent-source
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin \
  --dir definitions/nke-m2/catalog --tag v4.1.4 helm
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin \
  --dir definitions/nke-m2/catalog --tag v1.36.3 kubectl
GOTOOLCHAIN=local go run ./cmd/nkdctl catalog pin-artifacts \
  --dir definitions/nke-m2/image-catalog --all --arch amd64 --arch arm64
toolchains/nkd-packer/build.sh --check
```

Each source check invokes the existing one-source pin owner separately and requires
authorized access to the exact NKE repository. Existing
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
The Linux pipeline accessor regression can be run explicitly:

```sh
go test ./pkg/nkd/pipeline -run '^TestDiskInspectionSelectionProjectsAuthenticatedLogicalRuntime$' -count=1
```

This repository does not contain `nkd-inspector-closure-diagnostic.yml`. No committed
native workflow run is established by that command. The controlled accessor test does
not exercise a full SDK scan or produce measured native image pins.

The selected NKE #1211 source pin is an explicit review candidate, not
main-reachable approval or protected-ref evidence. Its dated GitHub state is
recorded separately and must be refreshed before accepting source approval.
The existing missing execution-class/profile capability gate still disables candidate
launch; the publication/signing configuration does not grant source approval.
No historical host-probe or installed-package replay is claimed as current evidence
here: those observations lack a retained record for this candidate. The checked
source locks contain no approved published native Packer/inspector image references.

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

## Chart-package membership limits

Unretained historical chart-default comparisons do not establish image selection
or execution evidence and are not reproduced as facts here. The required native
image set remains the explicit source-selected Definition membership below.
Optional chart-test references are separate from non-test runtime membership;
there is no retained render/acquisition evidence for either mode in this record.

The current `Definition.HelmOutput` supports image-slot bindings only, not deployment
values or overlays. Acquired charts therefore do not freeze the human-selected NKE
deployment values. The 24 explicit native image outputs record that selection;
the chart packages remain configurable under their normal component contract.
No unsupported values field or deployment-topology assertion is introduced.

## Native deployment image lock data

`definitions/nke-m2/image-catalog` selects the exact NKE deploy tree above, source values in
`controller/deploy/nke-sbom/{postgresql,spire,temporal}/values.yaml` and the three owning chart lock selections.
The human-selected non-test runtime set is PostgreSQL (one), SPIRE (eight, including TPM server),
and Temporal (three). Each image is independently required for both native architectures; the
formal v1 AMD64 restriction is expanded here by the sprint's native runtime-image requirement.
Both architecture subjects are present in the lock, without a retained live acquisition record here.
The original formal-v1 AMD64 operator executable
package remains explicitly AMD64; it is not substituted for chart-side kubectl or SPIRE containers.

The source pin was generated with `catalog pin --commit 33fb71b2f4224b470ebc628a9165fc4b92870406`.
The image lock was generated atomically with `catalog pin-artifacts --all --arch amd64 --arch arm64`.
No artifact digest was hand-authored. The informational source/artifact `tag` is the source-selection
commit, not a claimed upstream image version. Registry refs below and native artifact digests own acquisition.
The Bash declaration uses its exact source-owned index digest with the informational `latest` tag omitted.
The parent-binding maintenance follow-up used the existing owner in one transaction:
`catalog pin-artifacts --dir definitions/nke-m2/image-catalog postgresql-image spire-bash-image`.
It added exactly two authenticated `imageParentDigest` fields and left every existing
native digest and source pin unchanged. A second identical invocation reported no changes.
The dated diagnostic records this command and no-op lock hash. The catalog now admits
the complete image selection; runtime production admission remains independently gated.

The following digest table transcribes the maintained lock. Its historical media annotations have no
retained external diagnostic and are descriptive, not verified live acquisition evidence. `Docker v2` means
`application/vnd.docker.distribution.manifest.v2+json`; `OCI v1` means
`application/vnd.oci.image.manifest.v1+json`. Layer/media compatibility must be remeasured and recorded
before acquisition acceptance. This table does not prove layer acquisition, scanning, mirroring,
signing or independent consumer verification.

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

### External acquisition evidence gap

No complete Helm, kubectl or Bash acquisition diagnostic with exact source, commands and terminal
read-back is retained in this repository for this candidate. Historical private timings and read-backs
are intentionally not presented as recorded evidence. The generic parent-binding source
change and controlled TLS fixtures establish engineering contracts, not an upstream Bash
layer acquisition or production release. Contract fixtures and the lock data do not fill
this operational evidence gap.

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
