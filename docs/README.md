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
| Visualize dependencies and generate GitHub Actions | [Pipeline generation](generate-pipeline.md) |
| Understand how heterogeneous materials become a disk | [Node images](node-images.md) |
| Find published outputs, download a named output, and verify a consumer claim | [Release quickstart](release-quickstart.md) |
| Build nightly NVKR candidates and run NVKE end-to-end tests | [Nightly workflow and current admission](nvke-nightly.md) |
| Check a producing Run and configure qualification once | [Artifact checks and hosted qualification](lifecycle.md#authenticate-a-qualification-candidate), [candidate readiness](../inputs/definitions/nvke/README.md#qualification-readiness) |
| Try component replacements locally | [Runnable authoring lab](examples/authoring-lab/README.md) |
| Build and recover the local NVKE CLI publication | [Local NVKE walkthrough](../demos/m1-local.md) |

Example-specific inputs and commands live beside their Definitions: [three-output tutorial](examples/nvke-tutorial/README.md),
[nine-output deploy example](examples/nvke-deploy/README.md), [69-output M2 candidate and committed workflow](../inputs/definitions/nvke/README.md),
and [OS-image routes](../inputs/definitions/diagnostics/README.md). The separate
[Ubuntu 24 inputs](../inputs/definitions/diagnostics/ubuntu24/README.md) select native plain and
public-bootstrap FDE disk builds alongside the existing Ubuntu 26 default. The
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
| Registry/layout browsing, generic JSON export, output downloads, credentials, and metadata limits | [Release navigation](release-navigation.md) |
| Publication graph, artifact subjects, and signing scope | [Publication architecture](publication-architecture.md) |
| Coordinated upstream versions and exact locks | [Generic pin-set example](generic-pin-set-example.md), [NVKE acquired charts](nvke-pin-set-usage.md) |
| Reviewable updates and matching regeneration (the installed `catalog-refresh.yaml` is generated, never hand-edited) | [Catalog maintenance](catalog-maintenance.md) |
| Chart slots, image outputs, and registry-independent bytes | [Declarative Helm bindings](declarative-helm-bindings.md) |
| Software inventories and their exact subjects/stages | [Dependency SBOMs](dependency-sboms.md) |
| Discover and explicitly select provisional CLI validators | [CLI artifact testing](artifact-testing.md#provisional-validator-catalog) |
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
The [ADRs](adr) own engineering contracts, and [reconciliation](adr/reconciliation.md) is the current
implementation/evidence ledger. The [roadmap](milestones.md) links outcomes to the detailed
[acceptance specification](epics-v1-spec.md).

Proposed [ADR-24: imported validator catalog](adr/0024-imported-validator-catalog.md) defines discovery,
adapter selection and artifact bindings across repositories, including its diagnostic and evidence limits.
The provisional local-plan CLI slice implements catalog discovery and versioned-profile execution;
broader adapter and profile support remains deferred.

[Development](../DEVELOPMENT.md) covers setup, package ownership, generated files, and the completion gate.
Its [pipeline development loop](../DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch) covers branch
dispatch, separate generated development bundles, artifact downloads and short retention.
Use [CI steps and logs](ci-step-and-log-readability.md) to investigate failures and
[CI test performance](ci-test-performance.md) for the current profiling follow-up.
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
