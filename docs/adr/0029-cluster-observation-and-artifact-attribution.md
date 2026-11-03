# ADR-29: Cluster observation and artifact attribution

- **Decision status:** Proposed; pending named-owner review and ratification.
- **Implementation maturity:** Design only; the standalone collection route is not implemented.
- **Acceptance-test maturity:** No live acceptance for this proposal.
- **Deciders:** Pending; NVKR observation and downstream deployment owners must be named.
- **Index:** Proposed ADR-29 supplements ADR-01-22 and proposed ADR-28; it remains outside the
  consolidated contract set until accepted.
- **Relates to:** ADR-13 (build evidence), ADR-16/17 (existing observation ownership), ADR-19
  (deployment mapping), proposed ADR-28 (executable delivery).
- **Scope:** Gather cluster information and optionally correlate it with selected NVKR artifacts.
  Outputs are unsigned, descriptive diagnostics, lane none. Test execution, test verdicts, signing,
  publication, qualification and release gates are outside this decision.

## Context

A user needs to know what is deployed in a cluster: workloads, images, Helm releases, nodes, operating
systems and packages. When a build is selected, the user also wants to see which deployed items can be
connected to its artifacts and SBOMs, and where that connection cannot be established.

Collecting this information must be useful on its own. It must not require running a test suite or
supplying a build first.

## Decision

**Provide a scoped cluster observation, reuse AICR through Go APIs, and reuse NVKR's existing image
observation rules. Offer artifact comparison as a separate use of those facts.**

### 1. Keep facts and comparison separate

A **cluster observation** records discovered facts, their sources, collection interval and gaps.
An **artifact comparison** relates those facts to exact selected outputs and their evidence. It does
not change a Build identity, publisher SBOM or Release, or add another authored lifecycle object.

Report produced or acquired identity, installation attribution and measured runtime content as separate
claims. A matching version is useful information; it is not proof of matching artifact bytes.

| Information to gather | Limit on artifact comparison |
| --- | --- |
| Workloads, requested images, raw runtime imageIDs, node and platform | Reuse the existing native-manifest join; tags alone cannot establish identity. |
| Installed chart/release metadata | Exact archive identity also needs a deployment-owner installation record. |
| Node OS, kernel, runtime and boot metadata | These do not prove which disk image booted or its current file contents. |
| Installed package/binary metadata and supported file measurements | Exact content comparison needs subject-bound file evidence, not package versions alone. |

The maintained disk route records inspection as `not-performed`. General published installed-file
baselines are not supplied by an ordinary SBOM. Strict disk inspection and package/file evidence remain
separate ADR-13 prerequisites for stronger comparisons; missing baselines do not prevent information gathering.

### 2. Reuse the existing owners

- **NVKR `conformance`:** owns the existing Pod/node observation and native image-identity rules. Reuse
  those rules through a proposed observation-only entrypoint or refactor. The current functions are
  internal and coupled to disposable-cluster conformance execution; do not invoke a test run to collect facts.
- **AICR Go APIs:** use `pkg/inventory.Read` / `ReadReleases` for admitted installed-release reads,
  `pkg/client/v1.Client.LoadSnapshot` for a captured local file, and `Client.CollectSnapshot` only for
  separately admitted agent collection. Do not introduce an AICR CLI subprocess or duplicate collectors.
- **Deployment owners:** may supply pinned deployment declarations and installation/boot records as
  comparison inputs. These describe intended installation, not current state. They do not enter catalog
  locks or Build identity. NVKR consumes existing lifecycle receipts for any authenticated build baseline.

Go-library integration needs the contribution-policy exception and pinned dependency/license/security
review described in the [design](../cluster-observation-and-artifact-attribution.md#go-api-integration).
If it cannot be admitted, revisit this decision with owners; there is no silent CLI or copied-code fallback.

### 3. Make collection scope and gaps visible

The caller fixes namespaces, selectors, required targets/measurements and any expected counts before
collection. Record the scope and available resourceVersion for each list, observed object/node identities,
denied reads, missing data and exclusions. Do not shrink scope after a failed read.

Use one comparison vocabulary: **matched**, **mismatched**, **not observed**, **unverified**, and
**explicitly non-applicable**. Policy declares non-applicability. An overall match requires every required
item to match with complete required coverage. Unsupported or ambiguous comparisons stay unverified.
Unknown joins remain unknown; unexpected software stays visible.

Inventory completeness and artifact matching are different results. Scope, listed objects, absence of
extras and timestamps remain observation-source claims. A reporter-supplied digest or pass value carries
no authority. Record the observed kube-system namespace UID and caller-selected target when available; missing imported
identity/time stays unknown. Compare an expected UID only when independently supplied. Local collection times are not authenticated freshness or replay proof.

### 4. Admit access before collection

Live collection requires an explicit target and kubeconfig; reject ambient credential fallback. Default reads are bounded
get/list operations. Watch, Secret reads, exec, attach, port-forward and proxy are not admitted by default.
Record the resources and verbs used. Current AICR inventory APIs can read Secrets across namespaces,
including Argo destination resolution; they need separately admitted access or an upstream bounded API.

Agent/host collection is a separate capability, isolated from signing/publication credentials. Require an
explicit namespace and node selection, digest-pinned image, limits, tolerations and service-account/RBAC mode, with
privilege off unless separately admitted. Cleanup must be enabled and independently confirmed for owned
resources; AICR currently logs cleanup errors without failing the call. Until those boundaries are implemented
and exercised, the route fails with `engine-capability` before work. Do not delete preexisting shared RBAC.

Admit `LoadSnapshot` only for a bounded, once-captured local file, not a URL or ConfigMap. Mark imported facts
as reporter-asserted. Keep admitted observations private; do not retain raw Secret payloads, credentials,
kubeconfigs, token-bearing values or provider logs. Sharing needs an explicit field projection.

## Consequences

Users can obtain useful cluster information even when image lineage, package measurements or a build
baseline are unavailable. Missing access and unsupported collectors remain visible instead of becoming a
complete match. AICR does not currently supply every measurement required for all four artifact classes.

[ADR-28](0028-multi-platform-executable-indexes.md) delivery needs explicit consumption handling: image
volumes need an OCI native-child/mount observer, copied or repackaged contents need downstream lineage, and
extracted executables use file comparison. Container imageID checks cannot cover these modes automatically.

## Alternatives considered

- **Build another collector framework:** duplicates AICR and NVKR observation owners.
- **Compare version labels only:** useful inventory, insufficient artifact identity.
- **Run conformance to obtain a snapshot:** unnecessarily couples information gathering to tests and mutations.
- **Put deployment mapping in Definition:** violates ADR-19's downstream ownership boundary.

## Follow-ups

The [feature design](../cluster-observation-and-artifact-attribution.md) records source/API references,
collection modes, comparison prerequisites, examples and acceptance. Implementation must select and review
the actual dependency versions and expose the observation-only entrypoint. No CLI, wire schema, signed
observation protocol or new qualification capability is admitted here.
