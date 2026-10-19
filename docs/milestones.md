# NVKR v1 milestones

This roadmap summarizes the `pipeline-attested` delivery milestones. The
[acceptance specification](epics-v1-spec.md) owns artifact scope, numbered acceptance criteria,
approved fallbacks and completion requirements. [Reconciliation](adr/reconciliation.md) owns
implementation status and scoped operational evidence. A plan, local dry run or contract test does
not complete a milestone.

| Milestone | Outcome | Acceptance owner |
|---|---|---|
| M1 — Vertical slice | One pinned NVKE CLI built, published, signed and independently verified through a generated default-branch workflow | [M1 criteria](epics-v1-spec.md#epic-m1--vertical-slice) |
| M2 — Breadth | The complete required artifact set, including the NVKE OS image, with exact output membership and source-bound production evidence | [M2 criteria](epics-v1-spec.md#epic-m2--breadth) |
| M3 — Qualification | Complete-set build/evidence checks, delivered-tool smoke and signed pass/fail evidence under an independent identity | [M3 criteria](epics-v1-spec.md#epic-m3--qualification) |
| M4 — Automated releases | Reviewed pin updates, signed immutable release manifests, safe channel/tag behavior and revocation | [M4 criteria](epics-v1-spec.md#epic-m4--automated-releases) |

M2 disk delivery binds the exact produced disk and traced materials. The
initial scope decision
retains producer-only disk evidence and defers deep inventory, material reconciliation, waivers, offline
pull-request qualification, a dedicated verify-only dispatch and incremental reuse.

The current scope makes embedded-component declarations optional. Omission is not
applicable, while authored declarations must match their referenced
pinned upstream metadata. This comparison does not establish customized or installed composition. Exact source,
patch, configuration and material bindings remain required. A stronger input claim does not reproduce the
aggregate disk. M3 qualification is a build/evidence gate; cluster conformance and exact-byte reproduction
remain outside it. M4 does not grant the separate qualified-lane clearance.

Generated execution defaults to generic GitHub-hosted runners. ADR-18 admits only the explicit native
KVM ARC disk route; it does not permit arbitrary self-hosted labels or qualified claims. The
[34-output M2 candidate](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md) succeeded under that route and an
explicit validation-branch signing exception. It does not establish main-only execution, independent
full-payload disk consumption, full inventory, organizational approval or complete milestone acceptance.

V1 acceptance targets amd64. Additional native-architecture candidate evidence remains scoped to its
recorded inputs. NVKE owns embedded-software correctness and runtime validation; downstream consumers
own deployment topology and placement. The acceptance specification retains the full dependencies,
artifact matrix obligations and exclusions.

The separately authored [NVKE nightly E2E adapter](nvke-nightly.md) is downstream
repository CI infrastructure outside these product milestones. Its first target
uses kind and native ARM64 KVM guests under ADR-18's separate test-adapter contract;
it does not widen the generated ARC disk engine or complete M3 qualification.
NVKE owns guest/cluster provisioning and teardown; the parent harness owns outer
host and network restoration. A successful combined cycle remains unrecorded.

M2’s product floor remains AMD64 producer provenance. The approved initial scope defers strict
inspection and material reconciliation without organizational ADR ratification. ARM64 is an implementation extension; every additional
output explicitly selected by a Definition remains mandatory for that Run and Release.
