# Catalogs and components

A catalog defines reusable components and how to build or acquire them. A Definition chooses which
components belong in each required output. Start with the [quickstart](quickstart.md#build-only-the-cli) for CLI setup;
the [concept reference](user-concepts.md) explains the broader lifecycle.

Adding a catalog with existing NVKR capabilities requires declarations and pins, not Go code or
component-specific Go tests. The NVKE and OVN tests in this repository are regression coverage for
NVIDIA's supplied catalogs, not catalog authoring requirements. Generic validation checks declarations
and dependency graphs; actual builds and output checks validate the declared producer commands.

The example packages two application tools: `app-cli` for users and `config-cli` for administrators.
The `client-bundle` contains `app-cli`; the `admin-bundle` contains both tools.

GitHub tree generation uses `metadata.name` in a readable frozen-record path. Catalog validation alone
need not reject a name that storage cannot represent: generation refuses parent segments, control/format
characters and other unsafe path spellings with `NVKR-E-PIPELINE-DRIFT`. It does not normalize names.
Uppercase letters and safe nested names are permitted. See the [tree layout](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/generated-plan-tree.md).

## Author declarations; generate pins

The catalog has two files:

| File | What it owns | How to change it |
|---|---|---|
| `registry.yaml` | Sources, allowed versions, supported platforms, toolchains and producer instructions | Edit the declarations. |
| `pins.lock.yaml` | Exact source commits, trees and timestamps; acquired-artifact and tool digests | Use `catalog pin`/`bump` for source releases and their tool assets; `catalog pin-artifacts` for direct acquired-artifact/image pins. |

A command producer may declare `buildCommandByPlatform` with complete argument arrays for explicitly
supported platforms. The compiler selects the exact matching array, falling back to `buildCommand` when no
override exists, before deriving the IR and Build Digest. This keeps host linker differences in one producer:
the native NICoSim catalog retains Linux's static Make build and selects Go compilation on Darwin. It does
not duplicate source pins, material declarations or deployment orchestration. Unknown targets and empty
overrides fail validation; command overrides are unavailable to Packer, Nix and acquired producers.

Any operand in `buildCommand`, `buildCommandByPlatform` or a source's `vendorCommand` can reference
a file instead of an inline string:

```yaml
buildCommand: [bash, -ec, {file: scripts/build.sh}]
```

The loader substitutes the file's exact UTF-8 bytes, preserving trailing newlines. Paths resolve beneath
the declaring registry or fragment directory; absolute paths, `..` segments, symlinks and non-regular
files are rejected. The existing limits are 4 MiB per file and 32 MiB across a catalog load. Captured
compile inputs retain the script bytes for replay. Moving an identical inline body into a file preserves
Build and VendorSpec identities; changing its bytes changes the applicable identity. Keep the interpreter
and flags in the argv array. This does not add a shell mode or execute a file during catalog loading.

Before using script files beneath sibling fragments, note that toolchain repinning does not yet capture
those files in its edited-fragment input set. Loading and snapshot replay work, but that maintenance
operation fails closed. See the [current limitation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/adr-22.md).

A source can serve several components. A pin set can derive several source or acquired-artifact versions
from one upstream release's version file. Update those members through their owning source. Use
`catalog bump --check` to preview changes without writing the lock; pending changes return `NVKR-E-DIFF`.

`--catalog` is the catalog-directory flag for inspection, resolution, pinning and starter authoring.
`--data` and `--dir` remain compatibility aliases. Supply only one spelling per invocation;
combining aliases is a coded usage error before catalog reads or writes. The separate validator
commands use `--catalog` for a validator index, not a producer catalog directory; these aliases do not apply there.

## Producer bytes and final formats

A catalog's `outputType` describes what its producer emits. A Definition's `format` describes the
named deliverable assembled from the selected producers. They are separate stages; the compiler
validates the selected bytes, material closure and packaging requirements together.

| Producer output | Typical final format | Meaning |
|---|---|---|
| `oci-image` | `oci` | Native container image; supported platform sets form an image index. |
| `host-package` | `executable` | Selected executable files packaged for delivery. |
| `host-package` | `apt-repository` | Debian package producers assembled into an APT repository. |
| `host-package` | `debian-package` | One source-produced Debian archive retained as the final file. |
| `tarball` | `helm-oci` | A chart archive with chart packaging requirements. |
| `tarball` | `bundle-oci` | One bundle root and its explicitly selected material closure. |
| `disk-image` | `disk-image` | One QCOW2 root and its explicit material closure. |

These are packaging relationships, not automatic conversions or aliases. A tarball is not necessarily
a Helm chart, and a host package is not necessarily a Debian archive. Specialized pinned-tool producers
also have their own admitted output contracts. Existing `SourceBuildRegistry`, producer output types
and final format spellings remain unchanged; this vocabulary introduces no serialized schema migration.

## Share sources with explicit roles

Several producers can refer to one source key and reuse its exact pinned checkout. A single repository
can also have independent source keys when components need different module tags, toolchains, or
preparation steps. For example, NVKE's controller catalog uses `nvke` and `nvke-helm` for different build
roles. Sharing a repository URL does not create a pin-set relationship or guarantee matching commits.

Keep coordinated source roles aligned through a reviewed complete-lock update. A derived member uses
`pinnedBy` only when its owner explicitly maps an upstream version file to that member. See
[catalog maintenance](catalog-maintenance.md) and [the pin-set example](generic-pin-set-example.md).

## Choose components for an output

A **component** has an exact catalog key, such as `app-cli`, and a **producer** that builds or downloads
its bytes. An **output** is a named deliverable with a platform, packaging format and component selection.
A **material** is another component's produced bytes used as an input by a consuming producer.

Suppose the catalog defines `app-cli`, `config-cli`, and an alternate implementation, `app-cli-alt`.
This Definition excerpt selects the tools for each bundle; its schema header is omitted:

```yaml
catalog:
  base: ./catalog
outputs:
  - name: admin-bundle
    components: [app-cli, config-cli]
    platform: linux/amd64
    format: executable
  - name: client-bundle
    components: [app-cli]
    platform: linux/amd64
    format: executable
```

Both outputs use the default catalog. `components` selects exact producer keys; it does not redefine
them. Neither output selects `app-cli-alt` yet. Each output remains
independently required even when it shares a component with another output.

## Share catalog selections across outputs

Keep `catalog` for shared action and utility pins. Add an optional ordered `catalogs` list when outputs
select producers from several stacks:

```yaml
catalog:
  base: ./tools
catalogs:
  - base: ./applications
  - base: ./images
    overlays: [./image-toolchains.yaml]
outputs:
  - name: client-bundle
    components: [app-cli]
    platform: linux/amd64
    format: executable
```

An output without an override must find its complete component selection and every required platform
in exactly one listed stack. No match returns `NVKR-E-NO-MATCH`; multiple matches return
`NVKR-E-AMBIGUOUS`, even if their producers are identical. List order never resolves a collision.
Set that output's `catalog` explicitly to choose a stack. All listed stacks are validated, including
unused candidates. At most 64 distinct stacks are admitted; an omitted or empty list retains the
singular catalog behavior. Internal tools use the same selection rule. Composite members must share
the same ordered list after rebasing paths.

`run plan` shows each selected catalog, overlays and digest in its producer section. Its JSON view
records `builds[].selectedCatalog` and the complete `catalogCandidates` snapshots. Validation rechecks
the whole output's choice, including every native child of a platform set.

The retained combined NVKE comparison fixture uses four stacks: `nvke`, `runtime-dependencies`, `node-images`
with its native-toolchain overlay, and `tools`. Two explicit `nvkectl` overrides retain the
module-tag producer selection within the consolidated `nvke` catalog. Its 69 output names and all
73 native producer selections stay unchanged. Source-built image outputs already use platform sets. Acquired
image pairs remain separate: aggregate packaging currently requires built OCI exports. Disk outputs
remain native, and executable-index migration belongs to issues #297, #296 and #376 with their
qualification prerequisites. Catalog-list selection alone does not rename outputs or change consumer mappings.
Maintained generation still uses the composite built from family files.

## Find components

Browse all catalogs without first choosing a producer:

```sh
nvkr catalog list --catalog inputs/catalogs
nvkr catalog find --catalog inputs/catalogs nvkectl
```

Root listing includes imported components and keeps duplicate names separate by catalog path.
`find` matches an exact component name and reports each location with its recorded source or
acquisition pins; an unknown name returns a no-match error (exit 10). Both accept `--format json`.
Root JSON groups descriptions under root-relative `path`, catalog `name`, and `components`.
Missing locks, invalid imports and unsafe directory entries fail before results are printed.
Discovery is offline and read-only: it validates local declarations and recorded lock structure,
but does not re-derive pin sets from upstream Git trees or select anything for a Definition.
Normal executable catalog loading retains that upstream verification requirement.
The root scan admits at most 1,024 total files and directories and 16 directory levels,
using the same no-symlink entry policy as catalog loading. Scan a narrower root if it exceeds
those bounds. Each catalog load retains its existing file and aggregate byte limits.

The [generated catalog inventory](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/catalogs/INDEX.md) lists locations, component names and
producer modes from these same descriptions. Pins remain in `catalog find` so automated pin refreshes
do not change this navigation index.
After editing catalog inputs, run `make catalog-index-generate`; `make catalog-index-check`
rejects stale content and also runs as part of `make workflows-check`.

`nvkr catalog list --catalog DIR` prints every component. Two optional selectors narrow it, and
`--format json` prints a machine-readable projection:

```bash
nvkr catalog list --catalog inputs/catalogs/tools \
  --platform linux/arm64 --output-type host-package --format json
```

- `--platform` and `--output-type` match exactly and combine as an intersection. Give each at most once.
- A platform match means the catalog *declares* that platform. It says nothing about the current host or
  about a successful build. `darwin/arm64` is accepted wherever a catalog can declare it.
- The whole catalog is validated before filtering, so a filter cannot hide an invalid component.
- An unknown selector value or a repeated selector is a usage error (exit 2) before the catalog is read. A
  valid selector that matches nothing is a no-match error (exit 10). Neither prints a result.
- Unfiltered text output is unchanged.

For a leaf catalog, JSON output remains a stable array sorted by component name. Each entry has `name`, `platforms`,
`outputType` and `producerMode`. `producerMode` is one of:

| Value | Meaning |
|---|---|
| `source` | built from a pinned source |
| `acquired` | an executable or image delivered from a prebuilt URL or image (the `prebuilt` lane) |
| `pinned-tool` | a pinned build tool (`file` or `oci-layout` output) |
| `excluded` | declared but neither built nor acquired |

## Explain authored inputs

Use an explicit input root to inspect a file and the supported authored files that refer to it:

```sh
nvkr explain --root inputs inputs/definitions/nvke/definition.yaml
nvkr explain --root inputs inputs/catalogs/shared/nvke.yaml --json
```

The command recognizes Definitions, Environments, composites, catalogs, overlays, catalog fragments,
generated pin locks, hosted qualification policies and local qualification policies. Paths in the report are relative to the
printed scan root; the JSON `root` is an absolute locator and differs between checkouts. Fragment imports label local-to-fragment selections and fragment-to-local source bindings separately; Definition catalog selections
show overlay order, and overlays identify their exact predecessor binding. Locks identify their owning
catalog and the pin-generation commands. The public consumer `VerificationPolicy` currently has no
authored file form; the supported policy files here are hosted and local qualification policies.

The root must contain the referenced input closure. Reverse references cover these supported kinds only
within that root. Other schemas and the `.git`, `.nvkr` and `node_modules` directories are excluded.
An unsupported target schema, missing files, stale
fragment or composite pins, and ambiguous or unmatched overlay predecessors fail with coded errors and
no partial report. The scan is bounded to 4,096 entries, depth 16 and 32 MiB of YAML/JSON input; symlinks
are refused. Choose a smaller complete input tree if a broad repository scan exceeds those limits.

Explanation is read-only and offline. It validates recorded declarations through their existing loaders,
but does not re-derive upstream pin sets, execute producers, or authenticate release evidence. A fragment's
complete producer meaning still depends on its importing registry's mappings and pins. A validation error
elsewhere in the scanned authored graph must be fixed before the command prints an explanation.

## Replace a producer or change a selection

An overlay named `supplier.json` can replace the complete `app-cli` producer: a supplier download takes
the place of its source build. The component key stays `app-cli`. The overlay binds the exact preceding
catalog and must preserve the component's supported platforms and output type.

The overlay defines **what changes**. Its placement defines **where that change applies**. Compare
these three independent changes with the baseline above:

| Change in the Definition | Result | Outputs with changed Build Digests |
|---|---|---|
| Add `overlays: [supplier.json]` under the default `catalog` | Both bundles use supplier-provided `app-cli`. | `admin-bundle`, `client-bundle` |
| Give only `admin-bundle` its own `catalog` with `base: ./catalog` and `overlays: [supplier.json]` | The admin bundle uses supplier-provided `app-cli`; the client bundle keeps source-built `app-cli`. | `admin-bundle` |
| Change `client-bundle.components` to `[app-cli-alt]` | The client bundle selects the alternate tool; the admin bundle still selects `app-cli`. | `client-bundle` |

A Build Digest fingerprints selected build inputs and instructions, not future artifact bytes.
In all three cases, `config-cli` is unchanged.

Here is the complete `admin-bundle` output entry for the second case:

```yaml
- name: admin-bundle
  components: [app-cli, config-cli]
  platform: linux/amd64
  format: executable
  catalog:
    base: ./catalog
    overlays: [supplier.json]
```

An output-specific catalog is a complete stack: it does not inherit the default's base or overlays.
Overlays apply in list order, each checking the catalog produced by its predecessor. After changing
the base, review and reissue affected overlays against that exact catalog; a stale predecessor is an error.

Selecting `app-cli-alt` also changes executable packaging: its bundle entry is `files/app-cli-alt`, with
declared install path `/usr/local/bin/app-cli-alt`, even if its producer writes `bin/app-cli`.
An overlay retaining the component key `app-cli` retains that component-based naming.

## Keep material references consistent

This exact excerpt belongs to the NVKE `nvke-cni-ovn` catalog producer:

```yaml
materials:
  ovn-chart: ovn-kubernetes-chart
  ovn-image: ovn-kube-ubuntu
```

The left side is the consumer's input label; the right side is an exact component key. The output must
select those producers too. Changing only its selection to another chart does not retarget
`ovn-chart`. Update the consuming producer's mapping as well, or use a compatible overlay under the
existing component key. Missing selected materials fail validation.

A disk output currently selects its root and every transitive material producer explicitly, with no
unrelated extras. Root-only selection does not expand dependencies automatically. See
[node images](node-images.md) for the complete example.

To see what to select, explain the root component:

```sh
bin/nvkr catalog show --catalog inputs/catalogs/node-images nvke-node-fde-amd64
```

It prints the producer mode, exact pins and platforms, the sorted material closure, and every binding
edge, including each consumer of a shared material. Add `--format json` for a machine-readable copy.
It explains declarations only: it changes no Definition or lock, fetches and builds nothing, and a listed
platform is not a claim that the current host can execute it.

## Acquired executables

A source `tool` selects an exact upstream executable with `format: raw`, `tar-gzip` or `zip`, an HTTPS
URL, either one `platform` or a sorted nonempty `platforms` list, and an exact `executablePath`.
Multi-platform URLs, executable members and companion members render `{{.OS}}` and `{{.Arch}}`;
the lock retains one complete native asset record per declared platform. A missing or extra platform fails
validation, and pinning commits the set only after every acquisition succeeds. Its `releaseExecutable: true` component uses
`outputType: host-package`. `catalog pin` records both carrier and executable digests. Acquisition checks
both identities and scans all archive entries with bounded standard-library readers; it never extracts
archive paths into the filesystem. Archive format is part of the compiled identity. Existing tar-gzip IR
records retain their original encoding. Each Definition output still selects one concrete platform;
shared tool declarations do not create executable image indexes.

An archive `tool` may also list sorted `companions`, exact members that ship beside the executable. A
companion is delivered under its base name with mode 0644 in the same executable output. `catalog pin`
records each companion digest, and observation and packaging check it against that pin. The
[AICR CLI catalog](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/catalogs/aicr/notes/tools-aicr-arm64-README.md)
delivers `aicr-attestation.sigstore.json` beside `aicr` this way. A companion is pinned bytes, not verified
upstream evidence.

## Tools on multiple platforms

One tool source may declare `platforms: [linux/amd64, linux/arm64]` instead of `platform`.
Use `{{.OS}}` and `{{.Arch}}` in the URL and, when needed, the executable or companion paths.
`catalog pin` resolves every declared platform into one sorted `tools` lock list atomically.
The existing single-platform form stays valid. A missing platform is an error; there is no emulation fallback.

For native output utilities, an Environment may add `utilityContainers`, mapping each output platform
to a digest-pinned image. Include the default `utilityPlatform` with the exact `utilityContainer` value.
Observation, SBOM generation and packaging follow the output platform; aggregate packaging and release
signing use the default utility platform. Regenerate all consuming bundles after either input changes.
A different scanner executable changes its digest in dependency SBOMs even when inventory is identical.

## Pinned-tool producers

A `builderType: pinned-tool` component runs a source's pinned tool once, offline, inside a digest-pinned
`toolchainImage`, and produces new bytes. It names the source with `tool`, has no `source` or `fromSource`, and
declares exactly one platform. It selects that platform's exact tool pin from the source:

```yaml
components:
  aicr-gb200-training-recipe:
    builderType: pinned-tool
    tool: aicr
    toolchainImage: registry.example/runner@sha256:<digest>
    platforms: [linux/arm64]
    outputType: file
    artifactPath: recipe.yaml
    toolArgs: [recipe, --service, nvke, --accelerator, gb200, --os, ubuntu, --intent, training,
      --output, '{{.Output}}/recipe.yaml']
```

`toolArgs` is the argument vector after the executable; there is no shell. Arguments may use only
`{{.Output}}`, `{{.Arch}}` and `{{.Material "<binding>"}}`, and every `materials` binding needs a token.
`toolEnv` adds literal variables but cannot set `HOME`, `TMPDIR`, `TZ` or `LC_ALL`. The run has no network,
so tool modes that push, download or sign keylessly fail. The toolchain image must provide `/bin/sh` and
`/usr/bin/env` for the executor's fixed bootstrap.

A `file` result must be a material: the output must also select a producer that binds it, and the tool must
leave exactly that one regular file. Like every selected component, the file is also a member of that output.
An executable bundle installs it at `/usr/local/bin/<name>` with mode 0644, as it does tarball assets, never as
an executable. A material is staged under its binding name, so name the binding with the
extension the tool expects, such as `recipe.yaml` for AICR. For `outputType: oci-layout`, `{{.Output}}` is an
absent path the tool creates, and it must leave exactly one OCI layout there. That form is reserved for bundle
outputs, and selecting it fails until layout import is available. Execution requires Linux, and a Definition that selects `sbomTool` cannot yet include a
`file` producer. [ADR-27](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/0027-pinned-tool-producers.md)
and [reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/README.md) record the
remaining limits.

## Build tools and version stamps

The catalog also selects how source components are built:

| `builderType` | Build mechanism |
|---|---|
| `oci-hermetic` | A declared command in a pinned toolchain container. |
| `buildkit-rootless` | BuildKit produces a container-image archive. |
| `vm-image` | A source-owned VM-image build; connected execution uses direct Packer. |
| `nix` | A locked flake build in a pinned container; qualified realized-input observation remains unavailable. |
| `pinned-tool` | One run of a source's pinned release tool, offline, with no source fetch. See [Pinned-tool producers](#pinned-tool-producers). |

A builder type describes production, while an output format describes delivery. Runtime and evidence limits
still apply; [ADR-10](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/0010-pluggable-builder-types.md) and [reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/README.md) record them.

A **toolchain substitution** changes the build container for explicitly named components and an architecture,
checking the expected old digest. It keeps the existing source and command declarations. The
[maintained OS overlay](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/catalogs/node-images/variants/native-toolchains.yaml) shows the exact `from`, `to`, and
`components` fields. This differs from replacing the complete producer.

**Version stamps** render source-pin values into build environment variables. This excerpt from the
[NVKE CLI producer](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/catalogs/nvke/registry.yaml) embeds its exact commit, release tag and source date:

```yaml
versionStamp:
  NVKECTL_VERSION: '{{.ModuleTag}}'
  NVKECTL_COMMIT: '{{.Commit}}'
  NVKECTL_DATE: '{{.Date}}'
```

These are build inputs, not labels added after the build. For the pinned tag `v0.23.0`,
`.ModuleTag` is `v0.23.0`; `.Version` is `0.23.0`.

For new source-derived environment names, use the opt-in `sourceMetadata` namespace instead of adding
component-specific names to NVKR's environment allowlist:

```yaml
sourceMetadata:
  NVKR_SOURCE_COMMIT: '{{.Commit}}'
  NVKR_SOURCE_STREAM: '{{.ReleaseVersion}}'
  NVKR_SOURCE_REVISION: '1+git.{{printf "%.12s" .Commit}}'
```

The command reads these as quoted shell variables, for example `"${NVKR_SOURCE_COMMIT:?}"`.
`ReleaseVersion` strips the module prefix and leading `v`, and rejects commit-only or non-semantic tags.
For a commit-only pin, explicitly declare the expected stream and check the pinned chart's version during
packaging. Keep installer-time expressions in quoted heredocs; write generated constants separately with
safe shell quoting. The supplied OVN catalogs demonstrate this split.

Bindings must use `NVKR_SOURCE_` followed by 1–64 uppercase letters, digits or underscores, starting with
a letter. The namespace is reserved from `versionStamp`; arbitrary environment and reproducibility overrides
are rejected. Source metadata is supported by `oci-hermetic`, `buildkit-rootless` and `native-go` command
producers. Adding bindings changes their Build Digests. Resolved producer overlays retain these declarations;
use [producer-copy maintenance](catalog-maintenance.md#maintain-producer-copies) to refresh their source pins.

## Debian packages

A `host-package` component can produce a raw DEB using its existing command, pinned toolchain,
platforms, private `writeDir` and `artifactPath`. A standalone `format: debian-package` output selects one
such producer and an explicit platform. The existing session and producer-result authentication own the
build; bounded file retention and lifecycle staging preserve the raw archive. It is not wrapped as an
executable bundle or an APT repository. Signing, publication and hosted/direct-node execution remain gated.

The [NVSentinel catalog](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/catalogs/nvsentinel/README.md)
declares OCI, Helm, executable and Debian outputs. Its OS experiment places the same DEB in the existing
composer/Packer `debs/` folder and regenerates the local APT index. Upstream source is unchanged. Placement
does not install or enable NVSentinel; local composition metadata is not a full-content dependency SBOM,
ABI proof or runtime acceptance. See [reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/README.md).

## Run the local lab

Use macOS or Linux with Bash 3.2+, Git, Make, `jq`, GNU `timeout` or `gtimeout`, a SHA-256 utility
(`sha256sum` or `shasum`), and the Go version in `.go-version`. From the repository root:

```sh
GOTOOLCHAIN=local make build
bash docs/examples/authoring-lab/run.sh --nvkr bin/nvkr
```

The lab exercises the same overlay and selection operations with a separate fixture. It creates a fresh
temporary workspace and prints its location. Building the CLI may download modules; the lab then needs
no network or Docker. Inspect `identity-report.json` and the retained Plans to compare each change with
its baseline. The lab pins and compiles inputs; it does not execute producers,
download supplier bytes, publish, sign or install anything.

The [lab instructions](examples/authoring-lab/README.md) describe every retained file and the separate
source-update scenario.
