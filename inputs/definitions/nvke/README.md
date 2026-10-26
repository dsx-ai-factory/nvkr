# NVKE release candidate and family pilot

The combined [Definition](definition.yaml) is used by the committed `nvkr-run.yml`. It joins independently pinned NVKE CLI,
controller and chart builds, operator executables, native node images and third-party deployment images
into one release with **69 named outputs**. This remains a candidate; completed pipeline delivery and
full product acceptance are separate facts.

## Four-family pilot

Four independent Definitions partition the combined candidate's exact named outputs. They reuse the
shared catalogs and pins; they do not import each other or change the combined Definition into a parent.

| Definition | Outputs | Membership | Environment |
|---|---:|---|---|
| [Core](core.yaml) | 20 | CLI/operator tools, controller and bound chart, providers/reference fixture, CAPI and cert-manager images | [github-nvke-core.yaml](../../environments/github-nvke-core.yaml) |
| [Services](services.yaml) | 28 | SPIRE, PostgreSQL and Temporal charts and native images | [github-nvke-services.yaml](../../environments/github-nvke-services.yaml) |
| [Telemetry](telemetry.yaml) | 17 | NKTP chart and native runtime images | [github-nvke-telemetry.yaml](../../environments/github-nvke-telemetry.yaml) |
| [Disks](disks.yaml) | 4 | Native AMD64/ARM64 plain and public-bootstrap FDE disks, each with its complete material closure | [github-nvke-disks.yaml](../../environments/github-nvke-disks.yaml) |

Each Environment selects `githubBundle: nvke-<family>`, the corresponding
`.github/workflows/nvkr-run-nvke-<family>.yml`, an explicit main-only signer identity, and publication prefix
`ghcr.io/dsx-ai-factory/nvkr/m2-candidate/<family>`. Its Release publishes by digest beneath that prefix's
`releases` repository. Source-access and disk settings are scoped to the family's selected producers.
All four families retain the maintained production registry policy: `NVKR_REGISTRY_WRITE` handles
publication, and `NVKR_REGISTRY_READ` with `NVKR_REGISTRY_READ_USER` handles publication verification.
Only the disks family also uses that reader for private native toolchain preparation. These credentials
require the same operator provisioning as the combined candidate; generation does not verify package access.

From the repository root:

```sh
make nvke-split-generate
make nvke-split-check
```

The repository-wide `make workflows-generate` and `make workflows-check` also include these bundles.
Generated Plans, actions and manifests retain their normal ownership; edit the authored inputs and
regenerate. Generation and drift checking do not build, publish or qualify any family.

This is a reversible pilot alongside the combined production, development and qualification workflows.
Named production bundles have no generated qualification pair, and named development bundles remain
unsupported. The existing combined qualification policies do not cover four independent family Runs.
Keep the combined path until the consumer and qualification cutover has its own accepted contract.

Consumers select each exact digest-pinned family Release and verify it under independently chosen policy.
Any record associating those references belongs to the downstream consumer; this pilot emits no automatic
association file, native parent Release or cross-Release compatibility verdict. Family output publication
still requires every output and its exact evidence within that Definition. No hosted family build,
publication or independent consumer acceptance is established here. The
[partitioning strategy](../../../docs/definition-partitioning-strategy.md) records the rationale,
historical capacity measurements and remaining cutover work.

## Selected artifacts and their owners

| Output group | Owning inputs | Selection |
|---|---|---|
| CLI | `../../catalogs/nvke/module-tags` | AMD64 `nvkectl` and ARM64 `nvkectl-linux-arm64`, from their shared NVKE v0.22.0 source pin |
| Controller and charts | `../../catalogs/nvke/release` | AMD64/ARM64 aggregate `cwe`, image-bound `controller-chart`, PostgreSQL, SPIRE, SPIRE CRDs and Temporal chart packages |
| Operator tools | `../../catalogs/tools/operator-amd64` | AMD64 Helm and kubectl executables, selected from the NVKE distribution lock |
| Node images | `../../catalogs/node-images/release`, `../../catalogs/node-images/variants/native-toolchains.yaml` | Separate native AMD64 and ARM64 plain and public-bootstrap FDE QCOW2s, with explicit material producers |
| Deployment images | `../../catalogs/runtime-dependencies` | Twelve non-test runtime images for each native architecture: PostgreSQL, eight SPIRE images and three Temporal images |
| CAPNICo | `../../catalogs/nvke/release-components` | Source-built `capnico-controller`, one OCI index requiring AMD64 and ARM64 |
| Provisioned infrastructure provider | `../../catalogs/nvke/release` | Existing source-built `cluster-api-provider-provisioned`, independently delivered for both architectures; not CAPNICo or CAPI core |
| Reference-profile fixture | `../../catalogs/nvke/release-components` | `nico-reference-profiles-fixture`, one AMD64/ARM64 content image containing the two NICO test config profiles and their runbooks, not production fleet configuration or the demo's runnable `controller-profiles` |
| Telemetry | `../../catalogs/nvke/release-components` | Source-packaged `nktp` with its three vendored dependencies; eight required/optional runtime images for each native architecture, including the enabled Target Allocator and the uninstall hook's kubectl |
| Bootstrap runtime images | `../../catalogs/nvke/release-components` | CAPI core, kubeadm bootstrap/control-plane and three cert-manager images, each with separate AMD64 and ARM64 outputs |

### Additional release members and their source contracts

These are declarative catalog additions, not NVKE-specific compiler behavior. The existing generic
BuildKit, source-vendor, Helm and acquired-image routes build or acquire them. Each OCI index requires
both native subjects; acquired images retain separate native outputs because aggregation currently admits
built OCI exports only. The architecture-independent NKTP chart is packaged once. Existing outputs remain required.

| Member | Selection mirrored from pinned upstream inputs | Boundary |
|---|---|---|
| CAPNICo | NVKE's selected `deploy/nico/capnico/capnico.yaml` names controller `v0.0.45`; CAPNICo's own Dockerfile builds it | Independent source pin and source build, not the provisioned infrastructure provider |
| NKTP chart and dependencies | The same NVKE `v0.22.0` source as the maintained controller; `nktp/deploy/charts/nktp/Chart.yaml` and `Chart.lock` select three exact dependencies; packaging stamps chart `0.22.0` | Fetch verifies Helm `v4.2.4` and all three acquired chart archives; packaging uses the retained closure with no network |
| NKTP images | Selected source values and authenticated dependency charts: collector `0.162.0`, operator `0.159.0`, LGTM `0.32.0`, kube-state-metrics `v2.20.0`, node-exporter `v1.12.1`, optional metrics-server `v0.8.1`, uninstall kubectl `v1.34.1`; Operator `0.159.0`'s `versions.txt` selects Target Allocator `0.159.0` | Acquired images, not source builds; native subjects and parent indexes are lock-owned. Helm test-only BusyBox is excluded. Images are delivered separately; chart references are not automatically rewritten to publication repositories |
| Reference-profile fixture | The selected NVKE fixture Dockerfile, `nico-vms-cp.yaml`, `nico-vms-worker.yaml` and sibling runbooks | Explicitly named `nico-reference-profiles-fixture`: the control-plane profile skips its pre-join reboot. It must not be installed as production fleet configuration. No empty/failure-test config profile, placeholder example, copied OSProfile or new Dockerfile/patch is added |
| CAPI and cert-manager images | Selected NVKE `make/tool-versions.mk` chooses CAPI `v1.13.6`; its published provider manifests name the three controllers, and its clusterctl config selects cert-manager `v1.21.1` | Acquired runtime images only. Provider manifests/metadata and installation remain a separate consumer contract |

These version selections are explicit mirrors of the pinned upstream files, not live imports. A future
upstream version bump requires a reviewed catalog/lock update; automatic source-to-catalog version drift
detection is not implemented.

NKTP image-reference binding remains a nonblocking follow-up, not a completed deployment contract.
The signed chart still contains upstream tags, whereas the separately delivered native images have exact
digests. Follow-up acceptance requires binding every enabled parent/subchart image to the corresponding
published digest and testing rendered manifests for both architectures without mutable image tags.
Until then, consumers must explicitly relocate/bind those references; chart and image delivery alone
does not prove offline installation or that a deployed workload uses the signed image outputs.

The controller and CLI now select NVKE `v0.22.0`. The config-only reference image is not a complete
provisioning configuration. Fleet payload selection and delivery of the Kubernetes release resources
remain separate consumer work; updating source pins does not establish deployment acceptance.

The catalogs and their machine-managed locks own exact commits, versions, archive/member hashes and
native image digests. Do not maintain a second digest table in documentation. PostgreSQL and SPIRE Bash
retain their source-owned index digests as well as the selected native child pins; acquisition checks
actual parent-to-child membership before using the native image.

OS assembly, agent, Calico, kubeadm and SPIRE packaging all select NVKE `v0.22.0` at
`400c24fb6465b042be883d6b62472528520d6291`. SPIRE stages `1.14.1` and TPM plugin `1.9.0`,
then packages Debian revision `4` offline before image-input composition and Packer.
That source-owned packaging excludes DSX IMDS. SPIRE server/container outputs retain their
separately pinned chart-selected versions. The agent package stamps `v0.22.0`.
These updated inputs do not establish a new build, source-review acceptance or runtime result.
The pinned commit's source-owned direct-Packer contract receives `capture_apt_acquisitions=true` and exports
`non-fde/apt-acquisitions.tar` or `fde/apt-acquisitions.tar`, which each producer declares through `packer.acquisitionArchive`.
The source's composition command receives the required explicit native `--architecture` argument.
The selected material projection binds `image-inputs` as `archive` and `installer-iso` as `disk-image`
under `nvke.nvidia.com/os-image-materials/v1`; every selected material and disk target retains its own architecture.

This is a candidate Definition, not completed M2 acceptance. The production target Environment reuses
the existing registry credential, source access and workflow signing policy, but publishes only under
the separately authored `ghcr.io/dsx-ai-factory/nvkr/m2-candidate` prefix. The maintained signer identity
admits only `main`; the temporary PR #97 branch-test exception has been removed after the
cancelled attempt. Candidate execution is not organizational publication acceptance. Native disk
execution classes select the two ARC KVM pools under `githubARC: native-kvm-v1`. They describe requested
capacity, not observed allocation. `../../catalogs/node-images/variants/native-toolchains.yaml` substitutes the exact published native
Packer pin for the nine plain-image AMD64 producers plus the FDE root, retaining the separate BuildKit pin.
The OS catalog directly retains its published ARM64 Docker Packer pin.
The two inspector profiles use measured executable hashes and published native image manifests.
Their bounded budgets cover up to an 8 GiB subject and 16 GiB filesystem: 28 GiB inspection memory,
224 GiB private scratch and a ten-minute deadline. Execution classes request 36 GiB memory to
include the held subject snapshot. These are admission budgets, not measured full-M2 usage.
The inspector and Packer source locks retain their outstanding organizational approval gates;
the [candidate exception and publication evidence](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-native-arc.md)
records the narrower authorization and exact registry read-back.
The retained measurements came from developer-host preparation and publication. A hosted toolchain build,
its matching measurements and provenance, and completed organizational clearance are not recorded here;
native preparation commands alone do not supply that evidence.
The candidate exception's organizational acceptance owner and expiry are not recorded. Leaving that exception
requires recorded license/security clearance and the hosted closure/measurement/provenance evidence; a
developer-host image push does not supply those approvals.
The candidate Environment selects Docker through the existing typed runtime controls;
it declares no storage-driver field or ambient runtime-variable workaround. Storage behavior
is admitted and observed by the existing runtime owner. The historical explicit-VFS probe
is diagnostic evidence, not this Environment's configuration or production approval.
Runner labels do not supply runtime-capacity or full-inspector profile authority.
The current candidate selects `diskEvidence: build-provenance-v1`, authenticating all four held disks and their
material producers without deep inspection. Profiles remain available for plain-image strict mode but are not pulled or
executed in this mode; its signed evidence reports inspection as `not-performed`. The approved October 12
M3 MVP retains this producer-provenance floor. Full disk inspection and material reconciliation are deferred
beyond that MVP ([ADR-13](../../../docs/adr/0013-supply-chain-evidence.md#6-pipeline-sbom-and-acquisition-scope)
and [NKX-18052](https://nvidia.atlassian.net/browse/NKX-18052)).
The product acceptance floor remains AMD64; this candidate retains the separately selected ARM64
extension, and all four disk outputs remain mandatory for completion of this Definition.
Omitting producer `diskEvidence` still selects the strict contract. A complete producer-provenance
M2 candidate requires publication, signatures and consumer verification for every selected output.
The [failed delivery attempt](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-release-carrier-transport.md)
records the earlier aggregate transport failure; the per-output route below avoids aggregate payload downloads.
Per-output storage peaks still require measured admission.
The signed disk mode is descriptive. `VerificationPolicy.minimumDiskEvidence` defaults to `build-provenance-v1`
for pipeline-attested verification. Explicit `strict` rejects producer-only evidence; qualified policy defaults
to strict and rejects the producer-only minimum. The selected minimum is
policy-digest-bound. The historical validation run below predates these consumer-policy and timeout changes
and does not validate this updated head, current main execution, strict inspection or independent full-payload
consumer acceptance.

The [customer choices guide](../../../docs/nvke-customer-choices.md) explains supplier replacement through
the existing catalog interfaces and the precise scope of its graph tests. The
[release transport evidence](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-release-carrier-transport.md) records the
failed candidate's storage exhaustion and why correcting carrier selection alone does not complete delivery.

The [toolchain preparation guide](../../../toolchains/nvkr-packer/README.md)
lists repository-owned native preparation and measurement commands, their current
toolchain license/security gates, and the missing production inputs. The inspector's source
README gives separate AMD64 and ARM64 assembly commands. The generated workflow uses isolated
read-only image preparation with `NVKR_REGISTRY_READ` and `NVKR_REGISTRY_READ_USER`; provision access
independently of the repository token, as described in the
[production credential setup](../../../docs/generate-pipeline.md). Maintained manual dispatch and
signing admit only `main`. The October 7 branch attempt at `562a609` used a temporary exact
`codex/m2-native-arc-foundation` authored exception; it was cancelled after GitHub environment
protection rejected that branch before attestation, disk execution and finalization steps ran
([run 37661490433](https://github.com/dsx-ai-factory/nvkr/actions/runs/37661490433)).
That exception is removed from the authored inputs and regenerated bundle. No deployment-policy
entry was added, and existing unrelated entries are not changed here. Any further branch test
needs a separate non-merge candidate and actual environment admission; this maintained bundle
does not authorize it.
The equivalent validation-branch bundle completed all jobs in
[run 37533806963](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963);
that run used a different exact branch exception and is not evidence for this PR's head.

Each disk selects its root and complete material closure: agent, Kubernetes distribution, Calico, OVN,
SPIRE, installer ISO and aggregate inputs. The source composer receives an explicit native architecture.
The direct-Packer contract opts into APT capture and declares the matching plain or FDE acquisition archive under the
source-owned material projection. Capture byte correspondence alone does not authenticate repository
origin or establish a complete installed-software inventory.

The Environment explicitly selects `diskEvidence: build-provenance-v1`. M2 authenticates the held disk
and its live producer/material subjects and signs evidence describing the deferred inspection scope.
Deep content inspection and reconciliation are deferred beyond the approved M3 MVP. The existing strict
inspection profiles remain available for plain disks, but this mode neither prepares nor invokes the inspector.
A successful M2 pipeline still
requires every named output, exact-subject evidence, publication, signing and verification; deferral
cannot substitute for those gates or establish qualified reproduction.

## Native execution and publication

`nvke-node-fde-amd64` and `nvke-node-fde-arm64` select the pinned source's
`nvke-node-fde.qemu.nvke-fde` target and `fde/nvke-node.qcow2` output. They reuse each architecture's existing
material producers and native toolchain. Each explicitly declares the public bootstrap `luks_passphrase: changeit`;
no private passphrase or new secret channel is admitted. Build-provenance publication binds `luks2` through
the wrapper and signed release metadata. Strict FDE inspection/publication and qualified reproduction remain
unsupported. The [historical source audit](../diagnostics/fde-audit.md) records the prior pin's shared-key
limitations; it is not an audit of the beta update. These diagnostic images make no deployed confidentiality
claim, even after TPM enrollment. `swtpm` is not required for the bake.

The [recorded AMD64 diagnostic Run](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035)
completed a standalone FDE bake and raw artifact retention. It did not exercise FDE publication, an ARM64
FDE bake, or this combined 69-output pipeline. Those operational acceptance checks remain outstanding.

`../../environments/github-release.yaml` admits only ADR-18's `githubARC: native-kvm-v1` route on
`arc-amd64-kvm` and `arc-arm64-kvm`, with required native KVM. Each execution class requests 8 CPUs,
36 GiB memory, 224 GiB workspace capacity and a six-hour disk-producer budget. Declarations are admission
budgets, not observed allocation or general runner-capacity guarantees. Docker uses the typed runtime
and producer-declared build-network controls; no ambient storage-driver workaround is selected.

`../../catalogs/node-images/release` directly retains the historical published ARM64 Docker Packer candidate.
`../../catalogs/node-images/variants/native-toolchains.yaml` substitutes the measured published AMD64 Packer pin for its nine M2
source-map consumers and the FDE root, preserving OVN's separate BuildKit pin. These historical images do not establish
reproduction of the current Dockerfile or bootstrap compiler. Internal Packer/inspector images were
published under the user's scoped candidate exception. Their source locks retain unresolved general
license/security approval gates; the exception does not mark those reviews complete. See
[Packer preparation and selection](../../../toolchains/nvkr-packer/README.md#native-preparation-and-catalog-selection)
and [inspector measurement](../../../toolchains/nvkr-disk-inspect/README.md#local-measurement-not-publication-admission).

Strict profiles contain actual native image/executable pins and conservative bounds: up to an 8 GiB
subject, 16 GiB filesystem, 28 GiB inspection memory, 224 GiB private scratch and a ten-minute deadline.
These retained profiles do not demonstrate a successful full M2 inspection or measured peak usage.

Publication uses the separate `ghcr.io/dsx-ai-factory/nvkr/m2-candidate` prefix and
`publication.mode: per-output-v1`. Each output is packaged, published, signed and verified independently.
Assembly authenticates same-Plan, same-workflow metadata and evidence; it does not download payload layers.
The release remains one signed entry point. Retry assembly may retain earlier attempt identities;
[reconciliation](../../../docs/adr/reconciliation.md#m3-mvp-scope-and-acceptance-boundary) records a successful
producer completion with mixed original attempts. Successful independent retry qualification remains unproved.

Only admitted source retrieval and pinned-source revalidation steps receive `NKE_SOURCE_TOKEN` for NVKE;
native build jobs receive source bytes without that
credential. Internal toolchain preparation uses separate ephemeral read-only package access, removed
before producer execution. Grant the dedicated reader account only its required package roles and retire
source-repository Actions access after migrating and validating the new workflow, as described in the
[production credential setup](../../../docs/generate-pipeline.md). Restrict `nvkr-arc`, `nvkr-release`
and the signing identity to the selected refs. Maintained inputs
admit `main` only and contain no validation-branch dispatch exception. Ephemeral runner isolation and
provider access restrictions remain operator obligations.

## Generate and check the maintained workflow

The same Definition also supplies the committed feature-branch development workflow through
`../../environments/github-development.yaml`. It retains all 69 outputs, including plain and public-bootstrap FDE disks on both native routes, signing and verification.
Only execution/publication policy changes: separate development environments and credentials, the
`nvkr-dev.yml` signer identity, and run-attempt-scoped repositories beneath
`ghcr.io/dsx-ai-factory/nvkr/development`. `make pipeline-dev-generate` regenerates it;
`make pipeline-dev-run PIPELINE_DEV_REPO=dsx-ai-factory/nvkr` dispatches a clean pushed branch.
See [pipeline development](../../../DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch) for operator
setup, full-attempt retries and the default-branch cleanup owner. This lane does not establish production
or qualified acceptance, and no hosted development execution is recorded yet.
Development emits its Run record without a qualification handoff and does not generate the production
qualifier/smoke-worker pair.

Run from the repository root with the pinned Go toolchain and authorized access to the exact NVKE tree.
Catalog loading re-derives source-owned pin sets and may need Git access even for generation.

```sh
GOTOOLCHAIN=local go run ./cmd/nvkr run generate \
  --definition inputs/definitions/nvke/definition.yaml \
  --environment inputs/environments/github-release.yaml \
  --qualification-policy inputs/policies/nvke-qualification.yaml \
  --engine github --root .
make workflows-check
```

These commands regenerate the producer workflow, Plan and manifest plus the separately owned qualifier
and smoke worker. The worker is the generated reusable GitHub Actions workflow `nvkr-smoke.yml`, invoked
by `nvkr-qualify.yml` at the same commit; it is not a separately authored pipeline. The qualification manifest
records both workflow files. Generation and drift checks do not execute any producer or qualification job.
The generator rejects a workflow above 500,000 bytes before writing. YAML compaction preserves the
expanded job graph and credential boundaries. Changes to authored inputs must regenerate these files;
never edit the bundle by hand.
Measure the generated production and development files after regeneration. The maintained rendering
regression rejects growth above 95% of the provider limit so
further membership expansion must first preserve compaction headroom. This is a maintenance guard,
not a different provider or runtime limit.

Source/artifact maintenance belongs to reviewed catalog changes using `catalog pin`, `catalog bump`
and `catalog pin-artifacts`. SPIRE intentionally retains the temporary selection described above;
review [NVKE #1641](https://github.com/dsx-ai-factory/nvke/issues/1641) before changing that contract.
The opt-in full graph integration runs with
`NVKR_TEST_M2_ALL_OUTPUT_GRAPHS=1` and authorized pinned-source access; ordinary tests receive no
private-source credentials. Pinning and graph tests establish their own contracts, not production launch.

## Qualification readiness

The simplified [artifact check](../../../docs/lifecycle.md#authenticate-a-qualification-candidate) takes a Run URL and a
reusable policy. Upstream embedded-component manifests are not a qualification prerequisite. The maintained
catalogs omit the optional declarations, so their metadata checks are `not-applicable`. Any declaration added
later must strictly match its referenced map at the pinned source revision, including exact membership,
versions and known source pins. That comparison reads raw upstream metadata; it cannot establish effective
composition after patches, configuration or material substitutions, or verify installed contents.

Source, patch, configuration and selected material identities remain bound to production evidence. Exact
producer Run, original attempt identities, output membership, signatures, provenance and applicable
delivered-tool smoke remain required. Authenticated partial retries within the same producer or qualification
run preserve successful output/worker evidence when its exact inputs and policy remain unchanged. No source
or artifact pin changes are needed merely to satisfy the optional metadata check.

The [candidate policy](../../policies/nvke-qualification.yaml) records concrete cosign version/executable hashes, native
smoke-image manifest digests, three tool profiles and 56 exact deployment-image scope decisions. These
independently authored trust selections are reviewed with the generated workflow pair; they are not inherited
from the producing Run. The [hosted setup guide](../../../docs/lifecycle.md#publish-qualification-evidence)
explains the policy and generated worker. Concrete pins and repository validation do not establish
organizational approval or hosted acceptance.

The verifier is [cosign v3.1.3](https://github.com/sigstore/cosign/releases/tag/v3.1.3); both Linux
executables were downloaded and their hashes matched the release asset metadata and checksum file. Smoke
uses Docker Official Debian `trixie-20261005-slim`, pinned to the native
[AMD64 manifest](https://github.com/debuerreotype/docker-debian-artifacts/blob/a3751996111ce2c1c904050c84440184535e23b1/trixie/slim/oci/index.json)
and [ARM64 manifest](https://github.com/debuerreotype/docker-debian-artifacts/blob/cf1f4a45447842b45e9952e0f018ae734a7341c7/trixie/slim/oci/index.json).
Both passed the required utility, environment and container-isolation probes. Catalog-pinned Helm and
kubectl bytes passed their fixed version commands. ARM64 utility execution was native; AMD64 probes used
local emulation. No fresh nvkectl artifact or hosted qualification was exercised by these checks.

The reviewed implementation, policy and generated workflows may merge before the real acceptance runs.
After merge, complete a producer run with its original evidence and successful-attempt handoff, followed by native delivered-tool
checks, hosted positive/negative qualification and independent complete-set consumer verification. Those
results remain unproved and required before production-readiness or operational-acceptance claims. Earlier
successful builds and local contract tests do not replace them. Runtime authority checks remain unchanged.

## Recorded delivery and remaining acceptance

[Run 37533806963](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md) completed all 34 outputs and aggregate
publication, signing, verification and finalization. It used an exact validation-branch dispatch/signing
exception absent from the maintained main-only inputs. Independent full-payload signed disk consumption,
main-only acceptance and successful hosted mixed-attempt qualification remain unproved. Earlier deep inspections
timed out; the successful candidate used the explicit build-provenance mode.

CAPNICo, the provisioned provider, NKTP and the reference config-profile image are explicit required
members, not substitutes for each other or claims of deployed acceptance. Production fleet-profile
selection and provider/resource delivery remain separate gaps described above. Embedded SPIRE and
chart-side SPIRE containers are distinct artifacts; one cannot satisfy the other's evidence row.
M3 build/evidence qualification, delivered-tool smoke and organizational toolchain reviews remain product
acceptance work. Full disk inspection is a separate deferred capability, not an M3 MVP prerequisite.

[Reconciliation](../../../docs/adr/reconciliation.md) owns current implementation/evidence status;
[the epic specification](../../../docs/epics-v1-spec.md) owns product acceptance. This README owns the
candidate input selections and the maintained workflow instructions.
