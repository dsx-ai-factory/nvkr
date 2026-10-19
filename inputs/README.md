# Authored inputs

This directory owns the repository's NVKE build and release inputs. Definitions select named outputs
from pinned catalog producers; Environments select execution and publication policy. The maintained
release candidate starts at [the NVKE Definition](definitions/nvke/definition.yaml).

```text
inputs/
  definitions/   # Release, demo variants and focused diagnostics
  catalogs/      # Shared producer owners, including explicit version/packaging variants
  environments/  # Execution, credentials, signing and publication policy
  policies/      # Independently authored verification policy
  testing/       # Downstream nightly coordinator configuration
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
| [`definitions/nvke/definition.yaml`](definitions/nvke/definition.yaml) | [`environments/github-release.yaml`](environments/github-release.yaml) | Maintained 69-output release candidate; existing main-only signed publication |
| Same NVKE Definition | [`environments/github-development.yaml`](environments/github-development.yaml) | Separate signed development bundle and publication namespace |
| [`definitions/demo/arm64-nvidia-tcg.json`](definitions/demo/arm64-nvidia-tcg.json) | [`environments/local-arm64-tcg.json`](environments/local-arm64-tcg.json) | Local 32-output NVIDIA OVN demo |
| [`definitions/demo/arm64-upstream-tcg.json`](definitions/demo/arm64-upstream-tcg.json) | Same local TCG Environment | Local 32-output upstream OVN diagnostic demo |
| [`definitions/demo/arm64-nvidia-kvm.json`](definitions/demo/arm64-nvidia-kvm.json) | [`environments/github-nightly-nvidia.json`](environments/github-nightly-nvidia.json) | Named NVIDIA nightly producer; current content differences remain explicit |
| [`definitions/demo/arm64-upstream-kvm.json`](definitions/demo/arm64-upstream-kvm.json) | [`environments/github-nightly-upstream.json`](environments/github-nightly-upstream.json) | Separately signed upstream nightly producer |
| [`definitions/diagnostics/os-amd64.yaml`](definitions/diagnostics/os-amd64.yaml) | [`environments/local-linux-amd64-kvm.yaml`](environments/local-linux-amd64-kvm.yaml) | Native AMD64 plain-disk diagnostic |
| [`definitions/diagnostics/os-arm64.yaml`](definitions/diagnostics/os-arm64.yaml) | [`environments/local-linux-arm64-kvm.yaml`](environments/local-linux-arm64-kvm.yaml) | Native ARM64 plain-disk diagnostic |
| [`definitions/diagnostics/fde-amd64.yaml`](definitions/diagnostics/fde-amd64.yaml) | [`environments/github-diagnostics.yaml`](environments/github-diagnostics.yaml) | Build-only public-bootstrap FDE diagnostic; existing branch allowlist |

The native local KVM Environments retain their existing diagnostic capacity and utility pins. The
table does not imply that they can run the complete nightly demo unchanged. TCG/KVM differences affect
producer inputs as well as runtime admission, so the explicit demo Definitions remain separate.

## Native host demo inputs

The additional [Mac](definitions/demo/native-mac/README.md) and
[Linux](definitions/demo/native-linux/README.md) selections use one
[shared native tool catalog](catalogs/tools/native/README.md). Their local policies
are `environments/local-native-mac.json` and `environments/local-native-linux.json`.
QCOW builds use NVKR's existing disk-image Definitions listed above.
The recorded Mac runtime uses an existing QCOW, upstream OVN and TCG. These inputs
do not imply fresh-machine or full native Linux runtime acceptance.

The opt-in [Ubuntu 24 node-image inputs](definitions/diagnostics/ubuntu24/README.md) select
native AMD64 and ARM64 plain local builds, plus plain or public-bootstrap FDE GitHub builds.
The hosted variants use the matching `environments/github-ubuntu24-<arch>.yaml` Environment
and remain build-only; they do not change maintained release membership or Ubuntu 26 defaults.

## Focused inputs and examples

`definitions/diagnostics/demo-arm64/` retains the existing components, OS, profiles, providers,
simulator, site and installer-tool loops used by the local launcher. Additional controller and OVN
Definitions retain focused producer/packaging contracts. The standalone OVN diagnostic keeps its
[separate Environment](environments/github-ovn-diagnostics.yaml). These are transitional selections
with explicit dispositions in the migration map, rather than silently expanded full-product Runs.

The [three-output tutorial](../docs/examples/nvke-tutorial/README.md) and
[deploy example](../docs/examples/nvke-deploy/README.md) keep their example-specific Environments
beside their Definitions. Other authoring and pin-set teaching fixtures remain in `docs/examples/`.

## Maintenance

Resolve catalog paths relative to the containing Definition. Keep registry, lock and patch ownership
together. Maintain pins through `catalog pin`/`catalog bump`; use
[catalog maintenance](../docs/catalog-maintenance.md) for the complete workflow. Definition reuse is
explicit producer selection, without inheritance or execution-time output filtering.

`make workflows-generate` regenerates the maintained release, development, nightly and qualification
bundles. `make workflows-check` checks their exact recorded inputs. Generated bundles remain under
`.github/`; Run records and release artifacts remain with their execution/publication owners.

The [qualification policy](policies/nvke-qualification.yaml) and
[nightly coordinator configuration](testing/nightly-profiles.json) have independent owners and scopes.
[Reconciliation](../docs/adr/reconciliation.md) remains the evidence ledger for implementation and
external acceptance.
