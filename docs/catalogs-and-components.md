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

A source can serve several components. A pin set can derive several source or acquired-artifact versions
from one upstream release's version file. Update those members through their owning source. Use
`catalog bump --check` to preview changes without writing the lock; pending changes return `NVKR-E-DIFF`.

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

## Acquired executables

A source `tool` selects an exact upstream executable with `format: raw`, `tar-gzip` or `zip`, an HTTPS
URL, a concrete platform and an exact `executablePath`. Its `releaseExecutable: true` component uses
`outputType: host-package`. `catalog pin` records both carrier and executable digests. Acquisition checks
both identities and scans all archive entries with bounded standard-library readers; it never extracts
archive paths into the filesystem. Archive format is part of the compiled identity. Existing tar-gzip IR
records retain their original encoding.

An archive `tool` may also list sorted `companions`, exact members that ship beside the executable. A
companion is delivered under its base name with mode 0644 in the same executable output. `catalog pin`
records each companion digest, and observation and packaging check it against that pin. The
[AICR CLI catalog](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/catalogs/tools/aicr-arm64/README.md)
delivers `aicr-attestation.sigstore.json` beside `aicr` this way. A companion is pinned bytes, not verified
upstream evidence.

## Pinned-tool producers

A `builderType: pinned-tool` component runs a source's pinned tool once, offline, inside a digest-pinned
`toolchainImage`, and produces new bytes. It names the source with `tool`, has no `source` or `fromSource`, and
declares exactly one platform equal to the tool's platform:

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
[NVKE CLI producer](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/catalogs/nvke/module-tags/registry.yaml) embeds its exact commit, release tag and source date:

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
