# Input layout migration and remaining consolidation

The ownership relocation from upstream/main `f260c48570eacc0e2112b6a74eb07150ff0e828e` preserves all
21 Definitions and 14 Environments. At that relocation, nineteen Definitions and nine Environments moved
under `inputs/`; the other two Definitions and five Environments stayed under `docs/examples/`. Directory
placement alone does not determine support; the retained build-only Environment below remains supported.
Source versions, output membership, producer commands, lock bytes and policy values are unchanged by
this move. Authored references and generated identities must reflect their new paths.

The revised target for this NVKE migration cohort is **eight supported Definition entry points, nine catalog
owner families and eight supported Environments (8/9/8)**. The composite replaces the combined NVKE entry
point, and the two legacy ARM64 nightly paths are retired. The composite's six member Definitions remain
authored selection owners, not separate product entry points. Two focused Definitions and one build-only
Environment remain supported rather than converging into broader Runs or signed publication. The combined
Definition remains only as a comparison/authoring fixture. At the relocation baseline, the nine
owner directories contained **19 distinct catalogs**, plus one separate experimental catalog; grouping
them does not merge their different source baselines. These target counts exclude other product additions,
teaching-only Definitions and experiments; they are not counts of the entire current input tree.
The supported build-only Environment counts independently of its current example Definition.
Neither relocation nor this plan establishes completed consolidation, implemented parity or OSS alpha
acceptance. The [input index](../inputs/README.md) describes current entry points.

The engineering contracts remain [ADR-19](adr/0019-clusterdefinition-and-provisioning-adapter-boundary.md),
[ADR-21](adr/0021-generated-pipeline-maintenance-and-lifecycle.md) and
[ADR-22](adr/0022-catalog-pinning-and-the-lock-contract.md); [reconciliation](adr/reconciliation/README.md)
owns current implementation and external evidence. Product source, output membership and publication
policy changes require their own explicit review, even when performed during this migration.

## NVSentinel ownership

The [NVSentinel development catalog](../inputs/catalogs/nvsentinel/README.md) now lives under
`inputs/catalogs/nvsentinel/`. Its Definitions and Environments have separate owners under
`inputs/definitions/nvsentinel/` and `inputs/environments/nvsentinel/`. The NVKE consumer catalog and
predecessor-bound OS overlays remain an explicit [experiment](../inputs/experiments/nvsentinel/README.md).
The old `definitions/nvsentinel/` candidate exception is retired. These additions sit outside the retained
NVKE migration counts above; they do not change its maintained release selections or establish acceptance.
Current producers leave upstream source unchanged. Retired patch-based Run observations remain historical
in reconciliation; they do not validate the corrected recipes or reproduce their changed build identities.

## Supported target inputs

All Definition paths below are relative to `inputs/definitions/`. These are the intended final entry points;
they do not rename existing output subjects or assert that their current producers are equivalent. The four
demo Definitions and three Environments originally retained JSON. After nightly retirement, two supported
demo Definitions and one Environment retain JSON; conversion to YAML is not part of the move.
The two retained focused Definitions keep their existing JSON paths and exact producer selections.

| Target Definition | Purpose |
|---|---|
| `nvke/gpu-site-release.composite.yaml` | Maintained production/nightly entry point; six pinned members plus the derived release inventory |
| `nvke/gpu-site.composite.yaml` | Maintained development entry point; the same six pinned members without the release inventory |
| `demo/arm64-nvidia-tcg.yaml` | Complete ARM64 NVIDIA OVN demo with local TCG disk production |
| `demo/arm64-upstream-tcg.yaml` | Complete ARM64 upstream OVN diagnostic demo with local TCG disk production |
| `diagnostics/os-amd64.yaml` | One native AMD64 plain disk and its required material closure |
| `diagnostics/os-arm64.yaml` | One native ARM64 plain disk and its required material closure |
| `diagnostics/fde-amd64.yaml` | One native AMD64 public-bootstrap FDE disk and its required material closure |
| `diagnostics/demo-arm64/os.json` | Supported focused ARM64 TCG disk Run; does not become KVM or the complete demo |
| `diagnostics/demo-arm64/tools.json` | Supported focused ARM64 tools Run; preserves source-built Helm rather than substituting an acquired tool |

Environment paths below are relative to `inputs/environments/`, except the explicitly repository-relative
`docs/examples/` entry. An Environment remains an exact execution/publication policy, not a general
label that permits source or producer substitution.

| Target Environment | Purpose |
|---|---|
| `github-nvke-gpu-site.yaml` | Main-only signed publication for the NVKE composite |
| `github-development.yaml` | Feature-branch signed development publication with existing isolation and retention |
| `github-nvke-nightly-gpu-site.yaml` | Floating-source composite nightly with its exact bundle and signer identities |
| `local-arm64-tcg.yaml` | Local ARM64 Docker/TCG demo, without publication or signing |
| `local-linux-arm64-kvm.yaml` | Local native ARM64 Docker/KVM diagnostics and admitted demo execution |
| `local-linux-amd64-kvm.yaml` | Local native AMD64 Docker/KVM diagnostics |
| `github-diagnostics.yaml` | Build-only native diagnostic workflow; no publication or signing |
| `docs/examples/nvke-module-tags/environment-github-dev.yaml` (repository-relative) | Supported build-only branch development; retains its existing policy without gaining signing or publication |

The module-tags build-only Environment remains paired with its existing example Definition until caller
migration supplies an accepted successor Definition preserving that build-only contract. Its supported status does
not authorize retiring the companion Definition before that migration.

Production, development and floating-source nightly retain their separate credential and signing policies.
The diagnostic Environment starts from the FDE build-only policy; any broader branch or architecture
admission must be reviewed explicitly. Native KVM and TCG remain separate producer variants.
The [ADR-26 U10 decision](adr/0026-definition-composition.md)
retires combined publication and the two legacy ARM64 nightly paths. The maintainer reaffirmed that
decision on 2026-11-18: the combined Definition remains only a comparison/authoring fixture, and the
focused TCG disk, source-built tools and module-tags build-only Environment remain supported. The
retirement decision supersedes the former hosted/consumer/equivalence cutover prerequisites; it does
not supply missing acceptance evidence or waive Run and Release verification requirements.

## Definition migration map

Original paths are relative to the former `definitions/` directory; relocated paths are repository-relative.
Successors are relative to `inputs/definitions/` unless otherwise stated. **Seed** retains an entry point
without claiming convergence. **Retain supported** keeps the exact entry point and its distinct
producer or policy contract in the target count. **Merge** requires the named
producer/interface reconciliation. **Retain transitional** keeps the current input usable until that
work is complete. **Retire after caller migration** removes an input only after every launcher, guide,
test and demonstration has an accepted successor; it does not authorize silently expanding a small Run.

| Original Definition | Relocated now | Conditional target or successor | Remaining disposition |
|---|---|---|---|
| Combined NVKE Definition | `inputs/definitions/nvke/definition.yaml` | `nvke/gpu-site.composite.yaml` | Retain only as comparison/authoring fixture; combined publication retired under ADR-26 U10 without claiming output equivalence |
| `nvke-nightly-arm64/definition.json` | Retired 2026-11-17 | No maintained successor for this exact selection | Retired under ADR-26 U10; local TCG diagnostics retain their own selections |
| `nvke-nightly-upstream-ovn-arm64/definition.json` | Retired 2026-11-17 | No maintained successor for this exact selection | Retired under ADR-26 U10; local upstream TCG diagnostic retained |
| `nvke-local-dev-arm64/definition.json` | `inputs/definitions/demo/arm64-nvidia-tcg.json` | `demo/arm64-nvidia-tcg.yaml` | Merge; preserve the complete 29-output selection while reconciling its site/tool baseline with KVM |
| `nvke-local-dev-arm64/definition-upstream-ovn.json` | `inputs/definitions/demo/arm64-upstream-tcg.json` | `demo/arm64-upstream-tcg.yaml` | Merge; retain the matched disk, image, chart and profile selection; review output-name convergence separately |
| `nvke-os/definition-amd64-kvm.yaml` | `inputs/definitions/diagnostics/os-amd64.yaml` | `diagnostics/os-amd64.yaml` | Seed; retain its own catalog selection until OS producer convergence is reviewed |
| `nvke-os/definition-arm64-kvm.yaml` | `inputs/definitions/diagnostics/os-arm64.yaml` | `diagnostics/os-arm64.yaml` | Seed; preserve its ARM64 KVM overlay and material closure |
| `nvke-fde/definition.yaml` | `inputs/definitions/diagnostics/fde-amd64.yaml` | `diagnostics/fde-amd64.yaml` | Seed; preserve public-bootstrap FDE and build-only scope |
| `nvke-local-dev-arm64/definition-components.json` | `inputs/definitions/diagnostics/demo-arm64/components.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; this historical 22-output selection has older CLI/controller producers, not just fewer outputs |
| `nvke-local-dev-arm64/definition-os.json` | `inputs/definitions/diagnostics/demo-arm64/os.json` | `diagnostics/demo-arm64/os.json` | Retain supported; preserve the focused TCG disk Run without switching to KVM or expanding membership |
| `nvke-local-dev-arm64/definition-profiles.json` | `inputs/definitions/diagnostics/demo-arm64/profiles.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; preserve focused producer contract coverage in tests |
| `nvke-local-dev-arm64/definition-providers.json` | `inputs/definitions/diagnostics/demo-arm64/providers.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; both independent providers remain required in the complete demo |
| `nvke-local-dev-arm64/definition-simulator.json` | `inputs/definitions/diagnostics/demo-arm64/simulator.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; do not claim the complete demo replaces the fast simulator loop until callers adopt it |
| `nvke-local-dev-arm64/definition-site.json` | `inputs/definitions/diagnostics/demo-arm64/site.json` | `demo/arm64-nvidia-tcg.yaml` | Retire after caller migration; retain the five-artifact source/interface relationship in tests |
| `nvke-local-dev-arm64/definition-tools.json` | `inputs/definitions/diagnostics/demo-arm64/tools.json` | `diagnostics/demo-arm64/tools.json` | Retain supported; preserve the focused tool selection and source-built Helm contract |
| `nvke-controller/definition-amd64.yaml` | `inputs/definitions/diagnostics/controller-amd64.yaml` | `nvke/gpu-site.composite.yaml` | Retire after caller migration; differently named native outputs are not aliases for aggregate release outputs |
| `nvke-controller/definition-arm64.yaml` | `inputs/definitions/diagnostics/controller-arm64.yaml` | `nvke/gpu-site.composite.yaml` | Retire after caller migration; preserve native image/chart binding tests |
| `nvke-controller/definition-profiles.yaml` | `inputs/definitions/diagnostics/controller-profiles.yaml` | No equivalent supported target yet | Retain transitional as a fixture example; its runnable profile payload is not the release's config-only fixture |
| `nvke-deploy/definition.yaml` | `docs/examples/nvke-deploy/definition.yaml` | `nvke/gpu-site.composite.yaml` | Retain transitional as a nine-output example; CLI and runnable fixture-profile selection differ from the main release |
| `nvke-ovn-source/definition.yaml` | `inputs/definitions/diagnostics/ovn.yaml` | `demo/arm64-nvidia-tcg.yaml` | Retain transitional as a standalone producer example; its AMD64 outputs have no equivalent in the ARM64 demo targets |
| `nvke-v1/definition.yaml` | `docs/examples/nvke-module-tags/definition.yaml` | Existing generic quickstart, or a deliberately retained NVKE tutorial | Retire after caller migration; the three-output tutorial's historical producers and artifact names are not release aliases |

Transitional inputs remain available through the initial ownership relocation, with callers updated to
their exact relocated paths. They are not additional supported target Definitions. A deliberately
retained tutorial or producer fixture ultimately belongs with its owning demo or tests, outside the
eight-entry-point supported inventory. Relocation does not resolve its retirement decision. If a focused
workflow must remain supported, revise the target count explicitly instead of silently dropping it.

## Environment migration map

Original paths are relative to the former `definitions/` directory; relocated paths are repository-relative.
Successors are relative to `inputs/environments/` unless otherwise stated.
No two original Environment files are byte-identical. Similar resource declarations alone do not make
publication, signing, credential or runtime policies interchangeable.

| Original Environment | Relocated now | Conditional target or successor | Remaining disposition |
|---|---|---|---|
| Combined production Environment | `inputs/environments/github-release.yaml` | `github-nvke-gpu-site.yaml` | Old Environment retained only as comparison/authoring fixture; composite publication uses its own exact signer identity |
| Combined development Environment | `inputs/environments/github-development.yaml` | `github-development.yaml` | Retain supported for the composite; preserve development mode and its distinct credentials, signer and namespace |
| `nvke-nightly-arm64/environment-github.json` | `inputs/environments/github-nightly-nvidia.json` | None for the retired producer | Retain only as comparison/authoring fixture; no maintained nightly bundle |
| `nvke-nightly-upstream-ovn-arm64/environment-github.json` | `inputs/environments/github-nightly-upstream.json` | None for the retired producer | Retain only as comparison/authoring fixture; no maintained nightly bundle |
| `nvke-local-dev-arm64/environment-local-docker.json` | `inputs/environments/local-arm64-tcg.json` | `local-arm64-tcg.yaml` | Seed; preserve native ARM64, TCG capacity and no-publication policy |
| `nvke-os/environment-local-linux-arm64-docker.yaml` | `inputs/environments/local-linux-arm64-kvm.yaml` | `local-linux-arm64-kvm.yaml` | Seed; verify demo admission before presenting it as a replacement for other ARM64 environments |
| `nvke-os/environment-local-linux-amd64-docker.yaml` | `inputs/environments/local-linux-amd64-kvm.yaml` | `local-linux-amd64-kvm.yaml` | Seed; preserve AMD64 utility/runtime pins and KVM contract |
| `nvke-fde/environment-github.yaml` | `inputs/environments/github-diagnostics.yaml` | `github-diagnostics.yaml` | Seed; preserve build-only policy and current branch admission until separately reviewed |
| `nvke-deploy/environment-github.yaml` | `docs/examples/nvke-deploy/environment-github.yaml` | `github-nvke-gpu-site.yaml` | Retire after caller migration; current discovery tag, namespace and aggregate publication behavior differ |
| `nvke-deploy/environment-local.yaml` | `docs/examples/nvke-deploy/environment-local.yaml` | `local-linux-amd64-kvm.yaml` or retained deploy example | Retain transitional; current non-disk utility pin and no-publication policy must not change implicitly |
| `nvke-ovn-source/environment-github.yaml` | `inputs/environments/github-ovn-diagnostics.yaml` | Retained OVN producer example | Retain transitional; its fork source-access grant and publication contract are not covered by a renamed nightly policy |
| `nvke-v1/environment-github.yaml` | `docs/examples/nvke-module-tags/environment-github.yaml` | `github-nvke-gpu-site.yaml` or retained tutorial | Retire after caller migration; current namespace/publication behavior differs |
| `nvke-v1/environment-github-dev.yaml` | `docs/examples/nvke-module-tags/environment-github-dev.yaml` | `docs/examples/nvke-module-tags/environment-github-dev.yaml` (repository-relative) | Retain supported; preserve build-only development without signing or publication |
| `nvke-v1/environment-local.yaml` | `docs/examples/nvke-module-tags/environment-local.yaml` | Retained tutorial, until its caller is retired | Retain transitional; anonymous local HTTP registry and dry-run signing have no equivalent among the eight targets |

## Catalogs and other authored inputs

The [catalog ownership map](../inputs/catalogs/README.md) records the registry/lock pairs
and their exact former locations. At the relocation baseline, the nine maintained owner families were `nvke`, `capnico`, `node-images`,
`ovn-nvidia`, `ovn-upstream`, `controller-profiles`, `runtime-dependencies`, `tools` and `nicosim`.
The simulator producers now belong to the consolidated `tools` catalog. The 19 baseline catalogs retained explicit source
and toolchain variants. The experimental public OS
catalog remains separate at `inputs/experiments/public-os-sources/catalog`. Registry/lock pairs and
patches moved together; lock bytes remain unchanged. Use [catalog maintenance](catalog-maintenance.md)
for pin changes. Do not introduce public Definition inheritance, catalog mixins or an unimplemented
Run subset flag to reduce file count.

`definitions/nightly-profiles.json` moved to `inputs/testing/nightly-profiles.json`: it is downstream
coordinator configuration, not a Definition or Environment. Its legacy producer entries are now empty
after nightly retirement; maintained composite nightly configuration lives under `demos/`.
The combined NVKE qualification policy at `inputs/policies/nvke-qualification.yaml` remains an authored
fixture, with exact output membership checked against the retained combined Definition. It does not
enroll combined publication. Neither file counts toward the eight-entry-point/eight-Environment target.
Teaching-only Definitions remain under `docs/examples/`; their distinct payloads and Environments remain
explicit, including the separately supported module-tags build-only Environment.

## Migration phases and acceptance

1. **Ownership relocation — implemented.** The initial move retained all 35 input documents, separating
   catalog owners, overlays, qualification policy and downstream test configuration. It preserved metadata
   names, named outputs, platforms, producers, pins, policy values and JSON formats. The later ADR-26 U10
   retirement supersedes retention of the two legacy nightly Definitions and their generated bundles.
2. **Caller migration and regeneration — initial-move acceptance.** Update Make targets, shell launchers,
   nightly configuration, demo code, tests, documentation and the operator skill. Regenerate production,
   development, composite nightly and qualification bundles with the exact maintained inputs; never patch
   generated files directly. Compare resolved graphs, output membership and per-output Build Digests,
   preserve unchanged overlay predecessor bindings, check drift and run the repository completion gate.
   Changed authored paths can change Definition/Plan identity; they do not authorize new producers.
3. **Producer convergence — pending.** Review source/interface changes separately. Align demo site/tool
   ownership and review profile/resource payload differences. The retired nightly producer is not a
   convergence target.
   Regenerate overlay bindings through their owner when predecessors change. Keep the upstream diagnostic
   explicit. Establish parity tests over exact producer pins, material versions, profile content and
   chart/image bindings; record real producer/runtime acceptance separately.
4. **Remaining caller retirement — pending.** Combined publication and the two legacy nightly paths are
   already retired under ADR-26 U10. Assign each transitional row an owner and accepted successor. Retire
   redundant files only after launchers and tests adopt that successor. Preserve focused producer contracts
   in tests. Do not replace a fast loop with a larger Run without recording that workflow decision. Resolve
   the teaching examples' final scope explicitly rather than treating them as release aliases.
5. **Final count and format — pending.** Convert the three retained demo and Environment JSON entry points to the proposed YAML
   paths only with strict semantic equality checks and matching regeneration. The two retained focused JSON
   Definitions are not part of that conversion. Preserve the revised eight-entry-point/eight-Environment
   target unless another explicit support decision changes it. Nine catalog directories alone do not
   establish shared producer ownership; retain every variant still required by its callers.

The eight-entry-point/eight-Environment target is complete only when the supported entry points exist,
every original row has a resolved disposition, callers and generated bundles agree, and the required
checks pass. A directory rename or reduced file count alone does not satisfy these conditions.

## Product gaps that relocation does not close

At the recorded base, main uses CLI `ed9fb09b` and controller/NKTP/fixture `33fb71b2`; local site/profiles
use `8fb2d533`; nightly site and suite use `a8391755`. Main's profile output is explicitly
`nico-reference-profiles-fixture`, not the demo's `controller-profiles`. Main embedded upstream OVN 1.4.0;
local and standalone NVIDIA OVN used 2.4.0, while the nightly KVM overlay still embeds 1.5.18.
The selected local profile asked for 2.4.0 packages. These differences require reviewed corrections,
not path rewriting or an assumption that equal output counts establish parity.

The maintained release disks now embed the NVIDIA OVN fork `v2.4.2` (`8af51273`), matching NVKE
`v0.23.0`'s default tenant `cniVersion` and the native NVIDIA demo's producers. The upstream 1.4.0 demo
variants remain explicit overlays. The stale nightly KVM overlay is a retained historical input; its
producer path is retired, not restored by this migration plan.

NKTP still needs digest-bound deployment references, and the SPIRE packaging rollback omits the required
DSX IMDS payload; the blocked DSX producer and its credentials were removed before the base commit.
See [the maintained candidate's limits](../inputs/definitions/nvke/README.md) and
[nightly scope](nvke-nightly.md). Preserve these limitations through migration; completed generation,
contract tests and a coherent input layout do not establish a deployable or accepted OSS alpha.
