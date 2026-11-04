# ADR-29 — Cluster observation and artifact attribution

- **Decision status:** Proposed; pending named-owner review and ratification.
- **Implementation maturity:** Design only; the standalone collection route is not implemented.
- **Acceptance-test maturity:** No live acceptance for this proposal.
- **Deciders:** Pending; NVKR observation and downstream deployment owners must be named.
- **Index:** Proposed ADR-29 supplements ADR-01–22 and proposed ADR-28; it remains outside the
  consolidated contract set until accepted.
- **Relates to:** ADR-13 (build evidence), ADR-16/17 (observation ownership), ADR-18 §3
  (operator-host execution), ADR-19 (deployment mapping), proposed ADR-27/28 (released tools).
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
| Workloads, requested images, raw runtime imageIDs, node and platform | Share native-manifest classification in conformance; tags alone cannot establish identity. |
| Installed chart/release metadata | Exact archive identity also needs a deployment-owner installation record. |
| Node OS, kernel, runtime and boot metadata | These do not prove which disk image booted or its current file contents. |
| Installed package/binary metadata and supported file measurements | Exact content comparison needs subject-bound file evidence, not package versions alone. |

The maintained disk route records inspection as `not-performed`. General published installed-file
baselines are not supplied by an ordinary SBOM. Strict disk inspection and package/file evidence remain
separate ADR-13 prerequisites for stronger comparisons; missing baselines do not prevent information gathering.

### 2. Reuse the existing owners

- **Proposed NVKR `observation`:** owns request admission, frozen scope, the AICR adapter, observation
  records, comparison outcomes/coverage and descriptive serialization. CLI and admitted SDK routes delegate to it. Its comparison
  profile is invocation settings, distinct from the public Verification Policy and not another lifecycle object.
- **NVKR `conformance`:** owns shared pure OCI identity classification, extracted from its current internal
  helpers. Observation reports each outcome and retains partial facts. Existing `validate` keeps its strict
  PodSpec reference, exact replicas, running/Ready state, IDs and restart checks in addition to identity checks.
  A matched runtime identity alone cannot satisfy that gate. Do not invoke `New`, `Produce` or a suite to collect facts.
- **AICR Go APIs:** use `pkg/inventory.Read` / `ReadReleases` for admitted installed-release reads,
  `pkg/client/v1.Client.LoadSnapshot` for a captured local file, and `Client.CollectSnapshot` only for
  separately admitted agent collection. Do not introduce an AICR CLI subprocess or duplicate collectors.
- **Deployment owners:** may supply pinned deployment declarations and installation/boot records as
  comparison inputs. These describe intended installation, not current state. They do not enter catalog
  locks or Build identity. NVKR consumes existing lifecycle receipts for any authenticated build baseline.

Go-library integration needs the contribution-policy exception and pinned dependency/license/security
review described in the [design](../cluster-observation-and-artifact-attribution.md#go-api-integration).
If it cannot be admitted, revisit this decision with owners; there is no silent CLI or copied-code fallback.

Standalone observation runs on the operator host under ADR-18 §3, using bounded in-process Kubernetes reads
through the admitted AICR/client-go client owner shared with the inventory adapter. It needs no product/tools
Release or disposable-cluster acknowledgement. Conformance retains its held-kubectl execution route unchanged.
Both require explicit target/TLS admission, bounded I/O and owned resource cleanup. In-process reads do not
inherit conformance's restricted subprocess environment; §4 defines their separate transport controls.
No generated-CI observation lane is admitted.
This is a proposed read-only extension of that host model, without a filesystem, network or privilege sandbox claim.

### 3. Make collection scope and gaps visible

The caller fixes namespaces, selectors, required targets/measurements and any expected counts before
collection. Record the scope and available resourceVersion for each list, observed object/node identities,
denied reads, missing data and exclusions. Do not shrink scope after a failed read.

Use one comparison vocabulary: **matched**, **mismatched**, **not observed**, **unverified**, and
**explicitly non-applicable**. The comparison profile declares non-applicability. An overall match requires every required
item to match with complete required coverage. Unsupported or ambiguous comparisons stay unverified.
Unknown joins remain unknown; unexpected software stays visible.

Keep index, native-manifest and config digests distinct. An authenticated selected index supplies the
expected child for the node platform. A matching runtime index ID supports index-level identity, not an
independent measurement of that child. A requested index reference alone proves no running identity;
an observed different native child is still mismatched. Unsupported or ambiguous runtime IDs stay unverified.

Observation coverage and artifact matching are different results. Scope, listed objects, absence of
extras and timestamps remain observation-source claims. A reporter-supplied digest or pass value carries
no authority. Record the observed kube-system namespace UID and caller-selected target when available; missing imported
identity/time stays unknown. Compare an expected UID only when independently supplied. Local collection times are not authenticated freshness or replay proof.

### 4. Admit access before collection

All live collection runs without signing/publication credentials and requires an explicit target and the
existing self-contained kubeconfig profile: one exact context, HTTPS with embedded CA, and one embedded
token or certificate/key pair. Reject ambient fallback, exec/auth-provider plugins, external files and insecure
TLS before client creation. Pass AICR a fresh owner-only captured path for each admission; its client cache is
path-keyed and retains credential-bearing client configuration until process exit. That cached API is
limited to one-shot CLI processes with one target admission; deleting its file does not erase the cache.
Long-lived SDK collection requires an upstream uncached/config-taking or bounded, evicting client owner
and remains closed until its lifecycle is admitted. Existing exec-based EKS/GKE configurations need a separately reviewed profile.

The in-process transport must disable ambient proxy use explicitly, reject proxy environment variables and
kubeconfig proxy URLs, reject redirects, bound response bytes, pagination, total requests and deadlines,
and capture warnings through a bounded request-owned handler without raw stderr output. These controls
are ADR-29 requirements, not protections inherited from ADR-18. Enforcing them requires upstream
configuration/client injection before reads; unsupported APIs fail admission. Do not mutate global environment,
transport or warning handlers. No signing/publication credentials are admitted to this route.
Default reads are bounded get/list operations. Watch, Secret reads, exec, attach, port-forward and proxy are not admitted by default.
Record the resources and verbs used. Current AICR inventory APIs can read Secrets across namespaces,
including Argo destination resolution; they need separately admitted access or an upstream bounded API.

Agent/host collection is a separate capability. Require an
explicit namespace and node selection, digest-pinned image, limits, tolerations and service-account/RBAC mode, with
privilege off unless separately admitted. AICR host measurements require node-root privileges, hostPID/network/IPC,
privileged/root execution, capabilities and host mounts. Restricted agents contribute Kubernetes measurements only;
container-local OS/SystemD readings cannot stand for node-host facts. API NodeInfo remains useful without host access.
Existing-account mode requires expected UID checks and credentials unable to create ServiceAccounts or RBAC, or an upstream
existing-only/expected-UID capability; a precheck alone cannot prevent AICR's missing-account fallback. Verify mode
and absence of unexpected run-owned RBAC afterwards. Cleanup must be enabled and independently confirmed for owned
resources; AICR currently logs cleanup errors without failing the call. Until those boundaries are implemented
and exercised, the route fails with `engine-capability` before work. Current `CollectSnapshot` also writes agent
logs directly to process stderr; enabling it requires request-scoped upstream log control or reviewed containment,
not process-global stderr redirection. Do not delete preexisting shared RBAC.

Admit `LoadSnapshot` only for a bounded, once-captured local file, not a URL or ConfigMap. Mark imported facts
as reporter-asserted. Keep observations private. Parse bounded raw bytes transiently and retain only a reviewed
field-allowlisted projection, its projection description and, where permitted, original byte digest/length.
Do not retain raw snapshots, Secret payloads, credentials, kubeconfigs, token-bearing values or provider logs.
AICR can include ClusterPolicy environment/proxy/repository values; exclude these and unknown fields before
logging, export or durable capture. The projection is not a byte-exact raw snapshot. Sharing requires a separate field projection.

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
- **Run conformance to gather cluster facts:** unnecessarily couples information gathering to tests and mutations.
- **Held pinned AICR executable (ADR-27/28):** useful for build producers, but adds a subprocess/tool-acquisition
  and serialization boundary to this typed Go integration. Not selected for observation; reconsider explicitly if dependency admission fails.
- **Adapt AICR source:** the current contribution-policy route. It avoids a direct Go import but duplicates
  maintenance. This proposal instead requests the explicit import exception; a refusal requires revisiting the decision.
- **Put deployment mapping in Definition:** violates ADR-19's downstream ownership boundary.

## Follow-ups

The [feature design](../cluster-observation-and-artifact-attribution.md) records source/API references,
collection modes, comparison prerequisites, examples and acceptance. Implementation must select and review
the actual dependency versions and expose collection through the proposed `observation` owner. No CLI, wire schema, signed
observation protocol or new qualification capability is admitted here.
