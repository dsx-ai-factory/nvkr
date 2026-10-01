# Documentation

| Need | Read |
|---|---|
| Derive several pins from one upstream version file | [Generic pin-set example](generic-pin-set-example.md) |
| Use Definition → Run → Release | [Lifecycle guide](lifecycle.md) |
| Choose and run a demo | [Demo index](../scripts/README.md) |
| Learn the six public objects | [Domain vocabulary](../CONTEXT.md) |
| Understand architecture and failure behavior | [SADD](sadd.md) |
| Inspect the single release registry graph and signing scope | [Publication architecture](publication-architecture.md) |
| Change an engineering contract | Current [ADRs](adr/); public lifecycle in [ADR-17](adr/0017-cli-sdk-foundation-and-package-topology.md) |
| Determine what works and what is deferred | [Implementation/evidence record](adr/reconciliation.md) |
| Plan v1 delivery | [Milestones](milestones.md), [acceptance specification](epics-v1-spec.md) |
| Build and verify the combined CLI, CWE image and Helm outputs | [Combined pipeline](combined-nke-pipeline.md), [hosted acceptance](evidence/2026-09-10-combined-run.md) |
| Build or validate the NKE node image | [OS-image Definition](../definitions/nke-os/README.md), [validation plan](nke-os-validation-plan.md), [ARM64 execution evidence](evidence/2026-09-23-nke-arm64-local-os-build.md), [ARC ARM64/AMD64 evidence](evidence/2026-10-01-nke-arc-os-builds.md) |
| Bind controller charts to image outputs without registry-specific bytes | [Declarative Helm bindings](declarative-helm-bindings.md) |
| Pin and package NKE's external charts locally | [Acquired chart guide](nke-pin-set-usage.md) |
| Reproduce the local NKE demonstration | [NKE demo](demo-m1.md) |
| Inspect actual past execution | Dated [evidence](evidence/) |
| Understand retained modules and cleanup decisions | [Repository cleanup](repository-cleanup.md) |
| Understand code organization findings and their implementation | [Codebase organization audit](codebase-organization-audit.md) |
| Understand current GitHub files and execution limits | [GitHub audit](github-audit.md) |
| Inspect artifact dependencies and SBOM scope | [Dependency SBOMs](dependency-sboms.md) |
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
