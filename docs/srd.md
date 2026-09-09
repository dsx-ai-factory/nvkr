# nkdctl — Software Requirements Document

> [!CAUTION]
> **Historical requirements draft.** This document preserves the product target and requirement identifiers
> used during the POC. It contains known-stale behavior, interface, support, and acceptance claims and is not
> the current implementation specification or release authority. The proposed ADRs in `docs/adr/` are the
> maintained engineering baseline pending named-decider ratification; `docs/adr/reconciliation.md` records
> current maturity, disabled paths, evidence, and critiques. Treat changes here as requirements input that
> must be reconciled through those sources.

**Version:** 0.3 (DRAFT)
**Date:** 2026-08-07
**Status:** Historical draft retained for traceability

**Abstract.** nkdctl is a fully open-source (Apache-2.0), environment-agnostic command-line **tool** that lets any external Kubernetes operator — NCPs, sovereign, on-prem, and other-cloud operators — build a Kubernetes substrate **from upstream source**, inside their own environment, with reproducible, verifiable, from-source provenance. Its trust model is reproducibility: every input is pinned, outputs are byte-reproducible, the output digest is a pure function of the recipe, and any party can verify any other party's build bit-for-bit against a small signed **expected-digests manifest** without a binary ever being redistributed. This SRD deliberately separates the general-purpose **TOOL** (nkdctl — resolver, pipeline emitter, reproducible-build discipline, verify engine, and defaults) from any particular **CATALOG/RECIPE** (a pinned component set + expected-digests published on top of the tool). NVIDIA publishes one such catalog ("NKD"); this document specifies neither that catalog nor any NVIDIA-internal system. All NVIDIA-internal products named herein are **non-normative background only** — never a dependency or integration requirement — and interoperability is specified strictly against standard, generic interfaces (any OCI registry, any apt/deb or future rpm repository, any CI engine, standard PXE/iPXE/HTTP-boot provisioning, and standard sigstore).

---

## Document Control

### Revision History

| Version | Date | Author | Summary |
|---|---|---|---|
| 0.1 (DRAFT) | 2026-08-07 | nkdctl working group | Initial consolidated draft for review. |
| 0.2 (DRAFT) | 2026-08-07 | nkdctl working group | Incorporated the Bring-Your-Own-OS (BYO-OS) consumption model: decoupled node-OS readiness vs post-provision, packages-first from-source substrate delivery (§3.5, §5.11, NKD-FR-INT-052, NKD-CON-019/020, NKD-OI-013/014/015). |
| 0.3 (DRAFT) | 2026-08-07 | nkdctl working group | Default NKD catalog embedded in the binary via Go `//go:embed`, mirroring AICR's `EmbeddedDataProvider` (NKD-FR-RC-031/032). Added **IR** and **FDE** to the glossary. |
| 0.4 (DRAFT) | 2026-08-10 | nkdctl working group | **Reconciled toward the NKD v1 product definition** (`nkd-v1.md`), favoring its positions. See "Reconciliation with NKD v1" below. |

### Reconciliation with NKD v1

`nkd-v1.md` (the NVIDIA Kubernetes Definition v1 product/catalog spec) is the **canonical example catalog** this tool is built to publish. Where the two documents diverged, this SRD is amended to favor NKD v1's positions (see `docs/nkd-v1-vs-srd.md` for the full comparison). The load-bearing changes in v0.4:

1. **Reproducibility is bounded to a signed builder profile, not host-independent** (NKD v1 §8.2.1/§10.1). The prior "cross-environment bit-identity is the definitive pass" claim is reframed: byte reproduction holds **within a declared, digest-identified builder profile**; a host that diverges from the profile on a byte-determinative field is a *variant*, not a reproduction. A fail-closed **host preflight** runs before inputs resolve. See **§6.10 (NKD-FR-REP-030..034)**; NKD-NFR-002/003, NKD-FR-BLD-070, NKD-FR-REP-005 amended.
2. **RAW is the only canonical disk artifact** (NKD v1 §8.3.1). Byte-reproducibility attaches to the **RAW logical-disk (LBA) stream**; **qcow2/VMDK/VHD are derived envelopes** verified by the decoded logical-disk digest, never by the container-file digest (ISO keeps its own canonical boundary). NKD-FR-BLD-060/063, NKD-FR-INT-020, NKD-FR-REP-001/008, NKD-NFR-001/009 amended.
3. **A gated ValidatedVariant / controlled-fallback lane** (NKD v1 §8.4.1) supplements pure fail-closed: a documented, pre-classified representation cause auto-approves on the operator's own infrastructure into a *distinct, non-reproduction, expiring* identity; any other cause fails closed. See **§6.11 (NKD-FR-REP-035..037)** + the stable mismatch **cause-code taxonomy**.
4. **Conformance + a second, independent validation vector + a diagnosable evidence bundle + verifier isolation** (NKD v1 §8.1/§8.4.2/§14). The manifest carries a conformance claim + validation-vector reference; verify emits a deterministic evidence bundle with per-component cause codes and per-block/per-path disk granularity; the verifier is isolated from the build stages. See **§6.12 (NKD-FR-REP-038..042)**.
5. **AICR is the foundational declarative layer for the accelerated-runtime profile** (NKD v1 goal 10 / §8.1). The `accelerator/service/intent/platform` axes are no longer permanently reserved — they **activate for a recipe that declares a digest-pinned AICR profile** (identity+digest, carried as provenance); without one they still fail closed. NKD-CON-016, NKD-OI-008, §10 amended; **NKD-FR-RC-013a** added.

> **Terminology note:** this SRD's **"Phase 1 / Phase 2"** are the BYO-OS *consumption* model (node readiness vs post-provision substrate install, §3.5) — distinct from NKD v1's **rollout phases (P0–P3)** in its §16. The two are unrelated; when citing NKD v1's rollout, this SRD writes "NKD v1 rollout P0–P3".

### Intended Audience

- **External operators** (NCP, sovereign, on-prem, other-cloud) evaluating or adopting nkdctl to build a from-source Kubernetes substrate.
- **Catalog authors / publishers** (a distribution, an operator, or NVIDIA) who publish a signed, reproducible component set on top of the tool.
- **Engineering** implementing nkdctl against a single authoritative, testable definition.
- **Security / compliance auditors** verifying provenance, integrity, and airgapped reproducibility.
- **OSS community contributors** extending adapters, formats, and components under an Apache-clean posture.

---

## 1. Introduction

### 1.1 Purpose

This Software Requirements Document (SRD) specifies **nkdctl**, a fully open-source (Apache-2.0) command-line tool that lets any Kubernetes operator **pipeline and build their own Kubernetes substrate from upstream source**, for their own environment, with reproducible, verifiable, from-source provenance.

nkdctl is the general-purpose **TOOL**. It is deliberately separated from any particular **CATALOG/RECIPE** (the pinned component set an operator or publisher builds). NVIDIA publishes an "NKD" catalog on top of nkdctl; this SRD does **not** specify that catalog and does **not** require any NVIDIA-internal system. The tool ships sensible defaults but hardcodes no environment.

This document establishes the requirements for nkdctl v0.1 so that engineering, security/compliance auditors, and OSS community contributors share one authoritative, testable definition of the tool's behavior, interfaces, and trust model.

### 1.2 Scope

**In scope (v1)**

- An **OSS, environment-agnostic tool** that external operators run **inside their own environment** to produce a Kubernetes stack **built from source**.
- The pipeline: **criteria → resolve → recipe/IR → emit build pipeline → operator CI builds from source → artifacts → verify**.
- **Reproducibility as the trust model:** every input pinned, outputs byte-reproducible, output digest a pure function of the recipe; verification against a signed **expected-digests** manifest without ever redistributing a binary.
- **Baremetal-first** artifacts: host packages (`.deb` default), control-plane OCI images, and bootable disk-image SKUs where **RAW is the canonical disk artifact** (byte-reproducibility attaches to the RAW logical-disk stream; qcow2/VMDK/VHD are *derived envelopes* verified by the decoded logical-disk digest; ISO keeps its own canonical boundary), plus tarball — for two node SKUs (control-plane, worker). *(Reconciled to NKD v1 §8.3.1.)*
- **Standard, generic ecosystem interfaces only:** any OCI registry, any apt/deb (or future rpm) repository, any CI engine (GitHub Actions default; GitLab CI, Tekton, Argo Workflows, local runner via adapters), standard PXE/iPXE/HTTP-boot + disk-image provisioning, and standard sigstore (cosign / Fulcio / Rekor).

**Separation of concerns: TOOL vs CATALOG/RECIPE**

| Concern | Owned by | Specified here |
|---|---|---|
| **TOOL** — resolver, pipeline emitter, reproducible-build discipline, verify engine, defaults | nkdctl (this SRD) | Yes |
| **CATALOG / RECIPE** — pinned component set, k8s version, base OS, expected-digests manifest | any publisher (NVIDIA's NKD, a distro, or the operator themselves) | No — consumed via `--data` / catalog reference; not hardcoded |

Any catalog publisher can run the public pipeline, publish a signed expected-digests manifest, and ship **zero binaries**; any party can then verify any other party's build bit-for-bit, or self-attest by running the public pipeline once and pinning their own baseline. The model is not NVIDIA-centric.

**Out of scope.** Cloud/CSP image building, GPU/accelerated stacks, reserved criteria axes (accelerator/service/intent/platform/tier/compliance), and closed-source (Tier-4) components are out of v1 scope; see §10 for the full enumeration. A from-source tool inherently cannot build closed-source components; those remain the operator's out-of-band concern.

### 1.3 Product Overview

nkdctl is modeled conceptually on **AICR** (`github.com/NVIDIA/aicr`) — an **AICR-sibling compiler** for the Kubernetes substrate. Where AICR runs *criteria → recipe → bundle + SBOM* for cluster configuration, nkdctl runs:

```
criteria  →  recipe (resolved IR)  →  build pipeline  →  artifacts  →  verify
```

nkdctl mirrors AICR's proven engineering patterns: a thin CLI over a shared client/SDK facade that holds all business logic; structured errors with codes; context timeouts on all I/O; functional-options config; asymmetric-match criteria; base/overlay/mixin composition (a resolver hook in v1; recipes ship flat); a **declarative** registry so adding a component needs no code; deterministic YAML marshaling; and cosign keyless signing.

Key product properties:

- **The bundle is a portable build pipeline**, not a Packer/image config. nkdctl emits a substrate-neutral pipeline that the operator's CI engine runs; the recipe YAML alone is not an artifact — the recipe→pipeline→artifact conversion produces it.
- **Everything is built from source.** Referencing prebuilt binaries/containers/charts is de-facto redistribution and is disallowed; nkdctl compiles the substrate from upstream source with full source→artifact provenance.
- **The control plane runs as container images**, not host binaries — so from-source means build the Go binary *and* reproducibly OCI-package it, on top of the host-binary packages.
- **Ships defaults, hardcodes nothing.** Ubuntu/apt, GitHub Actions, qcow2+ISO are defaults; OS, CI engine, output formats, boot/provisioning path, and FDE are all operator-configurable.
- **Consumers are generic.** nkdctl feeds any operator's Kubernetes engine / provisioning / lifecycle system. NVIDIA's "NKE" is one non-normative example consumer.

### 1.4 Definitions & Acronyms

| Term | Definition |
|---|---|
| **NKD** (NVIDIA Kubernetes Definition) | The concept/product family; here, primarily the **catalog** NVIDIA publishes on top of nkdctl. Non-normative example, not a dependency. |
| **NKE** (NVIDIA Kubernetes Engine) | NVIDIA's provisioning/lifecycle engine; **one example** of a generic downstream consumer of nkdctl artifacts. Non-normative. |
| **nkdctl** | The OSS (Apache-2.0), environment-agnostic tool specified by this SRD: resolver + pipeline emitter + reproducible-build + verify engine. |
| **AICR** | NVIDIA AI Cluster Runtime (`github.com/NVIDIA/aicr`); the engineering-pattern sibling nkdctl is modeled on. |
| **catalog** | A pinned, validated component set + expected-digests published on top of nkdctl. Operators author their own; NVIDIA publishes "NKD". |
| **recipe** | The resolved, pinned specification (IR) produced from criteria + catalog; input to pipeline emission. Not itself a runnable artifact. |
| **IR (Intermediate Representation) / pipeline IR** | The substrate-neutral, fully-resolved form of a recipe — a typed DAG of pinned build steps (fetch → patch → build → collect → assemble → digest → verify). Computed once; `recipeDigest = sha256(IR)`; each engine adapter *lowers* the same IR to concrete CI YAML (one front-end, many back-ends). |
| **FDE (Full-Disk Encryption)** | Whole-root-disk encryption via **LUKS**, key optionally sealed to **TPM2** for auto-unlock at boot. Per-node and non-deterministic, so it is **outside** the reproducibility contract (§6.8). |
| **SourceBuildRegistry** | The declarative registry of buildable components; one YAML entry per component, no code. Unpinned source/tag refs fail resolution. |
| **expected-digests manifest** | Small signed JSON mapping `recipeDigest` → predicted per-component / control-plane / worker image sha256s + toolchain-image digests + public-run URL. The only artifact a publisher ships. |
| **SKU** | A logical node build target. v1 SKUs: **control-plane** and **worker**. |
| **hermetic build** | A build run with `--network=none` and an explicit env allowlist, over pinned-by-digest OCI toolchain containers, so outputs depend only on pinned inputs. |
| **byte-identity** | Bit-for-bit equality of two independently produced artifacts; the basis of the trust model. |
| **SBOM** | Software Bill of Materials; nkdctl emits deterministic **CycloneDX** (stripped serialNumber/timestamp, UUIDv5). |
| **cosign keyless** | Signing via a short-lived **Fulcio** cert bound to an OIDC identity, with **Rekor** transparency-log inclusion; no long-lived key. Signatures are **detached**. |
| **Rekor / Fulcio** | Sigstore transparency log (Rekor) and CA (Fulcio). |
| **mmdebstrap** | The tool used to build the reproducible Debian/Ubuntu base rootfs against a date-pinned snapshot. |
| **PXE / iPXE** | Network boot methods; operator-configurable, never assumed. |
| **LUKS / TPM2 FDE** | Full-disk encryption (LUKS keyed via TPM2); operator-configurable and **outside** the reproducibility contract. |
| **BYO-OS (Bring-Your-Own-OS)** | A consumption model decoupling operator-owned node-OS readiness (Phase 1) from post-provision, from-source cluster-software install (Phase 2). NKE's BYO-OS proposal (R7) is one non-normative example; the model is generalized in §3.5. |
| **node readiness contract** | The minimal operator-satisfied conditions that make a node ready for cluster bootstrap (base OS + configured package repo + any minimal agents the operator's lifecycle system needs), independent of how they were achieved. Operator-owned; not built by nkdctl. |
| **Phase 1 / Phase 2** | Phase 1 = operator brings a thin base OS to readiness by any means (Packer, cloud-init, Ansible, golden image, …). Phase 2 = the cluster-software substrate is installed post-provision from a signed package repo / OCI registry by a lifecycle system. |
| **packages-first delivery** | nkdctl's primary delivery mode: the from-source substrate is emitted as installable signed packages + control-plane OCI images consumed at bootstrap, decoupled from any base-OS image (vs a monolithic "baked" image). |
| **thin base image** | An optional bootable image containing only the base OS + minimal readiness set (no cluster software), as opposed to a "kitchen-sink" image with kubeadm/CNI/etc. baked in. |

### 1.5 References

| # | Reference |
|---|---|
| R1 | `nkd.md` — the authoritative context/positioning document for nkdctl. |
| R2 | AICR — `https://github.com/NVIDIA/aicr` (engineering patterns). |
| R3 | Reproducible Builds — `https://reproducible-builds.org/` (SOURCE_DATE_EPOCH, byte-identity, rebuilderd model). |
| R4 | Sigstore — `https://www.sigstore.dev/` (cosign, Fulcio, Rekor). |
| R5 | CycloneDX — `https://cyclonedx.org/` (SBOM specification). |
| R6 | RFC 2119 — key words for requirement levels (SHALL/SHOULD/MAY). |
| R7 | `NKE-BYO-OS-PROPOSAL.md` — Bring-Your-Own-OS proposal (decoupled node readiness vs post-provision cluster-software install); source of the consumption model generalized in §3.5. Non-normative example consumer. |

---

## 2. Stakeholders & Personas

nkdctl's **primary** stakeholder is the **external operator**. Every other persona is served either by using the same tool with no privileged path, or by consuming its generic, standard-interface outputs.

### 2.1 External Operator (PRIMARY)

*NCP, sovereign, on-prem, or other-cloud Kubernetes operator building their own stack in their own environment.*

- **Goal:** Produce a Kubernetes substrate **built from source** for their own baremetal environment, without redistributing any binary and without depending on any vendor-internal system.
- **Needs:** a self-contained OSS tool that resolves criteria against a catalog and emits a build pipeline for *their* CI engine; hermetic, reproducible builds provably identical to a published baseline; pluggable outputs (`.deb`, control-plane OCI images, qcow2/ISO/raw/tarball) landing in *their* registry / apt repo / provisioning path; configurable OS, boot/provisioning path, and FDE; and the ability to **self-attest** (run the public pipeline once, pin their own expected-digests baseline).

### 2.2 Catalog Author / Publisher

*Anyone who publishes a pinned component set + expected-digests — NVIDIA (the NKD catalog), a Linux distribution, or an operator publishing their own.*

- **Goal:** Publish a signed, reproducible definition of a Kubernetes stack that others can rebuild and verify, shipping **zero binaries**.
- **Needs:** a declarative `SourceBuildRegistry`/catalog format (one YAML entry per component, unpinned refs fail resolution); a deterministic `recipeDigest` and a way to compute the expected-digests manifest by running the public pipeline; cosign keyless, detached signing bound to *their own* OIDC identity with Rekor inclusion.

### 2.3 Operator's Kubernetes Engine / Lifecycle System (Generic Consumer)

*Any provisioning/rollout/lifecycle system consuming nkdctl artifacts. NVIDIA's NKE/Forge is ONE non-normative example.*

- **Goal:** Provision and lifecycle-manage nodes from from-source artifacts, including immutable node-replacement rollout and a CVE fast-path.
- **Needs:** artifacts delivered via **standard classes** (any apt repo, any disk-image + boot provisioning, a recipe + image-digest pair, and a minimal-diff reproducible rebuild for the CVE fast-path); stable, deterministic digests to pin against.

### 2.4 Security / Compliance Auditor

- **Goal:** Independently verify provenance and integrity of a built stack, including airgapped.
- **Needs:** `nkdctl verify` (cosign-verify against a **configurable pinned OIDC issuer+identity** + Rekor inclusion, fail closed; recompute `recipeDigest`; diff local vs predicted; non-zero exit naming the first divergent node); a **mandatory offline** cosign-verify path; a deterministic CycloneDX SBOM; and an explicit, enumerated exclusion of Tier-4 closed-source components.

### 2.5 OSS Community Contributor

- **Goal:** Extend the tool (new CI-engine adapters, output formats, components) and contribute catalogs, under a clean Apache-2.0 posture.
- **Needs:** an Apache-clean codebase (linked deps permissive; no copyleft engine/daemon; invoked GPL build tools acceptable as subprocesses); declarative extension points (catalog registry, per-engine adapters, pluggable formats); and clear OSS governance (Apache-2.0, CLA/DCO) — noted as an open governance issue.

---

## 3. System Context & Architecture

### 3.1 Context

nkdctl runs **entirely inside an operator's own environment** and interacts only with **standard, generic ecosystem interfaces**. It has no runtime dependency on any NVIDIA-internal system. NVIDIA consumes nkdctl exactly as any external operator does — *no privileged internal path; what NVIDIA builds is what an external operator builds*.

**NKD-CTX-001** — [MUST] nkdctl SHALL operate as a self-contained tool that requires no NVIDIA-internal service, network, or credential at resolve, emit, build, or verify time.
- *Rationale:* OSS, environment-agnostic mandate; external operators will not have internal systems.
- *Verification:* Run the full criteria→verify flow on a clean host with all NVIDIA-internal endpoints blocked; confirm success.

**NKD-CTX-002** — [MUST] nkdctl SHALL integrate only through standard interfaces — any OCI registry, any apt/deb (future rpm) repository, any CI engine (GitHub Actions, GitLab CI, Tekton, Argo Workflows, local runner), standard PXE/iPXE/HTTP-boot + disk-image provisioning, and standard sigstore (cosign/Fulcio/Rekor) — and SHALL NOT require any NVIDIA-specific product for any of these classes.
- *Verification:* Emit and run a pipeline against a generic self-hosted registry + apt repo + a non-GitHub CI engine; verify artifacts land and validate.

**NKD-CTX-003** — [MUST] nkdctl SHALL consume any catalog supplied via `--data` or a catalog reference, and SHALL NOT hardcode any single environment (OS, CI engine, output format, boot path).
- *Rationale:* TOOL/CATALOG separation; defaults (Ubuntu/apt, GitHub Actions, qcow2+ISO) are overridable.

**NKD-CTX-004** — [MUST] nkdctl's build pipelines SHALL emit to N engines via thin per-engine adapters over one substrate-neutral pipeline IR, with GitHub Actions as the default.
- *Verification:* Emit the same recipe to ≥2 engines; confirm both drive an equivalent job-per-component build.

**NKD-CTX-005** — [MUST] nkdctl's verify path SHALL support a fully offline mode (pre-staged/mirrored expected-digests manifest + Rekor bundle) that fails closed on any missing signature, identity mismatch, or absent Rekor inclusion.
- *Rationale:* Airgapped targets and networks blocking Fulcio/Rekor egress are common.

**NKD-CTX-006** — [MUST] NVIDIA-internal consumers (e.g., NKE/Forge, a private apt repo, a node-replacement/CVE-fast-path lifecycle) SHALL be treated as **non-normative example consumers** of standard interface classes only, and SHALL NOT appear as requirements, dependencies, or integration targets.

### 3.2 Data Flow

```
                         ┌───────────────────────────────────────────────┐
                         │        OPERATOR'S OWN ENVIRONMENT              │
   catalog (--data) ─┐   │   ┌─────────┐    ┌───────────────────────┐    │
   + criteria        ├──►│──►│ RESOLVE │──► │ recipe / pipeline IR  │    │
   (role,k8sVer,     │   │   │(nkdctl) │    │ (pinned, flat v1)     │    │
    os,arch)       ──┘   │   └─────────┘    └──────────┬────────────┘    │
                         │                             │ recipeDigest    │
                         │                    ┌────────▼───────────┐     │
                         │                    │  EMIT PIPELINE      │     │
                         │                    │  (adapter: GHA /    │     │
                         │                    │   GitLab / Tekton / │     │
                         │                    │   Argo / local)     │     │
                         │                    └────────┬───────────┘     │
                         │                    ┌────────▼───────────────┐ │
                         │                    │ OPERATOR CI BUILDS      │ │
                         │                    │ FROM SOURCE             │ │
                         │                    │ hermetic, --network=none│ │
                         │                    │ pinned-by-digest OCI    │ │
                         │                    │ toolchain, job/component│ │
                         │                    └────────┬───────────────┘ │
                         │        ┌────────────────────┼───────────────┐ │
                         │        ▼                    ▼               ▼ │
                         │  host packages      control-plane      bootable │
                         │  (.deb default)     OCI images        disk-image│
                         │                     (~7 targets +      SKUs      │
                         │                      reproducible      (qcow2 &  │
                         │                      base image)       ISO 1st;  │
                         │                                        raw/tar)  │
                         │        └──────────┬─────────┴───────────────┘  │
                         │                   ▼                            │
                         │          ┌─────────────────┐                  │
                         │          │  nkdctl VERIFY  │ ◄─ expected-      │
                         │          │ (final pipeline │    digests        │
                         │          │  node; fail     │    manifest       │
                         │          │  closed)        │   (signed JSON,   │
                         │          └────────┬────────┘   cosign keyless, │
                         │                   │            detached,       │
                         │                   │            Rekor incl.)    │
                         └───────────────────┼────────────────────────────┘
                                             ▼
        STANDARD SINKS ─►  any OCI registry │ any apt/deb repo │ PXE/iPXE/HTTP
                           boot + provisioning │ operator's k8s engine /
                           lifecycle system  (NVIDIA NKE = ONE example only)
```

### 3.3 Architecture Notes

- **Thin CLI over a shared client/SDK facade** holds all business logic (AICR pattern); the CLI performs only input validation, output formatting, and user interaction.
- **Resolver** performs AICR asymmetric-match over v1 active axes (`role`, `k8sVersion`, `os`, `arch{amd64}`), fails closed on reserved axes (accelerator/service/intent/platform/tier/compliance), and rejects unpinned source refs or tag-not-digest toolchains.
- **Reproducibility discipline is owned by nkdctl:** Go flag discipline (`CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, `-ldflags="-buildid= -s -w"`, `GOTOOLCHAIN=local`, `SOURCE_DATE_EPOCH` from the pinned commit), a date-pinned mmdebstrap base rootfs with normalization, and pinned-container image assembly (xorriso, mksquashfs, mke2fs, fixed GPT GUIDs). runc's CGO/musl+libseccomp lane is the hard case.
- **recipeDigest is a pure function of the resolved recipe/IR;** byte-reproducible outputs make the output digest predictable and verifiable against the signed expected-digests manifest.
- **Verify is the unconditional final pipeline node**, mirroring the standalone `nkdctl verify` command, so every public and operator run self-checks.

### 3.4 Assumptions & Open Issues (framing; see §11)

- A **two-environment bit-identity bake-off spike** is a mandatory gate before committing to the from-source thesis; this architecture is provisional on that spike.
- **Bootable-image format(s) and boot/provisioning defaults** must remain pluggable.
- **FDE/reproducibility boundary:** byte-identity is asserted on the pre-encryption rootfs; any per-node encrypted layer is out of contract.
- **Kernel ownership and reproducibility** of the pinned kernel is an open item.
- **Tier-4 proprietary components** cannot be built from source and are explicitly excluded.
- **GPU/accelerated stack** is out of v1 (later concern / separate catalog).
- **OSS governance** (Apache-2.0, CLA/DCO, community catalog contributions) is an open item.

### 3.5 Consumption Model: Decoupled OS Readiness & Post-Provision Substrate Install

nkdctl's outputs target a **two-phase consumption model** that separates *who owns the node OS* from *who delivers the Kubernetes substrate*. This generalizes the NKE Bring-Your-Own-OS (BYO-OS) proposal (R7), which is **one non-normative example** consumer; nkdctl requires no specific readiness contract, agent, or lifecycle system.

- **Phase 1 — node readiness (operator-owned, any means).** The operator brings a **thin base OS** to a readiness state — a supported base distribution, a configured signed package repository, and whatever minimal agents its lifecycle system needs — via Packer, cloud-init, Ansible, a golden image, or manual/CI. *How* this happens is out of scope for nkdctl.
- **Phase 2 — substrate install (post-provision).** A lifecycle/provisioning system then installs the **from-source Kubernetes substrate** — kubeadm/Kubernetes distro packages, CNI, control-plane images, add-on/ops agents — from a standard signed package repo / OCI registry, versioned with the *cluster*, not the OS image.

**Design consequence.** nkdctl's **primary** deliverable is reproducible **from-source packages (`.deb` default) + control-plane OCI images** consumable by a standard post-provision install — *not* a monolithic bootable image with the substrate baked in. Cluster-scoped software (kubeadm, CNI, ops agents) MUST be deliverable as post-provision-installable packages and MUST NOT be *required* to be baked into a bootable image; when nkdctl does produce a bootable image it SHOULD produce a **thin** base-OS image (readiness only) distinct from those packages. This keeps OS provenance decoupled from Kubernetes/CNI version choice and matches how mature operators already consume the substrate. FDE remains an explicit base-image overlay (§5.7, §6.8), never part of the minimal readiness set. Realizing requirements: §5.11 (delivery), §7.8 (interoperability, NKD-FR-INT-052); the operator-owns-OS boundary is recorded in §9 (NKD-CON-019/020).

---

## 4. Functional Requirements — Catalog, Recipe, Criteria & Registry

This section specifies the resolution front-end: how a query is matched against catalog recipes, which criteria axes are active in v1, how the resolved recipe / pipeline IR is serialized and digested, and how the declarative, operator-authored catalog / `SourceBuildRegistry` is structured, pinned, and consumed. The "NKD" catalog is merely one instance of an operator-authored catalog and carries no privileged status.

### 4.1 Criteria Taxonomy & Asymmetric-Match Resolution

**NKD-FR-RC-001** — [MUST] nkdctl SHALL resolve a build target by matching a caller-supplied **criteria query** against the recipes carried in the loaded catalog, selecting a single recipe (or failing closed when none, or more than one, match after tie-breaking).

**NKD-FR-RC-002** — [MUST] nkdctl SHALL implement **asymmetric matching** in which a recipe field set to the wildcard value matches any query value for that field, and the empty string `""` SHALL be treated as identical to the explicit wildcard `any`.
- *Rationale:* Mirrors AICR's asymmetric-match semantics.
- *Verification:* Recipe `{os: any}` matches queries `{os: ubuntu}` and `{os: ""}` identically.

**NKD-FR-RC-003** — [MUST] Given a query with **extra fields** on which the candidate recipe is silent (wildcard/absent), nkdctl SHALL still consider the recipe a match.
- *Verification:* Query `{role: worker, k8sVersion: 1.34.2, os: ubuntu, arch: amd64}` matches recipe `{role: worker}`.

**NKD-FR-RC-004** — [MUST] Given a recipe that **constrains a field the query omits** with a non-wildcard value, nkdctl SHALL NOT consider it a match.
- *Verification:* Query `{role: worker}` does NOT match recipe `{role: worker, os: ubuntu}`.

**NKD-FR-RC-005** — [MUST] When more than one recipe matches, nkdctl SHALL break ties by **specificity** (greatest number of non-wildcard fields explicitly satisfied by the query); if no unique winner, resolution SHALL fail closed with a structured, coded error naming the ambiguous candidates.
- *Verification:* Two equally-specific matches produce a non-zero exit naming both IDs; adding one distinguishing constraint yields a single winner.

**NKD-FR-RC-006** — [MUST] Each criteria axis parser (`Parse*`) SHALL be a **built-in fast-path for known v1 values plus a registry fallback seeded from the loaded catalog / `--data`**, so operator-defined axis values load and resolve with **no code change**.
- *Verification:* A catalog whose recipe uses `os: debian` resolves with no nkdctl rebuild.

### 4.2 v1 Active Criteria Axes

**NKD-FR-RC-007** — [MUST] nkdctl SHALL expose exactly the following **active** criteria axes in v1, each a typed value participating in asymmetric matching:

| Axis | Type / v1 domain | Notes |
|------|------------------|-------|
| `role` | enum `{control-plane, worker}` | Dominant axis; drives the two node SKUs. |
| `k8sVersion` | semver string | e.g. `1.34.2`. |
| `os` | operator-choosable string; default `ubuntu` | Single-valued in v1. |
| `arch` | typed enum, v1 domain `{amd64}` | Enum shape chosen so `arm64` is additive, not a schema break. |

- *Verification:* Schema/godoc enumerate exactly these four axes; `arch` is a typed enum such that adding `arm64` needs no field-shape change.

**NKD-FR-RC-008** — [MUST] The `role` axis SHALL be the **dominant** axis, selecting between the two logical node SKUs (`control-plane`, `worker`); a resolved recipe SHALL carry exactly one `role`.

**NKD-FR-RC-009** — [MUST] nkdctl SHALL treat `os` as **operator-choosable** and SHALL NOT hardcode Ubuntu; Ubuntu SHALL be a **default** only when the axis is otherwise unspecified.
- *Verification:* A catalog pinning `os: <other>` resolves without special-casing; no Ubuntu string is required in the resolver path.

**NKD-FR-RC-010** — [MUST] `k8sVersion` SHALL be carried as a **semver string**, matched by exact/wildcard semantics in v1 (no ranges/floating tags); a `k8sVersion` not corresponding to a pinned source ref in the selected recipe SHALL fail resolution.

### 4.3 Reserved & Fail-Closed Axes

**NKD-FR-RC-011** — [MUST] nkdctl SHALL define `tier` (controller/tenant) and `compliance` (fips/cc) as **reserved and inactive** in v1 (hard-reserved). The **AICR-governed** axes `accelerator`, `service`, `intent`, `platform` are reserved **by default** but **activate for a recipe that declares a digest-pinned AICR profile** (NKD-FR-RC-013a) — reconciled to NKD v1 goal 10 / §8.1.

**NKD-FR-RC-012** — [MUST] For every reserved axis, a **non-empty** value in a query, recipe, catalog entry, or `--data` override SHALL cause resolution to **fail closed** with a structured, coded "not supported in v1" error; a reserved axis SHALL NEVER be silently treated as a wildcard.
- *Rationale:* Silent wildcarding of e.g. `compliance: fips` would ship a non-compliant artifact under a compliant-looking request — the dangerous fail-open direction.
- *Verification:* Query `{compliance: fips}` returns non-zero naming `compliance`; empty/absent reserved axes resolve normally.

**NKD-FR-RC-013** — [SHOULD] Reserved axes SHALL remain declared in the typed schema (activation later is additive) but SHALL NOT contribute to matching or tie-breaking while inactive.

**NKD-FR-RC-013a** — [MUST] **AICR is the foundational declarative layer for the accelerated-runtime profile** (NKD v1 goal 10 / §8.1). A recipe MAY declare an **AICR profile** — an `{identity, digest}` reference (digest-pinned by `@sha256:`, fail closed on a tag) to an AICR (`github.com/NVIDIA/aicr`) validated accelerated-runtime component-pairing set. When a recipe declares one, the AICR-governed axes (`accelerator/service/intent/platform`) **activate** for that recipe and the AICR profile SHALL be carried as **provenance** into the resolved recipe and the manifest. The `aicrProfile` pins both the recipe-selection **criteria** (`service/accelerator/os/intent[/platform]`) and the canonical **digest** (`@sha256:`, fail closed on a tag). Absent an AICR profile, those axes still **fail closed** (`NKD-E-RESERVED-AXIS`). nkdctl does not build AICR's runtime from source; it **leverages AICR to generate the accelerated-runtime bundle** as part of the distribution via the real deterministic three-step: nkdctl SHALL invoke `aicr recipe <pinned criteria> -o recipe.yaml`, verify `aicr evidence digest --recipe recipe.yaml` equals the pinned `aicrProfile.digest` (**fail closed on drift** — the AICR reproducibility contract), then `aicr bundle --recipe recipe.yaml --output <dir>`. The default adapter shells the `aicr` CLI; an SDK-backed implementation MAY be injected via the facade so nkdctl core links no AICR dependency (NKD-NFR-041/044). nkdctl SHALL fail closed if AICR is unavailable rather than fabricate a bundle; the rendered bundle's reference SHALL be recordable in the manifest. *(Implemented: `catalog.AICRProfile` (criteria+digest), `criteria.AICRAxes`, `recipe.Resolved.AICRProfile`, `pkg/nkd/aicr` (`Bundler` seam + `CLIBundler`, recipe→digest-verify→bundle), `nkdctl aicr bundle`, `attest --aicr-bundle`/`attest.AICRRef`; demonstrator recipe `examples/operator-catalog`.)*
- *Verification:* `{accelerator: h100}` against a catalog with no AICR recipe fails closed naming the axis; the same query against a recipe declaring a digest-pinned AICR profile resolves and carries the profile; `nkdctl aicr bundle --print` shows the three `aicr` invocations, and with `aicr` on PATH it generates the recipe, verifies its canonical digest against the pin, and renders the bundle (demonstrated live: eks+h100+ubuntu+training → `f6fbb662…` → a 14-component Helm bundle).

### 4.4 Composition Model (Base / Overlay / Mixin)

Layered composition is now **active** (`pkg/nkd/compose`), a faithful port of AICR's
`pkg/recipe` overlay/mixin engine. A catalog ships `overlays/` (`base.yaml` + criteria-
keyed overlays in a single-parent `spec.base` inheritance tree) + `mixins/` (orthogonal
single-axis fragments) alongside, or instead of, flat `recipes:`. Flat recipes remain
supported and unchanged.

**NKD-FR-RC-014** — [MUST] For a layered catalog, nkdctl SHALL resolve a recipe by **composition**: select the maximal leaf overlays whose criteria match (asymmetric match + specificity), merge each one's `spec.base` inheritance chain root→leaf (child wins), apply the accumulated mixins, and emit the flattened effective component set. A layer ADDS a component, `enabled: false` DISABLES one, or `overrides:` deep-merges onto it. The composed set feeds the unchanged `recipe.Compile → IR → recipeDigest` (the digest is computed over the flattened result).
- *Verification:* `pkg/nkd/compose` (`Spec.Merge`/`mergeComponentRef`/`deepMergeMap`/`resolveInheritanceChain`/`findMatchingOverlays`/`filterToMaximalLeaves`/`mergeMixins`) + `TestComposeInheritanceChainChildWins`/`TestComposeMergesMultipleMaximalLeaves`/`TestComposeMixins`; end-to-end `TestResolveLayeredComposition`; live in `examples/layered-catalog` (base + control-plane|worker + os mixin → distinct deterministic recipeDigests).

**NKD-FR-RC-015** — [MUST] Composition SHALL be deterministic (sorted, no clock/random) and fail closed: an unknown `spec.base`/mixin/component, an inheritance cycle, a missing `base.yaml`, or a duplicate layer name is a coded error (`CodeInvalidCatalog`), never a partial/silent compose.
- *Verification:* `TestComposeFailClosed`, `TestLoadStoreFailClosed`, `Store.Validate`; `TestComposeDeterministic`.

**NKD-FR-RC-016** — [MUST] The **clusterType** axis (`operator | tenant`) distinguishes a management cluster (runs CAPI/NKE to provision tenants) from a workload cluster. It is a real active composition axis (asymmetric match, specificity, domain-validated) — overlays keyed on it diverge the substrate — but is **never defaulted**, so a catalog/query that does not use it leaves recipeDigest byte-identical to before the axis existed (verified: adding clusterType did not perturb the control-plane digest). A recipe is resolved one node role at a time (`nkdctl recipe --cluster-type operator|tenant --role control-plane|worker`); the atomic per-role recipe is the build/verify unit.
- *Verification:* `criteria.ClusterType` (`TestClusterTypeAxis`); live on `examples/layered-catalog` (operator vs tenant control-plane → distinct recipeDigests; `operator-control-plane` inherits `control-plane`, adds the CAPI controller, and overrides the substrate).

### 4.5 Resolved Recipe / Pipeline IR & recipeDigest

**NKD-FR-RC-016** — [MUST] nkdctl SHALL produce a single **resolved recipe / pipeline IR** that is the **single source of truth** for all downstream stages (emission, build, verification).

**NKD-FR-RC-017** — [MUST] nkdctl SHALL serialize the resolved IR with **deterministic YAML marshaling** (stable key ordering, no runtime-varying fields), so identical inputs always produce byte-identical output.
- *Verification:* Serialize the same IR N times across processes; all outputs byte-identical; re-ordering equivalent input keys yields the same bytes.

**NKD-FR-RC-018** — [MUST] nkdctl SHALL compute **`recipeDigest` = sha256 of the deterministically-serialized resolved IR**, and make it available to the pipeline emitter, the expected-digests manifest, and the verify engine.
- *Verification:* Two independent runs on the same catalog+query emit the identical `recipeDigest`; any pinned-input change changes it.

**NKD-FR-RC-019** — [MUST] The resolved IR SHALL fully enumerate every build input (component set, pinned source refs, patch series, toolchain image digests, build commands, output types, and the four active criteria values) so **no downstream stage re-reads the catalog** to reproduce the build.

**NKD-FR-RC-020** — [MUST] nkdctl SHALL NOT embed environment-/host-/clock-derived values (wall-clock time, random UUIDs, absolute local paths, usernames) into the resolved IR; any per-run identity SHALL be derived deterministically from pinned inputs.

### 4.6 Declarative Catalog / SourceBuildRegistry

**NKD-FR-RC-021** — [MUST] nkdctl SHALL consume a **declarative catalog** whose `SourceBuildRegistry` expresses each buildable component as a **single YAML entry**, so adding a component requires **no nkdctl code change**.
- *Verification:* Add a well-formed component entry; nkdctl resolves and emits a pipeline node for it with no rebuild.

**NKD-FR-RC-022** — [MUST] Each `SourceBuildRegistry` entry SHALL pin at minimum:

| Field | Meaning |
|-------|---------|
| upstream repo | Canonical source repository URL. |
| tag → full commit SHA | Human tag resolved to a full-length commit SHA (the authoritative pin). |
| expected tree hash | Expected source tree hash for the pinned commit. |
| patch series | Ordered, content-addressed patches applied before build (may be empty). |
| build command | The component's own canonical upstream build invocation. |
| toolchain image digest | Toolchain OCI image pinned **by digest** (not tag). |
| CGO flag | Whether CGO is used (e.g. runc's static musl+libseccomp lane) or `CGO_ENABLED=0`. |
| output type | Emitted artifact class (host package, OCI image, disk image, etc.). |

- *Verification:* Schema validation rejects an entry missing any required field; a fully-pinned entry resolves.

**NKD-FR-RC-023** — [MUST] nkdctl SHALL fail resolution closed on any **unpinned source reference** — a branch or floating tag without a resolved full commit SHA, or a component lacking an expected tree hash.
- *Verification:* An entry with `ref: main` (no commit SHA) yields a non-zero coded error naming the component.

**NKD-FR-RC-024** — [MUST] nkdctl SHALL fail resolution closed when a **toolchain image is referenced by tag rather than by digest**.
- *Verification:* `toolchainImage: ...:latest` fails; a `@sha256:` digest-pinned entry passes.

**NKD-FR-RC-025** — [MUST] The `SourceBuildRegistry` schema SHALL support a per-entry `outputType` from the pluggable format set (host package, control-plane OCI image, bootable disk image, raw, tarball) without hardcoding a single format, and SHALL support the `CGO` build-lane distinction (default `CGO_ENABLED=0` vs. an explicit static-CGO lane).

**NKD-FR-RC-026** — [MUST] nkdctl SHALL validate that every catalog entry declares **either** a from-source buildable component **or** an explicit Tier-4 "cannot-build-from-source" exclusion, and SHALL NOT permit silent treatment of a non-from-source component as from-source; excluded components SHALL be carried as declared exceptions, never built.
- *Verification:* A component marked `fromSource: false` is recorded as an exception and never emitted as a build node; a component claiming from-source but lacking pins fails per NKD-FR-RC-023/024.

### 4.7 Catalog Portability, Authorship & `--data` Override

**NKD-FR-RC-027** — [MUST] nkdctl SHALL treat the catalog as **portable and distributable**, and SHALL allow **any operator to author or consume** a catalog for their own component set, k8s versions, base OS, CI engine, and output formats; nkdctl SHALL NOT bind resolution to any single, built-in, NVIDIA-specific catalog.
- *Verification:* Resolve using an operator-authored catalog containing none of the NVIDIA default components; resolution succeeds using only generic machinery.

**NKD-FR-RC-028** — [MUST] nkdctl SHALL accept a catalog via a **`--data` / catalog reference** override, and criteria/component values it introduces SHALL load through the registry fallback with **no code change** (per NKD-FR-RC-006).

**NKD-FR-RC-029** — [SHOULD] nkdctl SHOULD ship a **sensible default catalog** (the Tier-1 upstream OSS set, Ubuntu/apt base, GitHub Actions engine, qcow2 + ISO outputs) used only when no `--data`/catalog reference is supplied, and SHALL document these defaults as overridable, not hardcoded.

**NKD-FR-RC-030** — [SHOULD] When multiple catalog sources are provided, nkdctl SHALL apply a **deterministic, documented precedence** and SHALL fail closed on a genuinely conflicting component/recipe definition rather than silently choosing one; the precedence outcome SHALL be reflected in the resolved IR (and thus `recipeDigest`).
- *Verification:* Two catalogs defining the same component with different pins produce a coded conflict error unless an explicit override precedence is declared; the chosen pin appears in the serialized IR.

**NKD-FR-RC-031** — [MUST] nkdctl SHALL **embed its default (NKD) catalog** — the `SourceBuildRegistry` plus `base`, `overlays/`, and `mixins/` — into the compiled binary at build time via Go `//go:embed`, so the tool is fully self-contained and usable with **zero external files**. This mirrors AICR's `EmbeddedDataProvider` over its embedded `recipes.FS` (which embeds `registry.yaml`, `overlays/*.yaml`, `mixins/*.yaml`, and `components/**`).
- *Verification:* Run a default-catalog `resolve` on a machine with no catalog files on disk; resolution succeeds using only the embedded catalog.

**NKD-FR-RC-032** — [MUST] All catalog reads SHALL flow through a single **DataProvider abstraction** (mirroring AICR): the default provider serves the embedded FS; `--data <dir|ref>` supplies an alternate provider that **fully replaces** the embedded catalog (not a partial overlay in v1); resolution semantics SHALL be identical across providers, and per-provider stores SHALL be cached independently for isolation.
- *Verification:* The same query resolves identically from the embedded provider and an equivalent on-disk `--data` copy; two different `--data` catalogs do not cross-contaminate cached state.

### 4.8 Open Issues / Assumptions (Section 4)

- **Composition activation** (NKD-FR-RC-014): concrete merge/override semantics deferred until the hook is activated.
- **Catalog layering precedence** (NKD-FR-RC-030): exact rules are an open design item; v1 MAY restrict to a single effective catalog to avoid ambiguity.
- **`os` value domain:** intentionally an open, operator-choosable string in v1 (single-valued); pinning discipline applies to the resulting base build, not a closed OS enum.
- **Reserved-axis activation:** out of v1 scope; the GPU/accelerated stack is a later, separate-catalog concern and MUST NOT be inferred from `accelerator` in v1.

---

## 5. Functional Requirements — Pipeline Emission, From-Source Build & Artifact Assembly

Prefix **NKD-FR-BLD**. This section specifies turning a *resolved recipe* into a portable build pipeline, executing a hermetic from-source build, and assembling pluggable output artifacts — all against standard, generic interfaces.

### 5.1 Pipeline emission & CI-engine adapters

**NKD-FR-BLD-001** — [MUST] nkdctl SHALL compile a resolved recipe into a single, engine-neutral pipeline IR that fully describes the build DAG (nodes, per-node inputs/toolchain-image digests/commands/outputs, edges), independent of any CI engine.
- *Verification:* The IR is a pure function of the recipe (identical bytes across runs) with no engine-specific fields.

**NKD-FR-BLD-002** — [MUST] nkdctl SHALL emit the pipeline to at least: GitHub Actions (default), GitLab CI, Tekton, Argo Workflows, and a local runner, via thin per-engine adapters over the one IR.
- *Verification:* Emit for each engine; run the same build under GitHub Actions and the local runner; assert byte-identical component outputs.

**NKD-FR-BLD-003** — [MUST] Every engine adapter SHALL preserve identical build semantics (same node commands, toolchain-image digests, hermeticity controls, DAG ordering); adapters SHALL contain only engine-syntax translation, no build logic.

**NKD-FR-BLD-004** — [SHOULD] Adding a CI engine SHALL require exactly one adapter against the IR and SHALL NOT require resolver, build-logic, or artifact-assembly changes.

**NKD-FR-BLD-005** — [MUST] The default GitHub Actions emission SHALL render job-per-component (one build job per catalog component) for a legible, watchable public run, and each adapter SHOULD preserve one-node-per-component granularity where the engine supports it.

**NKD-FR-BLD-006** — [MUST] Every emitted pipeline SHALL terminate in an unconditional `nkdctl verify` node that runs regardless of upstream success and fails the pipeline on any verification failure.
- *Verification:* Emit for each engine; confirm a terminal verify node with no `if: success()`-style gating; force a digest mismatch and confirm pipeline failure. (Verify semantics: §6.)

### 5.2 Hermeticity & toolchain

**NKD-FR-BLD-010** — [MUST] Every build node SHALL execute inside an OCI toolchain container referenced by immutable `sha256` digest; a tag-referenced toolchain image SHALL fail resolution.

**NKD-FR-BLD-011** — [MUST] Each from-source build node SHALL run with `--network=none` (or engine-equivalent no-network sandbox); any network access to obtain sources/dependencies SHALL occur only in explicitly-declared fetch/vendoring steps, never during compilation.

**NKD-FR-BLD-012** — [MUST] Each build node SHALL execute with an explicit environment-variable allowlist; non-allowlisted variables SHALL be cleared, and reproducibility-relevant variables (e.g. `SOURCE_DATE_EPOCH`, `TZ`, `LC_ALL`, `GOFLAGS`, `GOOS`/`GOARCH`/`GOAMD64`, `GOTOOLCHAIN`) SHALL be set to pinned values.

**NKD-FR-BLD-013** — [MUST] The container runtime executing build nodes SHALL be an Apache-2.0 runtime (e.g. BuildKit/containerd/podman); nkdctl SHALL NOT take a copyleft or source-available (e.g. BUSL) build engine/daemon as a linked or mandatory dependency.
- *Rationale:* Apache-clean core; invoking GPL tools as subprocesses remains acceptable, a copyleft engine does not.

### 5.3 From-source component builds

**NKD-FR-BLD-020** — [MUST] For every buildable component, nkdctl SHALL compile from pinned upstream source and SHALL NOT reference or repackage a prebuilt upstream binary, container image, or chart as the delivered artifact.

**NKD-FR-BLD-021** — [MUST] Each component SHALL be built via its own canonical upstream build path (e.g. kubernetes `make`/`hack`; etcd `build.sh`; containerd `make`; runc static `make`), inside the pinned toolchain container.

**NKD-FR-BLD-022** — [MUST] Every component source SHALL be pinned to an immutable reference (commit SHA or equivalent); an unpinned ref (branch, moving tag) SHALL fail resolution.

**NKD-FR-BLD-023** — [MUST] Go components SHALL be built with the reproducible flag discipline: `CGO_ENABLED=0` (except the runc lane, NKD-FR-BLD-025), `GOFLAGS=-trimpath`, `-buildvcs=false`, `-ldflags="-buildid= -s -w"`, `GOTOOLCHAIN=local`, `GOOS`/`GOARCH`/`GOAMD64` per target, and `SOURCE_DATE_EPOCH` from the pinned commit's committer timestamp (`%ct`), using a vendored/frozen module cache with a verified `go.sum`. `-X` version stamping from the pinned SHA is permitted.

**NKD-FR-BLD-024** — [MUST] The reproducible build SHALL NOT run code generation; any generated code SHALL be produced and pinned upstream/ahead of time.

**NKD-FR-BLD-025** — [MUST] runc SHALL be built in a dedicated lane using its own pinned musl and static-libseccomp toolchain image to produce a static binary; this lane MAY set `CGO_ENABLED=1` as required, while preserving all other reproducibility controls.

**NKD-FR-BLD-026** — [MUST] The `pause` component (C source under the kubernetes build tree) SHALL be compiled from source with a pinned C toolchain image under the same hermeticity and reproducibility controls.

### 5.4 Control-plane binaries and reproducible OCI images

**NKD-FR-BLD-030** — [MUST] For control-plane components that run as container images (kube-apiserver, kube-controller-manager, kube-scheduler, kube-proxy, etcd, coredns, pause), nkdctl SHALL build the Go/C binary from source AND reproducibly assemble it into an OCI image; the resulting image digest SHALL be byte-reproducible.

**NKD-FR-BLD-031** — [MUST] nkdctl SHALL produce a reproducible base image (or `scratch`) onto which control-plane images are assembled, with clamped timestamps and deterministic layer content, and SHALL NOT pull a floating upstream base image at assembly time.

**NKD-FR-BLD-032** — [MUST] OCI image assembly SHALL be deterministic: no build timestamps other than `SOURCE_DATE_EPOCH`, no random layer/build IDs, sorted/normalized file metadata, reproducible layer ordering.

### 5.5 Host package assembly

**NKD-FR-BLD-040** — [MUST for `.deb`; WONT-v1 for `.rpm`] nkdctl SHALL assemble host-binary components (kubelet, kubeadm, kubectl, containerd, runc, CNI plugins, crictl, helm) into standard host packages consumable by any standard package repository: `.deb` by default, with `.rpm` a future target.
- *Verification:* Produce `.deb` packages; install into a clean apt repo and target rootfs; confirm standard metadata.

**NKD-FR-BLD-041** — [MUST] Produced host packages SHALL be byte-reproducible (normalized/sorted metadata, clamped mtimes, `SOURCE_DATE_EPOCH`).

**NKD-FR-BLD-042** — [SHOULD] nkdctl SHALL emit packages unsigned-at-build or signable by the operator's own repository signing key (e.g. apt GPG), and SHALL NOT embed or require an NVIDIA-specific signing identity for host-package repositories.

### 5.6 Reproducible base rootfs / OS layer

**NKD-FR-BLD-050** — [MUST] nkdctl SHALL build the base OS rootfs reproducibly and SHALL make the base OS operator-choosable, with Ubuntu/apt a default and no hardcoded requirement on any specific distro.

**NKD-FR-BLD-051** — [MUST] The Debian/Ubuntu-family base rootfs SHALL be built with `mmdebstrap` against a single date-pinned package-snapshot timestamp, under `SOURCE_DATE_EPOCH`, `TZ=UTC`, `LC_ALL=C`.

**NKD-FR-BLD-052** — [MUST] The base rootfs SHALL be normalized for reproducibility: sorted dpkg metadata, clamped mtimes, empty `machine-id`, first-boot-deferred host SSH keys.

**NKD-FR-BLD-053** — [MUST] Rootfs and image tooling SHALL be invoked as permissively-licensed or acceptable-to-invoke subprocess tools (e.g. pinned-container `xorriso`, `mksquashfs -all-time`, `mke2fs -U`, fixed GPT GUIDs); nkdctl SHALL NOT take a BUSL/source-available imaging tool (e.g. HashiCorp Packer) as a core dependency. mkosi MAY be offered as an option but SHALL NOT be a mandatory core dependency (LGPL).

### 5.7 Pluggable bootable disk-image assembly

**NKD-FR-BLD-060** — [MUST] nkdctl SHALL support pluggable output formats; format selection SHALL be a recipe/config input, not a hardcoded assumption. Per NKD v1 §8.3.1, **RAW is the canonical bootable-disk artifact** and ISO is canonical (its own boundary); **qcow2/VMDK/VHD are derived delivery envelopes** produced by a declared, pinned, deterministic conversion of the reproduced RAW image and verified by the decoded logical-disk digest, never by the container-file bytes. tarball is also supported.

**NKD-FR-BLD-061** — [MUST] nkdctl SHALL assemble disk images for two logical node SKUs — control-plane and worker — driven by the `role` criterion.

**NKD-FR-BLD-062** — [MUST] The boot/provisioning path (PXE / iPXE / HTTP boot / installer) SHALL be operator-configurable and SHALL NOT be baked into the image as a fixed assumption.

**NKD-FR-BLD-063** — [MUST] The byte-reproducibility guarantee for bootable disk images SHALL attach to the **canonical boundary of each format** (NKD v1 §8.3.1): for a RAW disk, the SHA-256 over the complete **logical LBA stream `[0, virtual_size)`** (sparse holes read as zero), using deterministic tooling (fixed GPT GUIDs, `mke2fs -U`, `mksquashfs -all-time`, pinned `xorriso`); for ISO, the exact unsigned ISO bytes plus a content-tree manifest. **qcow2/VMDK/VHD SHALL NOT be byte-reproduced as container files** — a derived envelope reproduces correctly iff its **decoded** block stream equals the reproduced RAW digest; a conversion-step mismatch is a tooling defect (re-run the conversion), not a reproduction failure, and SHALL NOT enter VariantReview. *(nkdctl computes the RAW logical-disk digest and a per-block manifest — see NKD-FR-REP-041.)*

**NKD-FR-BLD-064** — [SHOULD] nkdctl SHALL support optional full-disk encryption (LUKS with optional TPM2 binding) as an operator-configurable feature, disabled by default.

**NKD-FR-BLD-065** — [MUST] When FDE is enabled, byte-reproducibility SHALL be asserted on the pre-encryption rootfs/disk image; the per-node encrypted layer SHALL be explicitly outside the reproducibility contract.

### 5.8 Airgap / offline vendoring

**NKD-FR-BLD-066** — [MUST] nkdctl SHALL provide an airgap/offline mode that vendors all build inputs (pinned sources, module caches, toolchain images by digest, package snapshot) into a self-contained set that can be pre-staged and built with no external network.
- *Verification:* Vendor inputs on a connected host; build the full pipeline on a network-isolated host; assert success and byte-identity to the connected build.

### 5.9 Default catalog & tiered build scope

**NKD-FR-BLD-067** — [MUST] nkdctl SHALL ship a default catalog containing the Tier-1 upstream OSS components (see **Appendix B**), each built from source at the pinned version, each a declarative catalog entry requiring no code to add.
- *Verification:* Resolve the default catalog; build all Tier-1 components from source; assert each produces its declared output form and is byte-reproducible.

**NKD-FR-BLD-068** — [MUST] nkdctl SHALL allow operators to define their own component sets via a catalog/registry (`--data` or a catalog reference) with no code change, including larger multi-language Tier-2 upstream components (e.g. Calico, OVN-Kubernetes, Kata, SPIRE) and operator/vendor-authored Tier-3 components, buildable when the operator owns the source.

**NKD-FR-BLD-069** — [MUST] nkdctl's from-source guarantee SHALL explicitly exclude Tier-4 proprietary/closed/opaque components (which an OSS from-source tool inherently cannot build); such components SHALL be marked in the catalog as out-of-band operator concerns and SHALL NOT be represented as from-source-provable artifacts.

### 5.10 Gate & fail-closed behavior

**NKD-FR-BLD-070** — [MUST] nkdctl SHALL support a within-profile bit-identity bake-off that builds the pipeline twice under the selected builder profile (varied unpinned host env) and asserts byte-identical component, image, and package outputs; a non-identity result SHALL fail the gate. *(Byte-identity is profile-bounded — NKD-NFR-002/NKD-FR-REP-030.)*

**NKD-FR-BLD-071** — [MUST] nkdctl SHALL reject unsupported or mutually-incompatible flag/format/feature combinations with a structured error and non-zero exit, and SHALL NOT silently downgrade, ignore, or substitute a default.
- *Verification:* Incompatible combinations (a reserved criterion, an FDE option on an unsupported format, a `.rpm` target in v1) each return a structured error naming the offending combination.

**NKD-FR-BLD-072** — [WONT-v1] nkdctl v1 SHALL implement baremetal artifact assembly only; cloud/CSP image building SHALL be out of v1 scope and SHALL fail closed if requested.

**NKD-FR-BLD-073** — [SHOULD] Where the pipeline builds or pins the OS kernel, the pinned kernel SHALL be reproducible under the same hermeticity and normalization controls as other components. *(Open issue: kernel-build ownership model is TBD — NKD-OI-007.)*

**NKD-FR-BLD-074** — [PARTIAL] The GPU/accelerated **build** stack SHALL be out of v1 from-source scope, but the **accelerated-runtime profile SHALL be represented declaratively via a digest-pinned AICR profile** (NKD-FR-RC-013a, NKD v1 goal 10 / §8.1) — the foundational declarative layer for validated accelerated component pairings. Accelerator axes without an AICR profile SHALL fail closed. *(Reconciled: AICR is referenced/pinned, not built from source.)*

### 5.11 Decoupled delivery: packages-first & thin base image

Realizes the two-phase consumption model of §3.5.

**NKD-FR-BLD-075** — [MUST] nkdctl SHALL support a **packages-first** delivery mode that emits the from-source substrate as installable signed host packages (`.deb` default) plus control-plane OCI images, consumable by a standard post-provision install (apt/OCI) and **decoupled from any base-OS disk image**.
- *Rationale:* Substrate versions track the cluster, not the OS image (§3.5); this is the primary consumption path for BYO-OS-style operators.
- *Verification:* On a stock/thin base OS with only the signed repo configured, install the emitted packages + pull the images at bootstrap; assert a working control-plane/worker set with no substrate baked into the base image.

**NKD-FR-BLD-076** — [MUST] Cluster-scoped software (kubeadm/Kubernetes distro packages, CNI, add-on/ops agents) SHALL be deliverable as post-provision-installable packages/images and SHALL NOT be *required* to be baked into a bootable image; baked-in inclusion SHALL be an optional, non-preferred mode.

**NKD-FR-BLD-077** — [SHOULD] When nkdctl produces a bootable base-OS image, it SHOULD produce a **thin** image (base OS + operator-declared minimal readiness set only), distinct from the cluster-software packages, rather than a monolithic "kitchen-sink" image.

**NKD-FR-BLD-078** — [SHOULD] nkdctl SHALL NOT own or require node-OS *construction* or hardening; producing the base image is optional and operator-substitutable by any means (Packer, cloud-init, Ansible, golden image). nkdctl SHALL own producing the installable from-source substrate that a ready node consumes.

**NKD-FR-BLD-079** — [MUST] The reproducibility and provenance guarantees (byte-identity, expected-digests, SBOM, verify — §6) SHALL apply to the **packages-first** artifacts (packages + OCI images) independently of whether any bootable image is produced.
- *Rationale:* An operator who consumes only packages must still get the full from-source trust model.

### 5.12 No-compile (prebuilt) acquisition lane

A deliberate, opt-in **parallel** to the from-source lane, for operators who do not require building from source. It preserves nkdctl's fail-closed, deterministic, coded-error, one-IR-many-adapters discipline while making an explicit, weaker trust claim. It does **not** replace §5.3; the default catalog remains entirely from-source.

**NKD-FR-BLD-050** — [MAY] A catalog MAY declare a component as **prebuilt** (an acquisition, not a build): a pinned `https` binary/tarball URL, or an OCI image reference pulled by digest. `nkdctl fetch` SHALL acquire each such artifact and verify it against a pinned, per-arch, content-addressed digest, failing closed on any mismatch, with **no compilation**. A component SHALL be from-source **XOR** prebuilt **XOR** a declared exclusion; declaring both from-source and prebuilt SHALL fail closed.
- *Trust model (stated honestly):* the from-source lane proves "the same pinned inputs rebuild the same bytes"; the prebuilt lane guarantees only "exactly these pinned, content-addressed bytes." The pinned artifact digest is the reproducibility anchor and is **part of `recipeDigest`** (a repin is a recipe change, exactly as a source-commit bump is). A prebuilt (all-prebuilt) manifest is **profile-independent** and SHALL carry no builder-profile digest, because content-addressed acquisition reproduces the same bytes on any host (the §6.10 builder-profile preflight does not apply to it).
- *Verification:* `nkdctl fetch/attest/verify` on `examples/operator-catalog` recipe `acme-prebuilt-node` (kubectl release binary + pause image, real dl.k8s.io/registry.k8s.io digests): download+verify passes; a tampered pin fails closed with exit 22; `--package` assembles a reproducible `.deb` (identical digest across runs); the four CI emitters lower each pull node to the same acquire+`sha256sum -c` / `docker pull @digest`.

**NKD-FR-BLD-051** — [MUST] For a prebuilt component, resolution SHALL fail closed unless a content digest (`sha256:<64-hex>`, never a tag) is pinned for the **resolved target arch**. Per-arch digests are machine-managed in the lock's `artifacts:` section (mirroring the from-source source-pin split); `nkdctl catalog pin-artifacts` resolves them (published `.sha256` sidecars for binaries; `docker manifest inspect` per-arch manifest digests for images).
- *Verification:* `TestPrebuiltMissingArchFailsClosed`, `TestPrebuiltFailClosed` (tag/short/missing digest → `CodeUnpinnedSource`), `TestLoadPrebuiltRendersRefAndMergesLock`; live `catalog pin-artifacts --all` wrote real amd64+arm64 digests.

---

## 6. Functional Requirements — Reproducibility, SBOM, Provenance & Verification

Prefix **NKD-FR-REP**. These requirements make reproducibility nkdctl's trust model. All are environment-agnostic and phrased against standard interfaces (OCI registries, sigstore/Fulcio/Rekor, CycloneDX); none depends on any NVIDIA-internal system.

### 6.1 Byte-determinism and the identity anchor

**NKD-FR-REP-001** — [MUST] Given a fully-pinned recipe (all source refs pinned by commit and all toolchain images pinned by digest) **under a selected builder profile (NKD-FR-REP-030)**, nkdctl SHALL produce build outputs whose content digests are a pure function of the recipe+profile — two independent executions of the emitted pipeline **on that profile** SHALL yield byte-identical per-component artifacts, control-plane and worker images, and the **RAW** disk image (pre-encryption; qcow2/VMDK/VHD are derived envelopes verified by the decoded logical-disk digest — NKD-FR-BLD-063).
- *Verification:* Within-profile bit-identity bake-off (NKD-FR-REP-005).

**NKD-FR-REP-002** — [MUST] nkdctl SHALL compute a `recipeDigest` equal to the SHA-256 of the deterministically-serialized resolved recipe/IR; this digest SHALL be the stable identity anchor referenced by the expected-digests manifest, the SBOM, and the `verify` engine.

**NKD-FR-REP-003** — [MUST] nkdctl SHALL serialize the resolved recipe/IR and all self-emitted machine artifacts (manifest JSON, SBOM) using deterministic marshaling (sorted keys, fixed field ordering, stable formatting, no wall-clock/random values), and SHALL NOT use non-deterministic Go map iteration order for any bytes feeding a digest, signature, or manifest.

**NKD-FR-REP-004** — [MUST] The reproducible build SHALL be executed hermetically: `--network=none` (or equivalent egress-denied sandbox), an explicit env allowlist, a vendored/frozen module cache with verified `go.sum`, and NO code generation during the reproducible build. Reproducible Go builds SHALL apply the pinned flag discipline (`CGO_ENABLED=0` except the runc CGO lane, `GOFLAGS=-trimpath`, `-buildvcs=false`, `-ldflags="-buildid= -s -w"`, `GOTOOLCHAIN=local`, `GOOS/GOARCH/GOAMD64` per target). `-X` version stamping is permitted (pure function of the pinned SHA).

**NKD-FR-REP-005** — [MUST] Before the from-source thesis is committed to a release, nkdctl's test suite SHALL include a mandatory bit-identity bake-off gate that builds the default catalog **twice within the selected builder profile** (varied build path/host env that the profile does not pin) and fails if any in-contract output digest diverges. *(Reconciled: NKD v1 §10.1 bounds byte-identity to a profile; two builds on the same profile, not two arbitrary hosts, are the gate. Divergence on a field the profile marks byte-determinative is a profile/preflight failure, not a reproduction failure.)*

### 6.2 Time, locale, and metadata normalization

**NKD-FR-REP-006** — [MUST] Every reproducible build node SHALL set `SOURCE_DATE_EPOCH` from the pinned commit's committer timestamp (`%ct`), and execute under `TZ=UTC` and `LC_ALL=C` (and `LANG=C`) so timestamps, sort order, and locale-dependent output are normalized.

**NKD-FR-REP-007** — [MUST] For the base rootfs / OS layer, nkdctl SHALL build against a single date-pinned package-snapshot timestamp under `SOURCE_DATE_EPOCH`/`TZ=UTC`/`LC_ALL=C`, then normalize the layer (sorted package-manager metadata, clamped mtimes, empty/absent machine-id, first-boot-deferred host keys).

**NKD-FR-REP-008** — [MUST] Image/disk assembly SHALL use pinned-by-digest tool containers invoked with reproducibility-preserving options (clamped/normalized timestamps, fixed filesystem UUIDs/GPT GUIDs, normalized archive ordering) so the assembled disk image (pre-encryption) is byte-reproducible.

### 6.3 Expected-digests manifest (publisher- or operator-authored, zero binaries)

**NKD-FR-REP-009** — [MUST] nkdctl SHALL compute and emit an expected-digests manifest binding a `recipeDigest` to: predicted per-component artifact SHA-256s, control-plane image SHA-256s, worker image SHA-256(s), the toolchain-image digests used, and a public-run URL for the pipeline execution that produced them.

**NKD-FR-REP-010** — [MUST] Manifest generation SHALL be usable by ANY catalog publisher AND by an operator self-attesting against their own baseline — the format and the producing command SHALL NOT assume an NVIDIA (or any single-party) publisher identity, registry, or run host.

**NKD-FR-REP-011** — [MUST] The published manifest SHALL contain no build binaries, container images, or package payloads — only digests, references, and metadata. nkdctl SHALL NOT require redistribution of any built artifact to publish a baseline.

**NKD-FR-REP-012** — [MUST] The expected-digests manifest SHALL itself be deterministically serialized so recomputing it from the same completed run yields byte-identical JSON.

### 6.4 Signing (cosign keyless, detached, configurable identity)

**NKD-FR-REP-013** — [MUST] nkdctl SHALL sign the expected-digests manifest with cosign keyless signing (Fulcio-issued certificate) and record a Rekor transparency-log inclusion proof.

**NKD-FR-REP-014** — [MUST] Signing SHALL be DETACHED — nkdctl SHALL NOT embed any signature into a built image or disk artifact; verification SHALL compare pre-signature bytes.

**NKD-FR-REP-015** — [MUST] Keyless signing SHALL be the DEFAULT, but the OIDC issuer and signing identity SHALL be configurable — nkdctl SHALL support any operator's sigstore/OIDC identity and SHALL NOT hardcode an NVIDIA-only issuer or identity.

**NKD-FR-REP-016** — [SHOULD] nkdctl SHOULD support a private-key / KMS cosign signing mode as an alternative to keyless for operators who cannot reach a public Fulcio, provided the detached-signature and byte-identity guarantees (NKD-FR-REP-014) still hold.

### 6.5 `nkdctl verify` engine and the unconditional final pipeline node

**NKD-FR-REP-017** — [MUST] nkdctl SHALL provide a `verify` operation that, given a signed expected-digests manifest and local build outputs, performs ALL of the following and FAILS CLOSED (non-zero exit) on any failure: (a) cosign-verify the manifest signature against the configured pinned OIDC issuer + identity; (b) verify Rekor inclusion; (c) recompute `recipeDigest` from the local resolved IR and assert equality; (d) diff locally-built per-component and image digests against the manifest's predictions.

**NKD-FR-REP-018** — [MUST] On a digest mismatch, `nkdctl verify` SHALL identify and name the FIRST divergent node/component (deterministically ordered) in its failure output.

**NKD-FR-REP-019** — [MUST] `verify` SHALL fail closed on ambiguous or unverifiable conditions (e.g., signature present but Rekor inclusion absent, unknown/unpinned issuer, manifest not deterministically re-serializable) rather than treating them as a pass.

**NKD-FR-REP-020** — [MUST] nkdctl SHALL emit `verify` as the UNCONDITIONAL final node of every generated pipeline, across all engine adapters, so a passing run inherently proves output-vs-manifest identity.

**NKD-FR-REP-021** — [MUST] The configured verification identity (OIDC issuer + identity, and optionally the Rekor/Fulcio endpoints) SHALL be sourced from the recipe/catalog or explicit configuration, never hardcoded to a single publisher, so operators verify against whichever publisher (or their own self-attested baseline) they trust.

### 6.6 Airgap / offline verification

**NKD-FR-REP-022** — [MUST] nkdctl SHALL provide a mandatory offline verification path: given a pre-staged/mirrored expected-digests manifest and its Rekor inclusion bundle, `nkdctl verify` SHALL complete all checks in NKD-FR-REP-017 with NO network egress to Fulcio or Rekor.

**NKD-FR-REP-023** — [MUST] nkdctl SHALL support producing and consuming a mirrorable verification bundle (manifest + signature + Rekor inclusion proof + trusted-identity/root material needed offline) suitable for pre-staging into an airgapped environment.

**NKD-FR-REP-024** — [MUST] Offline verification SHALL apply the same fail-closed semantics as online (NKD-FR-REP-019); a missing or stale mirrored Rekor bundle SHALL cause a non-zero exit, not a silent skip.

### 6.7 SBOM

**NKD-FR-REP-025** — [MUST] nkdctl SHALL emit a CycloneDX SBOM for each build, covering the built components and their pinned upstream sources and toolchain images.

**NKD-FR-REP-026** — [MUST] The SBOM SHALL be deterministically serialized: non-reproducible fields (e.g., `serialNumber`, timestamps) stripped or normalized, required identifiers derived deterministically (e.g., UUIDv5 from stable input identity), so the same build yields a byte-identical SBOM.

**NKD-FR-REP-027** — [SHOULD] The SBOM SHOULD reference the `recipeDigest` so a given SBOM is unambiguously bound to the recipe/IR that produced it.

### 6.8 Reproducibility boundary (encryption)

**NKD-FR-REP-028** — [MUST] Byte-reproducibility guarantees and all digest comparisons in this section SHALL apply to the PRE-ENCRYPTION rootfs / disk image. Any per-node encrypted layer (e.g., LUKS/TPM2 FDE) SHALL be explicitly OUTSIDE the reproducibility contract, and nkdctl SHALL document this boundary.

**NKD-FR-REP-029** — [MUST] nkdctl's emitted pipeline SHALL apply any FDE / per-node key provisioning as a distinct step that consumes the verified pre-encryption image, so verification (NKD-FR-REP-017) can run against the in-contract image before the out-of-contract encrypted layer is produced.

### 6.9 Open issues / assumptions (Section 6)

- The **two-environment bake-off** (NKD-FR-REP-005) is the enumerated project risk that could invalidate the from-source thesis; the digest-equality guarantees (NKD-FR-REP-001) assume it passes.
- The **runc CGO lane** is the known hard case; its dedicated musl + static-libseccomp toolchain image is assumed to achieve byte-identity, subject to the bake-off.
- **Kernel reproducibility** is an open risk; if the pinned kernel cannot be made byte-reproducible it becomes a documented exception analogous to the encryption boundary.
- **Airgap trust roots:** offline verification assumes the operator can pre-stage the trusted OIDC identity/root material; its offline distribution/rotation is an operator concern, not an nkdctl mechanism in v1.

### 6.10 Builder profiles & host preflight (reconciled to NKD v1 §8.2)

Byte reproduction is bounded to a **signed builder profile** — a digest-identified declaration of the host properties a reference build was produced under. *(Implemented: `pkg/nkd/profile`; `nkdctl profile show|digest|preflight`.)*

**NKD-FR-REP-030** — [MUST] nkdctl SHALL support a **builder profile**: a declaration pinning host-determinative properties (runner OS, kernel, arch, CPU features, container runtime, storage driver, virtualization, locale, timezone, clock/`SOURCE_DATE_EPOCH` policy, and toolchain digests). The profile's **SHA-256 digest is its authoritative identity**; a human label is not (NKD v1 §8.2.1).

**NKD-FR-REP-031** — [MUST] Each profile SHALL declare a **host-control class** (`operator-controlled` | `provider-controlled`); at least one `operator-controlled` profile SHALL exist for every released recipe, so a release is never reachable only through infrastructure neither the publisher nor the operator controls (NKD v1 §8.2.2).

**NKD-FR-REP-032** — [MUST] Each profile field SHALL be **hard-fail** or **advisory**; a field SHALL NOT be hard-fail unless a divergence in it has been **demonstrated to change canonical output**. Advisory divergences are recorded, not fatal.

**NKD-FR-REP-033** — [MUST] nkdctl SHALL run a **fail-closed host preflight** before resolving build inputs, comparing observed host facts to the selected profile and **stopping (non-zero exit) when a hard-fail field is absent or differs** (NKD v1 §8.2.1). All divergences SHALL be recorded for the evidence bundle.

**NKD-FR-REP-034** — [MUST] The expected-digests manifest SHALL record the **builder-profile digest** it was produced under, and `verify` SHALL treat a build under a **different** profile as a variant, not a Reproduced state (NKD v1 §10.1). *(Amended NKD-NFR-002/003, NKD-FR-BLD-070, NKD-FR-REP-005 are the profile-bounded reproducibility gates.)*

### 6.11 ValidatedVariant / controlled fallback (reconciled to NKD v1 §8.4.1)

A digest mismatch still fails closed for the **Reproduced** state; a documented host-representation nondeterminism MAY instead enter a gated variant lane. The current `pkg/nkd/variant` classifier and `--allow-variant` surface are diagnostic only: qualified verification and release authorization fail closed until an opaque semantic-equivalence receipt binds both exact artifacts, all policy evidence, and a current bounded expiry. An output-type predicate by itself is not evidence that divergent bytes differ only in representation.

**NKD-FR-REP-035** — [MUST] nkdctl SHALL classify a verify mismatch with a **stable cause-code taxonomy**: `NONDETERMINISTIC_METADATA` (the only representation cause eligible for a variant) vs the ineligible `CONTENT_MISMATCH` / `INPUT_MISMATCH` / `TOOLCHAIN_DEFECT` / `UNKNOWN`, which always fail closed.

**NKD-FR-REP-036** — [MUST] Approval SHALL be **two-tier**: a **pre-classified** cause enumerated in the validation vector with a machine-checkable predicate **auto-approves** on the operator's own infrastructure (no round trip) when the predicate holds and every gate passes; a **novel** eligible cause requires a signed exception. Anything not eligible-and-approved fails closed (`NKD-E-VARIANT-REJECTED`).

**NKD-FR-REP-037** — [MUST] An approved output SHALL receive a **distinct ValidatedVariant identity**, SHALL NOT reuse the reference identity, SHALL NOT be described as byte-reproduced, and SHALL **expire** (be rebuilt or re-approved). The default `verify` path SHALL remain fail-closed unless the variant lane is explicitly enabled.

### 6.12 Conformance, evidence bundle & verifier isolation (reconciled to NKD v1 §8.1/§8.4.2/§14)

**NKD-FR-REP-038** — [MUST] The manifest/validation-vector SHALL carry a **Kubernetes conformance claim** and a **validation-vector reference**. Reproduction and **runtime conformance are two independent gates** (NKD v1 §8.5): a byte-identical rebuild does NOT by itself assert a conformant cluster; runtime conformance of the instantiated cluster is a separate vector nkdctl records/hands off (it does not run the cluster).

**NKD-FR-REP-039** — [SHOULD] The runtime-conformance gate is an operator-run step against a cluster instantiated from the verified artifacts; nkdctl SHALL provide the machine-readable handoff (claim + vector reference) rather than owning cluster-level conformance execution.

**NKD-FR-REP-040** — [MUST] `verify` SHALL emit a **deterministic evidence bundle** recording the recipe/profile identity, conformance claim + vector reference, the preflight divergences, and a **per-component verdict with its cause code** (and the pre-classified id that approved any variant), so a mismatch is **explainable, not merely detectable** (NKD v1 §8.4.2). *(Implemented: `attest.EvidenceBundle`; `attest|verify --evidence-out`.)*

**NKD-FR-REP-041** — [MUST] For disk artifacts, the evidence/validation vector SHALL publish diagnosable granularity: the aggregate **RAW logical-disk digest**, **per-block digests** over the RAW LBA stream at a declared fixed block size, and a per-path filesystem manifest — sufficient to locate a divergence **without the reference bytes** (NKD v1 §8.4.2). *(Implemented: `assemble.LogicalDiskDigest` / `assemble.BlockManifestOf`.)*

**NKD-FR-REP-042** — [SHOULD] The **verifier SHALL be isolated** from the artifact-producing stages (an independent code path / node) so a single defect cannot forge both an artifact and its proof; the evidence bundle is the verifier's auditable, deterministically-serializable output.

---

## 7. Functional Requirements — CLI/SDK Surface & Standard-Ecosystem Interfaces

Prefix **NKD-FR-INT**. This section specifies the `nkdctl` CLI surface, the shared client/SDK facade holding all business logic, the cross-cutting engineering contracts, and interoperability against **standard, generic ecosystem interfaces**. It also specifies the environment-agnostic **handoff contract** any downstream provisioning/lifecycle system can consume, the generic CVE fast-path, and catalog authoring/publishing. All NVIDIA-internal wiring is explicitly out-of-tree (§7.9) and is not part of the OSS core.

### 7.1 SDK/Facade & CLI Architecture

**NKD-FR-INT-001** — [MUST] `nkdctl` SHALL be a thin CLI over a single reusable client/SDK facade (e.g. a public Go package `client/v1`) that holds ALL business logic; the CLI SHALL contain only argument parsing, input validation, and output formatting.
- *Verification:* CLI handlers contain no resolution/build/verify logic; a Go integration test drives the full recipe→pipeline→verify flow through the exported SDK with no CLI dependency.

**NKD-FR-INT-002** — [MUST] The SDK facade SHALL be configured exclusively via functional options (e.g. `WithData`, `WithEngine`, `WithFormats`, `WithOIDCIssuer`, `WithOIDCIdentity`, `WithAirgap`, `WithHTTPTimeout`); unset options SHALL fall back to documented defaults, never to a hardcoded environment-specific value.

**NKD-FR-INT-003** — [SHOULD] The SDK SHALL expose every CLI-reachable capability as a public method so any external Go program can embed `nkdctl` without shelling out.

**NKD-FR-INT-004** — [MUST] `nkdctl` SHALL NOT hard-depend on, link, or require at runtime any NVIDIA-internal system; all interoperability SHALL be expressed against the standard classes of interface in §7.8/§7.11.
- *Verification:* Dependency/import audit shows no NVIDIA-internal package or endpoint in the OSS core; the tool runs end-to-end against only public/standard services in CI.

### 7.2 Cross-Cutting Contracts (errors, exit codes, timeouts)

**NKD-FR-INT-005** — [MUST] All code paths SHALL return structured errors carrying a stable error code (e.g. `NotFound`, `InvalidRequest`, `Timeout`, `Conflict`, `Unavailable`, `Internal`); bare/unwrapped errors SHALL NOT cross package boundaries.

**NKD-FR-INT-006** — [MUST] The CLI SHALL map structured error codes to a documented, stable set of exit codes (e.g. `0` success; `2` invalid input/usage; `3` resolution error; `4` verification/attestation failure; `5` I/O/network/unavailable; `1` internal/unknown) so CI engines can branch on outcome without parsing text.

**NKD-FR-INT-007** — [MUST] Every I/O operation (git fetch, OCI push/pull, apt/rpm publish, sigstore calls, object-storage access, HTTP) SHALL run under a `context.Context` with an explicit, configurable timeout; no unbounded blocking call SHALL exist on any I/O path.

**NKD-FR-INT-008** — [MUST] Long-running operations (multi-component build orchestration, digest diffing) SHALL check context cancellation between units of work and abort promptly with a `Timeout`/cancelled error.

**NKD-FR-INT-009** — [MUST] Outbound HTTP clients SHALL use an explicit timeout (never a zero-timeout default client) and SHALL bound response bodies before fully reading them.

**NKD-FR-INT-010** — [SHOULD] Human-readable diagnostics SHALL go to stderr via structured logging honoring a documented log-level env var and `NO_COLOR`; machine-consumable results (digests, manifests, SBOMs, pipeline files) SHALL go to the requested output path/stdout so output is redirectable and testable.

### 7.3 CLI Subcommand Surface

**NKD-FR-INT-011** — [MUST] `nkdctl` SHALL provide the following subcommands, each a thin adapter over the SDK: `recipe`/`resolve`, `query`, `bundle`, `build` (optional local hermetic run), `sbom`, `attest`, `verify`, `diff`, `catalog` (a.k.a. `registry`: `list`/`validate`/`author`/`publish`), and `trust`.

**NKD-FR-INT-012** — [MUST] Every subcommand SHALL accept a `--data <catalogRef>` (or equivalent) to load an operator-authored catalog, and SHALL operate identically regardless of catalog publisher (NVIDIA, a distro, or the operator themselves).

### 7.4 Resolution & Query (`recipe`/`resolve`, `query`)

**NKD-FR-INT-013** — [MUST] `nkdctl recipe`/`resolve` SHALL resolve criteria (role, k8sVersion, os, arch) against the loaded catalog into a fully-pinned resolved recipe / pipeline IR, using asymmetric-match semantics (`any`/`""` = wildcard; extra query fields still match; a recipe constraining a field the query omits does not match; ties broken by specificity).

**NKD-FR-INT-014** — [MUST] Resolution SHALL fail closed with an `InvalidRequest` error when a **hard-reserved** axis (`tier`, `compliance`) carries a non-empty value, or when an **AICR-governed** axis (`accelerator`, `service`, `intent`, `platform`) is given a non-empty value **without a matching recipe that declares an AICR profile** (NKD-FR-RC-013a); reporting "not supported in v1" / "requires an AICR profile". A reserved axis SHALL NEVER be treated as a silent wildcard.

**NKD-FR-INT-015** — [MUST] Resolution SHALL fail closed when any source ref is unpinned (not a git SHA) or any toolchain image is referenced by tag rather than digest.

**NKD-FR-INT-016** — [SHOULD] `nkdctl query` SHALL return a single hydrated value from a resolved recipe addressed by a selector path (e.g. a component's pinned version or image digest) for scripting and CI assertions.

**NKD-FR-INT-017** — [MUST] The resolved recipe and pipeline IR SHALL be serialized with deterministic YAML/JSON marshaling so `recipeDigest = sha256(deterministic-serialization)` is a pure function of inputs.

### 7.5 Bundle / Pipeline Emission (`bundle --engine/--format/--airgap`)

**NKD-FR-INT-018** — [MUST] `nkdctl bundle` SHALL emit a portable **build pipeline** (not a Packer/image config) from a resolved recipe: one substrate-neutral pipeline IR rendered to the target engine via a thin per-engine adapter. The recipe alone SHALL NOT be a usable artifact — the recipe→pipeline→artifact conversion produces it.

**NKD-FR-INT-019** — [MUST] `bundle --engine <e>` SHALL support at minimum GitHub Actions (primary), GitLab CI, Tekton, Argo Workflows, and a local runner, all rendered from the same pipeline IR; adding an engine SHALL require only a new adapter.

**NKD-FR-INT-020** — [MUST] `bundle --format <f>` SHALL support pluggable, provisioning-agnostic output formats: host packages (`.deb` default; `.rpm` a future target), control-plane OCI images, and bootable disk images where **RAW is the canonical disk artifact** and **qcow2/VMDK/VHD are derived envelopes** (decoded-logical-disk-verified, NKD v1 §8.3.1); ISO is canonical; tarball supported. Formats SHALL be selectable per invocation with none hardcoded.

**NKD-FR-INT-021** — [MUST] The emitted pipeline SHALL express one job per component (legible, watchable public run) and SHALL terminate in an unconditional final `verify` node (§7.7).

**NKD-FR-INT-022** — [MUST] `bundle --airgap` SHALL emit-only a pipeline plus a mirrorable bundle manifest suitable for disconnected execution (offline verification inputs staged; no reliance on live Fulcio/Rekor egress at run time).

**NKD-FR-INT-023** — [MUST] The emitted pipeline SHALL run each component build hermetically (`--network=none` plus an explicit env allowlist) against pinned-by-digest toolchain images, so the CI engine only orchestrates jobs `nkdctl` defines.

### 7.6 Local Build, SBOM, Attest (`build`, `sbom`, `attest`)

**NKD-FR-INT-024** — [SHOULD] `nkdctl build` SHALL optionally execute the emitted pipeline locally in the same hermetic manner as the CI engine would, producing byte-identical artifacts to a CI run of the same recipe.

**NKD-FR-INT-025** — [MUST] `nkdctl sbom` SHALL emit a deterministic CycloneDX SBOM (stable serialization: omit/normalize `serialNumber` and timestamps, derive a UUIDv5 from input identity).

**NKD-FR-INT-026** — [MUST] `nkdctl attest` SHALL produce the signed **expected-digests manifest** — `recipeDigest` → predicted per-component + control-plane + worker image sha256s + toolchain-image digests + public-run URL — and sign it with cosign **keyless** (Fulcio cert + Rekor inclusion), **detached** (never embedding a signature into any image).

**NKD-FR-INT-027** — [MUST] `attest` SHALL accept a configurable OIDC issuer/identity so any operator's sigstore/OIDC identity can sign; the tool SHALL NOT hardcode an NVIDIA-only identity.

### 7.7 Verification (`verify` — artifact + node)

**NKD-FR-INT-028** — [MUST] `nkdctl verify` SHALL: (a) cosign-verify the expected-digests manifest against a **configurable pinned OIDC issuer + identity** and Rekor inclusion, failing closed; (b) recompute `recipeDigest` and assert equality; (c) diff locally-produced component + image digests against the manifest; and exit non-zero naming the first divergent node on any mismatch.

**NKD-FR-INT-029** — [MUST] `verify` SHALL be usable in two modes: as an operator-run **artifact** check (CLI) and as the unconditional final **node** of every emitted pipeline.

**NKD-FR-INT-030** — [MUST] `nkdctl verify` SHALL provide a MANDATORY fully-offline path that validates a pre-staged/mirrored expected-digests manifest + Rekor bundle with no live Fulcio/Rekor egress.

**NKD-FR-INT-031** — [MUST] `verify` SHALL scope byte-identity assertions to the **pre-encryption** rootfs/disk; any per-node FDE (LUKS/TPM2) layer SHALL be explicitly excluded from the reproducibility contract and SHALL NOT cause a verification failure.

**NKD-FR-INT-032** — [MUST] `nkdctl verify` SHALL allow one operator to verify another party's build bit-for-bit given only the small signed manifest — never requiring receipt of any binary artifact.

### 7.8 Standard-Ecosystem Interoperability

All interoperability targets a standard CLASS of interface; no requirement names an NVIDIA-internal product.

**NKD-FR-INT-033** — [MUST] Host packages SHALL be emitted in a form directly consumable by ANY standard apt/deb repository (`.deb` default), structured so `.rpm` for any standard yum/dnf repository can be added as a future format without core changes. `nkdctl` SHALL build the packages and SHALL NOT redistribute upstream binaries.

**NKD-FR-INT-034** — [MUST] Control-plane OCI images and optional artifacts SHALL be publishable to ANY OCI-compliant registry, including attaching the expected-digests manifest, SBOM, and signature as OCI **referrers**, using standard registry auth.

**NKD-FR-INT-035** — [MUST] Bootable disk images SHALL be producible for standard provisioning paths — PXE / iPXE / HTTP boot and installer/live ISO — with the exact boot/provisioning path OPERATOR-CONFIGURABLE and never a baked-in assumption.

**NKD-FR-INT-036** — [MUST] Signing and verification SHALL use standard sigstore (cosign / Fulcio / Rekor) with keyless as the DEFAULT and any operator's OIDC identity supported; no proprietary signing service SHALL be required.

**NKD-FR-INT-037** — [MUST] The base OS layer SHALL be operator-choosable (Ubuntu/apt the shipped DEFAULT, not a requirement); CI engine, output formats, and boot path SHALL likewise be defaults, never hardcoded.

**NKD-FR-INT-052** — [MUST] nkdctl's package and image outputs SHALL be consumable by a generic **two-phase** node-readiness + post-provision-install model (§3.5): installable at bootstrap from a standard signed apt/OCI source by ANY lifecycle/provisioning system, independent of how the base OS reached readiness, with no nkdctl-specific runtime dependency on the node or the installer.

### 7.9 Downstream Handoff Contract & CVE Fast-Path

**NKD-FR-INT-038** — [MUST] `nkdctl` SHALL emit a generic, documented, machine-readable **handoff contract** — `{ recipe, image-digest(s), minimal-diff, SBOM, expected-digests manifest }` — that ANY downstream provisioning or lifecycle/rollout system can consume via the standard interfaces of §7.8, with no `nkdctl`-specific runtime dependency on the consumer.

**NKD-FR-INT-039** — [MUST] `nkdctl diff` SHALL compute a minimal, reproducible diff between two resolved recipes (and their expected-digests), suitable as the `minimal-diff` input to an immutable node-replacement rollout.

**NKD-FR-INT-040** — [MUST] `nkdctl` SHALL support a generic CVE fast-path fold-back: a lifecycle system MAY live-patch a node in an emergency, and the fix SHALL fold into the next recipe as a minimal-diff reproducible rebuild such that the next node-replacement restores `image == recipe`. `nkdctl` SHALL provide the recipe-diff/rebuild/verify half of this loop; live-patching is the consumer's concern.

**NKD-FR-INT-041** — [MUST] Any NVIDIA-internal (or operator-internal) wiring to a specific downstream system SHALL be an out-of-tree/private adapter over the §7.8 standard interfaces and SHALL NOT be part of the OSS core.

### 7.10 Catalog Authoring & Publishing (`catalog`/`registry`, `trust`)

**NKD-FR-INT-042** — [MUST] `nkdctl catalog list` SHALL enumerate the components, pinned source refs, and toolchain-image digests of a loaded catalog.

**NKD-FR-INT-043** — [MUST] `nkdctl catalog validate` SHALL statically validate a catalog and fail closed on any unpinned source ref, tag-not-digest toolchain reference, or missing required component field, before it is used to resolve.

**NKD-FR-INT-044** — [MUST] `nkdctl catalog author` SHALL let an operator declaratively add or edit a buildable component via a single catalog/registry YAML entry with NO code change (declarative SourceBuildRegistry).

**NKD-FR-INT-045** — [MUST] `nkdctl catalog publish` SHALL let any operator (or NVIDIA) publish their own signed catalog + expected-digests manifest via the standard interfaces of §7.8; `nkdctl` SHALL consume ANY such catalog via `--data` or a catalog reference with identical behavior.

**NKD-FR-INT-046** — [MUST] `nkdctl trust` SHALL manage the configurable set of trusted OIDC issuer(s)+identity(ies) used by `verify`/`attest`, so an operator can pin, add, or self-attest against their own baseline.

**NKD-FR-INT-047** — [MUST] Each criteria `Parse*` (role, k8sVersion, os, arch) SHALL use a built-in fast-path plus a registry fallback seeded from the loaded catalog / `--data`, so operator-defined axis values load with no code change; the `arch` enum SHALL be typed so `arm64` is purely additive to the v1 `amd64`-only set.

### 7.11 External Interface Inventory & Constraints

**NKD-FR-INT-048** — [MUST] The tool's external interfaces SHALL be limited to standard, generic services: git source fetch **by SHA**, OCI registries (incl. referrers), apt/rpm repositories, sigstore (Fulcio/Rekor), and object storage; each SHALL be reachable via standard clients/credentials with configurable endpoints.

**NKD-FR-INT-049** — [MUST] Git source fetches SHALL pin by commit SHA and derive `SOURCE_DATE_EPOCH` from the pinned commit; a branch/tag-only ref SHALL be rejected at resolution (per §7.4).

**NKD-FR-INT-050** — [MUST] `nkdctl`'s own linked dependencies SHALL be permissively licensed (Apache/BSD/MIT); invoking GPL build tools as subprocesses is permitted, but no copyleft-licensed daemon/engine and no BUSL/source-available tool (e.g. HashiCorp Packer) SHALL be a linked or required core dependency.

**NKD-FR-INT-051** — [SHOULD] Every external-interface client SHALL surface failures as structured, coded errors mapped to the exit-code table of §7.2, so a CI engine can distinguish an input error (fail the recipe) from a transient `Unavailable`/`Timeout` (retryable).

### 7.12 Assumptions & Open Issues (Section 7)

- **Bake-off gate:** the byte-identity guarantees behind `build`/`verify` (INT-024, INT-028) assume the mandatory two-environment bake-off validates the from-source thesis (NKD-OI-001).
- **Airgap Rekor mirroring** (INT-030): the exact staging format of the offline Rekor bundle is TBD (NKD-OI-004).
- **Boot/format defaults** (INT-020/INT-035): ISO vs qcow2 vs raw and PXE/iPXE/HTTP/installer stay pluggable; per-environment defaults are unspecified pending operator input (NKD-OI-002).
- **`.rpm` and `arm64`** (INT-033, INT-047): additive future targets typed into the surface now but WONT-v1 for shipped support.
- **FDE boundary** (INT-031): the encrypted per-node layer is intentionally outside the `verify` contract.

---

## 8. Non-Functional Requirements

Prefix **NKD-NFR**. These constrain *how well* nkdctl performs, grounded in the reproducibility-as-trust-model thesis and the OSS, environment-agnostic positioning.

### 8.1 Reproducibility & Determinism

**NKD-NFR-001** — [MUST] For a fully-pinned recipe **under a selected builder profile**, nkdctl SHALL produce byte-identical output artifacts (host packages, control-plane OCI images, base image, **RAW** disk image pre-encryption) across repeated builds on that profile.

**NKD-NFR-002** — [MUST] nkdctl SHALL demonstrate bit-identity of all in-contract artifacts across **two independent builds within a selected builder profile** (differing build path, wall-clock, and any host property the profile does NOT pin). Byte-identity is **bounded to the profile** (NKD v1 §10.1): a divergence on a host property the profile marks byte-determinative is a preflight/profile failure (or a variant), not a claimed reproduction. *(Reconciled from the prior host-independent "cross-environment" claim.)*

**NKD-NFR-003** — [MUST] A within-profile bit-identity **bake-off spike SHALL be completed and pass as a mandatory gate before the from-source thesis is committed** for v1. A **two-machine** run on the SAME operator-controlled profile SHOULD be included to falsify unpinned host dependence; a field it surfaces as byte-determinative SHALL be added to the profile as hard-fail (NKD-FR-REP-032).

**NKD-NFR-004** — [MUST] nkdctl SHALL execute each component build with the network disabled (`--network=none`) and an explicit environment allowlist, sourcing all inputs from pinned-by-digest OCI toolchain containers and a vendored/frozen, `go.sum`-verified module cache.

**NKD-NFR-005** — [MUST] nkdctl SHALL apply reproducible Go build discipline for every Go component: `CGO_ENABLED=0` (except the runc lane), `GOFLAGS=-trimpath`, `-buildvcs=false`, `-ldflags="-buildid= -s -w"`, `GOTOOLCHAIN=local`, explicit `GOOS/GOARCH/GOAMD64`, and `SOURCE_DATE_EPOCH` from the pinned commit's `%ct`.

**NKD-NFR-006** — [MUST] The runc CGO component SHALL be built in a dedicated lane using its own pinned musl + static-libseccomp toolchain image and produce a byte-reproducible static binary.

**NKD-NFR-007** — [MUST] nkdctl SHALL NOT run code generation during the reproducible build; any codegen SHALL be pre-materialized and pinned as source input.

**NKD-NFR-008** — [MUST] nkdctl SHALL build the base OS rootfs against a single **date-pinned package-snapshot timestamp** under `SOURCE_DATE_EPOCH`, `TZ=UTC`, `LC_ALL=C`, and normalize the result (sorted dpkg metadata, clamped mtimes, empty machine-id, first-boot-deferred host keys).

**NKD-NFR-009** — [MUST] Disk-image assembly SHALL use pinned-container image tools with reproducible options (e.g. `xorriso`, `mksquashfs -all-time`, `mke2fs -U <fixed>`, fixed GPT GUIDs) so the **pre-encryption RAW** disk image is byte-reproducible on the canonical (logical-LBA) boundary. A qcow2/VMDK/VHD envelope is a deterministic post-reproduction conversion, verified by its decoded logical-disk digest (NKD-FR-BLD-063).

**NKD-NFR-010** — [MUST] The reproducibility contract SHALL apply to the **pre-encryption rootfs/disk only**; any per-node FDE layer (LUKS/TPM2) SHALL be explicitly excluded from byte-identity guarantees.

**NKD-NFR-011** — [MUST] nkdctl SHALL serialize the resolved recipe / pipeline IR and all emitted manifests (expected-digests, SBOM) using deterministic YAML/JSON marshaling, so identical inputs yield identical bytes.

### 8.2 Security & Supply-Chain

**NKD-NFR-020** — [MUST] `nkdctl verify` SHALL **fail closed**: cosign-verify the expected-digests manifest against a configurable pinned OIDC issuer + identity with Rekor inclusion proof, recompute `recipeDigest` and assert equality, and diff local component/image digests against the manifest — exiting non-zero and naming the first divergent node on any mismatch or missing proof.

**NKD-NFR-021** — [MUST] Verification SHALL be the unconditional final node of every emitted pipeline, regardless of target engine.

**NKD-NFR-022** — [MUST] Signatures SHALL be **detached** and SHALL NOT be embedded into any artifact whose bytes are covered by the reproducibility contract.

**NKD-NFR-023** — [MUST] No secrets, credentials, tokens, or signing keys SHALL appear in any byte-reproducible artifact or in the environment allowlist exposed to the hermetic build.

**NKD-NFR-024** — [MUST] Signing SHALL default to cosign **keyless** (Fulcio certificate + Rekor inclusion) while supporting any operator's sigstore instance and OIDC identity; nkdctl SHALL NOT hardcode an NVIDIA-specific issuer.

**NKD-NFR-025** — [MUST] nkdctl SHALL emit deterministic **CycloneDX SBOM** output with volatile fields normalized (strip `serialNumber`/timestamp, derive UUIDv5 from input identity).

**NKD-NFR-026** — [SHOULD] Emitted SBOM and image artifacts SHALL be consumable by standard CVE-scanning tooling (e.g. Grype/Trivy-class) without nkdctl-specific plugins.

**NKD-NFR-027** — [MUST] Every buildable component in a catalog SHALL be pinned by immutable digest or commit; an unpinned source ref or a tag-not-digest toolchain reference SHALL fail resolution.

**NKD-NFR-028** — [MUST] All nkdctl I/O operations SHALL be bounded by a context timeout, and errors SHALL be structured with codes (mirroring the AICR `pkg/errors` pattern).

### 8.3 Open-Source & Licensing

**NKD-NFR-040** — [MUST] nkdctl SHALL be released as a public open-source project under **Apache-2.0**.

**NKD-NFR-041** — [MUST] Every dependency **linked into** the nkdctl binary SHALL be permissively licensed (Apache-2.0 / BSD / MIT). nkdctl SHALL NOT link a copyleft (GPL/LGPL) library. *(Excludes Nix (LGPL-2.1) and Guix (GPL-3.0) as linked engines.)*

**NKD-NFR-042** — [MAY] nkdctl MAY invoke GPL/copyleft build tools (e.g. `go`, `gcc`, `dpkg`, `xorriso`, `mmdebstrap`) as **subprocesses**; such invocation SHALL NOT create a linking or derivative-work relationship.

**NKD-NFR-043** — [MUST] nkdctl SHALL NOT depend on a copyleft build **engine/daemon** as its mandated substrate, and SHALL NOT depend on **BUSL-1.1 HashiCorp Packer** (or any source-available/non-OSI-approved tool) as a core component. *(The substrate is a hand-rolled hermetic pipeline over Apache-2.0 runtimes; mkosi (LGPL) and Packer (BUSL) are excluded from the core.)*

**NKD-NFR-044** — [MUST] nkdctl SHALL NOT depend on, require, or embed any closed-source or NVIDIA-internal system; it SHALL be installable and runnable by an external operator using only standard, publicly available tools.

**NKD-NFR-045** — [SHOULD] The project SHALL adopt a documented contribution governance model (Apache-2.0 with DCO and/or CLA) that accepts community-authored **catalog** contributions.

### 8.4 Performance & Scale

**NKD-NFR-060** — [MUST] nkdctl SHALL emit a **job-per-component** pipeline so independent components build in parallel on the operator's CI engine.

**NKD-NFR-061** — [MUST] An operator SHALL be permitted to cache and reuse **their own rebuilt bytes** (component outputs, toolchain layers) across pipeline runs; such caching of self-built artifacts is not redistribution and SHALL NOT weaken the reproducibility or from-source guarantees.

**NKD-NFR-062** — [SHOULD] The v1 default Tier-1 catalog (~15 component entries / ~7 image targets + base image) SHALL build within a documented wall-clock budget on a reference CI runner, with the budget published per target.

**NKD-NFR-063** — [SHOULD] Resolve, emit, and verify operations (excluding the actual compile jobs) SHALL complete in seconds on a developer workstation for the default catalog.

### 8.5 Airgap / Disconnected Operation

**NKD-NFR-070** — [MUST] nkdctl SHALL provide a **mandatory offline cosign-verify path** using a pre-staged/mirrored expected-digests manifest and Rekor bundle, requiring no live Fulcio/Rekor egress.

**NKD-NFR-071** — [MUST] All inputs required to run a pipeline (toolchain containers by digest, source refs, package snapshot) SHALL be enumerable so an operator can pre-mirror them into an airgapped environment.

**NKD-NFR-072** — [MUST] The reproducible build itself SHALL execute with the network disabled, confirming no runtime dependency on connected registries or repositories.

### 8.6 Observability

**NKD-NFR-080** — [MUST] The emitted pipeline SHALL present a **legible, watchable public run**: one clearly-named node per component so any observer can follow a catalog publisher's public build in real time.

**NKD-NFR-081** — [MUST] nkdctl SHALL emit **structured logs** (mirroring AICR's `slog` pattern) for all operations, without writing user-facing output to stdout in a way that breaks scripting.

**NKD-NFR-082** — [MUST] The expected-digests manifest SHALL record the **public-run URL** alongside predicted digests so verifiers can trace a digest back to its watchable build.

**NKD-NFR-083** — [MUST] On verification failure, nkdctl SHALL name the **first divergent node** (component/image/recipe) to make the discrepancy diagnosable.

### 8.7 Portability & Extensibility

**NKD-NFR-090** — [MUST] nkdctl SHALL be environment-agnostic: it SHALL NOT bake in any specific OCI registry, OS base, CI engine, or boot/provisioning path, treating Ubuntu/apt, GitHub Actions, and qcow2+ISO strictly as overridable **defaults**.

**NKD-NFR-091** — [MUST] Adding a new buildable component SHALL require **only one declarative catalog/registry YAML entry** and no nkdctl code change.

**NKD-NFR-092** — [MUST] Supporting a new CI engine or output format SHALL require **only one thin adapter** over the single substrate-neutral pipeline / artifact model, with no change to the core resolver or reproducible-build engine.

**NKD-NFR-093** — [MUST] The criteria taxonomy SHALL be typed and extensible so a reserved axis value (e.g. `arch=arm64`) becomes additive without breaking existing recipes.

**NKD-NFR-094** — [MUST] Operator-defined criteria values SHALL load from a catalog / `--data` source with no code change, via a built-in fast-path plus registry fallback per `Parse*` axis.

---

## 9. Constraints & Assumptions

Prefix **NKD-CON**. These bound the solution space and record what the SRD assumes about the operator's environment. Constraints marked as assumptions are not independently verified by nkdctl; they frame the requirements above.

| ID | Constraint / Assumption | MoSCoW |
|----|-------------------------|--------|
| **NKD-CON-001** | nkdctl SHALL NOT assume any NVIDIA-internal system (publisher, node-images, Forge, NodeWright, nke-k8s-distro, site-workflow-engine, packages/builder) exists or is reachable. These are non-normative background only. | MUST |
| **NKD-CON-002** | The operator SHALL be assumed to bring their **own** OCI registry, CI engine, OS base, provisioning/boot path, and cluster lifecycle system; nkdctl integrates only against the standard *class* of each interface. | MUST |
| **NKD-CON-003** | Ubuntu/apt, GitHub Actions, and qcow2+ISO SHALL be treated as shipped **defaults, not requirements**; the tool hardcodes none of them (see NKD-NFR-090). | MUST |
| **NKD-CON-004** | v1 SHALL target **amd64 only**; arm64 is reserved as an additive typed-enum value, not delivered in v1. | WONT-v1 |
| **NKD-CON-005** | v1 SHALL be **baremetal-first**; CSP/cloud image building is out of v1 scope (§10). | MUST |
| **NKD-CON-006** | The v1 default component set SHALL be **Go-heavy Tier-1 upstream OSS**, built via each project's canonical upstream path (kubernetes make/hack; etcd build.sh; containerd make; runc make static). | MUST |
| **NKD-CON-007** | nkdctl SHALL use kubernetes' own **make/hack** build path and SHALL NOT depend on Bazel (Kubernetes deleted its Bazel build). | MUST |
| **NKD-CON-008** | The control plane SHALL be delivered as **container images** (kube-apiserver/cm/scheduler/proxy, etcd, coredns, pause), built as reproducible OCI images layered on the reproducible base image and host-binary packages — not host binaries. | MUST |
| **NKD-CON-009** | As an OSS from-source tool, nkdctl **inherently cannot build closed-source components** (Tier 4). Such components are the operator's out-of-band concern and are explicitly excluded from the from-source guarantee. | MUST |
| **NKD-CON-010** | Reproducibility guarantees SHALL hold only for **fully-pinned** recipes; any unpinned input is a resolution error, not a degraded-reproducibility mode (see NKD-NFR-027). | MUST |
| **NKD-CON-011** | The chosen substrate SHALL be a **hand-rolled hermetic pipeline over pinned-by-digest OCI toolchain containers** with Apache-2.0 runtimes (BuildKit/containerd/podman); Nix, Guix, Bazel-as-mandated-engine, apko/melange (apk/musl), mkosi (LGPL), and Packer (BUSL) are excluded as the core substrate. | MUST |
| **NKD-CON-012** | Base-OS reproducibility SHALL depend on the operator selecting a **date-pinned package snapshot**; if the chosen distro provides no snapshot service, base-layer bit-identity cannot be guaranteed (operator responsibility). | SHOULD |
| **NKD-CON-013** | Byte-reproducibility SHALL hold only for the **pre-encryption** rootfs/disk; per-node FDE layers are out of contract (see NKD-NFR-010). | MUST |
| **NKD-CON-014** | The kernel SHALL be an operator-owned input; its pinning and reproducibility are the operator's responsibility and an open risk for v1 (see §11). | SHOULD |
| **NKD-CON-015** | nkdctl SHALL assume standard, generic downstream interfaces only — any OCI registry, any apt/deb (or future rpm) repo, any CI engine, standard PXE/iPXE/HTTP-boot + disk-image provisioning, standard sigstore — and SHALL NOT be designed around any specific product. | MUST |
| **NKD-CON-016** | GPU/accelerated/driver stacks are **not built from source** in v1, but the accelerated-runtime profile is a first-class **declarative** concern: a recipe MAY pin a digest-identified **AICR profile** (NKD-FR-RC-013a) as the foundational layer for validated component pairings (NKD v1 goal 10 / §8.1). Building AICR's runtime from source remains out of v1. | PARTIAL |
| **NKD-CON-017** | Base/overlay/mixin composition SHALL be retained as a resolver **hook** only; v1 recipes ship **flat**. | SHOULD |
| **NKD-CON-018** | Hard-reserved axes (`tier`, `compliance`) SHALL **fail closed** in v1. The AICR-governed axes (`accelerator`, `service`, `intent`, `platform`) fail closed **unless** the matched recipe declares a digest-pinned AICR profile (NKD-FR-RC-013a); a non-empty value is never a silent wildcard. | MUST |
| **NKD-CON-019** | nkdctl SHALL NOT own node-OS **construction** or hardening; the operator owns bringing the base OS to readiness (Phase 1) by any means, and nkdctl owns producing the installable from-source substrate (Phase 2). See §3.5. | MUST |
| **NKD-CON-020** | The Kubernetes substrate SHALL be delivered as **post-provision-installable** packages/images versioned with the cluster, and SHALL NOT be *required* to be baked into the base OS image (packages-first; see NKD-FR-BLD-075/076). | MUST |

---

## 10. Out of Scope (v1)

The following are explicitly excluded from nkdctl v1. Exclusion is deliberate scope control ("small finite scope; expand under pressure"), not a statement of infeasibility.

| Item | Disposition |
|------|-------------|
| **GPU / accelerated / driver stack** (CUDA, GPU operator, accelerator drivers) **built from source** | Building the accelerated stack from source is deferred. The accelerated-runtime **profile is in scope declaratively** via a digest-pinned **AICR profile** (NKD-FR-RC-013a); the AICR-governed axes activate under it and fail closed without it. [PARTIAL] |
| **CSP / cloud image building** (AMI, GCE, Azure managed images) | Baremetal-first v1; cloud image formats out of scope. [WONT-v1] |
| **NixOS / Guix as build substrate** | Rejected on licensing (LGPL-2.1 / GPL-3.0 copyleft engine) — see NKD-NFR-041/043. [WONT-v1] |
| **Bazel-based k8s build** | Rejected; kubernetes deleted its Bazel build. Use make/hack (NKD-CON-007). [WONT-v1] |
| **apko / melange** as substrate | Rejected; apk/Wolfi/musl ecosystem fights the common Ubuntu/apt/glibc target. [WONT-v1] |
| **mkosi / Packer as core** | Rejected; mkosi is LGPL, Packer is BUSL-1.1 (NKD-NFR-043). [WONT-v1] |
| **Building AICR's own runtime from source** | AICR is the conceptual model, not a v1 build target. [WONT-v1] |
| **Tier-2 components** (Calico, ovn-kubernetes, Kata, SPIRE) | Expansion set; beyond the v1 default core. Buildable later via catalog entries. [WONT-v1] |
| **Tier-3 operator/vendor-authored components** | Each operator brings their own; not part of the shipped default catalog. [WONT-v1] |
| **Tier-4 closed/proprietary components** (e.g. Teleport Enterprise, NVMesh CSI, opaque vendor zips) | Cannot be built from source; explicitly excluded from the from-source guarantee and left as the operator's out-of-band concern (NKD-CON-009). [WONT-v1] |
| **arm64 and non-amd64 architectures** | Reserved as additive typed-enum values; v1 is amd64-only (NKD-CON-004). [WONT-v1] |
| **Any NVIDIA-internal-specific integration** | Not a normative target. At most an **out-of-tree private adapter** an operator maintains themselves; no such integration ships in the OSS core. [WONT-v1] |
| **In-contract reproducibility of the FDE / encrypted disk layer** | Out of the reproducibility contract by design (NKD-NFR-010). [WONT-v1] |
| **Full base/overlay/mixin recipe composition** | Only the resolver hook exists in v1; recipes ship flat (NKD-CON-017). [WONT-v1] |
| **Node-OS construction / hardening / golden-image pipeline** | Operator-owned (Phase 1, §3.5). nkdctl MAY optionally emit a thin reference base image but does not own how the OS is installed, hardened, or provisioned (NKD-CON-019). [WONT-v1] |

---

## 11. Open Issues & Risks

Prefix **NKD-OI**. Each item carries an **Owner-question** (the decision to resolve) and a **Proposed disposition** (current best direction — not a committed resolution; several remain BLOCKED/TBD pending the bake-off).

| ID | Issue / Risk | Owner-question | Proposed disposition |
|----|--------------|----------------|----------------------|
| **NKD-OI-001** | **From-source reproducibility thesis unproven.** The entire trust model rests on cross-environment bit-identity not yet demonstrated on the Tier-1 set. | Does a two-environment bake-off of the Tier-1 components (incl. runc CGO lane) produce bit-identical artifacts? | **Mandatory gate.** Run the two-environment bit-identity bake-off spike (NKD-NFR-002/003) before committing to the thesis. Block v1 build-engine finalization on its pass. |
| **NKD-OI-002** | **Bootable-image format & boot/provisioning defaults** vary per environment (ISO vs qcow2 vs raw; PXE/iPXE/HTTP/installer). | Which are the shipped defaults, and how are formats/boot paths kept pluggable per operator? | Keep formats and boot paths fully pluggable (NKD-NFR-090); ship qcow2 + ISO as first-class defaults; bake in no single boot path. |
| **NKD-OI-003** | **FDE / reproducibility boundary.** FDE is per-node and non-deterministic. | Where is the reproducibility contract cut relative to the encrypted layer? | Assert byte-identity on the **pre-encryption** rootfs/disk only; declare the encrypted layer out of contract (NKD-NFR-010, NKD-CON-013). |
| **NKD-OI-004** | **Offline / mirrored sigstore.** Fulcio/Rekor egress is frequently blocked in airgapped networks. | Can verification run fully offline against pre-staged manifest + Rekor bundle? | Provide the mandatory offline cosign-verify path over mirrorable signed JSON (NKD-NFR-070); make input pre-mirroring enumerable (NKD-NFR-071). |
| **NKD-OI-005** | **Tier-4 proprietary components** cannot be built from source. | How is the from-source guarantee scoped so proprietary components don't silently weaken it? | Enumerate Tier-4 as explicit exceptions handled out-of-band by the operator; exclude from the guarantee and document the boundary (NKD-CON-009). |
| **NKD-OI-006** | **Control-plane-as-container-images doubles targets** (binary + reproducible OCI image + reproducible base image). | Can the reproducible OCI path hit bit-identity for all ~7 image targets + base image within budget? | Build reproducible OCI images with pinned tooling on top of host-binary packages; validate image-layer determinism in the bake-off; track build-time budget (NKD-NFR-062, NKD-CON-008). |
| **NKD-OI-007** | **Kernel ownership & reproducibility** unresolved for v1. | Who owns the kernel input and can its build be made byte-reproducible? | Treat kernel as an operator-owned, pinned input for v1; flag full kernel-build reproducibility as an open follow-on risk (NKD-CON-014). **TBD.** |
| **NKD-OI-008** | **GPU / accelerated stack** — declarative in v1 via AICR, built-from-source later. | How deep does AICR integration go in v1 (profile reference only) vs building accelerated components from source? | v1: pin a digest-identified **AICR profile** as the declarative accelerated-runtime layer (NKD-FR-RC-013a); the AICR-governed axes activate under it. From-source accelerated builds and a full AICR component catalog are a later additive concern. |
| **NKD-OI-009** | **OSS project governance** (Apache-2.0, CLA/DCO, upstream-ability, community catalogs) undecided. | What contribution/governance model and catalog-contribution process does the project adopt? | Adopt Apache-2.0 with a documented DCO/CLA and a defined path for community-authored catalogs (NKD-NFR-045). **TBD — governance sign-off pending.** |
| **NKD-OI-010** | **Base-OS snapshot dependency.** Reproducible base builds require a date-pinned distro snapshot the operator may lack for a non-default OS. | How is base-layer reproducibility handled when the operator's distro lacks a snapshot service? | Document base-layer reproducibility as contingent on operator-provided snapshot pinning; the default (Ubuntu/apt) path is validated, non-default is operator responsibility (NKD-CON-012). |
| **NKD-OI-011** | **runc CGO lane** is the hardest reproducibility case (musl + static libseccomp). | Does the dedicated runc toolchain image achieve cross-environment bit-identity? | Build runc in its own pinned musl/libseccomp lane and include it explicitly in the bake-off scope (NKD-NFR-006). |
| **NKD-OI-012** | **CVE fast-path folding.** An emergency live-patch must fold back into the next recipe so image==recipe is restored. | How does an out-of-band emergency patch reconcile into a reproducible minimal-diff rebuild? | Support a minimal-diff reproducible rebuild: the fix folds into the next recipe so the next node-replacement restores image==recipe. Design as a recipe-level minimal diff; details **TBD**. |
| **NKD-OI-013** | **Phase-1 package version-pin policy.** Readiness-set packages could be pinned exactly or by channel/min-version. | Does node readiness (Phase 1) require exact package versions or "≥ N / from channel X"? | Recommend exact-pin for reproducible/attestable artifacts; allow a channel/min-version relaxation for operator-owned readiness agents. **TBD** with the consuming lifecycle model. |
| **NKD-OI-014** | **FDE under BYO-OS.** Disk encryption is decided before the substrate is installed and does not fit "bring any OS + apt-install the rest." | Is FDE always an operator-supplied encrypted base image, or does nkdctl ship a thin FDE reference image, and how much unlock automation lives pre- vs post-provision? | Treat FDE as an explicit base-image overlay (Phase 1), not part of the minimal contract; nkdctl MAY emit a thin FDE reference image; unlock/attest tooling belongs in Phase 1. Aligns with NKD-CON-013 / NKD-OI-003. **TBD.** |
| **NKD-OI-015** | **Phase boundary & base-OS baseline.** Which components are OS-readiness (Phase 1) vs cluster-software (Phase 2), and which base-OS baseline is the default. | Where exactly is the Phase-1/Phase-2 cut drawn, and is the default base a single pinned baseline (e.g. Ubuntu 24.04) with older releases sunset? | Cut at "can this node be installed onto post-provision": substrate = Phase 2; base OS + repo + minimal readiness = Phase 1. Default to a single pinned Ubuntu LTS baseline; older releases out of the default catalog. **TBD.** |

---

## Appendix A — Requirements Traceability Summary

Every requirement ID, a one-line summary, its MoSCoW priority, and the dependent open issue/assumption (if any). Open-issue items (NKD-OI-*) are tracked in §11; the constraints (NKD-CON-*) are the governing assumptions.

### A.1 Context (NKD-CTX)

| ID | Summary | MoSCoW | Dependency |
|----|---------|--------|------------|
| NKD-CTX-001 | Self-contained; no NVIDIA-internal service at any stage | MUST | — |
| NKD-CTX-002 | Integrate only via standard interface classes | MUST | — |
| NKD-CTX-003 | Consume any `--data` catalog; hardcode no environment | MUST | — |
| NKD-CTX-004 | Emit to N engines via adapters over one IR | MUST | NKD-OI-001 |
| NKD-CTX-005 | Fully offline, fail-closed verify mode | MUST | NKD-OI-004 |
| NKD-CTX-006 | NVIDIA-internal consumers non-normative only | MUST | — |

### A.2 Catalog / Recipe / Criteria (NKD-FR-RC)

| ID | Summary | MoSCoW | Dependency |
|----|---------|--------|------------|
| NKD-FR-RC-001 | Single-recipe resolution or fail closed | MUST | — |
| NKD-FR-RC-002 | Asymmetric wildcard match; `""` = `any` | MUST | — |
| NKD-FR-RC-003 | Extra query fields still match | MUST | — |
| NKD-FR-RC-004 | Recipe constraining an omitted field doesn't match | MUST | — |
| NKD-FR-RC-005 | Specificity tie-break; fail closed on ambiguity | MUST | — |
| NKD-FR-RC-006 | Parser fast-path + catalog registry fallback | MUST | — |
| NKD-FR-RC-007 | Exactly four active axes (role/k8sVersion/os/arch) | MUST | — |
| NKD-FR-RC-008 | `role` dominant; one role per recipe | MUST | — |
| NKD-FR-RC-009 | `os` operator-choosable; Ubuntu default only | MUST | NKD-CON-003 |
| NKD-FR-RC-010 | `k8sVersion` semver bound to a pinned ref | MUST | NKD-CON-010 |
| NKD-FR-RC-011 | Reserved axes defined inactive | MUST | NKD-CON-018 |
| NKD-FR-RC-012 | Non-empty reserved axis fails closed | MUST | NKD-CON-018 |
| NKD-FR-RC-013 | Reserved axes declared but non-matching | SHOULD | — |
| NKD-FR-RC-014 | Composition retained as inert resolver hook | MUST | NKD-CON-017 |
| NKD-FR-RC-015 | Populated composition fields fail closed | SHOULD | NKD-CON-017 |
| NKD-FR-RC-016 | Single resolved IR = source of truth | MUST | — |
| NKD-FR-RC-017 | Deterministic YAML serialization | MUST | — |
| NKD-FR-RC-018 | `recipeDigest` = sha256 of serialized IR | MUST | — |
| NKD-FR-RC-019 | IR fully enumerates all build inputs | MUST | — |
| NKD-FR-RC-020 | No clock/host/random values in IR | MUST | — |
| NKD-FR-RC-021 | Declarative registry; one YAML entry per component | MUST | — |
| NKD-FR-RC-022 | Required per-entry pin fields | MUST | — |
| NKD-FR-RC-023 | Fail closed on unpinned source ref | MUST | NKD-CON-010 |
| NKD-FR-RC-024 | Fail closed on tag-not-digest toolchain | MUST | — |
| NKD-FR-RC-025 | Pluggable `outputType` + CGO lane distinction | MUST | — |
| NKD-FR-RC-026 | Every entry either from-source or explicit Tier-4 exclusion | MUST | NKD-OI-005 |
| NKD-FR-RC-027 | Portable, operator-authorable catalog | MUST | — |
| NKD-FR-RC-028 | `--data` override loads with no code change | MUST | — |
| NKD-FR-RC-029 | Ship a sensible overridable default catalog | SHOULD | — |
| NKD-FR-RC-030 | Deterministic multi-catalog precedence; fail on conflict | SHOULD | §4.8 (layering TBD) |
| NKD-FR-RC-031 | Default NKD catalog embedded in binary via go:embed (AICR EmbeddedDataProvider) | MUST | — |
| NKD-FR-RC-032 | Single DataProvider abstraction; `--data` fully replaces embedded | MUST | — |

### A.3 Pipeline / Build (NKD-FR-BLD)

| ID | Summary | MoSCoW | Dependency |
|----|---------|--------|------------|
| NKD-FR-BLD-001 | One engine-neutral pipeline IR (build DAG) | MUST | — |
| NKD-FR-BLD-002 | Emit to GHA/GitLab/Tekton/Argo/local | MUST | NKD-OI-001 |
| NKD-FR-BLD-003 | Identical build semantics across engines | MUST | — |
| NKD-FR-BLD-004 | New engine = one adapter, no core change | SHOULD | — |
| NKD-FR-BLD-005 | Job-per-component, watchable run | MUST | — |
| NKD-FR-BLD-006 | Unconditional final verify node | MUST | — |
| NKD-FR-BLD-010 | Pinned-by-digest OCI toolchain containers | MUST | — |
| NKD-FR-BLD-011 | `--network=none` during compile | MUST | — |
| NKD-FR-BLD-012 | Explicit env allowlist, pinned repro vars | MUST | — |
| NKD-FR-BLD-013 | Apache-2.0 runtime; no copyleft/BUSL engine | MUST | NKD-CON-011 |
| NKD-FR-BLD-020 | Build from source; no prebuilt passthrough | MUST | — |
| NKD-FR-BLD-021 | Canonical upstream build path per component | MUST | NKD-CON-006 |
| NKD-FR-BLD-022 | Immutable source pins; unpinned fails | MUST | NKD-CON-010 |
| NKD-FR-BLD-023 | Reproducible Go flag discipline | MUST | NKD-OI-001 |
| NKD-FR-BLD-024 | No codegen in reproducible build | MUST | — |
| NKD-FR-BLD-025 | runc static musl+libseccomp lane | MUST | NKD-OI-011 |
| NKD-FR-BLD-026 | `pause` (C) from source, pinned toolchain | MUST | — |
| NKD-FR-BLD-030 | Control-plane binary + reproducible OCI image | MUST | NKD-OI-006 |
| NKD-FR-BLD-031 | Reproducible base/scratch image | MUST | NKD-OI-006 |
| NKD-FR-BLD-032 | Deterministic OCI image assembly | MUST | — |
| NKD-FR-BLD-040 | Host packages in standard repo form (`.deb`/`.rpm`) | MUST / WONT-v1 (.rpm) | — |
| NKD-FR-BLD-041 | Byte-reproducible packages | MUST | — |
| NKD-FR-BLD-042 | Operator-owned repo signing; no bundled keys | SHOULD | — |
| NKD-FR-BLD-050 | Operator-choosable base rootfs, Ubuntu default | MUST | NKD-CON-003 |
| NKD-FR-BLD-051 | mmdebstrap against date-pinned snapshot | MUST | NKD-OI-010 |
| NKD-FR-BLD-052 | Rootfs normalization | MUST | — |
| NKD-FR-BLD-053 | License-clean rootfs/image tooling | MUST | NKD-CON-011 |
| NKD-FR-BLD-060 | Pluggable formats; qcow2+ISO first-class | MUST | NKD-OI-002 |
| NKD-FR-BLD-061 | Two node SKUs (control-plane, worker) | MUST | — |
| NKD-FR-BLD-062 | Operator-configurable provisioning path | MUST | NKD-OI-002 |
| NKD-FR-BLD-063 | Reproducible disk images | MUST | NKD-OI-003 |
| NKD-FR-BLD-064 | Optional LUKS/TPM2 FDE, off by default | SHOULD | NKD-OI-003 |
| NKD-FR-BLD-065 | Reproducibility on pre-encryption image only | MUST | NKD-OI-003 |
| NKD-FR-BLD-066 | Offline vendoring mode | MUST | NKD-OI-004 |
| NKD-FR-BLD-067 | Tier-1 default build set | MUST | — |
| NKD-FR-BLD-068 | Operators bring own Tier-2/3 via catalog | MUST | — |
| NKD-FR-BLD-069 | Tier-4 excluded from from-source guarantee | MUST | NKD-OI-005 |
| NKD-FR-BLD-070 | Two-environment bit-identity bake-off gate | MUST | NKD-OI-001 |
| NKD-FR-BLD-071 | Unsupported flag/format combos fail closed | MUST | — |
| NKD-FR-BLD-072 | Baremetal-first; cloud/CSP fails closed | WONT-v1 | NKD-CON-005 |
| NKD-FR-BLD-073 | Pinned kernel reproducible if built | SHOULD | NKD-OI-007 |
| NKD-FR-BLD-074 | GPU/accelerated stack out of v1 | WONT-v1 | NKD-OI-008 |
| NKD-FR-BLD-075 | Packages-first delivery, decoupled from base image | MUST | NKD-OI-015 |
| NKD-FR-BLD-076 | Cluster software post-provision-installable, not baked | MUST | — |
| NKD-FR-BLD-077 | Prefer thin base image over kitchen-sink | SHOULD | — |
| NKD-FR-BLD-078 | nkdctl doesn't own OS construction/hardening | SHOULD | NKD-CON-019 |
| NKD-FR-BLD-079 | Repro/provenance apply to packages-first artifacts | MUST | — |

### A.4 Reproducibility / Provenance (NKD-FR-REP)

| ID | Summary | MoSCoW | Dependency |
|----|---------|--------|------------|
| NKD-FR-REP-001 | Output digest a pure function of the recipe | MUST | NKD-OI-001 |
| NKD-FR-REP-002 | `recipeDigest` = sha256 of serialized IR | MUST | — |
| NKD-FR-REP-003 | Deterministic marshaling of all machine artifacts | MUST | — |
| NKD-FR-REP-004 | Hermetic build (no network/env/codegen leaks) | MUST | — |
| NKD-FR-REP-005 | Mandatory two-environment bake-off gate | MUST | NKD-OI-001 |
| NKD-FR-REP-006 | SOURCE_DATE_EPOCH + TZ/LC normalization | MUST | — |
| NKD-FR-REP-007 | Date-pinned, normalized base rootfs | MUST | NKD-OI-010 |
| NKD-FR-REP-008 | Deterministic image/disk assembly | MUST | NKD-OI-003 |
| NKD-FR-REP-009 | Expected-digests manifest fields | MUST | — |
| NKD-FR-REP-010 | Manifest usable by any publisher/self-attest | MUST | — |
| NKD-FR-REP-011 | Manifest ships zero binaries | MUST | — |
| NKD-FR-REP-012 | Deterministically-serialized manifest | MUST | — |
| NKD-FR-REP-013 | Cosign keyless + Rekor inclusion | MUST | — |
| NKD-FR-REP-014 | Detached signing; pre-signature byte compare | MUST | — |
| NKD-FR-REP-015 | Configurable OIDC issuer/identity; keyless default | MUST | — |
| NKD-FR-REP-016 | Optional key/KMS signing fallback | SHOULD | NKD-OI-004 |
| NKD-FR-REP-017 | Verify performs 4 checks, fails closed | MUST | — |
| NKD-FR-REP-018 | Name first divergent node deterministically | MUST | — |
| NKD-FR-REP-019 | Fail closed on ambiguous/unverifiable state | MUST | — |
| NKD-FR-REP-020 | Verify = unconditional final node all engines | MUST | — |
| NKD-FR-REP-021 | Verification identity from config, never hardcoded | MUST | — |
| NKD-FR-REP-022 | Mandatory offline verify path | MUST | NKD-OI-004 |
| NKD-FR-REP-023 | Mirrorable verification bundle | MUST | NKD-OI-004 |
| NKD-FR-REP-024 | Offline verify same fail-closed semantics | MUST | NKD-OI-004 |
| NKD-FR-REP-025 | CycloneDX SBOM per build | MUST | — |
| NKD-FR-REP-026 | Deterministic SBOM serialization | MUST | — |
| NKD-FR-REP-027 | SBOM references `recipeDigest` | SHOULD | — |
| NKD-FR-REP-028 | Reproducibility on pre-encryption image | MUST | NKD-OI-003 |
| NKD-FR-REP-029 | FDE applied after verified pre-encryption image | MUST | NKD-OI-003 |

### A.5 CLI/SDK & Interfaces (NKD-FR-INT)

| ID | Summary | MoSCoW | Dependency |
|----|---------|--------|------------|
| NKD-FR-INT-001 | Thin CLI over `client/v1` SDK facade | MUST | — |
| NKD-FR-INT-002 | Functional-options config; documented defaults | MUST | — |
| NKD-FR-INT-003 | SDK exposes every CLI capability | SHOULD | — |
| NKD-FR-INT-004 | No NVIDIA-internal dependency | MUST | NKD-CON-001 |
| NKD-FR-INT-005 | Structured coded errors across boundaries | MUST | — |
| NKD-FR-INT-006 | Stable exit-code mapping | MUST | — |
| NKD-FR-INT-007 | Context timeout on every I/O | MUST | — |
| NKD-FR-INT-008 | Cancellation checks in long ops | MUST | — |
| NKD-FR-INT-009 | Bounded HTTP client + response bodies | MUST | — |
| NKD-FR-INT-010 | Logs to stderr; results machine-consumable | SHOULD | — |
| NKD-FR-INT-011 | Subcommand surface | MUST | — |
| NKD-FR-INT-012 | `--data` on every subcommand; publisher-agnostic | MUST | — |
| NKD-FR-INT-013 | `resolve` to fully-pinned IR, asymmetric match | MUST | — |
| NKD-FR-INT-014 | Reserved axis → InvalidRequest fail closed | MUST | NKD-CON-018 |
| NKD-FR-INT-015 | Unpinned ref / tag toolchain fail closed | MUST | NKD-CON-010 |
| NKD-FR-INT-016 | `query` selector-path value extraction | SHOULD | — |
| NKD-FR-INT-017 | Deterministic serialization; pure `recipeDigest` | MUST | — |
| NKD-FR-INT-018 | `bundle` emits portable pipeline, not image config | MUST | — |
| NKD-FR-INT-019 | `--engine` multi-engine from one IR | MUST | NKD-OI-001 |
| NKD-FR-INT-020 | `--format` pluggable; qcow2+ISO first-class | MUST | NKD-OI-002 |
| NKD-FR-INT-021 | Job-per-component + final verify node | MUST | — |
| NKD-FR-INT-022 | `--airgap` emit + mirrorable bundle | MUST | NKD-OI-004 |
| NKD-FR-INT-023 | Hermetic per-component build nodes | MUST | — |
| NKD-FR-INT-024 | `build` local hermetic run, byte-identical | SHOULD | NKD-OI-001 |
| NKD-FR-INT-025 | Deterministic CycloneDX `sbom` | MUST | — |
| NKD-FR-INT-026 | `attest` produces signed keyless detached manifest | MUST | — |
| NKD-FR-INT-027 | `attest` configurable OIDC identity | MUST | — |
| NKD-FR-INT-028 | `verify` 4 checks, name first divergence | MUST | — |
| NKD-FR-INT-029 | `verify` as artifact check and pipeline node | MUST | — |
| NKD-FR-INT-030 | Mandatory offline verify path | MUST | NKD-OI-004 |
| NKD-FR-INT-031 | Verify scoped to pre-encryption; FDE excluded | MUST | NKD-OI-003 |
| NKD-FR-INT-032 | Verify another party's build with manifest only | MUST | — |
| NKD-FR-INT-033 | Host packages consumable by any apt/rpm repo | MUST | — |
| NKD-FR-INT-034 | OCI images + referrers to any registry | MUST | — |
| NKD-FR-INT-035 | Disk images for standard provisioning paths | MUST | NKD-OI-002 |
| NKD-FR-INT-036 | Standard sigstore; keyless default | MUST | — |
| NKD-FR-INT-037 | Base OS/CI/format/boot are defaults, not hardcoded | MUST | NKD-CON-003 |
| NKD-FR-INT-038 | Generic machine-readable handoff contract | MUST | — |
| NKD-FR-INT-039 | `diff` minimal reproducible recipe diff | MUST | NKD-OI-012 |
| NKD-FR-INT-040 | Generic CVE fast-path fold-back | MUST | NKD-OI-012 |
| NKD-FR-INT-041 | Internal wiring stays out-of-tree adapter | MUST | NKD-CON-001 |
| NKD-FR-INT-042 | `catalog list` enumerates pins/digests | MUST | — |
| NKD-FR-INT-043 | `catalog validate` fails closed on unpinned refs | MUST | — |
| NKD-FR-INT-044 | `catalog author` single-YAML-entry component add | MUST | — |
| NKD-FR-INT-045 | `catalog publish` any operator's signed catalog | MUST | — |
| NKD-FR-INT-046 | `trust` manages configurable OIDC identities | MUST | — |
| NKD-FR-INT-047 | `Parse*` fast-path + registry fallback; `arch` additive | MUST | — |
| NKD-FR-INT-048 | External interfaces limited to standard services | MUST | — |
| NKD-FR-INT-049 | Git fetch by SHA; branch/tag rejected | MUST | NKD-CON-010 |
| NKD-FR-INT-050 | Permissive linked deps; no BUSL/copyleft core | MUST | NKD-CON-011 |
| NKD-FR-INT-051 | Coded errors distinguish input vs transient | SHOULD | — |
| NKD-FR-INT-052 | Outputs consumable by 2-phase readiness + post-provision model | MUST | NKD-OI-015 |

### A.6 Non-Functional (NKD-NFR)

| ID | Summary | MoSCoW | Dependency |
|----|---------|--------|------------|
| NKD-NFR-001 | Byte-identical artifacts on repeated builds | MUST | NKD-OI-001 |
| NKD-NFR-002 | Cross-environment bit-identity is the pass criterion | MUST | NKD-OI-001 |
| NKD-NFR-003 | Bake-off spike mandatory gate | MUST | NKD-OI-001 |
| NKD-NFR-004 | Network-disabled build + env allowlist | MUST | — |
| NKD-NFR-005 | Reproducible Go flag discipline | MUST | — |
| NKD-NFR-006 | runc dedicated musl/libseccomp lane | MUST | NKD-OI-011 |
| NKD-NFR-007 | No codegen in reproducible build | MUST | — |
| NKD-NFR-008 | Date-pinned normalized base rootfs | MUST | NKD-OI-010 |
| NKD-NFR-009 | Reproducible disk-image tooling | MUST | NKD-OI-003 |
| NKD-NFR-010 | Contract on pre-encryption disk only | MUST | NKD-OI-003 |
| NKD-NFR-011 | Deterministic IR/manifest/SBOM marshaling | MUST | — |
| NKD-NFR-020 | Verify fails closed with all checks | MUST | — |
| NKD-NFR-021 | Verify unconditional final node all engines | MUST | — |
| NKD-NFR-022 | Detached signatures | MUST | — |
| NKD-NFR-023 | No secrets in reproducible artifacts/env | MUST | — |
| NKD-NFR-024 | Keyless default; any OIDC identity | MUST | — |
| NKD-NFR-025 | Deterministic CycloneDX SBOM | MUST | — |
| NKD-NFR-026 | SBOM/images consumable by stock scanners | SHOULD | — |
| NKD-NFR-027 | All components pinned; unpinned fails | MUST | NKD-CON-010 |
| NKD-NFR-028 | Context timeouts + coded errors | MUST | — |
| NKD-NFR-040 | Apache-2.0 public OSS | MUST | NKD-OI-009 |
| NKD-NFR-041 | Permissive linked deps only | MUST | NKD-CON-011 |
| NKD-NFR-042 | GPL tools via subprocess allowed | MAY | — |
| NKD-NFR-043 | No copyleft engine / no BUSL Packer core | MUST | NKD-CON-011 |
| NKD-NFR-044 | No closed/NVIDIA-internal dependency | MUST | NKD-CON-001 |
| NKD-NFR-045 | Documented governance; community catalogs | SHOULD | NKD-OI-009 |
| NKD-NFR-060 | Job-per-component parallelism | MUST | — |
| NKD-NFR-061 | Operator may cache own rebuilt bytes | MUST | — |
| NKD-NFR-062 | Documented per-target build budget | SHOULD | NKD-OI-006 |
| NKD-NFR-063 | Resolve/emit/verify complete in seconds | SHOULD | — |
| NKD-NFR-070 | Mandatory offline cosign-verify path | MUST | NKD-OI-004 |
| NKD-NFR-071 | Enumerable pre-mirrorable inputs | MUST | NKD-OI-004 |
| NKD-NFR-072 | Reproducible build runs network-disabled | MUST | — |
| NKD-NFR-080 | Legible, watchable public run | MUST | — |
| NKD-NFR-081 | Structured logs; scripting-safe output | MUST | — |
| NKD-NFR-082 | Manifest records public-run URL | MUST | — |
| NKD-NFR-083 | Name first divergent node on failure | MUST | — |
| NKD-NFR-090 | Environment-agnostic; defaults only | MUST | NKD-CON-003 |
| NKD-NFR-091 | New component = one YAML entry | MUST | — |
| NKD-NFR-092 | New engine/format = one thin adapter | MUST | — |
| NKD-NFR-093 | Typed extensible criteria taxonomy | MUST | NKD-CON-004 |
| NKD-NFR-094 | Operator criteria values load with no code | MUST | — |

### A.7 Constraints & Assumptions (NKD-CON)

See §9 for the full statements. Priorities: NKD-CON-001/002/003/005/006/007/008/009/010/011/013/015/018/019/020 = MUST; NKD-CON-012/014/017 = SHOULD; NKD-CON-004/016 = WONT-v1. Each frames one or more requirements above and, where it names a live risk, links to §11 (e.g. NKD-CON-011→NKD-OI-001, NKD-CON-013→NKD-OI-003, NKD-CON-014→NKD-OI-007, NKD-CON-016→NKD-OI-008).

---

## Appendix B — Enumerated From-Source Build Set

nkdctl ships a **default/reference catalog** (Tier 1) so the tool is useful out of the box, and applies the **operator-brings-their-own** principle for everything else (Tiers 2–3 authored on top of the tool; Tier 4 explicitly excluded from the from-source guarantee). The tiers are a property of *catalogs*, not of the tool: any component an operator owns the source for can be added as a single declarative `SourceBuildRegistry` entry with no nkdctl code change (NKD-FR-RC-021, NKD-FR-BLD-068). Defaults are overridable, never hardcoded (NKD-CON-003).

### Tier 1 — Default / reference catalog (built from source, shipped)

The ~15-entry Go-heavy upstream OSS set, each pinned and built via its canonical upstream path. The control plane runs as reproducible OCI images layered on the reproducible base image and the host-binary packages (NKD-CON-008).

| Component | Source (upstream) | Pinned version | Output form |
|---|---|---|---|
| kube-apiserver | kubernetes/kubernetes | 1.34.2 | OCI image (+ from-source binary) |
| kube-controller-manager | kubernetes/kubernetes | 1.34.2 | OCI image |
| kube-scheduler | kubernetes/kubernetes | 1.34.2 | OCI image |
| kube-proxy | kubernetes/kubernetes | 1.34.2 | OCI image |
| kubelet | kubernetes/kubernetes | 1.34.2 | host package (.deb) |
| kubeadm | kubernetes/kubernetes | 1.34.2 | host package (.deb) |
| kubectl | kubernetes/kubernetes | 1.34.2 | host package (.deb) |
| etcd | etcd-io/etcd | 3.5.24 | OCI image |
| coredns | coredns/coredns | 1.12.0 | OCI image |
| pause | kubernetes build/pause (C) | 3.10 | OCI image |
| containerd | containerd/containerd | 1.7.x (pinned SHA) | host package (.deb) |
| runc | opencontainers/runc | pinned SHA (musl+libseccomp lane) | host package (.deb) |
| CNI plugins | containernetworking/plugins | 1.7.1 | host package (.deb) |
| crictl | kubernetes-sigs/cri-tools | 1.34.0 | host package (.deb) |
| helm | helm/helm | 3.20.0 | host package (.deb) |

### Tier 2 — Larger multi-language upstream OSS (operator-brings-their-own, buildable)

Not in the shipped default core, but buildable through the same hermetic path via operator-authored catalog entries when the operator supplies the pinned source (NKD-FR-BLD-068).

| Component (examples) | Upstream | Disposition |
|---|---|---|
| Calico | projectcalico/calico | Operator-authored catalog entry; not in default core [WONT-v1 default] |
| OVN-Kubernetes | ovn-org/ovn-kubernetes | Operator-authored catalog entry [WONT-v1 default] |
| Kata Containers | kata-containers/kata-containers | Operator-authored catalog entry [WONT-v1 default] |
| SPIRE | spiffe/spire | Operator-authored catalog entry [WONT-v1 default] |

### Tier 3 — Operator / vendor-authored components (from source when operator owns it)

Operator- or vendor-authored components the operator builds from their own source. Each operator brings their own; none is part of the shipped default catalog.

| Component class | Disposition |
|---|---|
| Operator-authored operators, agents, custom controllers, in-house CNI/CSI, internal tooling | Buildable from source via a catalog entry when the operator owns the source; not shipped by nkdctl [WONT-v1 default] |

### Tier 4 — Closed / proprietary / opaque (excluded from the from-source guarantee)

A from-source OSS tool inherently cannot build these; they are marked in the catalog as declared exceptions and handled out-of-band by the operator (NKD-FR-BLD-069, NKD-FR-RC-026, NKD-CON-009, NKD-OI-005). They SHALL NOT be represented as from-source-provable artifacts.

| Component (examples) | Reason | Disposition |
|---|---|---|
| Teleport Enterprise | Proprietary/closed source | Excluded; operator out-of-band concern [WONT-v1] |
| NVMesh CSI | Proprietary/closed source | Excluded; operator out-of-band concern [WONT-v1] |
| Opaque vendor zips / closed enterprise tooling / proprietary CSI | No buildable source | Excluded; carried as a declared from-source exception [WONT-v1] |
