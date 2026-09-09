# ADR-12 — Criteria model & AICR activation

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-12 in the consolidated NKD ADR set.
- **Relates to:** ADR-11 (composition), ADR-05 (ReleaseSet / Inventory), ADR-14 (delivery), ADR-10 (builder
  types), ADR-19 (ClusterDefinition).
- **Satisfies:** FR-01 (asymmetric match), FR-16 (accelerated-runtime activation), the criteria taxonomy.

## Context

Criteria select a recipe. NKD adopts **category-scoped** recipes — one recipe produces one *layer* of the stack —
rather than node-scoped recipes. Node role and cluster type are **composition** concerns, not recipe-selection
axes. This ADR fixes the axis set, the category taxonomy and its node-level vs cluster-level split, how `role` and
`clusterType` relate to it, and how the accelerated-runtime axes activate without perturbing core recipes.

## Decision

### 1. Recipe-selection axes

`category × k8sVersion × os × arch`, resolved by **asymmetric match**: an empty or `any` recipe field is a
wildcard; a specific recipe field requires the query to supply exactly that value; a generic query does not match
a specific recipe; ties break by specificity, and a remaining tie **fails closed**. `arch` is a real multi-arch
axis — builds are **native per arch (no cross-compilation)**, so one recipe yields a distinct, reproducible
identity per arch. `k8sVersion` and `os` are `any` where a category does not depend on them.

### 2. Category taxonomy — two families

`category ∈ { os, kubernetes, core-runtime, accelerated-runtime, operations, platform-services }`, in two families
with different delivery models.

**Node-level** — installed on the host (deb/nix/disk) at provision time:
- `os` — base OS image.
- `kubernetes` — the node k8s substrate: kubelet, kubeadm, kubectl, apiserver, controller-manager, scheduler,
  proxy, etcd, the node container runtime (containerd, runc), CNI *plugins*, crictl, pause.

**Cluster-level** — deployed *into* the running cluster as helm charts plus container images, post-provision:
- `core-runtime` — CNI *provider*, CSI, core add-ons.
- `accelerated-runtime` — GPU/accelerator workloads (for example gpu-operator, network-operator).
- `operations` — observability and telemetry (for example prometheus).
- `platform-services` — the operator/controller management stack.

`operations` and `platform-services` are **distinct** categories, and the node container runtime lives in
`kubernetes`. Both are catalog-content choices and are easily adjusted. Cluster-level categories carry
`outputType` = helm-chart (packaged as OCI) + oci-image; node-level categories carry deb / nix / disk.

### 3. Role and cluster type are composition, not recipe axes

- **`role` (`control-plane` | `worker` | `gpu-worker`)** filters *node-level* category artifacts into a Node SKU.
  Each component carries a `roles` **affinity metadata** field (apiserver → `[control-plane]`, kubelet →
  `[worker]`, kubectl → `[control-plane, worker]`), and composition/delivery filters by role. The `kubernetes`
  recipe still produces every binary under one identity.
- **`clusterType` (`tenant` | `operator`)** selects which Node SKUs and which *cluster-level* categories compose
  into a cluster: an operator cluster adds `platform-services`, a tenant GPU cluster adds `accelerated-runtime`.
- Neither triggers a build. Both select and filter already-verified category members by digest, and they live on
  the ReleaseSet / Inventory (ADR-05) and the ClusterDefinition (ADR-19).

### 4. AICR-axis activation and the Tier-1 identity boundary

The accelerator/service/intent/platform axes **activate only when the Resolved Recipe declares a digest-pinned
AICR profile** (an `accelerated-runtime` recipe); otherwise they **fail closed**. The profile pins both the
canonical AICR recipe digest and the lowercase SHA-256 digest of the exact AICR generator executable bytes. These
are **not** fields of the criteria struct; they are matched against the AICR profile after the four core axes. The
exact values passed to the AICR recipe generator (`service`, `accelerator`, `os`, `intent`, and optional
`platform`), the pinned canonical AICR recipe digest, **and** the generator-executable digest are **Tier-1
definition inputs**: a typed `AICRActivation` carries them on the resolved IR, so they bind `recipeDigest`. A
human profile identity and its repository/mirror location remain provenance/transport only and do not affect
identity. A core recipe with no AICR profile is byte-identical before and after this feature, because the typed
activation is absent.

Both flat and layered catalogs may declare the profile. In a layered catalog, exact AICR selection chooses the
matching `accelerated-runtime` profile leaf after the ordinary category/frame match; an incomplete tuple or two
matching profiles fails closed. An `accelerated-runtime` recipe may be **activation-only** (zero NKD component
producers) because the pinned AICR recipe is itself the declarative component closure; this avoids fabricating an
NKD artifact component merely to make the profile enumerable. Ordinary empty recipes remain invalid.

The CLI adapter resolves the executable once, captures those exact bytes as an anonymous sealed executable
capability, verifies the capability against the Tier-1 generator pin, and reuses that same capability for recipe
generation, both evidence-digest checks, and bundle rendering. The generated recipe is likewise held anonymously,
and every later generator phase receives that exact descriptor, never a mutable pathname. This authority-bearing
subprocess protocol is supported on Linux only and fails closed on platforms without descriptor execution.

A qualified render writes first into an untrusted private generator stage. After every generator phase finishes,
NKD descriptor-walks that stage, rejects symlinks and non-regular entries, captures every file as an immutable
held capability, and requires the captured `recipe.yaml` to equal the already-verified recipe capability. It then
materializes a distinct commit tree **only from those held capabilities**, recomputes its tree identity, publishes
it with an atomic no-replace primitive, and rechecks the visible destination. Generator-stage path replacement
therefore cannot choose the published bytes.

The resulting tree digest is a derived Tier-2 release input, and it must be bound into the signed AICR reference
before that reference can claim a rendered bundle. The signed ValidationVector schema can carry both
`generatorDigest` and `bundleDigest`. The current CLI deliberately rejects `attest --aicr-bundle`: no supported
cross-process publisher adapter can join the same-process render receipt to signed vector bytes. The local output
directory is diagnostic state and is omitted from the signed reference, so standalone rendering is locally
verified diagnostic output, not a release-authorizing bundle. A later content-addressed publication reference may
be added only when it is stable across hosts.

The authority-bearing render result is an opaque, same-process `QualifiedResult`, minted only by the built-in
verified-snapshot adapter. The public `Result` and the externally implementable `UnqualifiedBundler` seam are
diagnostic: even a caller-composed result whose paths and digests are internally consistent cannot authorize an
AICR reference. A future SDK adapter needs its own authenticated execution-evidence design before it can mint the
opaque capability; structural fields are not proof that the pinned generator ran.

The frozen v2 tree object has `schemaVersion: nkd.dev/aicr-bundle/v2` and an `entries` array sorted by relative
slash path. Each entry is exactly `{path, type, mode}` for a directory or `{path, type, sha256, mode}` for a file;
`sha256` is the lowercase digest of exact file bytes, and `mode` is `0644` or `0755` according only to whether any
executable bit is present. Empty directories are identity-bearing because a consumer can branch on their presence;
other non-executable read/write-bit differences do not affect identity. The object is encoded with ADR-01's
canonical codec and hashed under `nkd.aicr-bundle`. A qualified result requires exact held `recipe.yaml` and
regular `checksums.txt` members in addition to rejecting every non-directory/non-regular entry.

Output-affecting activation inputs belong in identity so definitions producing different runtime behavior cannot claim the same Recipe Digest.

### 5. Reserved axes fail closed

A non-empty reserved axis fails closed: `tier` and `compliance` always (no v1 activation);
`accelerator`/`service`/`intent`/`platform` unless a digest-pinned AICR profile activates them (§4). Never
silently wildcarded.

## Consequences

**Positive**
- One recipe = one stack layer, a clean reproducibility boundary: the `kubernetes` layer can be a nix flake while
  `os` is a disk image (ADR-10), independently.
- Categories are built once and composed differently per node role and cluster type — no rebuild to re-compose.
- The node-level / cluster-level split maps cleanly onto the two delivery phases (provision, then helm-deploy).
- AICR labels and transport stay out of recipe identity, output-affecting activation inputs are bound, and core
  recipes remain provably unaffected.
- PATH lookup is not a trust decision: every generator phase executes one content-verified held capability, and
  the published bundle is materialized only from a separately held, independently recomputable tree.

**Negative / costs**
- Role subsetting moves to composition/delivery (ADR-14/19), and the `roles` affinity metadata must be
  maintained.
- The category taxonomy is catalog-content that must be agreed and versioned.
- Pinning only the main executable does not identify its dynamic-loader/interpreter/library closure. If AICR's
  output is shown to depend on that closure, the profile must evolve to pin a hermetic generator image or closure;
  the executable pin must not be described as stronger than it is.
- The qualified executable protocol is Linux-only until another platform offers an equivalent held-execution
  capability or an authenticated out-of-process producer receipt.

**Neutral**
- Artifact type stays a per-component `outputType`, orthogonal to `category`.

## Alternatives considered

- **Node-scoped recipes (`role` as a recipe axis).** Rejected — cannot express the stack categories or the two
  cluster types cleanly, and couples a recipe to a node role.
- **Hybrid (`role` optional on recipes).** Rejected — `role` in two places blurs recipe selection vs composition.
- **Name the axis `layer`.** Rejected — collides with OCI/filesystem layers and with composition "layers."
- **`tier` for category.** Rejected — `tier` is a reserved axis.

## Follow-ups

- **ADR-11:** overlay selection matches on these axes.
- **ADR-05:** ReleaseSet members and the Inventory group by category (plus `role` for node-level); `clusterType`
  scopes the required set.
- **ADR-14:** Node SKU delivery filters node-level category artifacts by `role`.
- **ADR-19:** the ClusterDefinition composes categories per `role` / `clusterType`.
- **ADR-22 §7:** an upstream **curated pin set** is a *composition*, not a recipe-selection axis — the SKU takes
  the umbrella's shape (§3), and its members are composition members bound by digest.

## Acceptance evidence

Module tests prove that a changed or malformed generator pin fails before execution; replacing the original
binary path after the first phase cannot alter later phases; cancellation before the atomic rename leaves no
published bundle, while cancellation after that linearization point returns the durable committed result; stale
destinations are not overwritten; the complete commit tree and its parent directory are synced; replacing the
generator stage cannot alter the held commit tree or the exact directory inode published by no-replace rename;
symlinked output fails validation; and creation order and irrelevant permission bits do not perturb the tree
digest, while file content, directory presence, or executable-mode mutation does. Recipe
tests prove the generator pin changes `recipeDigest`, while profile labels and repository transport do not. Direct
IR tests prove a caller-composed AICR activation cannot gain a recipe identity or executable canonical graph
without both valid pins and supported generator inputs. Forgery tests additionally construct an arbitrary bundle
tree and a completely self-consistent public `Result`: the unqualified diagnostic check passes, while the same
result cannot acquire the private execution receipt required by the context-first `QualifiedResult.ValidateFor`.

The ValidationVector / AICR-reference schema can bind the measured generator digest and an `nkd.aicr-bundle`
digest, but the supported publisher CLI currently binds only the pinned profile/generator identity and rejects
render attachment. End-to-end acceptance requires both a publisher adapter that joins the opaque render receipt
to signed vector bytes and a delivery/activation consumer that recomputes the tree digest before using any bundle
member; schema validation alone is not byte verification at either boundary.
