# Documentation

| Need | Read |
|---|---|
| Use Definition → Run → Release | [Lifecycle guide](lifecycle.md) |
| Choose and run a demo | [Demo index](../scripts/README.md) |
| Learn the seven public objects | [Domain vocabulary](../CONTEXT.md) |
| Understand architecture and failure behavior | [SADD](sadd.md) |
| Change an engineering contract | Current [ADRs](adr/); public lifecycle in [ADR-17](adr/0017-cli-sdk-foundation-and-package-topology.md) |
| Determine what works and what is deferred | [Implementation/evidence record](adr/reconciliation.md) |
| Plan v1 delivery | [Milestones](milestones.md), [acceptance specification](epics-v1-spec.md) |
| Reproduce the local NKE demonstration | [NKE demo](demo-m1.md) |
| Inspect actual past execution | Dated [evidence](evidence/) |
| Understand retained modules and cleanup decisions | [Repository cleanup](repository-cleanup.md) |
| Build or contribute | [Development](../DEVELOPMENT.md), [contribution guide](../CONTRIBUTING.md) |

ADRs contain current decisions directly. Update the owning ADR and reconciliation when changing a contract;
create a new ADR only for an independently owned decision. Keep the SADD and glossary consistent. A current
implementation claim needs tests or a recorded run; proposed capability is labeled deferred in its owning section.

Historical planning and requirement exports live under [archive](archive/). They retain
their original scope and cannot establish current support or acceptance. The historical [SRD](srd.md) retains
FR/SEC/UC identifiers for traceability. Dated evidence is preserved rather than rewritten as a current demo.
Historical presentation exports are kept outside the repository. Current architecture diagrams live in the SADD.

Retired amendment references in historical records map to current owners: 23 → 18 (isolation); 24 → 20
(registration); 25 → 05/16/19 (coverage, handoff, cluster result); 26 → 04/06/09/10/13/16/18/21 (v1 contracts);
27 → 17/19/20/21 (public lifecycle). The amendment documents and superseded migration/review narratives have
been removed after their decisions and final evidence were incorporated.
