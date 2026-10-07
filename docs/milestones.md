# NKD v1 milestones

This roadmap summarizes the `pipeline-attested` delivery milestones. The
[acceptance specification](epics-v1-spec.md) owns artifact scope, numbered acceptance criteria,
approved fallbacks and completion requirements. [Reconciliation](adr/reconciliation.md) owns
implementation status and scoped operational evidence. A plan, local dry run or contract test does
not complete a milestone.

| Milestone | Outcome | Acceptance owner |
|---|---|---|
| M1 — Vertical slice | One pinned NKE CLI built, published, signed and independently verified through a generated default-branch workflow | [M1 criteria](epics-v1-spec.md#epic-m1--vertical-slice) |
| M2 — Breadth | The complete required artifact set, including the NKE OS image, with exact output membership and source-bound production evidence | [M2 criteria](epics-v1-spec.md#epic-m2--breadth) |
| M3 — Qualification | Separate signed build/evidence verdicts, tooling smoke checks, approved waivers and full disk inventory/reconciliation | [M3 criteria](epics-v1-spec.md#epic-m3--qualification) |
| M4 — Automated releases | Reviewed pin updates, signed immutable release manifests, safe channel/tag behavior and revocation | [M4 criteria](epics-v1-spec.md#epic-m4--automated-releases) |

M2 disk delivery binds the exact produced disk and traced materials while explicitly deferring deep
inventory and material-to-filesystem reconciliation to M3. A stronger input claim does not reproduce
the aggregate disk. M3 qualification is a build/evidence gate, not cluster conformance or exact-byte
reproduction. M4 does not grant the separate qualified-lane clearance.

Generated execution defaults to generic GitHub-hosted runners. ADR-18 admits only the explicit native
KVM ARC disk route; it does not permit arbitrary self-hosted labels or qualified claims. The
[34-output M2 candidate](evidence/2026-10-06-team-demo-pipeline.md) succeeded under that route and an
explicit validation-branch signing exception. It does not establish main-only execution, independent
full-payload disk consumption, full inventory, organizational approval or complete milestone acceptance.

V1 acceptance targets amd64. Additional native-architecture candidate evidence remains scoped to its
recorded inputs. NKE owns embedded-software correctness and runtime validation; downstream consumers
own deployment topology and placement. The acceptance specification retains the full dependencies,
artifact matrix obligations and exclusions.

M2’s product floor remains AMD64 producer provenance; M3 adds strict inspection and material
reconciliation. Ilya Kislenko’s October 7 task-chat instruction preserves this boundary without
organizational ratification or a Jira update. ARM64 is an implementation extension; every additional
output explicitly selected by a Definition remains mandatory for that Run and Release.
