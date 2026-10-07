# Exercise catalog and output replacements

This local lab demonstrates shared catalog authoring and the identity effects of four changes. It drives
public `nkdctl` commands, creates deterministic Git commits and tags in a new workspace, and keeps every
lock write inside `catalog pin` or `catalog bump`. No NKE repository or maintained Definition is modified.

The lab is an **authoring and compilation fixture**. It does not build containers, execute its declared
producers, download the supplier artifact, publish, sign, or install anything. Both CLI platform declarations
use simple fixture scripts; they do not prove native NKE CLI support or runner availability. Container
and utility image pins are taken from the maintained inputs but are not pulled or authenticated by this lab.

## Run it

Use Linux or macOS, Bash 3.2 or later, Git, `jq`, GNU `timeout` (or `gtimeout`), a SHA-256 utility
(`sha256sum` or `shasum`), and the repository's pinned Go toolchain. From the repository root:

```sh
make build
bash docs/examples/authoring-lab/run.sh --nkdctl bin/nkdctl --work .nkd/authoring-lab-01
```

Choose a new workspace for every run. The program refuses an existing directory and never deletes a
previous run. Omitting `--work` retains the results in a newly created temporary directory and prints its
path. After building the CLI, this journey requires no network or Docker. An isolated Git URL override
routes the fixture source to its local repository; it does not edit global Git configuration. The program
clears inherited Git repository, index, and object-directory overrides for its Git and CLI subprocesses.
Git and CLI commands have a 60-second deadline with a five-second termination grace period. Missing
prerequisites fail before the workspace is created.

Expected report:

```text
source-pin: changed Build Digests: cli-amd64, cli-arm64, controller, controller-chart
whole-producer: changed Build Digests: diagnostics, tools
output-catalog: changed Build Digests: tools
output-selection: changed Build Digests: diagnostics
```

## Read the inputs and results

[registry.yaml](catalog/registry.yaml) defines seven producers. [definition.yaml](definition.yaml) selects
seven required outputs. [environment.yaml](environment.yaml) supplies utility execution settings without
publication or signing. The `upstream/` files are the fixture's source-owned build inputs.

| Output | Selection and relationship |
|---|---|
| `cli-amd64`, `cli-arm64` | Select `cli` from the derived `app` source for two concrete target platforms. |
| `controller` | Select a BuildKit image producer from `app`. |
| `controller-chart` | Select a source chart from `app`; package its `controller` slot with the exact `controller` image package. |
| `diagnostics` | Select `doctor` from the derived `tools` source. |
| `tools` | Select `doctor` and `status` together; their graph shares one source fetch. |
| `support-chart` | Select an independent source chart from `tools`. |

The unselected `doctor-alt` producer is available for an explicit output substitution. Catalog membership
alone does not add it to a required output. Repeated use of `doctor` in two outputs does not authorize
cross-output artifact/evidence reuse.

Inspect the retained workspace:

| File | Purpose |
|---|---|
| `catalog/pins.lock.yaml` | Machine-managed final pin set after the `app` upgrade. |
| `plans/baseline.json`, `plans/baseline-repeat.json` | Byte-identical public plan output from the initial release. |
| `plans/source-pin.json` | Plan after the umbrella selects `app/v1.0.1` while `tools/v1.0.0` stays fixed. |
| `supplier.json` | Whole-producer overlay pinned to the original exact catalog digest. |
| `whole-producer.json` | Complete authored Definition applying that overlay to its default catalog. |
| `output-catalog.json` | Complete authored Definition applying the overlay only to `tools`. |
| `output-selection.json` | Complete authored Definition selecting `doctor-alt` for `diagnostics`. |
| `plans/whole-producer.json`, `plans/output-catalog.json`, `plans/output-selection.json` | Compiled effects of those independently authored variants before the source upgrade. |
| `pin-preview.txt` | Preview of the whole umbrella/member update without writing. |
| `identity-report.json` | Display-only report with full before/after Build Digests for every output. |

The program mechanically writes complete JSON Definitions from the baseline to make the changed field
obvious. This is fixture setup, not a Definition inheritance API. JSON is accepted by the authored YAML
loader; these indented inputs and the report are not stored canonical identity objects. The Plan files are
unmodified `run plan --json` result streams.

## Understand each change

1. **Source pin:** the platform's pinned `versions.yaml` declares `app/v1.0.1` and the unchanged
   `tools/v1.0.0`. The umbrella and members update together. Every selected `app` graph changes, even the
   controller chart whose own files did not change, because its exact source tree changed. The `tools`
   graphs retain their Build Digests.
2. **Whole producer:** `doctor` becomes a prebuilt supplier script, replacing the complete source-build
   producer. Its digest comes from the retained [supplier script](supplier-doctor.sh); both architecture
   entries identify those same fixture bytes. The placeholder HTTPS URL is not downloaded. The graph
   switches from build to acquisition, and both outputs selecting `doctor` through that stack change.
3. **Per-output catalog:** only `tools` selects the supplier overlay. `diagnostics` still selects the
   original source producer, even though the two outputs retain the same logical component name.
4. **Output selection:** `diagnostics` explicitly selects the alternate source producer. The required
   output name remains fixed while its selected component and Build Digest change.

The final source upgrade makes the original supplier overlay stale. Reusing it fails with
`NKD-E-UNVERIFIED-REF`; the lab checks that refusal. It also checks independent derived-member pin refusal
(`NKD-E-UNSUPPORTED`), no-op lock preservation, and preview status (`NKD-E-DIFF`). Reissuing an overlay
against an upgraded base requires a new reviewed decision; the lab does not silently refresh it.

## Inspect a retained workspace manually

Set `WORK` to the path printed by the lab. The URL override was private to the program's child processes;
repeat it for commands that load its pin set:

```sh
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url.$WORK/upstream.insteadOf"
export GIT_CONFIG_VALUE_0=https://authoring.example.invalid/platform
bin/nkdctl run plan --definition "$WORK/definition.yaml" --environment "$WORK/environment.yaml"
```

This describes the final upgraded source set. Old variant Definitions intentionally still reference the
original predecessor digest. Use the retained original plans and report to compare those variants.

The regression test executes this same program through the real CLI adapters:

```sh
go test ./cmd/nkdctl -run '^TestAuthoringLab' -count=1
```

For shared sources, complete per-output catalog stacks, and replacement identity, read
[catalogs and components](../../catalogs-and-components.md). The
[deploy Definition](../../../definitions/nke-deploy/README.md) provides the real NKE composition example.
