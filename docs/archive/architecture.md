# NKD — Architecture diagrams

> [!CAUTION]
> **Target-architecture snapshot.** These diagrams are design input, not evidence that every depicted adapter
> or lifecycle is implemented. Use the proposed ADRs and `docs/adr/reconciliation.md` for current boundaries.

Four views, from a single build to the full two-actor lifecycle.

**Reading these diagrams**
- **Subgraphs = ownership boundaries** (NVIDIA · operator · shared definition).
- **Solid = flow**; **dotted = compare / feedback / same-input**.
- Same upstream sources are pinned once and built *independently* by NVIDIA and the operator; the **validation
  vector** is the compare anchor; **no artifact bytes cross the release boundary**.

---

## 1. High-level architecture

Sources → catalog + lockfile → `nkdctl` recipe → generated pipeline → the operator builds every artifact from
the same pinned sources, verifies against the published digests, and publishes to its own registries.

```mermaid
flowchart TB
  subgraph up["Upstream sources (pinned)"]
    k8s["kubernetes"]
    etcd["etcd"]
    nke["NKE"]
    misc["containerd · runc · coredns<br/>cni · crictl · helm"]
    aicr["AICR"]
    os["OS base"]
  end

  subgraph def["NKD definition — published (no bytes)"]
    cat["pinned catalog"]
    prof["builder profile"]
    vec["validation vector"]
  end

  subgraph tool["nkdctl"]
    resolve["resolve criteria"]
    ir["recipe / IR"]
    emit["emit pipeline"]
  end

  gen["generated CI pipeline"]

  subgraph op["Operator CI"]
    fetch["fetch pinned source"]
    build["from-source build"]
    verify["verify byte-identity"]
    sign["sign + promote"]
  end

  subgraph art["Built artifacts"]
    images["control-plane images"]
    debs[".deb packages"]
    disk["node disk (RAW)"]
    sbom["SBOM + attest"]
  end

  subgraph reg["Operator registries"]
    oci["OCI registry"]
    apt["apt/deb repo"]
  end

  up --> cat
  cat --> resolve
  resolve --> ir --> emit --> gen
  prof --> gen
  gen --> fetch --> build --> verify --> sign
  sign --> images & debs & disk & sbom
  images --> oci
  disk --> oci
  sbom --> oci
  debs --> apt
  up -.->|"same source"| fetch
  vec -.->|"compare"| verify
```

---

## 2. Operator maintenance loop

New pins **or** a new `nkdctl` version → re-generate the pipeline, rebuild from the new pins, re-verify.

```mermaid
flowchart TB
  subgraph triggers["Upstream change"]
    newpins["new pins<br/>(versions / CVEs)"]
    newtool["new nkdctl"]
  end
  bump["bump catalog + nkdctl"]
  regen["re-generate pipeline"]
  run["build from new pins"]
  ver{"verify byte-identical?"}
  promote["promote → rollout"]
  triage["triage mismatch"]
  sched["scheduled bump CI"]

  newpins --> bump
  newtool --> bump
  sched -.->|"proposes"| bump
  bump --> regen --> run --> ver
  ver -->|Reproduced| promote
  ver -->|mismatch| triage
  triage -->|variant| promote
  triage -->|defect| bump
```

---

## 3. NVIDIA pipeline + qualification feedback loop

NVIDIA builds + qualifies reference artifacts and publishes only the definition + vector. Failures file issues
to component teams, whose fixes re-qualify tags; operator field signals feed triage.

```mermaid
flowchart TB
  subgraph nvpipe["NVIDIA NKD pipeline"]
    author["author / bump recipe"]
    refbuild["reference build"]
    qual["qualify<br/>repro · conformance · NKE · GPU/HPN"]
  end
  gate{"qualified?"}
  rel["publish definition + vector"]
  issues["file issues + evidence"]
  subgraph comps["Component teams"]
    nke["NKE"]
    deps["upstream deps"]
    aicrteam["AICR"]
  end
  operators["operators reproduce & run"]
  triage["triage → advisory"]

  author --> refbuild --> qual --> gate
  gate -->|yes| rel
  gate -->|bugs| issues
  issues --> nke & deps & aicrteam
  nke & deps & aicrteam -.->|"fix / re-qualify"| author
  rel --> operators
  operators -.->|"mismatch · incident · CVE"| triage
  triage --> author
```

---

## 4. End-to-end lifecycle

Both actors + the release boundary, with both feedback loops.

```mermaid
flowchart TB
  up["Upstream sources"]
  teams["component teams<br/>(NKE · deps · AICR)"]

  subgraph nvidia["NVIDIA"]
    author["author recipe"]
    refbuild["reference build"]
    qual["qualify"]
  end
  subgraph release["Release boundary (no bytes)"]
    def["definition + vector"]
  end
  subgraph operator["Operator"]
    emit["generate pipeline"]
    build["from-source build"]
    verify{"verify?"}
    variant["variant / triage"]
    promote["promote"]
    operate["operate fleet"]
  end

  up --> author
  up -.->|"same source"| build
  author --> refbuild --> qual
  qual -->|qualified| def
  qual -.->|bugs| teams
  teams -.->|"fix"| up
  def --> emit --> build --> verify
  verify -->|Reproduced| promote
  verify -->|mismatch| variant
  variant -->|approved| promote
  variant -->|defect| def
  promote --> operate
  operate -.->|"signals"| author
```
