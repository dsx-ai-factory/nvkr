# nkdctl — supported flows

> [!CAUTION]
> **Historical POC flow snapshot.** These diagrams contain removed flags and workflows that now fail closed.
> They are retained for design traceability, not as a current support or release claim. Use the README support
> matrix, `nkdctl --help`, and `docs/adr/reconciliation.md` instead.

This document mapped the flows through the POC `nkdctl` code as
diagrams. It reflected what the binary did at that time, including the
honest edges: `bundle --engine local` is a dry-run, `disk` shells the repo's qcow2 scripts,
and cosign signing shells out to the `cosign` binary. Where a path is plumbing-only it is
paths then marked as plumbing-only.

Every command shares one front half — **load catalog → resolve criteria → compile IR →
`recipeDigest`** — then diverges. That shared spine is [§2](#2-the-shared-spine-criteria--ir).
Diagrams are grouped: resolution, emission, build, trust, packaging/boot, and catalog maintenance.

- [1. Command surface](#1-command-surface)
- [2. The shared spine](#2-the-shared-spine-criteria--ir)
- [3. Catalog load & fail-closed validation](#3-catalog-load--fail-closed-validation)
- [4. Asymmetric criteria match + arch defaulting](#4-asymmetric-criteria-match--arch-defaulting)
- [5. `recipe` — resolve & print](#5-recipe--resolve--print)
- [6. `bundle` — one IR, many engines](#6-bundle--one-ir-many-engines)
- [7. `build` — the hermetic local DAG](#7-build--the-hermetic-local-dag)
- [8. The build graph shape](#8-the-build-graph-shape-concrete)
- [9. Multi-arch (model B)](#9-multi-arch-model-b)
- [10. `bake-off` — two-environment byte-identity](#10-bake-off--two-environment-byte-identity)
- [11. Trust model: `attest` → `verify`](#11-trust-model-attest-publisher--verify-operator)
- [12. `verify` fail-closed decision gate](#12-verify-fail-closed-decision-gate)
- [13. `sbom`](#13-sbom)
- [14. `package`](#14-package)
- [15. `image` → `disk` → boot](#15-image--disk--boot)
- [16. `catalog` — pin / bump / update-ci](#16-catalog--pin--bump--update-ci)
- [17. Fail-closed exit codes](#17-fail-closed-exit-codes)

---

## 1. Command surface

Every verb sits over the SDK; the query flags (`--role --k8s --os --arch [--data]`) resolve a recipe
for the build-oriented verbs. Reserved axes (`--accelerator --service --intent --platform --tier
--compliance`) are hidden and fail closed if set.

```mermaid
flowchart TB
  cli["nkdctl (urfave/cli v2)"]

  subgraph resolve["Resolve &amp; inspect"]
    recipe["recipe / resolve<br/>→ IR | digest | components<br/>(one node role; --cluster-type composes operator/tenant)"]
    bundle["bundle --engine<br/>local | github | gitlab | tekton | argo"]
    sbom["sbom → CycloneDX"]
  end

  subgraph buildg["Build (needs Docker + git)"]
    build["build<br/>hermetic DAG, -j, --package"]
    fetch["fetch<br/>no-compile: acquire + verify pinned artifacts"]
    bakeoff["bake-off<br/>build ×2 varied env"]
    pkg["package<br/>binary → oci-image / .deb"]
  end

  subgraph trust["Trust (zero-binary)"]
    attest["attest<br/>build → expected-digests (+sign)"]
    verify["verify<br/>rebuild → prove identity"]
  end

  subgraph osimg["Base OS / node"]
    image["image<br/>mmdebstrap rootfs"]
    disk["disk --format qcow2 [--boot]"]
  end

  subgraph cat["Catalog maintenance (git + network)"]
    cvalidate["catalog validate"]
    clist["catalog list"]
    cpin["catalog pin"]
    cbump["catalog bump"]
    cpina["catalog pin-artifacts<br/>(prebuilt digests → lock)"]
    cci["catalog update-ci"]
  end

  cli --> resolve & buildg & trust & osimg & cat
```

---

## 2. The shared spine (criteria → IR)

`resolveGraph()` is the front half of `recipe`, `bundle`, `build`, `bake-off`, `attest`, `verify`,
and `sbom`. It is pure and offline — no Docker, no clock, no host paths enter — so `recipeDigest` is a
deterministic function of (catalog, query).

```mermaid
flowchart LR
  q["query<br/>role × k8sVersion × os × arch<br/>+ reserved axes"]
  prov{"--data set?"}
  emb["Embedded() catalog<br/>(//go:embed)"]
  dir["Dir(path) catalog<br/>(full replacement)"]
  load["catalog.Load<br/>registry + lock → validate<br/>(fail closed)"]
  res["recipe.Resolve<br/>asymmetric match → one recipe"]
  comp["recipe.Compile<br/>recipe → build DAG (IR)"]
  norm["Graph.Normalize<br/>sort nodes / env / needs"]
  dig["Graph.Digest<br/>sha256(deterministic YAML)"]

  q --> res
  prov -->|no| emb --> load
  prov -->|yes| dir --> load
  load --> res --> comp --> norm --> dig
  dig --> out["recipeDigest<br/>sha256:…"]
```

**Invariant:** `recipeDigest = sha256(MarshalYAMLDeterministic(IR))`. Same catalog + same query
(+ same arch) ⇒ byte-identical IR ⇒ identical digest. A different arch ⇒ a distinct digest
([§9](#9-multi-arch-model-b)).

---

## 3. Catalog load & fail-closed validation

A catalog is split like `go.mod`/`go.sum`: a hand-authored **registry** (`registry.yaml`: sources +
recipes + components) and a machine-managed **lock** (`pins.lock.yaml`). `Load` merges them into
concrete components and validates. Any unpinned ref or tag-pinned toolchain is a coded error — never a
silent default.

```mermaid
flowchart TB
  reg["registry.yaml<br/>sources · recipes · components<br/>(strict-decode: unknown/typo axis fails)"]
  lock["pins.lock.yaml<br/>tag · commit · treeHash · sourceDateEpoch<br/>goVersion? · toolchainImage?"]
  merge["resolveComponent<br/>source + pin → Component<br/>render {{.Tag/.Commit/.Major}} stamps"]
  val{"Validate (sorted, deterministic)"}

  reg --> merge
  lock --> merge --> val

  val -->|"commit not 40-hex / missing treeHash / no pin"| e14["CodeUnpinnedSource (14)"]
  val -->|"toolchain by tag, not @sha256"| e15["CodeTagToolchain (15)"]
  val -->|"bad outputType / empty cmd / unknown {{token}} / reserved-in-recipe / composition hook"| e16["CodeInvalidCatalog (16)"]
  val -->|"reserved axis non-empty"| e12["CodeReservedAxis (12)"]
  val -->|ok| out["*catalog.Catalog<br/>sources · recipes · components"]
```

Toolchain pinning accepts two digest forms and rejects tags: `name@sha256:…` (registry) or a bare
`sha256:…` (local image id). Per-arch toolchains (`toolchainImagesByArch`) validate **every** listed
arch. The `{{.Arch}}` token is the only template allowed in a build command / artifact path; any other
`{{…}}` fails closed here.

---

## 4. Asymmetric criteria match + arch defaulting

`recipe.Resolve` picks exactly one recipe. Matching is **asymmetric**: a recipe field of `any`/empty is
a wildcard; a *specific* recipe field requires the query to supply exactly that value; a generic query
never matches a specific recipe. Ties break by specificity; a true tie fails closed.

```mermaid
flowchart TB
  start["query {role, k8s, os, arch} + reserved"]
  rax{"any reserved axis non-empty?"}
  arch["default arch: '' → amd64<br/>validate ∈ {amd64, arm64}"]
  scan["score each recipe by asymmetric match"]
  count{"# matching recipes"}

  start --> rax
  rax -->|yes| e12["CodeReservedAxis (12)"]
  rax -->|no| arch
  arch -->|"outside {amd64, arm64}"| e13["CodeUnsupported (13)"]
  arch --> scan --> count
  count -->|0| e10["CodeNoMatch (10)"]
  count -->|"&gt;1 tie (equal specificity)"| e11["CodeAmbiguous (11)"]
  count -->|"1 (or unique most-specific)"| ok["Resolved recipe<br/>+ components (pins merged)"]
```

Active axes: `role × k8sVersion × os × arch`. `versionFrom` fills `k8sVersion` from a source pin, so a
version bump doesn't touch the recipe.

---

## 4b. Layered recipe composition (overlays / mixins) — the AICR-ported engine

A catalog may ship `overlays/` + `mixins/` instead of flat `recipes:`. Resolution then
*composes* (a faithful port of AICR's `pkg/recipe`): the recipe for a query is built by
merging a base + the matching overlay inheritance chains + mixins, not by picking one
recipe. Each layer ADDS a component, `enabled: false` DISABLES one, or `overrides:`
deep-merges onto it (child wins). The flattened set feeds the unchanged IR → recipeDigest.

```mermaid
flowchart TB
  q["resolved criteria<br/>(role/os/arch/clusterType…)"] --> fm["findMatchingOverlays<br/>criteria.Matches + filterToMaximalLeaves<br/>sort by specificity, then name"]
  fm --> seed["seed = base.yaml spec<br/>appliedLayers=[base]"]
  seed --> walk["for each maximal leaf:<br/>resolveInheritanceChain (root→leaf,<br/>cycle/unknown-base → CodeInvalidCatalog)<br/>merge child-wins (shared ancestor once)"]
  walk --> mx["apply spec.mixins<br/>(unknown / constraint-collision → fail closed)"]
  mx --> eff["effective set = enabled refs, name-sorted<br/>+ appliedLayers provenance"]
  eff --> reg["map each ref → registry component<br/>(+ deep-merge overrides, re-validate)"]
  reg --> ir["recipe.Compile → IR → recipeDigest"]
```

## 4c. clusterType (operator / tenant) composition

`clusterType` (`operator | tenant`) is another composition axis: overlays keyed on it diverge
the substrate between a management (operator) cluster and a workload (tenant) cluster. You
resolve either with the ordinary resolver — `nkdctl recipe --cluster-type operator|tenant --role
control-plane|worker` — one node role at a time (the atomic recipe you build/verify). Operator
vs tenant genuinely diverge: an `operator-control-plane` overlay inherits `control-plane`, adds
the CAPI controller (prebuilt lane), and overrides the substrate — a distinct recipeDigest.

---

## 5. `recipe` — resolve & print

```mermaid
flowchart LR
  r["recipe / resolve"] --> spine["§2 spine → IR + digest"]
  spine --> o{"--output"}
  o -->|ir| ir["deterministic IR YAML<br/># recipeDigest: …"]
  o -->|digest| d["recipeDigest only"]
  o -->|components| c["component × outputType × toolchain table"]
```

---

## 6. `bundle` — one IR, many engines

The one IR is *lowered* to each engine. Adapters map topology + transport only — **the build step is
the exact `docker run --network=none --platform linux/<arch> …` the local runner executes** — so every
engine builds byte-identically. `--engine local` dry-runs the local runner instead of emitting YAML.

```mermaid
flowchart TB
  ir["IR (§2)"] --> eng{"--engine"}
  eng -->|local| lr["Local.Run(DryRun)<br/>prints the concrete docker plan"]
  eng -->|github| gh["GitHub Actions YAML<br/>if: always() verify"]
  eng -->|gitlab| gl["GitLab CI YAML<br/>when: always verify"]
  eng -->|tekton| tk["Tekton YAML<br/>finally: verify"]
  eng -->|argo| ar["Argo Workflows YAML<br/>finished-state depends verify"]

  gh & gl & tk & ar --> lower["each job = fetch (+vendor, network)<br/>then docker run --network=none --platform<br/>then UNCONDITIONAL verify"]
```

Adding an engine = one adapter; adding a component = one catalog entry, no code.

---

## 7. `build` — the hermetic local DAG

`pipeline.Local.Run` executes the IR as a concurrent DAG (`-j` jobs). The shared fetch is the only
barrier; independent component builds run in parallel and a single terminal `verify` waits on them all.
Builds are hermetic (`--network=none`) and parallel-safe (read-only shared source + a private writable
`WriteDir` per build). Fails closed on the first failure unless `--keep-going`.

```mermaid
flowchart TB
  ir["IR nodes"] --> topo["topo-sort (cycle → CodeInvalidCatalog)"]
  topo --> sel["selectNodes (--only ⇒ builds + their fetch + verify)"]

  subgraph fetch["FETCH (network=default)"]
    f1["git init → fetch pinned commit → checkout"]
    f2["assert HEAD == commit AND tree == treeHash<br/>(mismatch → CodeFetchFailed 20)"]
    f3["optional vendor step (network)<br/>for non-vendored repos"]
    f1 --> f2 --> f3
  end

  subgraph buildn["BUILD ×N (network=none, up to -j parallel)"]
    b1["docker run --rm --platform linux/&lt;arch&gt;<br/>--network=none -e &lt;sorted env allowlist&gt;<br/>-v src:/src:ro  -v privOut:/src/&lt;writeDir&gt;"]
    b2["run pinned toolchain @sha256 + command"]
    b3["sha256 the artifact<br/>(+ optional --package → oci/.deb digest)"]
    b1 --> b2 --> b3
  end

  verify["VERIFY (terminal, unconditional)<br/>re-hash every included build's artifact THIS run<br/>(missing/changed → CodeVerifyFailed 22)"]

  sel --> fetch --> buildn --> verify --> rep["Report<br/>recipeDigest + per-node status + artifact digests"]
```

Host tuning (GOMAXPROCS, job count) is deliberately **not** in the IR — it can't change output bytes, so
reproducibility holds regardless of `-j`.

---

## 7b. `fetch` — the no-compile (prebuilt) lane

A deliberate, opt-in **parallel** to `build` (NKD-FR-BLD-050/051). A catalog may declare a component
`prebuilt` (a pinned https binary or an OCI image); `nkdctl fetch` compiles it to a **pull** node and acquires
it — no fetch-source, no toolchain, no compile — verifying the bytes against a signable, per-arch,
content-addressed digest. Fail-closed on mismatch. The pinned digest is the reproducibility anchor and is part
of `recipeDigest`; an all-prebuilt manifest carries **no** builder-profile digest (acquisition is
host-independent). A component is from-source **XOR** prebuilt **XOR** an exclusion.

```mermaid
flowchart TB
  res["resolve → prebuilt components<br/>(no digest for target arch → CodeUnpinnedSource 14)"] --> comp["Compile → one PULL node each<br/>Artifact (arch-rendered) + pinned Digest"]

  subgraph pulln["PULL ×N (network=default, up to -j parallel)"]
    direction TB
    bin["binary URL:<br/>HTTP GET → sha256<br/>== pin? (else CodeVerifyFailed 22)<br/>optional --package → reproducible .deb"]
    img["OCI image:<br/>docker pull name@sha256<br/>(pull-by-digest IS the content-addressed check)"]
  end

  verify["VERIFY (terminal, unconditional)<br/>binary/.deb → re-hash file<br/>image → docker inspect present-by-digest (offline)"]

  comp --> pulln --> verify --> rep["Report → attest → verify (Reproduced, profile: none)"]
```

The four CI emitters lower a pull node identically: `curl -fsSL <url> && echo "<digest>  <f>" | sha256sum -c -`
for a binary, `docker pull <name>@<digest>` for an image; the unconditional `verify` job waits on every pull
job (one-IR-many-adapters, preserved).

---

## 8. The build graph shape (concrete)

A worked example: the **worker** recipe (8 components). Components sharing a `(repo, commit)` collapse
into one fetch; each build depends on its fetch; one verify depends on every build. `kubernetes` is one
fetch feeding four builds.

```mermaid
flowchart LR
  fk["fetch kubernetes"] --> bproxy["build kube-proxy"]
  fk --> bkubelet["build kubelet (CGO)"]
  fk --> bkubeadm["build kubeadm"]
  fk --> bkubectl["build kubectl"]
  frunc["fetch runc"] --> brunc["build runc (static CGO + libseccomp)"]
  fcd["fetch containerd"] --> bcd["build containerd"]
  fcni["fetch plugins"] --> bcni["build cni-plugins"]
  fcri["fetch cri-tools"] --> bcri["build crictl"]

  bproxy & bkubelet & bkubeadm & bkubectl & brunc & bcd & bcni & bcri --> v["verify (unconditional)"]
```

The **control-plane** recipe is the same shape at 8 fetches → 15 builds → 1 verify (adds etcd, coredns,
helm, and the kubernetes `pause` [C] + apiserver/controller-manager/scheduler).

---

## 9. Multi-arch (model B)

The **same** arch-agnostic recipe resolves for any arch and yields a **distinct, reproducible**
`recipeDigest` per arch. Each build is **native to its arch — no cross-compilation**: the IR pins
`platform: linux/<arch>`, the runner/emitters pass `--platform`, and the toolchain resolves per arch.

```mermaid
flowchart TB
  rec["recipe (arch: any)"] --> pa["criteria.Arch (amd64 default | arm64)"]
  pa --> comp["Compile: set platform=linux/&lt;arch&gt;,<br/>GOARCH, GOAMD64/GOARM64,<br/>substitute {{.Arch}} literals"]
  comp --> tc{"toolchain source"}
  tc -->|"6 pure-Go sources<br/>golang@sha256 (manifest list)"| mlist["--platform selects the NATIVE variant<br/>(zero catalog change)"]
  tc -->|"kubernetes / runc<br/>toolchainImagesByArch"| perarch["pin a NATIVE per-arch image<br/>(gcc without cross prefix)"]

  mlist & perarch --> run["docker run --platform linux/&lt;arch&gt;<br/>= native (or emulated on a foreign host)"]
  run --> proof["distinct recipeDigest per arch;<br/>bit-identical per arch under bake-off"]
```

On an arm64 host, arm64 builds run natively and amd64 builds run under qemu binfmt as a stand-in for a
native amd64 CI runner. The arm64 IR contains **zero** `amd64`/`x86_64` literals.

---

## 10. `bake-off` — two-environment byte-identity

Builds the recipe **twice** under deliberately different host state, then asserts the resulting artifact
digests match. The pinned toolchain container fixes `/src`, `SOURCE_DATE_EPOCH`, `TZ=UTC` — so nothing
from the host leaks in.

```mermaid
sequenceDiagram
  participant U as nkdctl bake-off
  participant A as env A
  participant B as env B
  U->>A: Local.Run(IR) — .nkd/bakeoff/a, host TZ/HOME
  A-->>U: report A → manifest A
  U->>B: Local.Run(IR) — .nkd/bakeoff/b, TZ=America/New_York, HOME=/tmp/…
  B-->>U: report B → manifest B
  U->>U: manifestA.VerifyAgainst(manifestB)
  alt digests match
    U-->>U: BIT-IDENTICAL (per-component table) ✅
  else any divergence
    U-->>U: NOT bit-identical → CodeVerifyFailed (22)
  end
```

This is the within-machine varied-env gate; the ultimate gate runs the two builds on two distinct
machines (documented as not-yet-done).

---

## 11. Trust model: `attest` (publisher) → `verify` (operator)

Zero binaries in the trust model. The publisher runs the pipeline in the open and ships only a signed
**expected-digests** manifest (cosign keyless, detached). The operator rebuilds from source and proves
byte-identity. Cosign steps shell out to the `cosign` binary (plumbing: the argument construction is
tested, the signature execution is not).

```mermaid
sequenceDiagram
  actor Pub as Publisher
  actor Op as Operator
  participant CS as cosign
  Pub->>Pub: nkdctl attest → build (§7)
  Pub->>Pub: FromReport → expected-digests.json
  opt --sign / --bundle
    Pub->>CS: sign-blob (detached, keyless) [--bundle → Rekor inclusion proof]
    CS-->>Pub: .sig + .pem (+ mirrorable bundle)
  end
  Pub-->>Op: publish manifest (+ sig/cert/bundle) — NO binaries

  Op->>Op: nkdctl verify -e expected-digests.json
  opt signature/bundle provided
    Op->>CS: verify-blob (identity/issuer [/offline])
    CS-->>Op: manifest signature verified
  end
  Op->>Op: rebuild from source (§7)
  Op->>Op: want.VerifyAgainst(rebuilt)
  alt every component reproduces byte-for-byte
    Op-->>Op: VERIFIED ✅
  else first divergence
    Op-->>Op: fail closed, names the node → CodeVerifyFailed (22)
  end
```

---

## 12. `verify` fail-closed decision gate

`verify` closes the offline fail-**open** hole *before* rebuilding: it refuses configurations that would
trust an unsigned or unprovable manifest unless an explicit escape hatch is set.

```mermaid
flowchart TB
  start["verify -e manifest.json (required)"] --> g1{"--require-signature<br/>but no --signature/--bundle?"}
  g1 -->|yes| f["CodeVerifyFailed (22): refuse"]
  g1 -->|no| g2{"--offline, no --bundle,<br/>no --insecure-ignore-tlog?"}
  g2 -->|yes| f
  g2 -->|no| g3{"signature or bundle given?"}
  g3 -->|yes| cs["cosign verify-blob<br/>(identity/issuer/offline);<br/>fail → CodeVerifyFailed"]
  g3 -->|no| rb["rebuild from source (§7)"]
  cs --> rb
  rb --> cmp["VerifyAgainst(expected)"]
  cmp -->|match| okv["VERIFIED ✅"]
  cmp -->|diverge| f
```

`--offline` verifies a mirrored bundle's Rekor **inclusion proof** with no network — a real airgap path,
not a tlog-ignore. `--insecure-ignore-tlog` is the one explicit, opt-in fail-open escape hatch.

---

## 13. `sbom`

Deterministic CycloneDX for the resolved recipe. With `--expected-digests` it folds in built artifact
hashes — after asserting the manifest's `recipeDigest` matches the resolved recipe (else fail closed).

```mermaid
flowchart LR
  s["sbom"] --> spine["§2 → Resolved + digest"]
  spine --> e{"--expected-digests?"}
  e -->|no| gen["sbom.FromRecipe(res, digest, nil)"]
  e -->|yes| chk{"manifest.recipeDigest == digest?"}
  chk -->|no| f["CodeVerifyFailed (22)"]
  chk -->|yes| fold["fold component SHA256s"] --> gen
  gen --> out["CycloneDX 1.5 JSON"]
```

---

## 14. `package`

Assembles a prior build's compiled binary into its reproducible delivery artifact and records **that**
digest. Requires that `build` already ran (locates each binary via the IR + source-root).

```mermaid
flowchart LR
  p["package (--only, --out)"] --> spine["§2 → IR (per component: arch, outputType)"]
  spine --> loop["for each from-source component"]
  loop --> found{"built binary present?"}
  found -->|no| f["CodeVerifyFailed (22): run build first"]
  found -->|yes| ot{"outputType"}
  ot -->|oci-image| oci["assemble.OCIImage(arch)<br/>single-layer OCI layout → manifest digest"]
  ot -->|host-package| deb["assemble.Deb(arch)<br/>name_ver_arch.deb → sha256"]
```

Both encoders are byte-reproducible (fixed uid/gid/mtime, `SOURCE_DATE_EPOCH`) and stamp the target
arch, so an arm64 image/`.deb` is labeled and digested distinctly from amd64.

---

## 15. `image` → `disk` → boot

The optional **thin base OS** path, decoupled from the substrate. `image` bootstraps a reproducible
rootfs via mmdebstrap from a date-pinned snapshot mirror; `disk` assembles a self-contained
UEFI-bootable qcow2 and can boot it under QEMU/OVMF to prove the from-source Kubernetes API is live.

```mermaid
flowchart TB
  image["nkdctl image<br/>mmdebstrap --variant=minbase<br/>date-pinned snapshot + SOURCE_DATE_EPOCH<br/>(pinned rootfs toolchain, --privileged)"] --> rootfs["reproducible rootfs.tar<br/>(sha256 reported)"]
  rootfs -.->|"install substrate .debs + preload OCI images"| disk
  disk["nkdctl disk --format qcow2<br/>(scripts/build-qcow2.sh:<br/>GPT/ESP + on-disk GRUB + kernel + ext4)"] --> qcow["reproducible qcow2<br/>(fixed GUIDs/UUID; byte-identical ×2)"]
  qcow --> boot{"--boot?"}
  boot -->|yes| qemu["QEMU/OVMF boot (scripts/boot-qcow2.sh)<br/>init → from-source etcd + kube-apiserver<br/>→ kubectl create ns/cm → read back<br/>→ NKD-API-PROOF-OK"]
  boot -->|no| stop["qcow2 artifact only"]
```

`--format` other than `qcow2` fails closed (`CodeUnsupported`). Byte-identity is asserted on the
pre-encryption image only; FDE is a documented overlay.

---

## 16. `catalog` — pin / bump / update-ci

The churn-decoupling story: the hand-authored **registry** never changes on a version bump; only the
machine-managed **lock** does, shipped as its own release train. `pin`/`bump` use git + network (resolve
tag → commit/tree/epoch, and `.go-version` → toolchain digest when the source declares
`toolchainBaseImage`). `update-ci` emits a scheduled GitHub Actions workflow that bumps, **rebuilds to
prove the bump still reproduces**, and opens a PR.

```mermaid
flowchart TB
  subgraph inspect["read-only"]
    v["catalog validate → Load + fail-closed"]
    l["catalog list → recipes + components"]
  end

  subgraph pinning["pin / bump (git + network, writes lock)"]
    sel["SelectTag (constraint 1.32.x → newest stable)"]
    resolvep["ResolvePin: ls-remote tag → commit;<br/>rev-parse tree; committer epoch;<br/>.go-version → golang@sha256 (if toolchainBaseImage)"]
    write["WriteLock (sorted, do-not-edit header)"]
    sel --> resolvep --> write
  end

  subgraph ci["update-ci"]
    wf["emit scheduled workflow:<br/>cron → bump all → rebuild/verify → open PR<br/>(pin lands only if it still builds byte-identically)"]
  end
```

```mermaid
sequenceDiagram
  participant Cron as scheduled CI
  participant Bump as nkdctl catalog bump --all
  participant Build as nkdctl verify/build
  participant PR as pull request
  Cron->>Bump: resolve newest tag per source constraint
  Bump->>Bump: rewrite pins.lock.yaml only
  Bump->>Build: rebuild from bumped pins
  alt still reproduces
    Build->>PR: open PR (lock diff only)
  else regression
    Build-->>Cron: fail closed, no PR
  end
```

---

## 17. Fail-closed exit codes

Every failure path returns a distinct, coded process status (`pkg/nkd/errors`). This is what makes
"fail closed" observable in CI.

```mermaid
flowchart LR
  subgraph resolution["resolution"]
    c10["10 NoMatch"]
    c11["11 Ambiguous"]
    c12["12 ReservedAxis"]
    c13["13 Unsupported"]
  end
  subgraph integrity["catalog integrity"]
    c14["14 UnpinnedSource"]
    c15["15 TagToolchain"]
    c16["16 InvalidCatalog"]
  end
  subgraph execution["execution"]
    c20["20 FetchFailed<br/>(commit/tree mismatch)"]
    c21["21 BuildFailed"]
    c22["22 VerifyFailed<br/>(divergence / bad signature)"]
  end
  c1["1 Internal"]
```

| Code | Exit | Raised when |
|---|---|---|
| `NoMatch` | 10 | no recipe matches the query |
| `Ambiguous` | 11 | two recipes match with equal specificity |
| `ReservedAxis` | 12 | a reserved axis (`accelerator`, …) is non-empty |
| `Unsupported` | 13 | arch outside `{amd64, arm64}`; unknown engine; `disk --format` ≠ qcow2; no packager |
| `UnpinnedSource` | 14 | floating/short commit, missing tree hash, or no lock pin |
| `TagToolchain` | 15 | a toolchain pinned by tag instead of `@sha256:` |
| `InvalidCatalog` | 16 | strict-decode/typo axis, composition hook, unknown `{{token}}`, bad outputType |
| `FetchFailed` | 20 | fetched commit or tree hash ≠ the pin |
| `BuildFailed` | 21 | a build container exits non-zero |
| `VerifyFailed` | 22 | rebuilt digest ≠ expected, or manifest signature fails |
| `Internal` | 1 | unexpected internal error |
