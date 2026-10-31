# ADR-27 — Producers that run pinned release tools

- **Decision status:** Proposed (design only; pending named-decider ratification)
- **Implementation maturity:** Designed, not implemented. No `pinned-tool` builder type, catalog form, IR
  field, executor or provenance mapping exists; see `reconciliation.md`.
- **Acceptance-test maturity:** None. The implementation units below name the contract suites that must pass;
  no contract test, generated bundle, hosted Run or consumer verification exercises a pinned-tool producer.
- **Deciders:** Pending; release-engineering, catalog, execution and evidence owners must be named before
  ratification.
- **Index:** Proposed ADR-27 supplements ADR-01–22 and proposed ADR-23/24/26; it remains outside the consolidated
  contract set until accepted. It coordinates with two separately drafted decisions that are not on this branch:
  the generic OCI bundle decision (#239, drafted as ADR-25) and producer OCI layout import (#285).
- **Relates to:** ADR-01 (typed digest families), ADR-02 §3 (logical identity versus transport), ADR-03 (coded
  errors), ADR-09 §5 (production provenance), ADR-10 §1 and source lanes (builder taxonomy, `wrapped-build`),
  ADR-11 §1–§2 (direct selection, explicit closures, declared edges and independent outputs), ADR-12 §1 (concrete
  targets), ADR-13 §2 (`evidence: none`), ADR-16 §5 (evidence lanes), ADR-18 §1, §3 and §5 (capability admission,
  helper-owned isolation, hermetic execution), ADR-22 (tool release assets).
- **Scope:** Catalog producers whose output is made by running one released, digest-pinned executable offline,
  with no shell, fixed arguments and declared inputs. `static-unqualified` local execution and the
  `pipeline-attested` hosted lane are designed. Qualified execution, directory outputs, companion-file staging,
  networked tool modes and tool-side signing are deferred and fail closed.

## Context

Some outputs are produced by running a released tool rather than by compiling source. AICR deployment bundles
are the first case: `aicr recipe` resolves criteria into a recipe, then `aicr bundle` renders that recipe for one
deployer. AICR already ships signed release binaries.

NVKR cannot express this today. Build nodes take only source fetches and other component outputs as inputs, and
pinned tools are acquired only run-wide, as the `sbomTool` scanner and `cosignTool` signer. Those `tool/<name>`
acquisitions sit in the factory execution graph, outside the logical IR, so they never enter Build identity. As a
result the #239 bundle producers build `aicr` from source at an unreleased commit inside a `bash -c` script,
render, and repack the result with GNU tar. The rendered bytes then depend on a compiled binary NVKR does not
otherwise publish, and NVKR keeps its own archive format beside AICR's.

ADR-10 §1 already requires any byte transformation to be a build node with a builder type, pinned
transformation inputs and profile-bound output; it cannot hide inside `prebuilt` acquisition. ADR-22
already pins release tool archives and their selected executable by exact digest. This decision joins the two.

### Released AICR v1.1.2 behavior

These facts come from the v1.1.2 release assets and a review of the source at that tag (commit
`2bf662b86c21a97b6e79e90035382ea252a44798`); the binary was not executed for this decision. They constrain
the first use; the contract below is not AICR-specific.

| Fact | Consequence |
|---|---|
| `aicr_1.1.2_linux_arm64.tar.gz` (sha256 `7898cd20…`, matching `aicr_checksums.txt`) holds `aicr` (sha256 `0ed855fd…`, a static ELF executable), `aicr-attestation.sigstore.json` and `recipe-catalog.sigstore.json` | The existing tool pin selects `aicr` by archive and executable digest |
| The recipe catalog is embedded in the binary; `aicr recipe` reads no network or cluster unless a snapshot, recipe or config input is a URL or `cm://` | A recipe renders offline from fixed arguments |
| `aicr recipe --output <file>` writes one key-sorted YAML file; source review found no timestamp or random value, and it embeds the CLI version | A single-file output; the version is covered by the executable digest |
| No command goes from criteria straight to a bundle; `aicr bundle` requires `--recipe` | Recipe and bundle are separate invocations |
| `aicr bundle` writes either a local directory, in place and without staging, or pushes `oci://` to a registry; the OCI layout is built in `$TMPDIR` and deleted after the push | v1.1.2 has no offline layout output; NVIDIA/aicr#3534 adds one |
| Rendering needs no network by default for any deployer; `--vendor-charts` runs `helm pull`, `--attest` needs OIDC, Fulcio, Rekor and TUF refresh, and `oci://` pushes | Network denial rules these modes out without NVKR parsing tool flags |
| Source review found no timestamp or random value in rendered files, but file modes follow the process umask | The executor fixes umask; output digests are observed, not assumed reproducible |
| `aicr bundle --attest` looks for `<executable>-attestation.sigstore.json` beside `os.Executable()`; no other CLI path reads it | An offline producer never needs the attestation file |

## Decision

### 1. A pinned-tool producer is a catalog component that runs one released executable

A component with `builderType: pinned-tool` names a catalog source whose ADR-22 `tool` declaration and lock
record supply the executable. It has no source fetch, vendor step or source mount. It is a build, not an
acquisition: its output is new bytes made by running the tool.

```yaml
sources:
  aicr:
    repo: https://github.com/NVIDIA/aicr
    constraint: 1.1.x
    tool:
      format: tar-gzip
      url: https://github.com/NVIDIA/aicr/releases/download/{{.Tag}}/aicr_{{.Version}}_linux_arm64.tar.gz
      platform: linux/arm64
      executablePath: aicr
components:
  aicr-gb300-generic-training-recipe:
    builderType: pinned-tool
    tool: aicr
    toolchainImage: <image>@sha256:<digest>
    platforms: [linux/arm64]
    outputType: file
    artifactPath: recipe.yaml
    toolArgs: [recipe, --service, generic, --accelerator, gb300, --os, ubuntu, --intent, training,
      --output, '{{.Output}}/recipe.yaml']
  aicr-gb300-generic-training-flux:
    builderType: pinned-tool
    tool: aicr
    toolchainImage: <image>@sha256:<digest>
    platforms: [linux/arm64]
    outputType: oci-layout
    materials:
      recipe: aicr-gb300-generic-training-recipe
    toolArgs: [bundle, --recipe, '{{.Material "recipe"}}', --deployer, flux,
      <aicr#3534 layout flag>, '{{.Output}}']
```

Field names are proposed; the catalog owner fixes them at implementation. The contract is:

- **Exactly one tool invocation per producer.** A producer runs the pinned executable once with a fixed argument
  vector. A multi-step tool flow is a chain of producers joined by material bindings (§4), never an ordered step
  list, shell script or wrapper inside one producer.
- **No shell.** `toolArgs` is the argument vector after the executable. The executable is always the pinned tool;
  a producer cannot name another program, interpreter or entrypoint.
- **Typed tokens only.** `{{.Output}}` resolves to the private output directory and `{{.Material "<binding>"}}`
  to a declared material's read-only staged path. The existing `{{.Arch}}` target token remains available.
  Tokens are rendered by the compiler into fixed sandbox paths, so the rendered vector is identity-bearing and
  contains no host path. Any other template syntax, an unknown binding, or a binding with no token fails catalog
  loading.
- **Literal environment only.** A producer may author literal environment variables as ordinary identity-bearing
  inputs. Credentials, credential references and Environment-supplied values are not admitted.
- **Catalog owns producers.** A Definition selects pinned-tool components by name exactly as it selects any
  producer (ADR-11 §1). There is no Definition-level tool, recipe, criteria or argument field.

### 2. Logical identity covers the tool, its arguments and its inputs

A pinned-tool producer lowers to an IR `KindBuild` node with `builderType: pinned-tool`. ADR-10 §1 is extended:
`pinned-tool` is the one source-less builder type. Its build-semantic fields are:

| Field | Identity |
|---|---|
| Tool platform, archive format, selected member path, archive digest, executable digest | Logical; any change is a new Build Digest |
| Tool release URL | Transport (ADR-02 §3); excluded from Build Digest, retained in the Plan |
| Toolchain image digest and target platform | Logical (ADR-10 §4, ADR-12 §1) |
| Rendered argument vector and literal environment | Logical |
| Material bindings, each with its exact producer edge | Logical, as for VM materials |
| Output type and artifact path | Logical |
| Execution requirements and binding | Plan metadata, outside Build Digest (ADR-02) |

The logical projection stays `nvkr.build` (ADR-01); no digest family is added. The IR carries the tool pin's
digests itself. The factory reuses the existing `tool/<name>` acquisition node for the bytes: it is compiled
once per tool name, and every selection of that name, run-wide or producer, must carry the identical pin, as
the current run-wide selector already requires. The producer node takes that acquisition as an explicit input.
The executor re-hashes the held executable against the IR's executable digest before launch; the acquisition's
own check never substitutes for that comparison.

Equal Build Digests still do not authorize sibling reuse (ADR-11 §2). Two outputs rendered by the same tool and
arguments remain independently required uses with their own producer, observation and evidence.

### 3. Execution: offline, isolated, fixed

A pinned-tool node runs through the shared helper launcher as an ordinary build node (ADR-18 §3, §5):

- **Network denied.** The node's network mode is always `none`. A pinned-tool component cannot author
  `buildNetwork`, and an Environment cannot grant it. Tool modes that need network, such as registry push, chart
  vendoring, keyless signing or URL inputs, fail inside the sandbox and fail the producer; NVKR does not parse or
  allowlist tool flags.
- **Pinned container.** The tool runs inside the component's digest-pinned toolchain image for the target
  platform. The image is part of Build identity, so a static executable does not make its execution root
  invisible to identity.
- **Platforms agree exactly.** The tool pin's platform must equal the producer's target platform and its
  toolchain platform. There is no emulation, cross-platform fallback or inferred alternative pin. A second
  platform needs a separate source name with its own tool pin (ADR-22).
- **Inputs are exact and read-only.** Only the digest-checked executable and the declared materials are staged,
  read-only, at fixed paths. Other members of the tool archive, including companion files, are never staged
  (§6).
- **Fixed, credential-free environment.** The executor fixes the working directory, a private empty `HOME` and
  `TMPDIR`, `TZ=UTC`, `LC_ALL=C` and umask `022`, adds the producer's literal variables, and passes no ambient
  credential, token, registry configuration, kubeconfig or cache.
- **Bounded and owned.** The node honors its execution requirements' wall-time budget and the caller's context;
  cancellation terminates and joins the child before returning.
- **Linux only.** Held executable capabilities are Linux-only (ADR-22); other hosts fail admission.

### 4. Declared inputs are typed material bindings

A pinned-tool producer's only inputs besides its tool are other component outputs bound through the existing
`materials` map. Each binding names a component in the same output's selected producer set and becomes an exact
IR producer edge. The executor stages each bound output only after authenticating its producer result and
observed digest, at the fixed path its `{{.Material}}` token resolves to.

As for disk outputs (ADR-11 §1), the Definition selects the root producer and its complete explicit material
closure; resolution never adds an unselected component. Each material must be one regular-file artifact; OCI
layouts and directory payloads are not admitted as materials. A material that is missing, unselected, bound
twice, produced for another platform or not a regular file fails before work.

Authored files from the Definition repository, remote inputs and Environment-supplied inputs are not admitted.
Adding them needs its own content-addressed input decision.

### 5. Outputs: one file or a producer-written OCI layout

The producer's private output directory must contain exactly one declared form when the tool exits successfully:

| `outputType` | Admission | Use |
|---|---|---|
| `file` | Exactly one regular file at `artifactPath`; nothing else in the output directory | A material for another producer in the same output |
| `oci-layout` | Exactly one OCI image layout at the output directory root, with nothing beside it; shape checks belong to the layout import owner (#285) | The payload of a `bundle-oci` output, imported byte-for-byte rather than rewrapped |

Directory trees, archives NVKR would have to create, symbolic links, special files and extra entries are
refused. NVKR never archives or normalizes a tool's directory output: doing so would create a second definition
of a format the tool already owns and an artifact digest that differs from the tool's own publication.

Publishing a `file` result as its own named output needs an output format that admits it; this decision adds
none. An `oci-layout` result reaches a Release only through the `bundle-oci` format, whose layout import (#285)
checks the manifest, config, layer and `artifactType` allowlist, and applies the bounded archive admission before
any output work. Existing producer staging gives no partial output: a failed or nonconforming run publishes no
artifact or producer result.

### 6. Tool evidence, companions and tool-side signing

The tool is an acquired executable. Its own evidence follows its catalog declaration: `evidence: none` (ADR-13
§2) until upstream verification for it exists. Verified Sigstore evidence read from the tool archive is proposed
separately (#287). Recording `evidence: none` never satisfies an evidence-required gate, and running the tool
makes no claim that NVKR built or verified it.

Companion files that the tool pin may declare (#286, an ADR-22 change) serve delivery of the tool as an
executable output. A pinned-tool producer never stages them. In AICR v1.1.2 the only reader of the attestation
companion is `aicr bundle --attest`, which needs network for keyless signing and so cannot run here. Staging a
companion for a future offline mode needs an explicit, digest-bound declaration and is deferred.

Tool-side signing or attestation is not admitted. Outputs are signed and attested by NVKR's own production
signing (ADR-04, ADR-20), which binds the exact published digest.

### 7. Lane, provenance and Release membership

A pinned-tool producer belongs to the existing `wrapped-build` source lane: a pinned external entrypoint, here
an upstream release executable, inside a pinned container with declared network (ADR-10). It adds no lane or
build-type vocabulary.

- **Production provenance (ADR-09 §5).** Any pinned-tool node makes the plan-level `buildType` `wrapped-build`
  under the existing weakest-lane rule. `resolvedDependencies` records the tool exactly as a selected release
  executable: the transport URL bound to the archive digest, plus a separate logical `nvkr:tool/<name>` URI with
  `archive` and `member` parameters bound to the executable digest. The toolchain image is recorded as for other
  builds. Material producers are already plan members and are not duplicated as external dependencies.
- **Release membership.** Each pinned-tool output's member records source lane `wrapped-build`.
- **Evidence lanes (ADR-16 §5).** `static-unqualified` for local and dry-run use; `pipeline-attested` for hosted
  GitHub Runs. A matching tool digest or identical rendered bytes never turns a pinned-tool output into a
  reproduction.

### 8. Capability admission

Admission runs before tool acquisition, staging, subprocess or output work.

| Use | Admission |
|---|---|
| `definition validate`, `run plan`, `run graph`, `run diff`, dry-run | Supported once implemented; `static-unqualified` |
| `run local` on Linux | Supported once implemented; `static-unqualified` |
| `run local` on another host | `engine-capability`; held executable capabilities are Linux-only |
| `run generate`, hosted GitHub Run | Supported once implemented; `pipeline-attested`, on ADR-18 §3 default hosted routing |
| Native ARC disk route | Not admitted; ADR-18 limits it to disk producers |
| Qualified execution, `run check-artifacts`, `run qualify` over a pinned-tool output | Deferred; `engine-capability` |
| Directory output, companion staging, authored-file inputs, networked tool modes | Deferred; `engine-capability` or catalog rejection per §11 |

### 9. AICR: what v1.1.2 supports and what waits

The AICR pin that #286 adds for the executable output is the same pin the bundle producers name. With v1.1.2:

- A recipe producer can be declared, compiled, executed offline and observed. Its `file` output is admitted only
  as a material.
- A bundle producer cannot yet satisfy §5. v1.1.2 has no offline layout output, so a bundle invocation either
  leaves a directory tree, refused with `build-failed`, or attempts a registry push, which network denial stops.
  Bundle producers wait until the catalog pins an AICR release that writes a local OCI layout
  (NVIDIA/aicr#3534).

Shipping AICR bundles (#288) therefore needs that AICR release, layout import (#285) and this decision's
implementation. When it ships, `bundle-oci` outputs select the bundle producer and its recipe producer as an
explicit material closure (§4). This adjusts the #239 draft's single-component `bundle-oci` selection, which
the layout-import change revises. Bundles rendered this way carry no AICR in-bundle attestation (v1.1.2
`aicr verify` reports such a bundle as unverified); their authenticity comes from NVKR's signature and evidence.

### 10. Determinism and bounds

The tool's output is observed, not predicted. Pipeline-attested Runs record the produced digest without claiming
that re-running the tool reproduces it. The executor removes the environment hazards the first tool exposes:
fixed umask, time zone, locale, private `HOME` and `TMPDIR`, and no network. Output bounds are those of the
consuming owner: a `file` material inherits the existing producer-result and held-file bounds, and an
`oci-layout` inherits the layout import and archive admission limits. Qualification would additionally need
independent exact-byte reproduction; that is deferred with the rest of the qualified lane.

### 11. Coded errors

Pinned-tool producers reuse existing ADR-03 codes.

| Condition | Code |
|---|---|
| Malformed pinned-tool component; unknown token or template; binding without token or token without binding; unselected, duplicate or cross-platform material; authored network request | `invalid-catalog` |
| Named tool source has no tool declaration or lock record; tool pin out of date with its declaration; two selections of one tool name with different pins | `unpinned-source` |
| Tool, target and toolchain platforms differ; non-Linux execution host; deferred output form, lane or input kind | `engine-capability` |
| Held executable or staged material differs from its pin or observed digest | `verify-failed` |
| Tool exits nonzero, or the output directory does not hold exactly the declared form | `build-failed` |
| Producer result or provenance omits the tool dependency | `provenance-missing` |
| Cancellation or wall-time expiry | `timeout` |

## Implementation plan

Each unit lands with table-driven contract tests that assert the coded error, not just failure.

| Unit | Owner | Contract |
|---|---|---|
| T1 Catalog form | `pkg/nvkr/catalog` | `pinned-tool` component schema, tool reference to a pinned source, token grammar, material validation, rejected fields (`buildNetwork`, source, vendor, patches) |
| T2 IR and identity | `pkg/nvkr/ir`, `pkg/nvkr/buildgraph` | Source-less `KindBuild` validation; tool digests and member in the logical node, URL only in transport; Build Digest changes for every logical field in §2 and not for transport |
| T3 Factory dataflow | `pkg/nvkr/factory` | Producer-selected tools compile to the shared `tool/<name>` node; one-pin-per-name; platform agreement; explicit tool and material inputs |
| T4 Executor | `pkg/nvkr/pipeline` | Held executable re-hash before launch, read-only staging, fixed environment and umask, network `none`, exact output-form admission, no partial output, cancellation join |
| T5 Provenance and membership | `pkg/nvkr/factory`, `pkg/nvkr/provenance`, `pkg/nvkr/releasemanifest` | `wrapped-build` plan type and member lane; tool transport and `nvkr:tool/<name>` dependencies; finalization rejects a missing or altered tool dependency |
| T6 Layout handoff | `pkg/nvkr/delivery` (#285) | `oci-layout` results feed the layout import with no NVKR rewrap |
| T7 Generation | `pkg/nvkr/factory` GitHub emitter | Pinned-tool nodes render on default hosted routing; `run generate --check` is byte-stable |

## Consequences

**Positive**
- Released tools run as pinned producers without being rebuilt from source or wrapped in shell scripts.
- Build identity covers the exact tool bytes, arguments, inputs and execution image.
- NVKR stops owning a second copy of formats a tool already defines, and published digests can match the tool's.

**Negative / costs**
- A source-less builder type extends ADR-10's taxonomy, and `KindBuild` consumers that assume a source fetch
  must admit it.
- Plans with any pinned-tool producer are `wrapped-build`, the weakest production lane.
- AICR bundles wait for an upstream release with layout output, and the recipe becomes a separately selected
  component.

**Neutral**
- Run-wide `sbomTool` and `cosignTool` acquisition is unchanged. A tool used both ways shares one acquisition and
  one pin.
- Delivering the tool itself as an executable output remains the separate releaseExecutable path (#286).

## Alternatives considered

- **Archive the tool's directory output in NVKR.** Rejected: NVKR would own a second bundle format and its
  artifact digest would differ from the tool's own push, the problem #285 removes.
- **Run a registry inside the sandbox to capture `oci://` output.** Rejected: it adds a networked service to an
  offline build.
- **Ordered steps in one producer.** Rejected: the intermediate recipe would never be observed or bound to
  identity, and it reopens a scripting surface.
- **A new `pinned-tool` lane and build type.** Rejected for now: more precise, but it changes provenance, the
  release manifest, qualification member maps and consumers for no stronger claim. `wrapped-build` already
  describes a pinned upstream entrypoint.
- **Label the output `hermetic-build`.** Rejected: it would claim that NVKR built the executed code from source.
- **A new IR node kind.** Rejected: every `KindBuild` consumer would need a parallel case. ADR-10 §1 already makes
  any transformation a build node with a builder type.
- **Model the tool as an IR pull node.** Rejected: it duplicates the `tool/<name>` acquisition.
- **Run the tool in the Environment's utility container.** Rejected: that image is outside Build identity, so
  the execution root of a producer would be invisible to identity.

## Follow-ups

- **ADR-10:** on acceptance, fold `pinned-tool` into §1's builder taxonomy and the source-lane table.
- **ADR-11:** on acceptance, add pinned-tool material edges beside VM material edges as declared edges in §2, and
  the pinned-tool root and material closure beside the disk closure in §1.
- **ADR-22:** tool companion files (#286) stay delivery-only for producers; any producer staging needs this
  decision's deferred digest-bound declaration.
- **ADR-13:** once #287 lands, record verified tool evidence where a tool pin carries it.
- **#239 and #285:** move AICR bundles from source-built producers to pinned-tool producers with an imported
  layout, and select the recipe material closure.
- **#288:** pin an AICR release containing NVIDIA/aicr#3534 and ship the bundles.

## Decisions recorded

The design questions left open by the issue were decided on 2026-10-30:

1. Outputs: a single file or a producer-written OCI layout imported through #285; directories are refused, and
   AICR bundles wait for an AICR release containing NVIDIA/aicr#3534 (§5, §9).
2. Steps: one tool invocation per producer; the recipe is its own component, consumed by the bundle producer
   through a typed material binding (§1, §4).
3. Lane: the existing `wrapped-build` lane per ADR-10 (§7).
4. IR: `KindBuild` with the source-less builder type `pinned-tool`, carrying the tool pin digests, arguments,
   materials and toolchain image; the executor re-checks the executable digest (§2).

## Acceptance evidence

None yet. The implementation units above are the contract target. No pinned-tool producer has run, and no claim
follows from the #239 source-built AICR renders.
