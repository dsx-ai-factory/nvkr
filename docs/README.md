# Documentation

| Need | Read |
|---|---|
| Build/package the pinned DSX OVN fork and declare native image/Helm OCI outputs | [OVN source-build option](../definitions/nke-ovn-source/README.md) |
| Build the NKE v0.21.0 deploy release | [NKE deploy pipeline](nke-deploy-pipeline.md) |
| Automate reviewable pin updates and matching regeneration | [Catalog maintenance](catalog-maintenance.md) |
| Manage shared catalogs and understand replacement identity | [Authoring analysis](catalog-definition-authoring.md), [local replacement lab](examples/authoring-lab/README.md) |
| Inspect the incomplete combined M2 candidate and its input gaps | [M2 candidate inputs](../definitions/nke-m2/README.md), [input mapping](m2-input-evidence-map.md), [customer supplier choices](nke-m2-customer-choices.md), [release carrier transport evidence](evidence/2026-10-06-m2-release-carrier-transport.md), [October 5 diagnostic observations](evidence/2026-10-05-m2-input-acquisition.md) |
| Derive several pins from one upstream version file | [Generic pin-set example](generic-pin-set-example.md) |
| Use Definition → Run → Release | [Lifecycle guide](lifecycle.md) |
| Choose and run a demo | [Demo index](../scripts/README.md) |
| Learn the six public objects | [Domain vocabulary](../CONTEXT.md) |
| Understand architecture and failure behavior | [SADD](sadd.md) |
| Review generated Plans, visualize dependencies and explain regeneration changes | [Plan and graph inspection](plan-and-graph-inspection.md) |
| Inspect the single release registry graph and signing scope | [Publication architecture](publication-architecture.md) |
| Browse a registry release and its artifact/evidence relationships | [Release navigation](release-navigation.md) |
| Change an engineering contract | Current [ADRs](adr/); public lifecycle in [ADR-17](adr/0017-cli-sdk-foundation-and-package-topology.md) |
| Determine what works and what is deferred | [Implementation/evidence record](adr/reconciliation.md) |
| Plan v1 delivery | [Milestones](milestones.md), [acceptance specification](epics-v1-spec.md) |
| Build and verify the combined CLI, CWE image and Helm outputs | [Combined pipeline](combined-nke-pipeline.md), [hosted acceptance](evidence/2026-09-10-combined-run.md) |
| Build or validate the NKE node image | [OS-image Definition](../definitions/nke-os/README.md), [validation plan](nke-os-validation-plan.md), [ARM64 execution evidence](evidence/2026-09-23-nke-arm64-local-os-build.md), [ARC ARM64/AMD64 evidence](evidence/2026-10-01-nke-arc-os-builds.md) |
| Build public OS binaries directly from pinned upstream Git (experimental, not the full image) | [Source-build catalog and tests](../definitions/nke-os-source/README.md), [v0.21.0 dependency inventory](nke-v0.21.0-os-dependency-inventory.md) |
| Bind controller charts to image outputs without registry-specific bytes | [Declarative Helm bindings](declarative-helm-bindings.md) |
| Pin and package NKE's external charts locally | [Acquired chart guide](nke-pin-set-usage.md) |
| Reproduce the local NKE demonstration | [NKE demo](demo-m1.md) |
| Inspect actual past execution | Dated [evidence](evidence/) |
| Understand retained modules and cleanup decisions | [Repository cleanup](repository-cleanup.md) |
| Understand code organization findings and their implementation | [Codebase organization audit](codebase-organization-audit.md) |
| Find the failed operation and understand generated transfer steps | [CI steps and log guide](ci-step-and-log-readability.md) |
| Understand slow repository checks and measured improvements | [CI test performance](ci-test-performance.md) |
| Understand current GitHub files and execution limits | [GitHub audit](github-audit.md) |
| Inspect artifact dependencies and SBOM scope | [Dependency SBOMs](dependency-sboms.md) |
| Understand the internal staged disk-material tracing contract and its gaps | [Local disk material tracing](nke-os-material-trace.md) |
| Check guest package capture bytes and understand their diagnostic scope | [APT capture contract](nke-os-acquisition-contract.md), [retained byte-correspondence evidence](evidence/2026-10-03-apt-capture-correspondence.md) |
| Inspect installed packages before a local Run exports its QCOW2 (optional SDK path, not a full SBOM) | [Live disk inspection](nke-os-live-inspection.md), [diagnostic inspector runtime](../toolchains/nkd-disk-inspect/README.md), [recorded runtime-only inspection](evidence/2026-10-02-nke-os-disk-inspection.md) |
| Build or contribute | [Development](../DEVELOPMENT.md), [contribution guide](../CONTRIBUTING.md) |
| Triage repository vulnerabilities | [CVE management](cve-management.md), [security reporting](../SECURITY.md) |
| Manage GitHub issues, discussions, and pull requests | [GitHub issue and PR management](github-issue-pr-management.md) |

ADRs contain current decisions directly. Update the owning ADR and reconciliation when changing a contract;
create a new ADR only for an independently owned decision. Keep the SADD and glossary consistent. A current
implementation claim needs tests or a recorded run; proposed capability is labeled deferred in its owning section.

Superseded plans, design drafts, and the former SRD are removed from the working tree. They remain available
in Git history (the last baseline containing them is `1793699`). Current requirements live in the milestone
specification and owning ADRs. Older FR/SEC/UC identifiers in contracts and code are historical trace labels;
the current documents state the requirements directly.

Dated evidence is preserved with its original scope, rather than rewritten as current operational acceptance.
Current architecture diagrams live in the SADD and the publication architecture document.
