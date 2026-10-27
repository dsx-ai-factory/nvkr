# AICR deployment bundles

These Definitions build NVIDIA AI Cluster Runtime (AICR) from its pinned source,
render its deployment bundles, and deliver each bundle through NVKR's ordinary
Run and Release workflow as a `bundle-oci` artifact. The catalog contains no
AICR-specific NVKR profile or independent deployment lifecycle.

| Definition | Upstream recipe coordinate | Scope |
| --- | --- | --- |
| [definition.yaml](definition.yaml) | `generic / gb300 / ubuntu / training` | Bare-metal GB300 NVL72 with self-managed Kubernetes |
| [definition-vr200-preview.yaml](definition-vr200-preview.yaml) | `rke2 / vr200 / ubuntu / training` | Upstream Vera Rubin VR200 **Preview**, with its host-image prerequisites |
| [definition-gb200-eks-reference.yaml](definition-gb200-eks-reference.yaml) | `eks / gb200 / ubuntu / training` | Optional cloud reference; **not GB200 bare-metal support** |

The three deployment-bundle Definitions each select six independent named outputs: `argocd`, `argocd-helm`,
`flux`, `helm`, `helmfile`, and `terraform`. These are the six deployers implemented
at [AICR commit fa705ec263a54eeb0ca25cb24c9f20c5d8504dca](https://github.com/NVIDIA/aicr/tree/fa705ec263a54eeb0ca25cb24c9f20c5d8504dca).
The default Definition selects ARM64 rendering on a native generic hosted runner.
The producers also admit AMD64 rendering when an independently authored Definition
selects it. A renderer platform does not prove cluster compatibility.

The pinned upstream rejects `generic / gb200 / ubuntu / training` with
`INVALID_REQUEST`: it has no matching recipe. Supporting GB200 bare metal requires
an upstream recipe or a separately reviewed pinned external AICR catalog; the EKS
recipe must not be relabeled as bare metal. No `VR` wildcard is accepted: the
available coordinate is specifically `vr200`.

## Pinned build contract

[AICR catalog](../../catalogs/aicr/registry.yaml) owns the explicit source producers.
The generated [source lock](../../catalogs/aicr/pins.lock.yaml) binds the commit, tree and
source epoch. The Go 1.27.2 toolchain is the existing immutable multiarchitecture
image `golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c`.
The initial lock was generated, without manual editing, using:

```sh
bin/nvkr catalog pin --dir inputs/catalogs/aicr \
  --commit fa705ec263a54eeb0ca25cb24c9f20c5d8504dca --timeout 5m aicr
```

The fetch phase downloads and verifies the pinned Go module closure, vendors it,
and rejects any change to `go.mod` or `go.sum`. The producer compiles AICR offline,
uses explicit recipe criteria without collecting a snapshot, renders a local
bundle, and runs AICR's local verification. It archives the complete generated
bundle with sorted relative paths, normalized ownership and source-epoch mtimes,
then uses `gzip -n`. The archive has no `./` root entry. Generated file contents,
including checksums, the resolved recipe and `bundle-info.yaml`, are preserved.
The executable used to generate the bundle is not included in the archive.
Generic bundles are bounded to 512 MiB compressed and expanded tar bytes, with
100,000 entries and 4,096 bytes per path. Links, special files, privileged modes
and unsupported extended metadata are rejected.

NVKR authenticates the selected tarball, publishes it as one OCI bundle layer,
and emits its file inventory through the ordinary evidence path. AICR is not
asked to push or sign; NVKR owns publication and Release membership. The
Definitions reuse the maintained NVKE catalog's action and cosign pins only;
they select no NVKE producer.

## Generate and inspect

Run from the repository root after `make build`. Generate into a scratch root to
review the workflow before choosing to retain it:

```sh
aicr_bundle_root=$(mktemp -d /tmp/nvkr-aicr-bundle.XXXXXX)
bin/nvkr run generate \
  --definition inputs/definitions/aicr/definition.yaml \
  --environment inputs/environments/aicr-github.yaml \
  --engine github --root "$aicr_bundle_root"
bin/nvkr run generate \
  --definition inputs/definitions/aicr/definition.yaml \
  --environment inputs/environments/aicr-github.yaml \
  --engine github --root "$aicr_bundle_root" --check
```

[aicr-github.yaml](../../environments/aicr-github.yaml) owns the separate `aicr`
generated namespace and admits the `nvkr-run-aicr.yml` default-branch signer. Its
configured routes are
`ghcr.io/dsx-ai-factory/nvkr/aicr-candidate/<output-name>@sha256:<digest>`.
These settings are not a publication record.
[aicr-github-dev.yaml](../../environments/aicr-github-dev.yaml) is a build-only
alternative; use a separate scratch root for its development workflow.
[aicr-local-arm64.yaml](../../environments/aicr-local-arm64.yaml) selects local Docker
rendering without publication or signing. With Docker running on an ARM64 host
and the repository's pinned Go version available, build all six GB300 outputs:

```sh
bin/nvkr run local \
  --definition inputs/definitions/aicr/definition.yaml \
  --environment inputs/environments/aicr-local-arm64.yaml \
  --attempt aicr-gb300-local-1 --jobs 1
```

Use a fresh attempt identifier for each Run. The source-fetch phase needs network
access; producer execution is offline. Successful execution records the
`unqualified-local-execution` lane and does not sign or publish; previews remain
`static-unqualified`. Authority-bearing execution and consumer verification
retain their supported Linux runtime requirements.

Use the same generation commands with the explicit VR200 Preview or GB200 EKS
reference Definition to review that selection. Changing recipe criteria, AICR
settings, source pins or the deployer is a producer change that enters the build
identity.

## Native Helm OCI option

[definition-native-helm.yaml](definition-native-helm.yaml) selects one additional
GB300 producer for AICR's genuine Argo CD Helm root chart. It keeps all generated
files inside one chart directory and uses the existing `helm-oci` package path.
Its output is named `aicr-bundle`, matching upstream `Chart.yaml` and the Helm OCI
repository basename. Its configured repository is
`ghcr.io/dsx-ai-factory/nvkr/aicr-candidate/aicr-bundle`.

This option establishes native Helm packaging; **Argo CD reconciliation remains
unverified, with an unresolved upstream OCI path limitation**. The generated
GB300 chart includes child Applications that select paths such as
`006-nodewright-customizations` inside the same published chart. Current
[Argo CD OCI documentation](https://argo-cd.readthedocs.io/en/stable/user-guide/oci/#declarative)
requires `path: .` for Helm chart layers. That conflicts with the pinned AICR
generator's subpath approach and needs an upstream fix or evidence from the
intended Argo CD version before this is used for deployment. Its external OCI
chart references also require reconciliation validation; successful Helm
templating alone does not resolve controller fetch behavior.

For inspecting rendered templates, the upstream chart's `repoURL` value is the
parent namespace, for example
`oci://ghcr.io/dsx-ai-factory/nvkr/aicr-candidate`. The templates append
`aicr-bundle` themselves and default `targetRevision` to the chart version,
currently `0.0.0-nvkr.fa705ec263a5`. This matches the
[pinned AICR templates](https://github.com/NVIDIA/aicr/blob/fa705ec263a54eeb0ca25cb24c9f20c5d8504dca/pkg/bundler/deployer/argocdhelm/argocdhelm.go).
These are rendering inputs, not deployment instructions. Independent consumer
verification and the signed Release determine which published bytes are
acceptable.

Only GB300 is selected by this optional Definition. Publishing other coordinates
under the same upstream chart name and source-derived version would collide at
the Helm discovery tag; a reviewed naming or namespace design is required before
adding those publications.
Changing recipe arguments or renderer settings under the same source pin also
requires a distinct chart version or repository when it changes chart bytes.

## Consumption and deployment limits

`helm` produces per-component Helm values and deployment scripts, not one Helm
chart. `argocd-helm` does produce a root chart, but this Definition transports its
complete directory as a generic OCI bundle. Neither output is advertised as a
native Helm OCI chart. Extract the authenticated bundle before following its
upstream instructions. The separate native Helm Definition above packages the
root chart with standard Helm OCI media types and retains the documented
deployment limitations.

The local GitOps renders retain upstream repository placeholders: Argo CD and
Flux require the consumer's Git or OCI delivery configuration, while the Argo CD
Helm chart requires its repository and revision values. They cannot directly
consume the NVKR generic bundle reference as an already configured deployment.
Any edits to generated files create new bytes; preserve the delivered artifact
and manage deployment-specific adaptations separately. For product-specific
build defaults, author the reviewed AICR arguments in the catalog so NVKR binds
them into the producer identity.

Chart versions are selected by the pinned AICR catalog, but upstream chart bytes
and container images are not included or mirrored by these producers. This is
not an air-gap payload or an authenticated deployed dependency closure. AICR's
`--vendor-charts` requires a separately pinned Helm executable and a captured
chart-fetch closure; it is intentionally absent from the offline producer.

AICR also emits deployment and upgrade guidance with these bundles. In
particular, driver/DRA eviction coordination and training-workload protection
need reviewed deployment choices, and existing installations may require the
steps in `UPGRADING.md`. The maintained inputs do not invent node labels,
storage classes, fleet topology or workload selectors. See the pinned upstream
[GB300 bare-metal recipe](https://github.com/NVIDIA/aicr/blob/fa705ec263a54eeb0ca25cb24c9f20c5d8504dca/recipes/overlays/gb300-generic-ubuntu-training.yaml)
and [VR200 setup guide](https://github.com/NVIDIA/aicr/blob/fa705ec263a54eeb0ca25cb24c9f20c5d8504dca/docs/integrator/rke2-vr200-setup.md)
for hardware, OS, driver, kernel and Kubernetes prerequisites.

## Recorded validation

The complete GB300 Definition ran through NVKR locally as attempt
`aicr-gb300-local-20261018-r2`. All six outputs completed source preparation,
offline container builds, terminal observation, file inventories, OCI packaging
and independent final observation. The immutable Run record reports `complete`
in lane `unqualified-local-execution`; the CLI returned exit code 0 with complete cleanup. Its Plan
digest is `sha256:34a006d3caa099af8ec284c77a4489cc6a22d61edb144650f6eaef9bbf2c6b33`.
This local Run did not publish or sign artifacts.

The AICR binary retained by that completed Run, reporting
`0.0.0-nvkr.fa705ec263a5`, was reused in the pinned Linux ARM64 Go 1.27.2 image
offline against all three declared coordinates and all six formats. Both
passes resolved every recipe, rendered all 18 selections and passed
`aicr verify` on all 36 generated directories. Repeated recipe bytes, rendered
file bytes and final normalized gzip-tar bytes matched for every pair at the
fixed source epoch. All six GB300 archives also matched the completed Run's
retained producer archives byte-for-byte. NVKR's bundle delivery path admitted
all 18 Linux-rendered archives into local OCI layouts. A separate recipe probe
rejected the unsupported generic GB200 coordinate.

The same retained binary separately rendered and verified the optional native
Helm chart offline; the native producer's archive command packaged its complete
directory. That chart passed Helm 4.2.3 lint, template and package commands with
its required repository input. NVKR's existing Helm delivery path packaged the
Helm-created archive into a local OCI layout and exported identical archive
bytes; Helm lint also accepted the exported archive. This separate rendering
and delivery smoke does not claim a complete native Helm Run.

These results establish source preparation, rendering, local checksum checks
and the stated packaging paths. They do not establish NVKR publication, Argo CD
reconciliation, cluster deployment, hardware performance, qualification or
acceptance. VR200 retains upstream Preview status.
