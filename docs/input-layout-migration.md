# Input layout migration and remaining consolidation

The ownership relocation from upstream/main `f260c48570eacc0e2112b6a74eb07150ff0e828e` preserves all
21 Definitions and 14 Environments. Nineteen Definitions and nine Environments now live under `inputs/`;
the other two Definitions and five Environments remain teaching examples under `docs/examples/`.
Source versions, output membership, producer commands, lock bytes and policy values are unchanged by
this move. Authored references and generated identities must reflect their new paths.

The conditional end state is eight supported Definitions, nine catalog owner families and eight
supported Environments. The nine owner directories currently contain **19 distinct catalogs**, plus
one separate experimental catalog; grouping them does not merge their different source baselines.
Neither relocation nor this plan establishes completed consolidation, implemented parity or OSS alpha
acceptance. The [input index](../inputs/README.md) describes current entry points.

The engineering contracts remain [ADR-19](adr/0019-clusterdefinition-and-provisioning-adapter-boundary.md),
[ADR-21](adr/0021-generated-pipeline-maintenance-and-lifecycle.md) and
[ADR-22](adr/0022-catalog-pinning-and-the-lock-contract.md); [reconciliation](adr/reconciliation.md)
owns current implementation and external evidence. Product source, output membership and publication
policy changes require their own explicit review, even when performed during this migration.

## Retained candidate exception

The [NVSentinel candidate](../definitions/nvsentinel/README.md) intentionally remains at
`definitions/nvsentinel/` with its catalog, overlays, Environments and source patches together. It is a
pre-migration local packaging experiment outside the maintained release/demo selections and the counts
recorded for the relocation above. This exception preserves its reviewed paths and historical observations;
it does not establish a preferred layout for new inputs. A later move to `inputs/experiments/` must update
all references and rederive authored identities while preserving producer pins and output membership.

## Conditional supported target inputs

All Definition paths below are relative to `inputs/definitions/`. These are the intended final entry points;
they do not rename existing output subjects or assert that their current producers are equivalent. The four
demo Definitions and three Environments currently retain JSON; conversion to YAML is not part of the move.

| Target Definition | Purpose |
|---|---|
| `nvke/definition.yaml` | Main NVKE distribution; seed from the current 69-output candidate |
| `demo/arm64-nvidia-kvm.yaml` | Complete ARM64 NVIDIA OVN demo on native KVM, including nightly production |
| `demo/arm64-upstream-kvm.yaml` | Complete ARM64 upstream OVN diagnostic demo on native KVM |
| `demo/arm64-nvidia-tcg.yaml` | Complete ARM64 NVIDIA OVN demo with local TCG disk production |
| `demo/arm64-upstream-tcg.yaml` | Complete ARM64 upstream OVN diagnostic demo with local TCG disk production |
| `diagnostics/os-amd64.yaml` | One native AMD64 plain disk and its required material closure |
| `diagnostics/os-arm64.yaml` | One native ARM64 plain disk and its required material closure |
| `diagnostics/fde-amd64.yaml` | One native AMD64 public-bootstrap FDE disk and its required material closure |

All Environment paths below are relative to `inputs/environments/`. An Environment remains an exact
execution/publication policy, not a general label that permits source or producer substitution.

| Target Environment | Purpose |
|---|---|
| `github-release.yaml` | Main-only signed publication for the NVKE distribution |
| `github-development.yaml` | Feature-branch signed development publication with existing isolation and retention |
| `github-nightly-nvidia.yaml` | Named NVIDIA nightly bundle, signer and publication namespace |
| `github-nightly-upstream.yaml` | Named upstream nightly bundle, signer and publication namespace |
| `local-arm64-tcg.yaml` | Local ARM64 Docker/TCG demo, without publication or signing |
| `local-linux-arm64-kvm.yaml` | Local native ARM64 Docker/KVM diagnostics and admitted demo execution |
| `local-linux-amd64-kvm.yaml` | Local native AMD64 Docker/KVM diagnostics |
| `github-diagnostics.yaml` | Build-only native diagnostic workflow; no publication or signing |

The two nightly Environments remain distinct because their bundle names, signer identities and publication
repositories differ. Release and development retain their separate credential and signing policies.
The diagnostic Environment starts from the FDE build-only policy; any broader branch or architecture
admission must be reviewed explicitly. Native KVM and TCG remain separate producer variants.

## Definition migration map

Original paths are relative to the former `definitions/` directory; relocated paths are repository-relative.
Successors are relative to `inputs/definitions/` unless otherwise stated. **Seed** retains an entry point
without claiming convergence. **Merge** requires the named
producer/interface reconciliation. **Retain transitional** keeps the current input usable until that
work is complete. **Retire after caller migration** removes an input only after every launcher, guide,
test and demonstration has an accepted successor; it does not authorize silently expanding a small Run.

| Original Definition | Relocated now | Conditional target or successor | Remaining disposition |
|---|---|---|---|
| `nvke-m2/definition.yaml` | `inputs/definitions/nvke/definition.yaml` | `nvke/definition.yaml` | Seed; retain all 69 outputs initially, including the explicitly named profile fixture |
| `nvke-nightly-arm64/definition.json` | `inputs/definitions/demo/arm64-nvidia-kvm.json` | `demo/arm64-nvidia-kvm.yaml` | Merge; preserve 29-output membership, reconcile the stale embedded OVN producers explicitly |
| `nvke-nightly-upstream-ovn-arm64/definition.json` | `inputs/definitions/demo/arm64-upstream-kvm.json` | `demo/arm64-upstream-kvm.yaml` | Merge; preserve 29-output membership and the downstream renderer's profile-output naming contract |
| `nvke-local-dev-arm64/definition.json` | `inputs/definitions/demo/arm64-nvidia-tcg.json` | `demo/arm64-nvidia-tcg.yaml` | Merge; preserve the complete 29-output selection while reconciling its site/tool baseline with KVM |
| `nvke-local-dev-arm64/definition-upstream-ovn.json` | `inputs/definitions/demo/arm64-upstream-tcg.json` | `demo/arm64-upstream-tcg.yaml` | Merge; retain the matched disk, image, chart and profile selection; review output-name convergence separately |
| `nvke-os/definition-amd64-kvm.yaml` | `inputs/definitions/diagnostics/os-amd64.yaml` | `diagnostics/os-amd64.yaml` | Seed; retain its own catalog selection until OS producer convergence is reviewed |
| `nvke-os/definition-arm64-kvm.yaml` | `inputs/definitions/diagnostics/os-arm64.yaml` | `diagnostics/os-arm64.yaml` | Seed; preserve its ARM64 KVM overlay and material closure |
| `nvke-fde/definition.yaml` | `inputs/definitions/diagnostics/fde-amd64.yaml` | `diagnostics/fde-amd64.yaml` | Seed; preserve public-bootstrap FDE and build-only scope |
| `nvke-local-dev-arm64/definition-components.json` | `inputs/definitions/diagnostics/demo-arm64/components.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; this historical 22-output selection has older CLI/controller producers, not just fewer outputs |
| `nvke-local-dev-arm64/definition-os.json` | `inputs/definitions/diagnostics/demo-arm64/os.json` | `diagnostics/os-arm64.yaml` or `demo/arm64-nvidia-tcg.yaml` | Retain transitional; one TCG disk cannot become a KVM disk or a 29-output Run without an explicit workflow change |
| `nvke-local-dev-arm64/definition-profiles.json` | `inputs/definitions/diagnostics/demo-arm64/profiles.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; preserve focused producer contract coverage in tests |
| `nvke-local-dev-arm64/definition-providers.json` | `inputs/definitions/diagnostics/demo-arm64/providers.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; both independent providers remain required in the complete demo |
| `nvke-local-dev-arm64/definition-simulator.json` | `inputs/definitions/diagnostics/demo-arm64/simulator.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; do not claim the complete demo replaces the fast simulator loop until callers adopt it |
| `nvke-local-dev-arm64/definition-site.json` | `inputs/definitions/diagnostics/demo-arm64/site.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; retain the five-artifact source/interface relationship in tests |
| `nvke-local-dev-arm64/definition-tools.json` | `inputs/definitions/diagnostics/demo-arm64/tools.json` | `demo/arm64-nvidia-tcg.yaml` | Retain transitional; local source-built Helm and nightly acquired Helm are different producer contracts |
| `nvke-controller/definition-amd64.yaml` | `inputs/definitions/diagnostics/controller-amd64.yaml` | `nvke/definition.yaml` | Retire after caller migration; differently named native outputs are not aliases for aggregate release outputs |
| `nvke-controller/definition-arm64.yaml` | `inputs/definitions/diagnostics/controller-arm64.yaml` | `nvke/definition.yaml` | Retire after caller migration; preserve native image/chart binding tests |
| `nvke-controller/definition-profiles.yaml` | `inputs/definitions/diagnostics/controller-profiles.yaml` | No equivalent supported target yet | Retain transitional as a fixture example; its runnable profile payload is not the release's config-only fixture |
| `nvke-deploy/definition.yaml` | `docs/examples/nvke-deploy/definition.yaml` | `nvke/definition.yaml` | Retain transitional as a nine-output example; CLI and runnable fixture-profile selection differ from the main release |
| `nvke-ovn-source/definition.yaml` | `inputs/definitions/diagnostics/ovn.yaml` | `demo/arm64-nvidia-kvm.yaml` and `demo/arm64-nvidia-tcg.yaml` | Retain transitional as a standalone producer example; its AMD64 outputs have no equivalent in the ARM64 demo targets |
| `nvke-v1/definition.yaml` | `docs/examples/nvke-tutorial/definition.yaml` | Existing generic quickstart, or a deliberately retained NVKE tutorial | Retire after caller migration; the three-output tutorial's historical producers and artifact names are not release aliases |

Transitional inputs remain available through the initial ownership relocation, with callers updated to
their exact relocated paths. They are not additional supported target Definitions. A deliberately
retained tutorial or producer fixture ultimately belongs with its owning demo or tests, outside the
eight-entry supported inventory. Relocation does not resolve its retirement decision. If a focused
workflow must remain supported, revise the target count explicitly instead of silently dropping it.

## Environment migration map

Original paths are relative to the former `definitions/` directory; relocated paths are repository-relative.
Successors are relative to `inputs/environments/` unless otherwise stated.
No two original Environment files are byte-identical. Similar resource declarations alone do not make
publication, signing, credential or runtime policies interchangeable.

| Original Environment | Relocated now | Conditional target or successor | Remaining disposition |
|---|---|---|---|
| `nvke-m2/environment-github.yaml` | `inputs/environments/github-release.yaml` | `github-release.yaml` | Seed; preserve main-only signer, namespace, credentials and both native execution classes |
| `nvke-m2/environment-github-dev.yaml` | `inputs/environments/github-development.yaml` | `github-development.yaml` | Seed; preserve development mode and its distinct credentials, signer and namespace |
| `nvke-nightly-arm64/environment-github.json` | `inputs/environments/github-nightly-nvidia.json` | `github-nightly-nvidia.yaml` | Seed; preserve named bundle and signing/publication identities |
| `nvke-nightly-upstream-ovn-arm64/environment-github.json` | `inputs/environments/github-nightly-upstream.json` | `github-nightly-upstream.yaml` | Seed; preserve its separate named bundle and identities |
| `nvke-local-dev-arm64/environment-local-docker.json` | `inputs/environments/local-arm64-tcg.json` | `local-arm64-tcg.yaml` | Seed; preserve native ARM64, TCG capacity and no-publication policy |
| `nvke-os/environment-local-linux-arm64-docker.yaml` | `inputs/environments/local-linux-arm64-kvm.yaml` | `local-linux-arm64-kvm.yaml` | Seed; verify demo admission before presenting it as a replacement for other ARM64 environments |
| `nvke-os/environment-local-linux-amd64-docker.yaml` | `inputs/environments/local-linux-amd64-kvm.yaml` | `local-linux-amd64-kvm.yaml` | Seed; preserve AMD64 utility/runtime pins and KVM contract |
| `nvke-fde/environment-github.yaml` | `inputs/environments/github-diagnostics.yaml` | `github-diagnostics.yaml` | Seed; preserve build-only policy and current branch admission until separately reviewed |
| `nvke-deploy/environment-github.yaml` | `docs/examples/nvke-deploy/environment-github.yaml` | `github-release.yaml` | Retire after caller migration; current discovery tag, namespace and aggregate publication behavior differ |
| `nvke-deploy/environment-local.yaml` | `docs/examples/nvke-deploy/environment-local.yaml` | `local-linux-amd64-kvm.yaml` or retained deploy example | Retain transitional; current non-disk utility pin and no-publication policy must not change implicitly |
| `nvke-ovn-source/environment-github.yaml` | `inputs/environments/github-ovn-diagnostics.yaml` | Retained OVN producer example | Retain transitional; its fork source-access grant and publication contract are not covered by a renamed nightly policy |
| `nvke-v1/environment-github.yaml` | `docs/examples/nvke-tutorial/environment-github.yaml` | `github-release.yaml` or retained tutorial | Retire after caller migration; current namespace/publication behavior differs |
| `nvke-v1/environment-github-dev.yaml` | `docs/examples/nvke-tutorial/environment-github-dev.yaml` | `github-development.yaml` or retained tutorial | Retain transitional; current build-only development must not silently gain signed publication |
| `nvke-v1/environment-local.yaml` | `docs/examples/nvke-tutorial/environment-local.yaml` | Retained tutorial, until its caller is retired | Retain transitional; anonymous local HTTP registry and dry-run signing have no equivalent among the eight targets |

## Catalogs and other authored inputs

The [catalog ownership map](../inputs/catalogs/README.md) records the 19 maintained registry/lock pairs
and their exact former locations. The nine maintained owner families are `nvke`, `capnico`, `node-images`,
`ovn-nvidia`, `ovn-upstream`, `controller-profiles`, `runtime-dependencies`, `tools` and `nicosim`.
Their 19 distinct catalogs retain explicit source and toolchain variants. The experimental public OS
catalog remains separate at `inputs/experiments/public-os-sources/catalog`. Registry/lock pairs and
patches moved together; lock bytes remain unchanged. Use [catalog maintenance](catalog-maintenance.md)
for pin changes. Do not introduce public Definition inheritance, catalog mixins or an unimplemented
Run subset flag to reduce file count.

`definitions/nightly-profiles.json` moved to `inputs/testing/nightly-profiles.json`: it is downstream
coordinator configuration, not a Definition or Environment. Its Definition/Environment references move
without changing suite revisions, output names, bundle names or workflow identities.
`definitions/nvke-m2/qualification.yaml` moved to `inputs/policies/nvke-qualification.yaml`; its
independently authored trust selections and exact output scope are preserved. Neither counts toward
the eight/eight target. Teaching-only inputs now reside in `docs/examples/nvke-tutorial/` and
`docs/examples/nvke-deploy/`; their current distinct payloads and Environments remain explicit.

## Migration phases and acceptance

1. **Ownership relocation — implemented.** Retain all 35 input documents at the mapped paths, with
   catalog owners, overlays, qualification policy and downstream test configuration separated. Preserve
   metadata names, named outputs, platforms, producers, pins and policy values. JSON stays JSON.
2. **Caller migration and regeneration — initial-move acceptance.** Update Make targets, shell launchers,
   nightly configuration, demo code, tests, documentation and the operator skill. Regenerate production,
   development, named nightly and qualification bundles with the exact relocated inputs; never patch
   generated files directly. Compare resolved graphs, output membership and per-output Build Digests,
   preserve unchanged overlay predecessor bindings, check drift and run the repository completion gate.
   Changed authored paths can change Definition/Plan identity; they do not authorize new producers.
3. **Producer convergence — pending.** Review source/interface changes separately. Align demo site/tool
   ownership, fix NVIDIA nightly OVN version drift, and choose the release profile/resource payload.
   Regenerate overlay bindings through their owner when predecessors change. Keep the upstream diagnostic
   explicit. Establish parity tests over exact producer pins, material versions, profile content and
   chart/image bindings; record real producer/runtime acceptance separately.
4. **Caller retirement — pending.** Assign each transitional row an owner and accepted successor. Retire
   redundant files only after launchers and tests adopt that successor. Preserve focused producer contracts
   in tests. Do not replace a fast loop with a larger Run without recording that workflow decision. Resolve
   the teaching examples' final scope explicitly rather than treating them as release aliases.
5. **Final count and format — pending.** Convert the seven retained JSON entry points to the proposed YAML
   paths only with strict semantic equality checks and matching regeneration. Reassess the eight/eight
   count if a focused workflow or distinct execution policy must remain supported. Nine catalog directories
   alone do not establish shared producer ownership; retain every variant still required by its callers.

The eight/eight target is complete only when the supported entry points exist, every original row has
a resolved disposition, callers and generated bundles agree, and the required checks pass. A directory
rename or reduced file count alone does not satisfy these conditions.

## Product gaps that relocation does not close

At the recorded base, main uses CLI `ed9fb09b` and controller/NKTP/fixture `33fb71b2`; local site/profiles
use `8fb2d533`; nightly site and suite use `a8391755`. Main's profile output is explicitly
`nico-reference-profiles-fixture`, not the demo's `controller-profiles`. Main embeds upstream OVN 1.4.0;
local and standalone NVIDIA OVN use 2.4.0, while the nightly KVM overlay still embeds 1.5.18.
The selected local profile asks for 2.4.0 packages. These differences require reviewed corrections,
not path rewriting or an assumption that equal output counts establish parity.

NKTP still needs digest-bound deployment references, and the SPIRE packaging rollback omits the required
DSX IMDS payload; the blocked DSX producer and its credentials were removed before the base commit.
See [the maintained candidate's limits](../inputs/definitions/nvke/README.md) and
[nightly scope](nvke-nightly.md). Preserve these limitations through migration; completed generation,
contract tests and a coherent input layout do not establish a deployable or accepted OSS alpha.
