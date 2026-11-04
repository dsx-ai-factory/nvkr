# ADR-28 — Multi-platform executables as OCI image indexes

- **Decision status:** Proposed (design only; pending named-decider ratification)
- **Implementation maturity:** Designed, not implemented. No multi-platform tool declaration, `tools` lock entry,
  executable `platforms` output, executable-index identity projection or index packaging exists; see
  `reconciliation/README.md`.
- **Acceptance-test maturity:** None. The implementation units below name the contract suites that must pass;
  no contract test, generated bundle, hosted Run or consumer verification exercises an executable index.
- **Deciders:** Pending; catalog, release-engineering, delivery and evidence owners must be named before
  ratification.
- **Index:** Proposed ADR-28 supplements ADR-01–22 and proposed ADR-23/24/26/27; it remains outside the
  consolidated contract set until accepted. It builds on the tool companion contract proposed in #286 (PR #294),
  which is not on `main`. Implementation is tracked in #296 and the retrofit in #297.
- **Relates to:** ADR-01 (`nvkr.build` schemas), ADR-02 §2 (aggregate logical projection), ADR-03 (coded errors),
  ADR-04 (signing), ADR-11 §1–§2 (direct selection, no sibling reuse), ADR-12 §1 (concrete targets and the
  executable-aggregate exclusion this decision lifts), ADR-13 (SBOMs, `evidence: none`), ADR-16 (artifact check
  and qualification), ADR-20 (Binary Bundle, runnable image indexes, `release pull`), ADR-21 (regeneration),
  ADR-22 (tool release assets, lock v1), ADR-27 §3 (one tool pin per platform).
- **Scope:** `linux/amd64` and `linux/arm64` executable outputs on the `static-unqualified` and
  `pipeline-attested` lanes. Darwin children, qualification over an index output and the qualified lane are
  deferred and fail closed. Single-platform executable outputs and their Binary Bundle are unchanged.

## Context

One upstream tool release usually ships an asset per platform, at a URL that differs only in OS and
architecture. NVKR cannot express that in one declaration.

- **Tool pins are single-platform.** An ADR-22 `tool` declaration has one `platform` and one URL; a second
  platform needs a second source name. The maintained catalogs therefore repeat each tool. `tools/operator-amd64`
  and `tools/operator-arm64` pin the same Helm and kubectl releases twice, `tools/native` declares Helm three
  times (`helm`, `helm-linux`, `helm-linux-amd64`), `nvsentinel` declares `syft-amd64` and `syft-arm64`, and
  PR #294 adds `tools/aicr-arm64` because the AICR source can carry only one URL. Each copy is pinned and bumped
  on its own, and nothing checks that the copies stay on one version.
- **Executable outputs are single-platform.** ADR-12 §1 states that "the image-index contract does not create
  an executable aggregate". The NVKE Definitions publish `nvkectl` and `nvkectl-linux-arm64` as two outputs, and
  NVSentinel publishes 19 executables twice, from two Definitions.
- **The Binary Bundle is not an image.** ADR-20 publishes each executable output as an OCI manifest with NVKR
  config and layer media types and files under `files/`. ORAS can download its layer. BuildKit `COPY --from`
  expects an image config, and Kubernetes guarantees image volumes only for the types the container image
  field accepts; the Binary Bundle's NVKR media types fall outside both. Consumers choose a platform by output
  name.
- **Companions ride beside the executable.** PR #294 delivers `aicr-attestation.sigstore.json` beside `aicr` at
  mode 0644, because `aicr bundle --attest` reads it from beside `os.Executable()`.

The catalog already has the per-platform pieces this needs. Prebuilt artifact URLs render `{{.Arch}}`, and the
lock stores one digest per declared architecture (ADR-22 §3, §7). OCI image outputs declare `platforms` and
publish one standard index over native children (ADR-12 §1, ADR-20). AICR v1.1.2 publishes
`aicr_1.1.2_linux_amd64.tar.gz` and `aicr_1.1.2_linux_arm64.tar.gz`; both hold `aicr`,
`aicr-attestation.sigstore.json` and `recipe-catalog.sigstore.json` at the archive root.

## Placement

This is a new ADR rather than an amendment. ADR-12 §1 explicitly excludes an executable aggregate, and the
change spans the ADR-22 tool contract, ADR-20 publication, the ADR-01/02 identity projections, ADR-13
evidence and ADR-27 §3. No existing decision owns all of it. On acceptance, each part folds into its owning
ADR (Follow-ups).

## Decision

### 1. One tool source declares every platform

```yaml
sources:
  aicr:
    repo: https://github.com/NVIDIA/aicr
    constraint: 1.1.x
    tool:
      format: tar-gzip
      platforms: [linux/amd64, linux/arm64]
      url: https://github.com/NVIDIA/aicr/releases/download/{{.Tag}}/aicr_{{.Version}}_{{.OS}}_{{.Arch}}.tar.gz
      executablePath: aicr
      companions: [aicr-attestation.sigstore.json]
```

- `platforms` is a nonempty, sorted, unique list from the existing executable platform domain (`linux/amd64`,
  `linux/arm64`, `darwin/arm64`). It excludes `platform`. A declaration with `platform` keeps its current
  meaning and lock bytes.
- `url`, `executablePath` and each companion path may use `{{.OS}}` and `{{.Arch}}`, rendered for each declared
  platform with the existing target spellings (`linux` or `darwin`; `amd64` or `arm64`). Version tokens render
  as today. Every tool URL in the maintained catalogs uses these spellings.
- Two platforms that render the same URL and executable path fail. NVKR never infers that one asset runs on
  two platforms.
- Format and the companion list apply to every platform. A tool whose platforms need different formats or
  companions keeps one source per platform.
- Other spellings, such as `x86_64`, `aarch64` or `Darwin`, are not admitted. Such an upstream keeps one source
  per platform until a spelling map is decided (open question 2).

### 2. The lock records one entry per platform

```yaml
pins:
  aicr:
    tag: v1.1.2
    commit: 2bf662b86c21a97b6e79e90035382ea252a44798
    treeHash: 60fa9de5b313d80d7a675c41921cb9798aec4bfb
    sourceDateEpoch: 1792572941
    tools:
      - platform: linux/amd64
        format: tar-gzip
        url: https://github.com/NVIDIA/aicr/releases/download/v1.1.2/aicr_1.1.2_linux_amd64.tar.gz
        executablePath: aicr
        archiveDigest: sha256:6350c5a50e7866569ec746b87b4c1c5e836f9ffba2b9505eb24241852f52e350
        executableDigest: sha256:05c2c6ff0bec39d989a2b5c87930e7d57efdb82172b9f49fd2241b5ea9bc9c46
        companions:
          - path: aicr-attestation.sigstore.json
            digest: sha256:57a1cb3771174c07dd38ae11172027400d6d22643f38f6083bcf73ecf7c8c91f
      - platform: linux/arm64
        format: tar-gzip
        url: https://github.com/NVIDIA/aicr/releases/download/v1.1.2/aicr_1.1.2_linux_arm64.tar.gz
        executablePath: aicr
        archiveDigest: sha256:7898cd203a6a51b452528bd0a5013e9424cec3ae2200e184a556537eefb9c91d
        executableDigest: sha256:0ed855fd9e6ed0177ed1c1288be096a8d3b88ea1747043c7e2a20a3ccd35fc0d
        companions:
          - path: aicr-attestation.sigstore.json
            digest: sha256:e9e661119160fb822b757a3a1189b10225e06da6d0d6b7fc6d3e1302a927ba79
```

The ARM64 entry is the record PR #294 pins. The AMD64 digests were measured from the v1.1.2 release asset for
this decision; the archive digest matches `aicr_checksums.txt`. `catalog pin` produces the real entries.

- Lock v1 gains an optional `tools` list, sorted by platform and exclusive with `tool`. Each entry has exactly
  the single-platform `tool` record shape. Older strict readers reject the field, as they reject
  `derivedFrom` (ADR-22 §1).
- The optional `companions` field, in the declaration and in each entry, is the companion contract proposed in
  #286 (PR #294). It is not on `main`. Until it lands, the shape is the current `tool` record without
  `companions`, and companion declarations remain unadmitted.
- A list rather than an architecture-keyed map, as prebuilt artifacts use, because `darwin/arm64` and
  `linux/arm64` share an architecture.
- Completeness is an exact key set: one entry per declared platform. A missing entry, or one that disagrees with
  its declaration, fails loading with `unpinned-source`, as a stale single tool pin does today. An extra or
  duplicate entry fails with `invalid-catalog`, as an orphan tool pin does today.
- `catalog pin` and `catalog bump` resolve the tag once, then download and verify every platform's asset with
  the existing bounded selector, and commit every entry in one compare-and-swap write. A failure on any platform
  leaves the lock unchanged, and `--check` prints every entry. One platform cannot be pinned alone.
- The source's commit, tree and epoch stay shared, so every platform builds from one upstream release.

Tool selection uses the entry for the platform that needs it. Run-wide `sbomTool` and `cosignTool` use the
entry for the utility execution platform; a source without one fails with `engine-capability` before
acquisition, as a mismatched single-platform tool does today. An ADR-27 pinned-tool producer uses the entry for
its target; ADR-27 §3's "separate source name per platform" becomes "separate lock entry of the same source".

### 3. Native producer identity is unchanged

A `releaseExecutable` producer on one platform lowers to the same IR v4 node as today, filled from that
platform's lock entry. Archive format, archive digest, member, executable digest and companion pins are
logical, and the URL is transport. The node carries neither the source name nor the platform list.

- Moving a single-platform tool into a multi-platform source with the same component name and pinned bytes
  leaves that platform's Build Digest unchanged. The catalog snapshot and Plan identities change.
- Renaming a component while consolidating, such as `helm-linux` to `helm`, changes its Build Digest, because the
  component name is logical. That is an intended, reviewed change.
- From-source host-package components, such as `nvkectl`, already declare several platforms. They need no
  catalog change.

### 4. An executable output may declare a platform set

```yaml
outputs:
  - name: aicr
    platforms: [linux/amd64, linux/arm64]
    catalog:
      base: ../../catalogs/tools/aicr
    format: executable
    components: [aicr]
```

ADR-12 §1's exclusion is lifted for `format: executable`. `platforms` is a nonempty, sorted, unique subset of
`linux/amd64` and `linux/arm64`, exclusive with `platform`, exactly as for OCI aggregates; the existing
validator rejects `darwin/arm64` in a set with `invalid-catalog`. Darwin stays a single-platform local
diagnostic output.

Every selected component must support every platform in the set. For each platform, the compiler derives the
native closure that a single-platform output on that platform derives today, then adds one index package node
that joins every child's exact results and evidence. Each child keeps its own Build Digest, producer results,
observations and evidence obligations. An output may select several components, as `operator-tools` selects
Helm and kubectl; each child then holds all of them for its platform. Equal child Build Digests across named
outputs still authorize no reuse (ADR-11 §2).

### 5. Aggregate identity

The output's logical Build Digest uses a new schema inside the existing `nvkr.build` family,
`nvkr.dev/executable-index-build/v1`: sorted `requiredPlatforms`, plus one child per platform holding only
`platform`, the `buildDigest` of that platform's native closure and its sorted `components`. Index, manifest,
config and layer digests, file names, modes, annotations, repositories and runner routes are absent. No digest
family is added. The image-index projection stays unchanged, because it admits one component per child and
names image semantics. As for image indexes, recomputing the vector authenticates no execution or evidence
(ADR-02 §2).

### 6. Publication: a standard image index over minimal per-platform images

```text
<repository>@sha256:<index>     OCI image index; one descriptor per platform, sorted
├─ linux/amd64 → image manifest
│    config  application/vnd.oci.image.config.v1+json     os, architecture, rootfs
│    layer   application/vnd.oci.image.layer.v1.tar+gzip   /aicr, /aicr-attestation.sigstore.json
└─ linux/arm64 → image manifest, same shape
referrers on the index:   NVKR signature, SLSA provenance
referrers on each child:  NVKR signature, SLSA provenance, SBOM; verified upstream evidence once #287 lands
```

- **Children are standard images.** Each child is an OCI image manifest with a standard image config holding
  `os`, `architecture` and `rootfs.diff_ids`. Architecture follows the existing native admission, so the
  variant is omitted. The config has no creation time, history, environment, user, entrypoint or command. A
  child carries files; it is not a runnable image and claims nothing about how the executable runs.
- **One deterministic layer.** The layer holds exactly the files that a single-platform output on that
  platform delivers today: every selected executable and its declared companions, with the same names and
  modes. It reuses the Binary Bundle layer encoding: PAX regular-file headers, owner 0:0 with empty names, the
  bundle's fixed modification time, and gzip with a zeroed header time. The one difference is that files
  sit at the layer root instead of under `files/`. No directory, link or special entry is written. Equal inputs
  give equal child bytes. The layer carries an `org.opencontainers.image.title`, so `oras pull` saves it as one
  tarball.
- **The file record is unchanged.** The canonical document that the Binary Bundle stores as its config lists
  OS, architecture, purpose and each file's name, install suggestion, mode, SHA-256 and size. It becomes each
  child's semantic record, bound in the signed release metadata and named by the child descriptor's
  `dev.nvkr.semantic.digest` annotation. Verification recomputes it from the layer; the image config stays
  standard.
- **The index is plain.** It is a standard OCI image index with one descriptor per platform, in sorted order,
  each carrying its `platform`. It has no NVKR media type. The signed release metadata identifies the output's
  format; NVKR does not infer it from media types.
- **Release metadata.** PublicationRoot and Release metadata bind the aggregate Build Digest, the required
  platforms and each child's platform, native Build Digest, manifest digest and semantic record, as they do for
  image indexes. Adding this output kind follows ADR-01 versioning: a reader that does not know the kind
  rejects it rather than reinterpreting it.
- **Single-platform outputs keep the Binary Bundle.** An output with `platform` publishes exactly as today. A
  one-element `platforms` set publishes a one-child index. The authored field selects the format; NVKR never
  infers it.

### 7. Signatures, SBOMs and companions

- **Signing.** NVKR signs and attests the index and each child manifest (ADR-04, ADR-20). Each deliverable gets
  its own signature and subject-bound SLSA provenance, and the index-subject signature covers the aggregate.
  Hosted verification requires all of them before recording success.
- **SBOMs.** Each child carries the SBOM obligations its executables carry today (ADR-13): one CycloneDX
  predicate in a Sigstore bundle, published as an empty-config referrer. Its OCI subject is the child manifest,
  and its statement subject is the exact executable. Companions are not scanned. A member with
  `evidence: none` stays so and never satisfies an evidence-required gate.
- **Companions stay in the payload.** A declared companion ships in the child's layer beside its executable,
  because the tool reads it there; `aicr bundle --attest` looks for `aicr-attestation.sigstore.json` beside the
  running executable. Carrying it only as a referrer would drop it for every consumer in §8 that copies files
  without walking referrers. A companion delivered this way is pinned bytes, not verified evidence.
- **Verified upstream attestations become referrers.** Once #287 verifies a tool's upstream Sigstore evidence,
  NVKR records the verified bundle as evidence with its own typed membership and attaches it as a referrer to
  the child it describes. A companion copy, if declared, stays in the layer. The referrer carries the
  authenticity claim; the companion carries only the file the tool reads.
- **Tool-side signatures** confer no authority (ADR-27 §6).

### 8. Consumption

| Consumer | Use | Notes |
|---|---|---|
| Dockerfile | `COPY --from=<repository>@sha256:<index> /aicr /aicr-attestation.sigstore.json /usr/local/bin/` | BuildKit selects the child for the build's target platform. Copying the companion keeps `aicr bundle --attest` working |
| Shell | `crane export --platform linux/arm64 <repository>@sha256:<index> - \| tar -x aicr aicr-attestation.sigstore.json` | Any OCI image client; no NVKR decoder |
| ORAS | `oras pull --platform linux/arm64 <repository>@sha256:<index>`, then `tar -xzf` | The titled layer downloads as one tarball |
| Kubernetes | An `image` volume referencing `<repository>@sha256:<index>` | The kubelet selects the node's platform. Image volumes are stable since v1.36 and mount read-only. The v1.31 alpha documented the mount as `noexec`, so a pod that runs the tool copies it to an executable volume unless its runtime permits exec (open question 6) |
| NVKR diagnostic | `release pull --output aicr --out <directory>` | Writes `<directory>/<os>-<arch>/<file>` for every platform with the existing exact-file checks; selects no single platform, as for OCI aggregates |
| NVKR disk producers | Material binding | Bind the native producer result for the disk's platform, as today. The index is publication packaging and never a material; ADR-27 §4 likewise refuses OCI layouts as materials |
| Downstream OS image builds | Extract at build time | Select the child for the image's platform by digest, verify its signatures, extract the files and install them under the build's own policy |

Digest selection gives integrity, not authenticity. Consumers verify the index and child signatures under their
Verification Policy.

### 9. Migration and compatibility

- Existing `platform` tool declarations, `tool` lock records, single-platform executable outputs and Binary
  Bundles keep their bytes, Build Digests and meaning. Nothing migrates implicitly.
- **AICR is the first migration.** Rename PR #294's `tools/aicr-arm64` to `tools/aicr`, declare
  `platforms: [linux/amd64, linux/arm64]`, and pin v1.1.2 for both. Then change the nvke-core `aicr` output to
  `platforms: [linux/amd64, linux/arm64]`. The ARM64 producer keeps its Build Digest (§3), and a contract test
  must assert that. The output's identity becomes the new aggregate Build Digest, and consumers move from the
  ARM64 Binary Bundle to the index.
- **Output renames reach consumers.** Replacing `nvkectl` and `nvkectl-linux-arm64` with one index output
  changes release navigation, quickstart guides, generated bundles and every downstream reference. Each retrofit
  in §10 lands with its consumer, generated-bundle and documentation updates.

### 10. Retrofit inventory

Every executable delivery in the maintained catalogs and Definitions, its current form and its migration.
#297 tracks this list. Each row is its own reviewed change after the units it needs land.

| Delivery | Current form | Migration |
|---|---|---|
| AICR CLI | `tools/aicr-arm64` (PR #294): one ARM64 tool source and `releaseExecutable` component with a companion; nvke-core output `aicr`, ARM64 Binary Bundle | First slice (§9): one source with both platforms; `aicr` output with `platforms` |
| `nvkectl` | From-source component for both platforms in `nvke/release`, `nvke/module-tags` and `nvke/native-site`; ARM64-only copies in `nvke/module-cli-arm64` (as `nvkectl-arm64`), `nvke/local-site-arm64` and `nvke/nightly-site-arm64`; outputs `nvkectl` (AMD64) and `nvkectl-linux-arm64` in `nvke/core.yaml` and `nvke/definition.yaml` | One `nvkectl` output with `platforms` in both Definitions. The catalog needs no change. The combined Definition's qualification requires native smoke for each executable member, so this waits for unit E7 |
| Operator tools (Helm, kubectl) | `tools/operator-amd64` and `tools/operator-arm64` pin the same releases twice; `operator-tools` is AMD64 in the NVKE Definitions and ARM64 in demo and diagnostic Definitions | Merge into `tools/operator` with multi-platform sources (Helm's member becomes `linux-{{.Arch}}/helm`). The NVKE `operator-tools` output takes `platforms` |
| Installer tools | `tools/installer-hosted-arm64` (Helm release asset; clusterctl, kind, kubectl and ORAS from source) and `tools/installer-local-arm64`; `installer-tools-arm64` outputs in demo and diagnostic Definitions | ARM64 only today. Move Helm to the multi-platform source form; add `platforms` when a consumer needs AMD64 |
| Native demo tools | `tools/native` pins Helm, kubectl, kind, clusterctl and ORAS three times each (`<tool>` Darwin, `<tool>-linux` ARM64, `<tool>-linux-amd64`), Packer and its QEMU plugin twice, and vmnet-helper and vmnet-client for Darwin only; also from-source nerdctl, NICoSim, nvkectl and the site-identity helper. Outputs `native-tools-<platform>`, `installer-tools-amd64`, `site-startup-tools-<arch>` and `nicosim-tools-<arch>` in `demo/tools/*.json` and the demo site Definitions | Collapse each tool to one source whose `platforms` include `darwin/arm64`. Component renames move Build Digests (§3), and the native demo adapter that maps per-platform IDs to command names changes with them. These Definitions run on one host, so their outputs may stay single-platform; Darwin outputs must stay single-platform |
| SBOM scanner (Syft) | Run-wide tool, not an output: AMD64 `syft` in `nvke/release` and `nvke/module-tags`, ARM64 `tools/sbom-arm64` (with a `releaseExecutable` component), `syft-amd64` and `syft-arm64` in `nvsentinel`; Definitions select `syft`, `syft-amd64` or `syft-arm64` | One `syft` source with both platforms; each Run uses its utility platform's entry (§2). Retires `tools/sbom-arm64` and the per-architecture names |
| Signer (cosign) | Run-wide AMD64 tool in `nvke/release` and `nvke/module-tags` | No change until an ARM64 utility platform signs; then add `platforms` |
| NVSentinel binaries | 19 from-source components for both platforms; 19 outputs each in `nvsentinel/binaries-amd64.yaml` and `binaries-arm64.yaml` | One Definition with 19 `platforms` outputs; retire the per-architecture Definitions after consumers move |
| Kubernetes conformance tools | Hydrophone (Darwin, AMD64, ARM64) and kubectl (`kubectl`, `kubectl-1.34`; Darwin, ARM64) from source; outputs `hydrophone-arm64`, `kubectl-arm64` and Darwin variants | ARM64-only Linux use today; add `platforms` when AMD64 conformance needs it. Darwin outputs stay single-platform |
| NICo simulator | From source in `nicosim` (ARM64) and `tools/native`; outputs `nicosim-tools-<arch>` | Demo use; as for native demo tools |
| Examples and quickstart | `docs/examples/authoring-lab`, `docs/examples/nvke-tutorial` (per-architecture Definitions), `docs/examples/nvke-deploy`, `demos/quickstart` | Show the index form once implemented, and keep one single-platform example |

Not in scope: toolchain images and helper pins under `toolchains/` (the execution helper, Packer, disk
inspection and NVSentinel Debian toolchains, the quickstart Go SDK checksums and the govulncheck module) are
build inputs with their own pin owners, not delivered executables.

### 11. Capability admission

Admission runs before acquisition, packaging or output work.

| Use | Admission |
|---|---|
| `definition validate`, `run plan`, `run graph`, `run diff`, dry-run | Supported once implemented; `static-unqualified` |
| `run local` | Supported once implemented, on the hosts that already admit each child's native executable output; `static-unqualified` |
| `run generate`, hosted GitHub Run | Supported once implemented; `pipeline-attested`, on ADR-18 §3 default hosted routing with one native job per child plus the index job |
| `darwin/arm64` in an executable `platforms` set | `invalid-catalog` |
| Qualification, `run check-artifacts` or `run qualify` over an index output | `engine-capability` until unit E7 |
| Qualified lane | Deferred; `engine-capability` |

### 12. Coded errors

The decision reuses existing ADR-03 codes.

| Condition | Code |
|---|---|
| Tool declaration with both or neither of `platform` and `platforms`; an empty, unsorted or duplicate set; an unknown token; two platforms rendering the same URL and executable path; executable `platforms` set outside the Linux domain, or combined with `platform`; a component that does not support a platform in the set | `invalid-catalog` |
| Missing lock entry; an entry that disagrees with its declaration | `unpinned-source` |
| Extra or duplicate lock entry | `invalid-catalog` |
| Run-wide or producer tool without an entry for the platform that needs it; qualification over an index output; qualified lane | `engine-capability` |
| Held child whose layer, file record, config platform or media types differ from the frozen expectation; missing or extra child; a child with NVKR media types | `verify-failed` |
| Missing signature, provenance or SBOM membership on the index or a child | The code the image-index path returns for the same omission |

## Implementation plan

Each unit lands with table-driven contract tests that assert the coded error, not just failure.

| Unit | Owner | Contract |
|---|---|---|
| E1 Catalog and lock | `pkg/nvkr/catalog`, `internal/releaseasset` | `platforms` declaration and exclusivity; `{{.OS}}` and `{{.Arch}}` in URL, member and companion paths; distinct-asset rule; `tools` entries with an exact platform key set; all-platform atomic pin, bump and `--check`; per-platform tool selection |
| E2 Identity | `pkg/nvkr/buildgraph`, `pkg/nvkr/ir` | Per-platform node filled from its entry; unchanged node bytes for a migrated pin (AICR ARM64 fixture); `nvkr.dev/executable-index-build/v1` vector with an independently measured digest |
| E3 Definition and compilation | `pkg/nvkr/definition`, `pkg/nvkr/factory` | Executable `platforms` admission; per-platform native closures plus one index package node; Plan and artifact-graph membership |
| E4 Packaging | `pkg/nvkr/delivery` | Child image and layer encoding, semantic record and index assembly; byte-stable fixtures; held verification rejecting extra or missing files, non-regular entries, wrong platforms and NVKR child media types |
| E5 Evidence and publication | `pkg/nvkr/factory`, `pkg/nvkr/delivery`, `pkg/nvkr/releasemanifest` | Index and child signatures and provenance; child SBOM referrers; PublicationRoot and Release binding; reader rejection of the unknown kind |
| E6 Consumption | `pkg/nvkr/delivery`, `pkg/nvkr/lifecycle` | `release pull` per-platform projection; artifact verification and retention for index outputs |
| E7 Qualification and smoke | `pkg/nvkr/qualification`, `pkg/nvkr/lifecycle` | Per-child native smoke from the child's files; until it lands, index outputs fail qualification admission |
| E8 Generation | `pkg/nvkr/factory` GitHub emitter | Per-platform native jobs and the index job on default hosted routing; `run generate --check` is byte-stable |
| E9 AICR migration | `inputs/` | §9, with the ARM64 Build Digest preservation test and regenerated nvke-core bundle |

## Consequences

**Positive**
- One source and one lock transaction per tool release; platforms cannot drift apart.
- One named output per executable, published in a form that Docker, BuildKit, crane, ORAS and Kubernetes
  already consume, with the platform chosen by the client.
- Native producer identity, results and evidence are unchanged, so existing guarantees carry over per child.

**Negative / costs**
- A second executable publication format beside the Binary Bundle until single-platform outputs migrate
  (open question 5).
- A new lock field, a new aggregate projection schema and a new release metadata output kind, each rejected by
  older readers.
- Retrofits rename consumer-facing outputs and must move every downstream reference with them.

**Neutral**
- Disk producers keep binding native producer results; the index is publication packaging only.
- `evidence: none` tools stay unverified; packaging them as images adds no upstream authenticity.

## Alternatives considered

- **Keep bare files with a platform list.** One source, but per-platform Binary Bundles, or one bundle holding
  every platform under `<os>-<arch>/`. No new format, but consumers still choose by name or path, every
  download carries every platform, and standard image clients still cannot consume it. Rejected.
- **ORAS artifact index.** An index over artifact manifests with an NVKR `artifactType` and one titled layer per
  file. `oras pull --platform` would write files without `tar`. Rejected: BuildKit `COPY --from` needs an image
  config, and Kubernetes only guarantees image volumes for the types the container image field accepts. The
  titled image layer keeps ORAS usable.
- **Children as Binary Bundles under an index.** Keeps the NVKR config and layer media types that standard image
  clients refuse. Rejected.
- **Upstream-published index (ask AICR to publish one).** No NVKR packaging, and AICR would sign it. Not a
  substitute: most pinned tools (Helm, kubectl, kind, clusterctl, ORAS, Syft, Packer) publish no such index,
  NVKR still republishes into its release namespace with its own signatures and SBOMs, and the companion
  question remains. Where an upstream does publish one, the existing `prebuilt.image` path can acquire it
  (open question 7).
- **Runnable images with a base layer.** `docker run` would work, but a base image adds identity, CVE surface
  and a runtime claim NVKR cannot check. Consumers that want a runnable image build one from the index.
- **Reuse `nvkr.dev/image-index-build/v1`.** Rejected: it admits one component per child.
- **Keep one source per platform, aligned by a `SameRelease` maintenance group.** Keeps the duplication and
  makes alignment an invocation option rather than a catalog property. Rejected.
- **Install path such as `/usr/local/bin` in the layer.** Root placement keeps image-volume mounts and
  `COPY --from` short, and the semantic record keeps the existing `/usr/local/bin/<name>` install suggestion
  (open question 4).

## Open questions

1. **Companion form.** This decision keeps companions in the payload and makes only verified upstream evidence a
   referrer (§7). Deciders may prefer referrer-only companions once consumers fetch referrers; AICR's
   `--attest` would then need the consumer to place the file.
2. **Architecture spellings.** Whether to add a declared spelling map (`x86_64`, `aarch64`) or keep one source
   per platform for such upstreams.
3. **Darwin children.** Publishing `darwin/arm64` children would require lifting ADR-12 §1's exclusion of Darwin
   from hosted execution, signing and publication.
4. **Layer path.** Root placement or a conventional `/usr/local/bin`.
5. **Retiring the Binary Bundle.** Whether single-platform outputs should later publish one-child indexes by
   default, leaving one executable format.
6. **Image-volume execution.** Whether current containerd and CRI-O mounts permit executing from an image volume,
   to be checked before the guide documents in-place execution.
7. **Upstream indexes.** Whether to ask AICR to publish its own multi-platform image and acquire that instead.

## Follow-ups

- **ADR-12 §1:** on acceptance, admit `platforms` for executable outputs and remove the exclusion sentence.
- **ADR-22:** on acceptance, move §1–§2 into "Tool release assets" and lock v1. Per-platform `companions`
  depend on #286 (PR #294) landing first.
- **ADR-01/02:** add `nvkr.dev/executable-index-build/v1` beside the image-index projection.
- **ADR-20:** add the executable index publication form, its metadata binding and its `release pull` projection.
- **ADR-13:** child SBOM referrers; verified upstream evidence referrers once #287 lands.
- **ADR-27 §3:** replace the separate-source rule with per-platform lock entries.
- **Retrofit:** #297 tracks §10.

## Acceptance evidence

None yet. No executable index has been built, published, signed or consumed.
