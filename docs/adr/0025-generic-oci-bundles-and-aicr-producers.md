# ADR-25 — Generic OCI bundles and AICR producers

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation/README.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation/README.md`; local contracts and rendering do not
  establish hosted publication, deployment compatibility or hardware acceptance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-25, extending the consolidated NVKR ADR set.
- **Relates to:** ADR-01/02 (identity), ADR-11/12 (selection and targets), ADR-13 (evidence), ADR-17/19
  (lifecycle and downstream ownership), ADR-20 (publication).
- **Scope:** Unqualified local execution and the existing pipeline-attested GitHub publication lane.
  Qualified bundle reproduction, deployment and air-gap dependency closure remain deferred.

## Context

AICR renders accelerated-runtime recipes into several deployment formats. An Argo CD application directory,
a collection of Helm values and scripts, and a Helm chart are different deliverables. The previous AICR
integration mislabeled rendered trees as Helm OCI, introduced dedicated profile inputs, and never completed
its qualified publication boundary. Those removed schemas and the retired `nvkr.aicr-bundle` digest domain
must retain their old meaning.

The concrete consumer now needs immutable AICR bundles for on-premises accelerated systems, including
GB300 and preview Vera Rubin selections. This requires a generic artifact format within Definition → Run →
Release, without a new public lifecycle object or a hardware-deployment taxonomy.

## Decision

### 1. Ordinary pinned producers, one explicit archive output

`format: bundle-oci` selects exactly one ordinary source-build component with `outputType: tarball` and a
concrete supported platform. The producer emits a gzip-compressed tar archive. Catalog source commits,
toolchain digest pins, fetch/vendor instructions, renderer selection and complete build commands remain
output-affecting Build inputs under the existing graph identity. Environment owns runner, publication
destination and credentials. No `aicrProfiles`, special AICR producer, Definition recipe field, SKU enum,
classification or retired identity family is restored.

The initial AICR producers compile a pinned upstream release and render with explicit recipe coordinates
and deployer names. Each writes AICR's own OCI image layout (`aicr bundle --output oci-layout:<dir>`), requires
exactly one manifest referencing one config and one layer, verifies the unpacked bundle with `aicr verify`, and
emits that layer's exact bytes as its archive. NVKR does not repack AICR output, so the archive digest is the
layer AICR packaged. Layout output renders AICR's registry variant, whose Argo CD deployer requires a repository
URL. Build identity cannot take a publication destination from the Environment, so the catalog passes the
non-resolving literal `oci://registry.invalid/aicr-bundle` and consumers set their own `repoURL`. Importing
AICR's manifest unchanged through a pinned-tool producer, and dropping both this wrapper and the literal, is
the ADR-27 follow-up ([ADR-27](0027-pinned-tool-producers.md) §5 and §9).
Each selected renderer is an independent named output with its own required producer,
terminal observation and evidence use. Equal Build Digests cannot satisfy sibling outputs. Unsupported
NVKR formats, platforms, component selections and producer representations fail before source or output
work. Recipe criteria are ordinary producer arguments; the pinned AICR renderer validates them after
source preparation and compilation and refuses unknown values and criteria that no applied overlay covers,
without producing an artifact. A producer that names one exact leaf may also require it to be the recipe's
last applied overlay before bundling, so a later pin that resolves the coordinate differently fails.

The platform selects the admitted producer route; a bundle's declared platform is not an inspection of its
referenced deployment images or proof that a cluster or GPU is compatible. Upstream recipe metadata and
preview status are preserved and described by the maintained inputs. Missing upstream hardware recipes
are limitations, not permission to substitute a cloud service or claim bare-metal support.

### 2. Generic OCI representation and independent observation

Delivery wraps the exact archive in one OCI image manifest with:

| Field | Value |
|---|---|
| Manifest media type | `application/vnd.oci.image.manifest.v1+json` |
| Artifact type | `application/vnd.nvkr.bundle.v1` |
| Config | `application/vnd.oci.empty.v1+json`, exact `{}` bytes |
| One payload layer | `application/vnd.nvkr.bundle.layer.v1.tar+gzip` |

The layer's SHA-256 measures exact archive bytes. The public output digest identifies the OCI manifest;
publication metadata separately retains the archive's blob/semantic digest. Neither is logical Build
identity. No new structured digest family is necessary. The standard layout index is transport scaffolding,
and the published subject is the payload manifest.

The shared archive owner admits only nonempty gzip tar content with clean relative names, regular files
and directories: at most 512 MiB compressed, 512 MiB expanded tar bytes, 100,000 admitted entries and
4,096 UTF-8 bytes per path. Permission modes are limited to `0000`–`0777`. It rejects traversal, duplicate
paths, links, special files, conflicting file/directory ancestors, sparse files, unrecognized extended
metadata (including ACLs and xattrs), oversized expansion and trailing non-padding or compressed data.
Packaging and full payload inspection use the same rules. Inputs are held and matched to their observed digest before
packaging; lifecycle observation independently reconstructs the manifest from the exact named producer's
bytes and compares it with the reported output. Existing staged delivery commits preserve
failure behavior.

All renderers may use this generic format. AICR's `helm` output is a bundle of deployment inputs, not
implicitly an installable Helm OCI chart. `argocd-helm` is AICR's packaged chart archive, but transporting it
through `bundle-oci` still makes no native Helm media-type promise. `helm-oci` retains its existing
real-chart-only contract; any producer selecting it must actually emit the admitted chart representation.


### 3. Evidence and consumption

Every bundle receives a bounded `bundle-files/v1` CycloneDX inventory even without a selected Syft tool.
The built archive's regular files are recorded by relative path, exact hash, size and permission mode.
This internal reader executes no producer code and fetches no referenced chart, image, Terraform provider
or script dependency. It establishes file inclusion only. Serialized inventory validation rejects package
or runtime claims under this method, missing file bindings and an invented external scanner pin.

The pipeline-attested release evidence set retains separate source-build/source-archive and
packaging/bundle subjects. The final packaging inventory binds the manifest subject and the exact archive
inspection digest. Existing source provenance, terminal evidence, same-attempt completeness, signed SBOM
membership and policy-scoped consumer checks remain mandatory. Static local output is not a signed Release.

Release browsing/export describes the generic format without inventing image architecture or Helm chart
version from its contents. Full publication verification checks the archive. Diagnostic `release pull`
checks complete metadata and the selected payload, then writes `<output-name>.tar.gz` beneath a new
destination. It preserves bytes and does not execute, install or apply the bundle. Authenticity and
artifact-publication scopes retain their existing distinct policy requirements. Qualification policies
must explicitly admit future bundle testing; the new format does not acquire a blanket smoke exemption.

### 4. Upstream and hardware acceptance limits

The maintained AICR inputs pin the upstream repository and renderer implementation. Their README owns
exact available coordinates and rendering commands. Upstream preview recipes remain previews. Building
all admitted renderer outputs proves the recorded build/render scope only; it does not prove that Helm,
Argo CD, Flux, Terraform or Kubernetes accepts them or that a GB200, GB300 or Vera Rubin system operates
correctly with them. Downstream owners retain deployment topology, node placement, admission, runtime
validation, credentials and offline dependency completeness (ADR-19).

## Consequences

Future renderers can use the same artifact path by declaring another pinned tarball producer. Bundle
integrity, evidence and publication have one owner rather than a renderer-specific release bypass.
The archive is portable OCI content, but a consumer must extract and invoke the appropriate downstream
deployer. Rendering does not vendor referenced dependencies or establish air-gap readiness.

## Acceptance evidence

Contracts cover strict output/producer admission, deterministic OCI wrapping, bounded archive safety,
exact producer/output joins, always-on file inventories, staged evidence membership, metadata/full
inspection, and selected-output registry retrieval. Maintained input compilation and real upstream
rendering have separate recorded scope in reconciliation. Hosted signing/publication, standard client
interoperability and actual hardware/deployment acceptance require their own recorded external Runs.
