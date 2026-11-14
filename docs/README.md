# Documentation

Start with the [quickstart](quickstart.md): copy one block to build NVKR from source and produce
the NVKE CLI, controller image, Helm chart and dependency SBOMs locally on macOS or Linux.
The guide shows the resulting files and links to larger builds, publication and deployment.
For a preview without Docker, see the [tutorial inputs](examples/nvke-tutorial/README.md#preview-the-native-build).
The [34-concept reference](user-concepts.md) explains the vocabulary and connects each concept to a worked example.

## User tasks

| I want to… | Read |
|---|---|
| Ask an AI agent to acquire the CLI, author inputs, and operate NVKR | [Use NVKR with Your AI Agent](use-with-agents.md), [operator skill](../.agents/skills/nvkr-operator/SKILL.md) |
| Understand NVKR and try my first local artifact | [Quickstart](quickstart.md) |
| Build only the CLI for planning and inspection | [CLI setup](quickstart.md#build-only-the-cli) |
| Present catalog → build → publication → NVKE through the Go CLIs | [Runnable commands](nvkr-demo.md) |
| Start local Linux ARM64 NVKE development, including images and charts | [First-time local build guide](local-development.md), [example inputs](../inputs/definitions/demo/README.md) |
| Build and run the native Ubuntu ARM64 or Mac demo | [Shared build-and-demo guide and validation limits](local-nvke-demo.md) |
| Understand native Linux/Mac execution with one QEMU site and two tenant guests | [Shared native topology and validation scope](local-nvke-demo.md#native-linux-and-mac-path) |
| Inspect my first Plan | [Plan and graph inspection](plan-and-graph-inspection.md) |
| Browse public command results | [Command result browser](lifecycle.md#browse-public-command-results) |
| Watch a local Run or inspect its recorded outcome | [Lifecycle dashboard and read-only inspection](lifecycle.md#choose-local-run-presentation) |
| Choose components, pin inputs, and scope replacements | [Catalogs and components](catalogs-and-components.md) |
| Start a small executable Definition from catalog components | [Definition init](lifecycle.md#start-a-small-executable-definition) |
| Visualize dependencies and generate GitHub Actions or Tekton | [Pipeline generation](generate-pipeline.md) |
| Understand how heterogeneous materials become a disk | [Node images](node-images.md) |
| Build AICR deployment bundles as OCI artifacts | [AICR inputs, supported hardware recipes and rendering limits](../inputs/definitions/aicr/README.md) |
| Find published outputs, download a named output, and verify a consumer claim | [Release quickstart](release-quickstart.md) |
| Diagnose Helm/registry publication errors and select an explicit compatible format | [Registry troubleshooting](publication-architecture.md#troubleshoot-helm-and-registry-rejections), [format selection](publication-architecture.md#oci-representation) |
| Plan an NVKE GPU site from NVKR Releases (not yet runnable end to end) | [Working Definitions and GPU system matrix](../inputs/definitions/nvke/README.md#working-definitions-for-an-nvke-gpu-site), [site install outline](release-quickstart.md#install-an-nvke-site-from-the-releases) |
| Run the nightly NVKE build and end-to-end test | [Nightly workflow and current admission](nvke-nightly.md) |
| Run full build/site/tenant E2E on my PR before merging | [Run E2E tests for your pull request](nvke-nightly.md#run-a-workflow-branch-before-merging) |
| Check a producing Run and configure qualification once | [Artifact checks and hosted qualification](lifecycle.md#authenticate-a-qualification-candidate), [candidate readiness](../inputs/definitions/nvke/README.md#qualification-readiness) |
| Try component replacements locally | [Runnable authoring lab](examples/authoring-lab/README.md) |
| Build and recover the local NVKE CLI publication | [Local NVKE walkthrough](../demos/m1-local.md) |

Example-specific inputs and commands live beside their Definitions: [three-output tutorial](examples/nvke-tutorial/README.md),
[nine-output deploy example](examples/nvke-deploy/README.md), [69-output M2 candidate and committed workflow](../inputs/definitions/nvke/README.md),
and [OS-image routes](../inputs/definitions/diagnostics/README.md). The separate
[Ubuntu 24 inputs](../inputs/definitions/diagnostics/ubuntu24/README.md) select build-only native plain and
public-bootstrap FDE disk diagnostics; the sibling Ubuntu 24.04 disks family selects them for publication. The
[FDE-only diagnostic workflow](../inputs/definitions/diagnostics/fde.md) selects one native disk for branch iteration.
The [demo index](../demos/README.md) lists presentations, runnable journeys, and prerequisites.

Experimental [public OS source producers](../inputs/experiments/public-os-sources/README.md) and the
[OVN source-build option](../inputs/catalogs/ovn-nvidia/README.md) are separate candidates, not full OS
migration or maintained M2 membership. The [v0.21.0 dependency inventory](nvke-v0.21.0-os-dependency-inventory.md)
records source availability and remaining gaps.

## Detailed references

| Topic | Reference |
|---|---|
| Immutable attempts, completeness, consumer policy, and SDK | [Lifecycle](lifecycle.md) |
| Canonical Plans, graph views, comparison, and drift | [Plan and graph inspection](plan-and-graph-inspection.md) |
| Registry/layout browsing, comparing two Releases, generic JSON export, output downloads, credentials, and metadata limits | [Release navigation](release-navigation.md) |
| Publication graph, artifact subjects, and signing scope | [Publication architecture](publication-architecture.md) |
| Configure or review required object-store publication | [Publication guide](publication-architecture.md#required-s3-copy), [ADR-20 contract](adr/0020-artifact-publication-and-target-configuration.md#s3-file-delivery-contract), [implementation boundary](adr/reconciliation/object-store-publication.md#object-store-publication) |
| Review object-store dependency admission | [Dated dependency evidence](evidence/2026-11-04-object-store-dependency-admission.md) |
| Coordinated upstream versions and exact locks | [Generic pin-set example](generic-pin-set-example.md), [NVKE acquired charts](nvke-pin-set-usage.md) |
| Reviewable updates and matching regeneration (the installed `catalog-refresh.yaml` is generated, never hand-edited) | [Catalog maintenance](catalog-maintenance.md) |
| Refresh native pipeline Packer and inspector images, review exceptions, or roll back pins | [Toolchain maintenance](toolchain-maintenance.md) |
| Chart slots, image outputs, and registry-independent bytes | [Declarative Helm bindings](declarative-helm-bindings.md) |
| Software inventories and their exact subjects/stages | [Dependency SBOMs](dependency-sboms.md) |
| Discover and explicitly select provisional CLI validators | [CLI artifact testing](artifact-testing.md#run-locally) |
| External test output descriptions and configured diagnostic checks | [External test output contract](external-test-output-contract.md) |
| Bounded guest capture format and acquisition ownership | [OS acquisition contract](nvke-os-acquisition-contract.md) |
| Optional live-owner disk inspection and material observations | [OS live inspection](nvke-os-live-inspection.md) |

## Local ARM64 diagnostics

| Task | Guide |
|---|---|
| Build artifacts, install a local site, and validate a real QCOW2-backed tenant plus workload | [Integrated Ubuntu ARM64 guide](local-nvke-demo.md) |
| Review exact-revision evidence from the successful fresh upstream OVN diagnostic | [2026-10-11 runtime evidence](evidence/local-upstream-ovn-2026-10-11.md) |
| Build the diagnostic ARM64/Ubuntu 26.04 plain controller profiles image | [Local profiles guide](local-controller-profiles.md) |
| Inspect older staged-installer sessions and recovery controls | [Legacy handoff and recovery](nvkr-to-nvke-demo.md) |
| Determine what remains for a full source-built local NVKE distribution | [Full-source gap map](full-nvke-source-build.md) |

## Engineering and maintenance

[Input layout and consolidation](input-layout-migration.md) maps every relocated Definition and Environment,
separates the completed ownership move from conditional consolidation, and records the remaining parity gaps.
The [input index](../inputs/README.md) and [catalog ownership map](../inputs/catalogs/README.md) own current locations.

[CONTEXT](../CONTEXT.md) owns formal vocabulary; the [SADD](sadd.md) explains architecture and failure behavior.
The [ADRs](adr) own engineering contracts, and [reconciliation](adr/reconciliation/README.md) is the current
implementation/evidence ledger. The [roadmap](milestones.md) links outcomes to the detailed
[acceptance specification](epics-v1-spec.md).

[Definition partitioning strategy](definition-partitioning-strategy.md) explains the NVKE family
pilot, historical graph-capacity measurements, shared catalog ownership and related Release options.
The [input instructions](../inputs/definitions/nvke/README.md#family-pilot) own generation and checking;
the combined candidate remains available pending consumer and qualification cutover.

Proposed [ADR-24: imported validator catalog](adr/0024-imported-validator-catalog.md) defines discovery,
adapter selection and artifact bindings across repositories, including its diagnostic and evidence limits.
The provisional local-plan CLI slice implements catalog discovery and versioned-profile execution;
broader adapter and profile support remains deferred.

Proposed [ADR-26: Definition composition](adr/0026-definition-composition.md) designs composite Definitions
that release several digest-pinned Definitions as one Release, with a worked NVKE four-family example and
implementation units. Units U1–U8 are implemented and contract-tested. The
[hosted publication record](adr/reconciliation/adr-26.md#hosted-gpu-site-publication) owns execution evidence
and its consumer-acceptance and qualification limits.
Member Release reuse and local composite qualification rehearsal remain deferred.

Proposed [ADR-27: Pinned-tool producers](adr/0027-pinned-tool-producers.md) designs producers that run one
released, digest-pinned tool offline with fixed arguments and typed inputs, with AICR bundles as the first use.
It is design only.

Proposed [ADR-28: Multi-platform executables](adr/0028-multi-platform-executable-indexes.md) designs one tool
source for every platform of a release, where the platforms share a format, companions and admitted URL
spellings, and executable outputs published as standard OCI image indexes. The AICR CLI is the first
migration, and the ADR inventories existing executables to retrofit. It is design only.

Proposed [ADR-29: Cluster observation and artifact attribution](adr/0029-cluster-observation-and-artifact-attribution.md)
and its [feature design](cluster-observation-and-artifact-attribution.md) describe gathering cluster facts
and optionally comparing them with selected NVKR artifacts. The design names AICR Go APIs, reuse of NVKR's
existing OCI identity rules, access boundaries and missing measurements. It adds no standalone collector or
test, signing or qualification capability.

Proposed [ADR-30: authenticated result reuse](adr/0030-authenticated-result-reuse.md) designs cross-Run reuse
of complete named outputs and the test matrix for implementing it; reuse is not implemented.

Proposed [ADR-31: Qualification results index](adr/0031-qualification-results-index.md) publishes each production
qualification attempt, failures included, as an unsigned outcome and a TestGrid build. The selection, projection
and separate publisher helper are implemented and contract-tested only; the generated publish job is not
implemented, and nothing has been published.

[Development](../DEVELOPMENT.md) covers setup, package ownership, generated files, and the completion gate.
Its [pipeline development loop](../DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch) covers branch
dispatch, separate generated development bundles, artifact downloads and short retention.
Use [CI steps and logs](ci-step-and-log-readability.md) to investigate failures and
[CI test performance](ci-test-performance.md) for the current profiling follow-up.
[Execution partition scheduling](execution-partition-scheduling.md) records the partition layout measurement and decision.
[CVE management](cve-management.md), [issue and PR management](github-issue-pr-management.md), and
[contribution guidance](../CONTRIBUTING.md) own ongoing maintainer procedures.
The [source-release procedure](../RELEASE.md) covers releasing NVKR itself,
building a released tag, validated SBOM evidence, approval and recovery.

Reconciliation cites scoped external Runs and historical observations. Unit tests, a Plan, metadata
browsing, and authenticated consumer verification establish different facts. Historical captures,
superseded plans, and completed progress reports remain in Git history.

## Demos

The [demos directory](../demos/README.md) contains the 14-slide deck, 34 concept examples,
HTML walkthrough, offline graph explorer, facilitator guide, and self-guided scripts. The presentation
includes a fast real local build and the v4 publication from Run 37736529644; the task guides above
own current user instructions.

[NVKE customer choices](nvke-customer-choices.md) explains supplier replacement and its current validation scope.

[Multiarch CWE delivery](multiarch-cwe-delivery.md) describes aggregate image membership, evidence and current validation limits.
