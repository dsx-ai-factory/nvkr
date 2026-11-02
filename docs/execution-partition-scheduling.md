# Execution partition scheduling

Opt-in GitHub execution partitioning places the original jobs of one logical Run into same-commit worker
workflows. A worker call completes only when every member completes, so a job waits for its partition's
slowest sibling as well as its own prerequisites. This page records how the grouping was measured, the
layouts compared, and the decision. Partitioning changes execution organization only: one Definition, one
complete Plan, one logical Run, one finalizer and one Release remain, and no layout changes Plan, Definition
or Build identity.

## Method

`TestExecutionScheduleHarness` in `pkg/nvkr/factory/execution_schedule_harness_test.go` is a deterministic
local harness. It loads the committed Plans for the maintained NVKE Definitions (`definition.yaml` with the
development and release Environments, plus the core, services, telemetry and disks families) and compiles
the hosted diagnostic selections (`fde-amd64`, `ovn`, and the Ubuntu 24 FDE AMD64 and image ARM64
Definitions). For each Plan it derives the same execution graph and entry-pinned OIDC signers as the
generator, then simulates every layout in an event loop:

- a worker partition starts when every unit owning one of its members' prerequisites completes, plus an
  estimated 0.5-minute reusable-workflow call start;
- an inline entry job starts when its prerequisites' owners complete;
- released jobs take free runners from their pool in release-time, then job-ID order;
- the job-level reference schedules every job individually with no partition barrier.

Reported metrics are makespan, its excess over the job-level reference (critical-path cost of the
barriers), and the summed time each job was eligible by data dependencies but not running.

### Duration source

Per-job durations are estimates, not recorded per-job timings. Recorded anchors come from reconciliation:
the native FDE disk job took 43m07s in Run 38443178035, one signing-adapter replay took 33 seconds and
remote verification had a median of 9.25 seconds per subject. Every other value is an estimate per Plan
node kind (fetch 1, acquire 2, build 6, terminal observation 1, SBOM 1.5, package 1, publish 1.5,
SBOM attestation, signing and publication verification 0.5 each, release assembly 3 minutes), plus
1.5 minutes of runner start per job and 3 minutes for runner bootstrap jobs. Each layout is scored at the
estimate and at eight deterministic perturbations that scale every job independently by 0.5–1.5. Layouts
are selected before any duration is known: the adopted layout uses declared job timeouts and the other
candidates use the estimate. Perturbed durations only score a layout and never select one.

### Capacity assumptions

Three capacity scenarios are assumptions, not measured limits: unbounded; 60 concurrent hosted jobs with
two runners in each native ARC pool; and 20 concurrent hosted jobs with one runner in each ARC pool.
Hosted Linux AMD64 and ARM64 runners share one account-wide pool. Runner bootstrap jobs for native ARC
targets run on the hosted runner the workflow renders, so they count against the hosted pool.

## Layouts compared

| Layout | Grouping |
|---|---|
| Depth (original) | Lexical dependency-depth layers in chunks of 64; OIDC signers then moved inline |
| Finish-ordered | Depth layers ordered by estimated finish before chunking; no extra partitions |
| Duration ≤30 / ≤50 | Finish-ordered, then split at the widest estimated finish gaps up to 30 or 50 partitions |
| Resource-aware | Depth layers split by runner pool |
| Resource + duration ≤30 | Pool split, finish order and gap splits up to 30 |
| Capacity ≤30 | Resource + duration, with chunks no larger than the pool's runner capacity |
| Timeout (adopted) | Duration ≤30 using each job's declared `timeout-minutes` instead of the estimate table |

Every layout is a refinement of the dependency-depth layers. Members of one layer are independent and every
edge increases depth, so each candidate remains an exact disjoint cover with an acyclic contracted graph.
The harness also checks each candidate against the generator's partition validator and provider limits
(at most 64 jobs per worker and 50 workers) and requires identical output from repeated runs.

## Results

These numbers were measured on 2026-10-29 against the committed Plans at that date and reproduce with:

```sh
go test ./pkg/nvkr/factory -run TestExecutionScheduleHarness -count=1 -v
```

Edits to maintained Definitions, Environments or the estimate table can change them, and can change the
adoption result, without a generator change. Remeasure before comparing a later run against this page.

For the development Plan, the only maintained Environment that enables partitioning (563 operations, 139 of
them entry-pinned signers), ranges cover the estimate and eight perturbations:

| Capacity | Layout | Workers | Makespan (min) | Over job-level | Change vs depth |
|---|---|---:|---:|---:|---:|
| Unbounded | Depth (original) | 21 | 90.5–123.8 | 21.5–33.9 | — |
| Unbounded | Timeout (adopted) | 30 | 74.0–100.0 | 5.0–11.6 | −16.9% to −22.1% |
| Hosted 60, ARC 2 | Depth (original) | 21 | 98.5–128.2 | 28.2–38.4 | — |
| Hosted 60, ARC 2 | Timeout (adopted) | 30 | 76.5–102.9 | 6.6–14.5 | −17.5% to −22.3% |
| Hosted 20, ARC 1 | Depth (original) | 21 | 172.8–217.4 | 54.3–72.7 | — |
| Hosted 20, ARC 1 | Timeout (adopted) | 30 | 127.3–171.4 | 8.8–26.3 | −20.2% to −26.3% |

Across every Plan, capacity and perturbation the other layouts changed makespan against depth as follows:
finish-ordered −2.6% to +1.5%; resource-aware −10.6% to 0%; duration ≤30 and ≤50 −32.1% to 0%;
resource + duration ≤30 −32.1% to 0%; capacity ≤30 −25.9% to 0%. The adopted layout ranged from
−26.3% to 0%. Family Plans gained up to 19.4% (core), 8.8% (services) and 8.1% (telemetry), and the
OVN diagnostic up to 9.8%. The disks family and the three disk diagnostics, whose layers have uniform
timeouts, kept the original grouping.

The cost of the original grouping comes from mixed layers: native disk producers and long image builds
share a depth with short jobs, so every descendant of a short job waits for the longest sibling. Reordering
without additional workers barely helps because the long and short jobs still land together. Splitting a
layer where finish times diverge removes most of that cost. Separating runner pools helps only where a pool
also separates durations. Capacity-sized chunks add workers without improving on gap splitting, and the
50-worker budget gains nothing over 30 while removing the provider headroom.

## Decision

The generator adopts timeout-gap splitting with a 30-worker target. Declared job timeouts are reviewed,
Plan-derived bounds the generator already emits, so scheduling does not depend on an unrecorded estimate
table. The adoption rule, enforced by the harness, is that the adopted layout improves every worst case for
a partitioned Plan by at least 5% and never loses more than 1% for any Plan, capacity or perturbation.

The change is local to provider scheduling. Membership, prerequisite results, recovery conditions,
entry-pinned OIDC signers, collectors and the single finalizer are unchanged; the generator still rejects a
layout that is not an exact disjoint, acyclic cover or that exceeds 64 jobs per worker or 50 workers.
Regeneration of the development bundle is byte-for-byte deterministic and leaves its Plan unchanged; it
moves from 21 to 30 workers, and the entry grows from 368,371 to 373,817 bytes against the 500,000-byte
limit.

## Limits

These results come from a local, simulated contract. They are not hosted wall-time measurements and not
qualification evidence. The recorded hosted development Run in reconciliation used the original depth
layout; the adopted layout has not been externally exercised. Confirming the gain requires a dispatched
development Run of the regenerated bundle and its recorded per-job timings, which then become recorded
anchors for this harness. Reusable-workflow call start, runner pickup and ARC pool sizes remain assumptions.

The model charges each worker only its members' operations plus the call start. It excludes each worker's
hosted result-reporter job, whose pickup, runtime and queueing a worker call also waits for. Both layouts pay
one reporter delay per worker on the critical path, but the adopted layout can chain more workers and adds
nine reporter jobs, over the original 21, to hosted load. The reported gain is therefore an upper bound
under this model. The harness does not show that the excluded cost removes the gain.
