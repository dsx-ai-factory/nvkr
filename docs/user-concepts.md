# NVKR concepts

A compact reference to the 34 concepts used in NVKR's user workflow. Follow the linked guides for examples
and commands. Start with the [quickstart](quickstart.md) if NVKR is new to you.

## Public workflow

**Definition → Run → Release** connects requested work to observed results and published bytes.
Users author Definitions, Environments, and catalogs. NVKR generates Plans and graphs for inspection.

| Public object | Question it answers |
|---|---|
| Definition | Which named outputs must be delivered, using which components, platforms, and formats? |
| Environment | How will the work execute, where will it publish, and which signing settings apply? |
| Run | What happened in one attempt, and did every required output complete? |
| Artifact | Which exact bytes were produced or acquired? |
| Release | Which artifacts and evidence belong to this immutable publication? |
| Verification Policy | Which identities and claims will the consumer accept? |

Deployment topology, node placement, and operational acceptance belong to downstream systems.
Choose a [task guide](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/README.md#user-tasks) for commands or use the concept list below to look up a term.

## Understand the inputs

| Concept | Meaning | Read more |
|---|---|---|
| Definition, Run, Release | Declare deliverables, record one attempt, and publish exact artifacts with their evidence. | [Lifecycle](lifecycle.md) |
| Catalog | Reusable producer declarations in `registry.yaml`, loaded with their machine-managed `pins.lock.yaml`. | [Catalogs](catalogs-and-components.md) |
| Source | A named upstream repository and release policy used by one or more producers. | [Shared sources](catalogs-and-components.md#share-sources-with-explicit-roles) |
| Component producer | Instructions to build or acquire the bytes behind an exact catalog component key. Catalog presence makes it available; an output must select it. | [Components](catalogs-and-components.md) |
| Definition and output selection | Every output declares its selected component keys, concrete platform, and delivery format. | [Components](catalogs-and-components.md) |
| Materials and dependencies | A producer's `materials` map connects an input label to an exact component key. Changing a selection does not redirect that map. | [Node images](node-images.md) |
| Environment | Execution routes, runtime, registry destination, credential references, and signing settings. Build tools remain catalog-owned inputs. | [Lifecycle](lifecycle.md) |

## Compose and maintain

| Concept | Meaning | Read more |
|---|---|---|
| Exact pins | Recorded commits, tree identities, timestamps, and acquired-byte digests freeze selected inputs. Tags help select a version; they are not its immutable identity. | [Catalog pins](catalogs-and-components.md#author-declarations-generate-pins) |
| Coordinated pin sets | An explicit mapping derives several input versions from one pinned upstream file. Update derived members through their owner. | [Pin-set example](generic-pin-set-example.md) |
| Catalog overlays | Replace complete producer instructions under the same component key, bound to the exact preceding catalog digest. Existing references retain that key. | [Replacement examples](catalogs-and-components.md) |
| Selection and replacement scope | Select a different component key, or apply a replacement through the default or an output-specific catalog stack. Each choice has a distinct effect. | [Replacement examples](catalogs-and-components.md) |
| Toolchain substitutions | Replace a build container for explicit components and an architecture, checking the expected old digest. Sources and commands keep their existing declarations. | [Build tools](catalogs-and-components.md#build-tools-and-version-stamps) |
| Version stamps | Render values from an exact source pin into build inputs, such as a CLI's version and commit string. | [Version stamps](catalogs-and-components.md#build-tools-and-version-stamps) |
| Helm image bindings | Map a chart's declared image slot to a Definition image output, then put its exact digest at the catalog-declared chart value path. | [Helm bindings](declarative-helm-bindings.md) |
| Catalog maintenance | Preview eligible pin changes and apply them with matching generated files for review. Rendering a maintenance workflow does not schedule it. | [Catalog maintenance](catalog-maintenance.md) |

## Plan and produce

| Concept | Meaning | Read more |
|---|---|---|
| Plan | The generated, canonical record of selected inputs, operations, artifacts, and required outputs. It describes work rather than proving execution. | [Plan inspection](plan-and-graph-inspection.md) |
| Plan graph views | `producers` shows producer dependencies; `artifacts` shows operation inputs and results; `complete` joins both views. | [Graph inspection](plan-and-graph-inspection.md) |
| Plan comparison | Compare saved canonical Plans to explain changed outputs and settings. A changed registry destination can change the Plan without changing build identity. | [Plan comparison](plan-and-graph-inspection.md) |
| Generator adapters | Translate the planned workflow into provider files. GitHub is the maintained hosted target; unsupported providers are rejected. | [Pipeline generation](generate-pipeline.md) |
| Generated pipeline files | A workflow, canonical Plan, and manifest of generator-owned files. Regenerate them from authored inputs and check exact drift. | [Pipeline generation](generate-pipeline.md) |
| Builder types | Mechanisms such as `oci-hermetic`, `buildkit-rootless`, `vm-image`, and `nix` describe how to produce bytes, independently of delivery format. Each has runtime and evidence limits. | [Build tools](catalogs-and-components.md#build-tools-and-version-stamps) |
| Run and completeness | One attempt must account for every exact named output. Matching Build Digests do not let one successful output satisfy a missing sibling. | [Lifecycle](lifecycle.md) |
| Artifacts and identities | Build Digest identifies selected build inputs; manifest digest identifies an OCI package document; payload digest identifies a file's bytes. | [Release quickstart](release-quickstart.md) |
| OS-image output | One disk-producing root plus every explicitly selected transitive material producer. The source-owned builder assembles those inputs into the delivered disk. | [Node images](node-images.md) |

## Publish and verify

| Concept | Meaning | Read more |
|---|---|---|
| Evidence lanes | `static-unqualified` describes previews; `pipeline-attested` uses hosted production evidence; `qualified` has stronger reproduction and authentication requirements that remain unavailable in the public acceptance path. | [Implementation scope](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md) |
| Software inventories and SBOMs | A software bill of materials describes software for an exact subject and stage. It is not a vulnerability verdict; disk build provenance is not a full disk inventory. | [Dependency SBOMs](dependency-sboms.md) |
| Production provenance | A subject-bound statement describing the production process and inputs. It answers a different question from an inventory or a signature. | [Publication architecture](publication-architecture.md) |
| Signing and attestations | Authenticate the signer of an exact artifact or statement. Keyless GitHub signing uses a workflow identity and OIDC issuer; consumer policy chooses which are accepted. | [Release quickstart](release-quickstart.md) |
| Release publication | One digest-pinned entry point connects selected output artifacts to their evidence. Use the exact publication root emitted by the Run. | [Publication architecture](publication-architecture.md) |
| Release browsing | Inspect metadata, identities, subjects, and evidence memberships without downloading artifact payload layers or verifying signatures and policy. | [Release quickstart](release-quickstart.md) |
| Verification Policy | Consumer-owned identity, issuer, lane, and scope requirements. `authenticity` checks signatures and evidence without artifact payloads; `artifact-publication` includes payload integrity. Neither establishes deployment readiness or full release qualification. | [Release verification](lifecycle.md) |
| Go SDK | `client/v1` exposes Definition, Run, and Release operations to applications. It preserves the CLI's platform, tool, and policy requirements. | [SDK lifecycle](https://github.com/dsx-ai-factory/nvkr/blob/main/client/v1/lifecycle.go) |
| Errors and failed requirements | Stable `NVKR-E-*` codes identify invalid inputs, drift, missing capabilities, and failed verification. Unsupported requirements fail closed. | [Error contract](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/0003-coded-error-and-exit-code-contract.md) |
| Supported capabilities | Support depends on the chosen operation, producer, platform, runtime, and evidence scope. Contract tests and recorded external runs establish different facts. | [Implementation and evidence](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md) |

For executable bundles, the component selection chooses which files are packaged together. For disk images,
today's list explicitly repeats the root and dependency membership, and NVKR checks that it agrees with the
catalog's material graph. Neither list order nor similar names tell the builder how to assemble its output.
