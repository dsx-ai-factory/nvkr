# Browse, export, and pull release outputs

`nvkr release browse` reads the bounded metadata and evidence of an exact release publication root and
shows its declared artifacts and memberships. `release export` writes that complete description as versioned
JSON for other tools. Both validate the complete metadata closure before writing a result, without downloading
or reading artifact payload layers. They can read a registry or an existing local OCI layout, and run on
macOS as well as Linux. Metadata inspection does not verify payload bytes, signatures or consumer policy;
use `release verify` for authenticated consumption.

## Start with the publication root

Use the release index reference emitted by publication, including its `@sha256:…` digest. Supply the full
registry and repository name. A tag, URL scheme, implicit registry, or an individual output manifest is not a
release publication root.

```sh
bin/nvkr release browse --reference "$RELEASE_REF"
```

The default text view shows the exact root and content identities, the recorded Definition and Environment
identities, and each named output's type, derived payload reference, Build Digest, components and evidence.
Executable, native-image and disk platforms come from their checked config metadata. They describe the
declared artifact platform, without observing payload bytes. Platform is omitted where the metadata provides
no platform declaration; it is never inferred from an output name.

The displayed qualification is the publication's declared value. Every format labels the result as metadata
inspection and states that payloads, signatures and policy are unverified. Successful browsing does not
establish an accepted Release, independent reproduction, or operational qualification.

Registry access uses the existing local credential keychain by default. Add `--anonymous` for a public
registry. `--plain-http` explicitly enables HTTP for a local test registry. Inspection has a ten-minute default
deadline; `--timeout 20m` changes it. A caller's earlier cancellation still wins. Registry downloads use a
private system temporary directory that is removed on success, failure, or cancellation. By default, browsing
does not write to the current working directory. With `--workspace`, only the private inspection child is removed; the
caller-owned workspace remains. Registry browsing fetches the release index, metadata manifests and configs,
and bounded evidence documents from their derived sibling repositories. It checks exact metadata digests,
sizes, schemas and memberships. Artifact payload layers, including container layers, executable archives and
disk images, are not downloaded.

## Browse interactively

Opt into the read-only terminal browser from an interactive terminal:

```sh
bin/nvkr release browse --reference "$RELEASE_REF" --ui=tui
```

The browser opens after the complete metadata inspection and starts with named outputs. Rows show the
artifact kind, recorded platforms or chart version when space permits. Details retain the complete values,
including component build subjects, aggregate image children and declared disk metadata. The metadata-only
scope stays visible; publisher declarations are not verification results.

- Use Up/Down to select an output and Enter to open Details.
- Press `/`, type a name, kind, platform, component, version or digest, then Enter to search the supplied
  outputs and evidence. Search is case-insensitive and matches a substring; for example, `linux/arm64`.
- Press `g` to switch between the output overview and all supplied entries when evidence is present.
- In Details, use Up/Down to select an evidence link and Enter to follow it. Esc returns to the previous
  selection. Shared evidence lists every bound output; links target only outputs included in the view.
- Press `?` for help, `q` then `y` to close, or Ctrl+C to close immediately. Closing preserves the command's
  result and performs no download, verification or publication action.

`--output` can restrict the supplied view to one exact output and its evidence. Interactive search and
navigation cannot widen that selection. `--layout` works with the same browser for a local OCI layout.
At least 40 columns and 12 rows are needed; smaller terminals pause browsing until resized. Add `--no-color`
or set `NO_COLOR` to disable colors.

Plain output remains the default. `--ui=auto` selects the browser only with eligible interactive input/output
and an empty `CI` environment variable. Explicit `--format json` or `--format mermaid` keeps its output in
auto mode and conflicts with `--ui=tui`. Unsupported terminal or conflicting explicit TUI requests fail
before inspection. See [command result navigation](lifecycle.md#browse-public-command-results) for shared keys.

## Focus on one output

```sh
bin/nvkr release browse --reference "$RELEASE_REF" --output nvkectl-linux-arm64
```

Selection is an exact output name. Unknown names fail with a coded usage error. Selection changes the display
after all release metadata and evidence memberships have been checked; it cannot hide invalid sibling
metadata or missing evidence. Payload integrity remains outside browsing's scope.
Shared evidence keeps its complete list of output memberships in text and JSON even when only one output is
shown. Evidence references identify the exact manifest under the derived owning output repository.

## Pull an output

Choose an exact output name from `release browse` and an explicit new destination directory:

```sh
bin/nvkr release pull --reference "$RELEASE_REF" \
  --output nvkectl-linux-arm64 --out ./nvkectl-linux-arm64
```

The selected output's format determines what appears directly beneath `--out`:

| Output format | Destination contents |
|---|---|
| `executable` | The bundle's named files with recorded permission modes, such as `nvkectl` |
| `disk-image` | `<output-name>.qcow2`, preserving the exact plain or declared LUKS2-encrypted disk payload bytes without decryption |
| `helm-oci` | `<output-name>.tgz`, preserving the unchanged chart archive |
| `oci` | A standard OCI layout with `oci-layout`, `index.json`, and `blobs/`; native OCI and Docker schema-2 images retain their exact manifests, configs, and layers; aggregate OCI indexes include every declared platform |
| `apt-repository` | The checked APT repository tree of regular files and directories, directly beneath the destination directory |

For the example above, the executable is `./nvkectl-linux-arm64/nvkectl`. The destination's parent must exist;
the destination itself must not already exist, and it is exposed only after successful integrity checks.
Bundle installation suggestions do not choose destination paths. Pulling neither installs nor executes a
file and does not import images into a runtime. It does not infer a platform from the host or output name or
reduce an aggregate image to the current host's platform. Inspect the declared platform before choosing an output.
Failure before the directory is committed leaves no destination. If the final parent-directory sync fails,
the command reports failure even though the complete destination may already exist.

The operation validates the complete bounded release metadata and evidence closure before selecting the
payload. It checks the selected output's exact payload closure and format-specific integrity, including
descriptor digests and sizes, archive membership where applicable, and executable file digests and modes.
An aggregate image's closure includes all its declared native children. Payloads belonging only to other
outputs are not downloaded. An unknown output is a coded usage error. Malformed or unsupported format metadata
fails with a coded error before payload or destination work. APT archives reject links and special files;
directories must be readable and searchable by their owner. Every file, including repository indexes, is bound
to the downloaded archive by its digest.

`release pull` accepts only a registry reference. Use `publish extract` for an executable bundle already in a
local layout. Registry credentials, `--anonymous`, `--plain-http`, `--workspace`, and the ten-minute default
`--timeout` follow the browsing rules above. `--workspace` selects the parent for private temporary registry
data; `--out` selects the new directory containing the downloaded output. Temporary data is removed on success,
failure, or cancellation; a caller-owned workspace remains.

The default result is plain text. `--ui=tui` opens the [read-only result browser](lifecycle.md#browse-public-command-results)
after a successful pull, showing the format, declared platforms and written file identities. Closing the browser
does not repeat the download or undo the written files. Presentation admission precedes acquisition.

Successful pulling establishes byte integrity against the supplied release metadata. Its result grants no
signature, consumer-policy, qualification, installation, or runtime acceptance. Apply `release verify` to the
same exact root under an independently accepted policy before treating the downloaded output as trusted.

## Export a complete release description

Supply the immutable publication reference as the only metadata input:

```sh
bin/nvkr release export --reference "$RELEASE_REF" > release-description.json
```

The result uses `schemaVersion: nvkr.dev/release-description/v1`. It preserves every named output and every
platform in the publication, including components unfamiliar to a particular consumer. Export needs no Plan,
mapping file, platform selection, producer checkout or build workspace. It does not accept an unpublished
workspace as a release. An optional `--layout` supplies a cached copy of the exact referenced publication;
the local-layout integrity and reference rules below still apply.

Unfiltered `release browse --format json` and `release export` use the same versioned wire description. A
browse selection remains a filtered view of that schema; export always emits the complete description.
The [checked-in JSON example](https://github.com/dsx-ai-factory/nvkr/blob/main/pkg/nvkr/releaseview/testdata/release-description-v1.json) illustrates all
supported artifact kinds and multiple source revisions with synthetic identities.

Consumers must require an exact supported `schemaVersion` before interpreting the description. Incompatible
changes to field shape or meaning require a new schema version. The naming cutover is a breaking namespace
change: current readers reject historical `nkd.dev` publication records and digest families. Old signed
records cannot be renamed into an NVKR release or used to validate the renamed reader.
Output and evidence names, component and output memberships, build subjects, required platforms and image
children are sorted deterministically.
Stable ordering makes comparisons repeatable; the JSON bytes still do not define release identity.

| Field | Meaning |
| --- | --- |
| `schemaVersion` | `nvkr.dev/release-description/v1`, the version of this descriptive JSON contract |
| `reference`, `rootDigest` | Supplied immutable publication locator and its exact root digest |
| `referenceScope` | Locators derive from the supplied root reference; availability is not established |
| `declaredQualification` | Publisher-declared qualification, separate from this inspection's result |
| `inspection`, `inspectionDetails` | Human-readable inspection scope and explicit machine-readable checks |
| `totalOutputs`, optional `selectedOutput` | Complete publication output count and any browse-only selection |
| `metadata` | Recorded Definition, Environment, Plan and content identities, with invocation ID when present |
| `outputs` | Every named artifact's exact kind, digest, reference, Build Digest, components and kind-specific metadata |
| `evidence` | Exact evidence references/digests, output/component/stage/subject bindings and available source descriptions |

`inspectionDetails.scope` is `metadata-only`, `metadataClosure` is `complete`, and `outputSelection` is
`all` for export or an unfiltered browse. A selected browse uses `filtered` after checking the complete
metadata closure. Under `inspectionDetails.checks`, `metadataIntegrity` is `checked`; `payloadIntegrity`,
`signatures`, `consumerPolicy`, `registryAvailability` and `installationCompatibility` are `not-checked`.
These values report the work this operation performed; they do not downgrade or authenticate the publisher's
declared qualification.

Native artifact `os` and `arch` fields describe checked config metadata where available. An aggregate
image's `imageIndex` retains its exact required platform set, child manifests and build subjects; the output
digest remains the published index digest. Helm `chartVersion` comes from the exact chart config and is
distinct from `appVersion`. A chart package's version or platform label does not establish the compatibility
of its rendered workloads.

Source-built evidence may carry `source.repo` and `source.commit` alongside its exact `binding`. Source
metadata is not collapsed into one release-wide repository or revision, and uses of one repository at
different commits remain distinct records. Missing or redacted properties remain absent or partial; a
normalized component version does not reconstruct an original Git tag. Consumers join sources to outputs
through evidence bindings and decide which revisions are compatible with their own use.

NVKR does not rename components into installer roles or create consumer-specific installation formats.
Consumers own role mapping, target selection, deployment topology, compatibility and complete offline
dependency inventories. JSON export does not create a new lifecycle input or canonical identity, and a
description cannot restore a held verification capability.

## Export a filtered view or diagram

```sh
bin/nvkr release browse --reference "$RELEASE_REF" --format json > release-view.json
bin/nvkr release browse --reference "$RELEASE_REF" --format mermaid > release-graph.mmd
```

JSON retains full artifact, manifest, blob, evidence-document and subject identities. It includes the total
output count and optional selected output, making a filtered view explicit. `referenceScope` labels locators as
derived from the supplied root reference, without claiming registry availability. Empty component build-subject
collections are arrays. Nonprinting Unicode, including bidirectional controls, is escaped for terminal display;
decoding the JSON preserves the exact string values. The versioned JSON description is indented for
readability; its serialized bytes are not a canonical identity document or a verification receipt.

The Mermaid diagram shows release membership, output components, and evidence-to-output and evidence-to-subject
relationships. It abbreviates digest labels for readability; use text or JSON for exact locators. This is the
published artifact/evidence graph. It does not reconstruct build ordering or claim relationships absent from
the publication metadata. Registry-provided labels are escaped before rendering.

## Inspect a local layout

```sh
bin/nvkr release browse --reference "$RELEASE_REF" --layout /path/to/release-layout
bin/nvkr release export --reference "$RELEASE_REF" --layout /path/to/release-layout
```

The layout may contain the complete publication or only its metadata and evidence; browsing does not open
artifact payload layers in either case. The root digest must match the local index exactly. The repository namespace in the reference determines the
descriptive sibling references; a local view neither contacts that host nor proves those repositories contain
the artifacts. Text explicitly labels artifact/evidence locators as derived, and the detached description
makes no registry-availability claim in either acquisition mode. Registry and workspace flags cannot be combined
with `--layout`. The existing `release inspect --reference … --layout …`
continues to return its compact artifact array.

## Apply a verification policy separately

For authenticated consumption, follow [the lifecycle verification guide](lifecycle.md#run-and-verify) and pass
the same pinned reference to `release verify` with the consumer's accepted identity, issuer and evidence scope.
Browsing invokes neither cosign nor a signer, and does not treat a raw preparation SBOM as a signed attestation.
The existing Linux and capability requirements for policy verification still apply.
Select `--scope authenticity` to authenticate signatures, provenance and required signed evidence without
artifact payload downloads, or `--scope artifact-publication` to include payload integrity checks.

## Implementation and validation scope

For browsing, delivery owns the bounded metadata reader and the output/evidence projection. Lifecycle admits
the reference and options, bounds the operation, reads or pulls the metadata, obtains its detached description and removes
temporary resources. Presentation owns the explicit versioned wire description and filters and formats only
that description. No new release identity or publication format is introduced. Artifact-publication verification
retains its full payload and policy checks. The pre-cutover exporter passed targeted tests, the full local
completion gate with uncached tests, and the uncached race suite on pinned Go 1.27.2 on 2026-10-10. That
validation and the existing inspection evidence below retain their original namespace and scope; they do
not establish external acceptance of the renamed reader.

For pulling, delivery owns admission, complete metadata validation, retrieval and integrity of the selected
payload closure, destination publication, and private temporary cleanup. Lifecycle supplies the overall deadline.

Contract tests cover anonymous and authenticated loopback registries, identical local/remote descriptions,
exact sibling references and evidence subjects, config-declared platforms, zero artifact payload-layer
downloads including disk images, failed admission before filesystem/network work, temporary-workspace cleanup
in a read-only current directory, rejection of evidence without output membership, empty Helm build-subject
arrays, filtered shared membership, Unicode-safe JSON round trips and CLI option admission. Those fixtures
establish contracts, not GHCR, signing-service or production acceptance. Pulling contracts cover exact output
selection, the five supported formats, complete aggregate-platform membership, exclusion of unrelated payloads,
integrity failures, existing-destination refusal, and cleanup. They do not establish registry-service,
installation, image-runtime, or production acceptance. Current support and external evidence remain recorded in
[reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md).

The [recorded loopback replay](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-03-release-navigation.md) exercised the earlier full-closure CLI
against a Docker registry carrying actual AMD64/ARM64 NVKE CLI bundles and their dependency SBOMs. It is
unsigned diagnostic evidence, separate from signed release consumption and the current metadata-only path.
The [historical v3 metadata browsing validation record](https://github.com/dsx-ai-factory/nvkr/blob/a00a6b11ddf711268bc10a7ea2baa33f839ccec9/docs/evidence/2026-10-06-release-browse-metadata.md) records
the scope and outcome of browsing the release published by Run `37533806963`. Current readers require
publication-root v4 and reject that earlier v3 release; the dated record does not establish current-schema
external acceptance.

That record identifies the missing consumer source snapshots and distinguishes the original live observations
from later local replays. Its [retained transport guard](https://github.com/dsx-ai-factory/nvkr/blob/a00a6b11ddf711268bc10a7ea2baa33f839ccec9/docs/evidence/2026-10-06-release-browse-metadata/inspect_transport.go.txt)
is historical evidence with fixed local paths, not a maintained executable or a compiled repository test.
The original guard bytes remain unchanged in Git history. It replaced the package-global
`remote.DefaultTransport`; its request and byte counts describe that single execution through that transport,
not later consumers or code using a different transport. The guard imports unretained local package sources,
so it supplies only partial source provenance.
