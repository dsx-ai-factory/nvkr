# Documentation

Start with the [quickstart](quickstart.md): validate a Definition, inspect a Plan, and preview a Run
without Docker. The [34-concept reference](user-concepts.md) explains the vocabulary and connects
each concept to a worked example.

## User tasks

| I want to… | Read |
|---|---|
| Start local ARM64 NKE development, including images and charts | [First-time local build guide](local-development.md), [example inputs](../definitions/nke-laptop-arm64/README.md) |
| Boot an existing NKD disk through the real local NICoSim VM backend | [Artifact-only QCOW2 guest probe](local-qcow2-guest-probe.md) |
| Understand Lia's local Colima/NICoSim demo and the missing NKD-to-deployment handoff | [Whole-demo analysis](lia-local-demo-analysis.md), [staged-installer handoff](nkd-to-nke-demo.md), [tested container bootstrap](evidence/2026-10-07-local-container-bootstrap.md), [source-built simulator evidence](evidence/2026-10-07-local-nicosim-build.md) |
| Determine what remains for a full source-built local NKE distribution | [Full-source gap map](full-nke-source-build.md), [provider build evidence](evidence/2026-10-07-laptop-provider-builds.md) |
| Inspect my first Plan | [Quickstart](quickstart.md) |
| Choose components, pin inputs, and scope replacements | [Catalogs and components](catalogs-and-components.md) |
| Visualize dependencies and generate GitHub Actions | [Pipeline generation](generate-pipeline.md) |
| Understand how heterogeneous materials become a disk | [Node images](node-images.md) |
| Find published outputs and verify a consumer claim | [Release quickstart](release-quickstart.md) |
| Try component replacements locally | [Runnable authoring lab](examples/authoring-lab/README.md) |
| Build and recover the local NKE CLI publication | [Local NKE walkthrough](../demos/m1-local.md) |

Example-specific inputs and commands live beside their Definitions: [three-output tutorial](../definitions/nke-v1/README.md),
[six-output deploy example](../definitions/nke-deploy/README.md), [34-output M2 candidate and committed workflow](../definitions/nke-m2/README.md),
and [OS-image routes](../definitions/nke-os/README.md). The [demo index](../demos/README.md) lists presentations, runnable journeys, and prerequisites.

Experimental [public OS source producers](../definitions/nke-os-source/README.md) and the
[OVN source-build option](../definitions/nke-ovn-source/README.md) are separate candidates, not full OS
migration or maintained M2 membership. The [v0.21.0 dependency inventory](nke-v0.21.0-os-dependency-inventory.md)
records source availability and remaining gaps.

## Detailed references

| Topic | Reference |
|---|---|
| Immutable attempts, completeness, consumer policy, and SDK | [Lifecycle](lifecycle.md) |
| Canonical Plans, graph views, comparison, and drift | [Plan and graph inspection](plan-and-graph-inspection.md) |
| Registry/layout browsing, credentials, and metadata limits | [Release navigation](release-navigation.md) |
| Publication graph, artifact subjects, and signing scope | [Publication architecture](publication-architecture.md) |
| Coordinated upstream versions and exact locks | [Generic pin-set example](generic-pin-set-example.md), [NKE acquired charts](nke-pin-set-usage.md) |
| Reviewable updates and matching regeneration | [Catalog maintenance](catalog-maintenance.md) |
| Chart slots, image outputs, and registry-independent bytes | [Declarative Helm bindings](declarative-helm-bindings.md) |
| Software inventories and their exact subjects/stages | [Dependency SBOMs](dependency-sboms.md) |
| Bounded guest capture format and acquisition ownership | [OS acquisition contract](nke-os-acquisition-contract.md) |
| Optional live-owner disk inspection and material observations | [OS live inspection](nke-os-live-inspection.md) |

## Engineering and maintenance

[CONTEXT](../CONTEXT.md) owns formal vocabulary; the [SADD](sadd.md) explains architecture and failure behavior.
The [ADRs](adr) own engineering contracts, and [reconciliation](adr/reconciliation.md) is the current
implementation/evidence ledger. The [roadmap](milestones.md) links outcomes to the detailed
[acceptance specification](epics-v1-spec.md).

[Development](../DEVELOPMENT.md) covers setup, package ownership, generated files, and the completion gate.
Use [CI steps and logs](ci-step-and-log-readability.md) to investigate failures and
[CI test performance](ci-test-performance.md) for the current profiling follow-up.
[CVE management](cve-management.md), [issue and PR management](github-issue-pr-management.md), and
[contribution guidance](../CONTRIBUTING.md) own ongoing maintainer procedures.

Reconciliation cites scoped external Runs and historical observations. Unit tests, a Plan, metadata
browsing, and authenticated consumer verification establish different facts. Historical captures,
superseded plans, and completed progress reports remain in Git history.

## Demos

The [demos directory](../demos/README.md) contains the 14-slide deck, 34 concept examples,
HTML walkthrough, offline graph explorer, facilitator guide, and self-guided scripts. The presentation
includes a fast real local build and the v4 publication from Run 37736529644; the task guides above
own current user instructions.

[NKE M2 customer choices](nke-m2-customer-choices.md) explains supplier replacement and its current validation scope.
