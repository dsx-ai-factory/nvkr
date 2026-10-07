# NKD team walkthrough

For the browser experience, open the [HTML walkthrough](demo-team-walkthrough.html) and
[concept gallery](demo-team-concepts.html). The 13-slide deck links directly to these HTML examples.

**One takeaway:** declare the artifacts you want, inspect how they will be produced, then navigate exactly
what was published and the evidence attached to it. NKD's public journey is **Definition → Run → Release**.
Catalogs supply reusable, pinned producers; Environments supply execution and publication settings.

For the catalog examples, keep three nouns distinct: a **component** is a named piece with a recipe;
an **output** is a deliverable the Definition promises; an **artifact** is the actual result produced.
The [replacement comparison](demo-team-concepts.html#overlays) uses `diagnostics = doctor` and
`tools = doctor + status` to show a default overlay, an output-specific overlay and alternate-component
selection. Each starts from the same baseline. Its [runnable lab](demo-team-walkthrough.html#composition)
checks which Build Digests change without executing a build.

Use the [slide deck](nkd-team-tour.html) for the story and the [offline graph explorer](demo-team-explorer.html)
for the hands-on stops. The explorer is a presentation companion built for this walkthrough. NKD itself
provides native Plan inspection, linked graphs in Mermaid/DOT/JSON, semantic Plan comparisons, and release
text/JSON/Mermaid. The HTML explorer supplements those native commands.

The walkthrough is based on [PR #108](https://github.com/dsx-ai-factory/nke-recipes/pull/108), exact head
`00c71385580e798eb3d7f74b88fc694225cfe175`. Its release browser validates bounded metadata and evidence
without downloading artifact payload layers. The featured pipeline ran at its separately recorded source
commit below; the demo CLI comes from this PR head.

## Present the core story in 25 minutes

| Time | Show | Invite the team to discover |
|---|---|---|
| 0–3 min | Opening slides and lifecycle | Which decisions belong to the product author, execution owner and consumer? |
| 3–12 min | Catalog → Definition → Plan → generated pipeline | Where is one producer pinned? Which graph edges make the chart depend on the image? |
| 12–17 min | OS-image material graph | Which exact materials flow into the disk root? Which host requirements must execution satisfy? |
| 17–23 min | Candidate release graph, including both OS architectures | Which output, payload identity and evidence subject are we looking at? |
| 23–25 min | Verification and next steps | What did inspection establish, and what remains a consumer-policy decision? |

Scripts pause at each checkpoint. You can shorten the session by preparing the exports beforehand and
using the browser during the meeting. Offline rendering needs no server or package installation.

## Prepare once

From the repository root, use the Go patch specified in `.go-version` and build the current CLI:

```sh
GOTOOLCHAIN=local make build
bash demos/demo-team-catalog.sh --help
bash demos/demo-team-os.sh --help
bash demos/demo-team-release.sh --help
```

The scripts accept `NKDCTL_BIN=/absolute/path/to/nkdctl` to reuse a binary built from this PR head.
The same CLI supports all three stops, including the featured candidate's release metadata. The scripts install
nothing and leave exported files in a new temporary directory printed at startup. `--out` selects a new
directory beneath an existing parent. Existing directories are rejected. `--non-interactive` skips pauses.
They never modify checked-in catalog pins or generated CI files, dispatch CI or publish artifacts.

The default catalog/OS tour needs neither Docker nor a source checkout after the CLI is built. The live
release tour needs Python 3, registry access and existing credentials unless `--anonymous` is appropriate.
Keep the deck and explorer available together with the recorded snapshot for a network-independent session.

## Stop 1: turn inputs into a pipeline

```sh
bash demos/demo-team-catalog.sh
```

This uses the small maintained `nke-v1` example: the CLI, CWE image and controller chart. Read a catalog
source and its machine-managed lock side by side. Find that component in the Definition, then follow it
through the exported `plan.json` in the explorer. The Environment supplies the registry namespace, runtime
and signing policy; those are separate from the producer's logical Build identity.

The script also exports native text/pretty Plan views and complete linked graphs. These are first-class
NKD inspection commands; use the printed export directory as `DEMO_DIR`:

```sh
bin/nkdctl run plan --plan "$DEMO_DIR/plan.json"
bin/nkdctl run plan --plan "$DEMO_DIR/plan.json" --pretty
bin/nkdctl run graph --plan "$DEMO_DIR/plan.json" --format mermaid
bin/nkdctl run graph --plan "$DEMO_DIR/plan.json" --view artifacts --format dot
```

The complete graph joins producer instructions, operation inputs/outputs, required slots and publication
routes. Choose `--view producers` or `--view artifacts` to focus on one layer. Mermaid needs a compatible
diagram viewer; DOT uses Graphviz. No renderer is installed by the scripts. The HTML explorer works offline
from the canonical `plan.json`. Pretty JSON and graph JSON are display projections, not executable Plans.

Show the chart's dependency on the image. A component pin changes the selected producer graph's Build
Digest. The output name and transport location are not substitutes for that identity. Every named output
still needs its own observations and evidence, even if two outputs share a Build Digest.

The script generates a GitHub workflow in its scratch directory and immediately runs `--check` against
that exact bundle. Open the generated workflow and ownership manifest. This proves repeatable generation
for these inputs and this generator. It does not prove CI ran, a signature exists or an artifact is qualified.
The Plan can request `pipeline-attested`; that label alone is not runtime evidence.
The script finishes with `run diff` between the exported and generated Plans. Here it should report no
change. For a reviewed input update, compare retained before/after Plans to explain changed outputs,
Build Digests and execution settings. This semantic comparison complements the byte-exact drift gate.
See [native Plan and graph inspection](../docs/plan-and-graph-inspection.md).

For the checkout's six-output NKE deploy Definition, use:

```sh
bash demos/demo-team-catalog.sh --deploy
```

This route needs access to the private upstream Git objects: its catalog re-derives external chart versions
from the pinned upstream deploy file. It selects CWE, the controller chart, PostgreSQL, SPIRE, SPIRE CRDs
and Temporal. See [the deploy Definition](../definitions/nke-deploy/README.md). This maintained example is distinct from
the larger candidate Run used in the release stop below.

## Stop 2: follow the OS-image material graph

```sh
bash demos/demo-team-os.sh
# Optional second architecture:
bash demos/demo-team-os.sh --arch amd64
```

Load `os-plan-arm64.json` or `os-plan-amd64.json` into the explorer. Start at the disk root and trace the
agent, networking, packages and installer ISO into the source-owned Packer build. This is a real compiled
Plan from the current checkout, not a hand-drawn set of dependencies. Find the native platform, KVM and
resource requirements in the Environment. Runtime admission is separate from those declarations.
The script also emits the native `run graph --view producers --format mermaid` view and a complete
graph JSON projection. In the producer view, follow the `requires` edges into the disk-producing instruction.

The final dry-run is `pending`, `static-unqualified`, with zero observed outputs. For a completed local
execution, the retained QCOW2 and Run record live under the immutable attempt. The actual build is a
separate exercise after following the prerequisites in the [OS-image guide](../definitions/nke-os/README.md):

```sh
# On a prepared native Linux ARM64 Docker/KVM host; choose a fresh attempt name.
CONTAINER_RUNTIME=docker NKE_OS_ARCH=arm64 scripts/nke-os-local.sh team-os-arm64-001 1
```

This takes substantially longer than the presentation and produces local unqualified execution evidence.
The maintained Docker wrapper freezes the observed toolchain image ID per attempt. The [recorded ARM64
and AMD64 builds and boot smoke](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-01-nke-arc-os-builds.md) describe exact historical Runs.
They do not establish qualification, FDE support or production acceptance for a different checkout.

## Stop 3: navigate the candidate release

The selected [GitHub Run 37533806963](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963)
uses the `nke-m2` Definition on candidate branch `codex/m2-arc-validation` at
`625980e30b1a9f5ef27a9b46534de65eefff385a`. Its workflow display name still says `nke-deploy`.
Its 34 outputs comprise 25 OCI images, five Helm charts, two executable bundles and two OS-image outputs
(ARM64 and AMD64). The disk outputs use `build-provenance-v1` evidence, not a full native disk SBOM. Keep that candidate's results separate from the current
checkout's supported paths and from the small examples in Stops 1 and 2.

Use the explorer's **Pipeline Release** button during the presentation, or open
[the ARM64 disk directly](demo-team-explorer.html#publication/nke-node-image-arm64). See its
[capture, status and exact identities](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md). The saved CLI browse view
is a metadata description; opening it does not refetch metadata or verify published payloads, signatures or
policy. The archived workflow result and final Run record report 34 expected outputs that
match 34 observed outputs. Its successful finalization is distinct
from the publication/signing jobs and from an independent consumer-policy decision.

The saved release uses publication-root v3. Current NKD requires v4 and rejects it, including from
a local layout. Use the embedded explorer for this historical snapshot. To replay the commands below,
set `NKDCTL_BIN` to a separately built CLI from the exact historical PR #108 head above. For a current-CLI
browse or verification exercise, replace `RELEASE_REF` with a current v4 publication root.

Historical metadata replay:

```sh
export RELEASE_REF='ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases@sha256:292524ba607d5a45326ae4801e81867f5358921824df3e12c59d274360baaf0e'
bash demos/demo-team-release.sh --reference "$RELEASE_REF"
```

Browsing fetches the release index, manifests, configs and bounded evidence documents. It checks their
digests, sizes, schemas and memberships, without fetching image layers, executable archives or QCOW2 bytes.
All metadata and evidence memberships are validated before an output filter is applied. Private GHCR
access uses the existing local credential keychain; add `--anonymous` only when appropriate. Preflight
registry access before the meeting, or use a saved metadata-only layout for the offline route.

Load the exported `release-view.json` into the explorer. Start at the release root, choose an OS image,
inspect its config-declared platform and exact identities, then follow its evidence subjects. Contrast the OCI manifest
identity with the declared disk payload identity. Browsing does not measure the disk bytes. Compare a chart or executable to see the same output/evidence
model across artifact types. Search for exact output names rather than inferring platform from a label.

Native CLI navigation and graph export are also available:

```sh
# Native text focus and Mermaid export; each validates the complete metadata closure.
bash demos/demo-team-release.sh --reference "$RELEASE_REF" \
  --output nke-node-image-arm64 --mermaid

# A saved full or metadata-only OCI layout avoids repeated network access.
bash demos/demo-team-release.sh --reference "$RELEASE_REF" \
  --layout /path/to/release-metadata-layout --output nke-node-image-arm64 --mermaid
```

Mermaid output is a diagram source file; it needs a Mermaid-capable renderer. The HTML explorer renders
the JSON offline with its own inline SVG. A local-layout view derives registry locators from the supplied
reference and does not establish that those locations are available online.

## Optional stop: apply a consumer policy

`release browse` checks metadata integrity and evidence membership; payload bytes, signatures and policy
remain unverified. The separate `release verify` command accepts a reference plus the
consumer's trusted identity, issuer and evidence scope. Run this on Linux with an existing, digest-pinned,
statically linked cosign binary and a signed publication compatible with the reader:

```sh
"${NKDCTL_BIN:-bin/nkdctl}" release verify --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope artifact-publication \
  --identity "$ACCEPTED_BUILD_IDENTITY_REGEXP" --issuer "$ACCEPTED_OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST"
```

Supply policy values your team has accepted. Do not broaden a default-branch policy merely to make a
candidate pass. The candidate Run above used a branch identity; it is not the maintained `main` identity.
CI verification is evidence of that pipeline's checks, distinct from an independent consumer invocation.

The supported `pipeline-attested` / `artifact-publication` scope checks signed production evidence and
exact published contents. It does not establish qualified reproduction, release qualification/revocation
acceptance or deployment readiness. The stronger unavailable scopes fail closed. Details and SDK examples
are in [the lifecycle guide](../docs/lifecycle.md#run-and-verify).

## Explore the rest at your own pace

| Capability | Follow-up | Boundary to keep visible |
|---|---|---|
| Native producer/artifact graphs and Plan comparisons | [Plan inspection](../docs/plan-and-graph-inspection.md) | Descriptive graph/diff views cannot authorize execution |
| Pin previews, source/tool/action updates and deterministic maintenance | [Catalog maintenance](../docs/catalog-maintenance.md), `scripts/demo-catalog-change.sh --help` | Preview and data maintenance do not publish an accepted Release |
| Catalog reuse, whole-producer overlays and output-specific selection | [Authoring lab](../docs/examples/authoring-lab/README.md), [toolchain substitution example](demo-team-concepts.html#toolchain-substitutions) | The lab exercises producer replacement and selection; toolchain substitution is a separate example |
| Pin sets and acquired Helm charts | [Pin-set usage](../docs/nke-pin-set-usage.md), [generic example](../docs/generic-pin-set-example.md) | Pinned-tree access is required; referenced container images are not implicitly acquired |
| Local CLI/image/chart execution and pull-back | [Local walkthrough](m1-local.md) | Docker/source access required; local and dry-run signing are unqualified |
| Image-bound chart packaging | [Declarative Helm bindings](../docs/declarative-helm-bindings.md) | Consumer chooses deployment topology and repository overrides |
| SBOMs and production provenance | [Dependency SBOMs](../docs/dependency-sboms.md), [publication architecture](../docs/publication-architecture.md) | Evidence names exact outputs/subjects; a chart SBOM is not a scan of its runtime images |
| Integrity failures and malformed inputs | `scripts/demo-tamper-detection.sh --help` | Negative demonstrations establish their exact checked contract |
| CI logs, transfer and failure diagnosis | [CI log guide](../docs/ci-step-and-log-readability.md) | A missing finalizer record is never success |
| Programmatic integration | [Lifecycle SDK example](../docs/lifecycle.md#use-the-sdk) | Exported JSON cannot recreate an opaque verified capability |
| OS inventory and installed package inspection | [Live disk inspection](../docs/nke-os-live-inspection.md) and candidate Run evidence | Keep metadata inspection, full disk evidence and operational qualification distinct |

This is the feature map for a new team, not a promise that every internal diagnostic is production-enabled.
The current [implementation/evidence record](../docs/adr/reconciliation.md) owns deferred capabilities: full qualified
release acceptance, FDE, additional CI renderers, air-gap lifecycle and deployment orchestration are not part
of the supported end-to-end demo. The candidate Run's exact evidence can demonstrate more than this
checkout's maintained paths without retroactively changing those support claims.

## Walkthrough validation

[Recorded October 6–7 checks](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-07-team-presentation-validation.md) cover the
PR #108 CLI, catalog/OS scripts, embedded release snapshot, original browser pages and clarity pass.
They distinguish presentation and metadata inspection from production execution and independent
consumer verification. Later document edits do not inherit those earlier results.
