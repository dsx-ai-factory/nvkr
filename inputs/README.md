# Authored inputs

This directory owns the repository's NVKE, NVSentinel and AICR build and release inputs. Definitions select named outputs
from pinned catalog producers; Environments select execution and publication policy. The maintained
production/nightly candidate starts at [the NVKE release composite](definitions/nvke/gpu-site-release.composite.yaml),
which adds the derived inventory to the five NVKE families and GB200 AICR member. Development uses the same
pinned members through the [payload-only composite](definitions/nvke/gpu-site.composite.yaml). The
[three maintained bundles](definitions/nvke/README.md#gpu-site-composite) cover those selections.

```text
inputs/
  definitions/   # Release, demo variants and focused diagnostics
  catalogs/      # Shared producer owners, including explicit version/packaging variants
  environments/  # Execution, credentials, signing and publication policy
  policies/      # Verification policy and maintained generation selections
  testing/       # Retained downstream test inputs; maintained hosted nightly configuration lives in demos/
  qualification/ # Committed NVKE plan fixtures and their provenance for the NVKE hardware E2E
  experiments/   # Producers outside the maintained release catalog
```

The relocation preserves the existing output membership, source pins, profile payloads and policy
values. It does not establish release/demo content parity or operational acceptance. The
[migration map](../docs/input-layout-migration.md) records every former entry point, retained
diagnostic and remaining consolidation decision. [Catalog ownership](catalogs/README.md) records the
producer mapping and the variants that still need reconciliation.

## Primary entry points

Paths in this table are relative to `inputs/`. JSON and YAML are both admitted authored formats;
the move preserves each file's existing format.

| Definition | Environment | Scope |
|---|---|---|
| [`definitions/nvke/gpu-site-release.composite.yaml`](definitions/nvke/gpu-site-release.composite.yaml) | [`environments/github-nvke-gpu-site.yaml`](environments/github-nvke-gpu-site.yaml) | 84-output [GPU site release composite](definitions/nvke/README.md#gpu-site-composite) of the five families, GB200 AICR bundles and package-derived inventory; one Run and one Release |
| [`definitions/nvke/gpu-site.composite.yaml`](definitions/nvke/gpu-site.composite.yaml), 83 payload outputs | [`environments/github-development.yaml`](environments/github-development.yaml) | The repository's fixed signed development bundle (`nvkr-dev.yml`) and disposable publication namespace |
| Same release composite | [`environments/github-nvke-nightly-gpu-site.yaml`](environments/github-nvke-nightly-gpu-site.yaml) | Floating-source signed NVKE candidate (`nvkr-run-nvke-nightly.yml`), manual dispatch |
| [`definitions/demo/arm64-nvidia-tcg.json`](definitions/demo/arm64-nvidia-tcg.json) | [`environments/local-arm64-tcg.json`](environments/local-arm64-tcg.json) | Local 32-output NVIDIA OVN demo |
| [`definitions/demo/arm64-upstream-tcg.json`](definitions/demo/arm64-upstream-tcg.json) | Same local TCG Environment | Local 32-output upstream OVN diagnostic demo |
| [`definitions/demo/amd64-nvidia-local-kvm.json`](definitions/demo/amd64-nvidia-local-kvm.json) | [`environments/local-amd64-kvm.json`](environments/local-amd64-kvm.json) | Native AMD64 local build and Go site-bootstrap smoke inputs; build and runtime acceptance require a completed Run |
| [`definitions/diagnostics/os-amd64.yaml`](definitions/diagnostics/os-amd64.yaml) | [`environments/local-linux-amd64-kvm.yaml`](environments/local-linux-amd64-kvm.yaml) | Native AMD64 plain-disk diagnostic |
| [`definitions/diagnostics/os-arm64.yaml`](definitions/diagnostics/os-arm64.yaml) | [`environments/local-linux-arm64-kvm.yaml`](environments/local-linux-arm64-kvm.yaml) | Native ARM64 plain-disk diagnostic |
| [`definitions/diagnostics/fde-amd64.yaml`](definitions/diagnostics/fde-amd64.yaml) | [`environments/github-diagnostics.yaml`](environments/github-diagnostics.yaml) | Build-only public-bootstrap FDE diagnostic; existing branch allowlist |
| [`definitions/kubernetes-conformance-arm64/definition.yaml`](definitions/kubernetes-conformance-arm64/definition.yaml) | [`environments/local-conformance-tools-arm64.yaml`](environments/local-conformance-tools-arm64.yaml) | Local four-output ARM64 Kubernetes conformance tools; [1.34 variant](definitions/kubernetes-conformance-arm64/demo-1.34/README.md) uses the same Environment |

The native local KVM Environments retain their existing diagnostic capacity and utility pins. The
table does not imply that they can run the complete nightly demo unchanged. TCG/KVM differences affect
producer inputs as well as runtime admission, so the explicit demo Definitions remain separate.

## Native host demo inputs

The additional [Mac](definitions/demo/native-mac/README.md) and
[Linux](definitions/demo/native-linux/README.md) selections use one
[shared native tool catalog](catalogs/tools/notes/tools-native-README.md). Their local policies
are `environments/local-native-mac.json` and the matching `environments/local-<arch>-kvm.json`.
Host tool Definitions live in `definitions/demo/tools/<os>-<arch>.json`.
QCOW builds use NVKR's existing disk-image Definitions listed above.
The recorded Mac runtime uses an existing QCOW, upstream OVN and TCG. These inputs
do not imply fresh-machine or full native Linux runtime acceptance.

The opt-in [Ubuntu 24 node-image inputs](definitions/diagnostics/ubuntu24/README.md) select
native AMD64 and ARM64 plain local builds, plus plain or public-bootstrap FDE GitHub builds.
The hosted variants use the matching `environments/github-ubuntu24-<arch>.yaml` Environment
and remain single-disk, build-only diagnostics. The maintained `disks-ubuntu24` family above selects
the same four disks for signed publication; no publication is recorded yet.

## Focused inputs and examples

The [dedicated NVSentinel catalog](catalogs/nvsentinel/README.md) declares OCI images, Helm, executable
binaries and DEB packages. Its focused callers live under `definitions/nvsentinel/`, with local execution
policies under `environments/nvsentinel/`. The [NVKE OS material experiment](experiments/nvsentinel/README.md)
keeps separate Debian producer copies with byte-identical commands, matching source declarations and pins,
and the existing composer/Packer folder handoff. These development inputs do not
change the maintained NVKE release or demonstrate current NVSentinel build/runtime acceptance.

The [AICR example](definitions/aicr/README.md) selects six deployment-bundle formats
from one pinned catalog with a local build-only Environment. Its GB200 NVKE Kubeflow
training Definition selects three of those formats from the same catalog.

`definitions/diagnostics/demo-arm64/` retains the existing components, OS, profiles, providers,
simulator, site and installer-tool loops used by the local launcher. Additional controller and OVN
Definitions retain focused producer/packaging contracts. The standalone OVN diagnostic keeps its
[separate Environment](environments/github-ovn-diagnostics.yaml). The focused OS and source-built
installer-tool loops remain supported. The other selections retain their explicit transitional or
retirement dispositions in the migration map; none silently expands into a full-product Run.

The [three-output tutorial](../docs/examples/nvke-tutorial/README.md) and
[deploy example](../docs/examples/nvke-deploy/README.md) keep their example-specific Environments
beside their Definitions. The [module-tags build-only development Environment](../docs/examples/nvke-module-tags/environment-github-dev.yaml)
remains supported per the migration map. Other authoring and pin-set teaching fixtures remain in `docs/examples/`.

## NVKE hardware E2E plan

`qualification/` holds the copy of NVKE's `qualify plan` output that the NVKE hardware E2E workflow (`.github/workflows/nvkr-nvke-hardware-e2e.yml`)
runs against, with a provenance record naming the NVKE run, commit, tag, retrieval date and the SHA-256 of both the
original artifact and the committed bytes. The fixture masks the NGC organization identifier in its `nvcr.io`
references. It is a description of NVKE's plan, not an authored Definition: the lane substitutes NVKR-built references
for the slots NVKR produces, and leaves NVKE's imager and tool pins as they are. Replace the fixture only by
retrieving a newer NVKE plan and updating both files in one reviewed change.

An outputs manifest for the lane's `nvkr-outputs` source also belongs directly under `qualification/`, added in a
reviewed change after a producer run. Each node image entry names its verification, `http-get` or `s3-readback`
with the publication run it cites; `scripts/hardware-e2e-substitute-plan.sh` owns the schema.

## Maintenance

Resolve catalog paths relative to the containing Definition. Keep registry, lock and patch ownership
together. Maintain pins through `catalog pin`/`catalog bump`; use
[catalog maintenance](../docs/catalog-maintenance.md) for the complete workflow. Definition reuse is
explicit producer selection, without inheritance or execution-time output filtering.

`make workflows-generate` and `make workflows-check` own exactly three maintained composite bundles:
`nvkr-dev`, `nvkr-run-nvke` and `nvkr-run-nvke-nightly`. `make nvke-generate` and `make nvke-check`
select production only. Family files remain pinned members; their standalone Environments and the old
combined input are comparison fixtures, not maintained releases.

The [qualification policy](policies/nvke-gpu-site-qualification.yaml) and
[nightly build/test workflow](../.github/workflows/nvkr-nightly.yml) have independent owners and scopes.
[Reconciliation](../docs/adr/reconciliation/README.md) remains the evidence ledger for implementation and
external acceptance.
