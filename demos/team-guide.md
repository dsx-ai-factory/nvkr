# Presenting NVKE Recipes

Use the [14-slide deck](nkd-team-tour.html) for the story, the [concept gallery](demo-team-concepts.html)
for questions, and the [HTML walkthrough](demo-team-walkthrough.html) for copyable commands.
The [graph explorer](demo-team-explorer.html) displays a generated one-output `hello` Plan and recorded
release metadata offline. The embedded Plan is a planning example, not the Plan of the featured NKE release;
use **Open JSON** for a full NKE or OS Plan exported from your checkout.
Keep all four HTML files together; they need no server or runtime downloads.

NVKE Recipes turns declared build inputs into generated pipelines and traceable releases. The deck
explains the concepts without requiring knowledge of a demo script. Use the linked examples when the
audience wants to see a concept in action.

## Present the concepts in 12–15 minutes

The 14 slides give the high-level story. Follow the diagrams and short CLI snippets; open a gallery
example when a question needs more detail. The snippets illustrate operations using your Definition,
Environment and artifact paths. Use the walkthrough for complete setup and runnable examples.

| Time | Slides | Explain |
|---|---|---|
| 0–3 min | 1–3 | Purpose, lifecycle and authored inputs. |
| 3–6 min | 4–6 | Exact pins, scoped changes and the generated Plan. |
| 6–9 min | 7–9 | Generated CI, Run completeness and OS-image materials. |
| 9–12 min | 10–12 | Release structure, browsing and signed evidence. |
| 12–15 min | 13–14 | Consumer policy, questions and next steps. |

Use `←` / `→` or Space to advance, Home / End to jump, and F for fullscreen. The slides and companion
pages adapt to the browser size. In the explorer, select a node and choose **Focus node** to see its
direct relationships. **Show output path** restores the broader graph; **Fit** follows window resizing.

The gallery covers all 34 concepts. The optional live stops below add about 10 minutes: build a small
artifact at slide 8, then browse the hosted release at slide 11. They illustrate the concepts without
being prerequisites for understanding the deck.

## Prepare once

Use the exact Go version in `.go-version`, Git, Python 3 and a running Docker daemon. Build the CLI
from the repository root, then rehearse the local demo to warm its helper and small Alpine image:

```sh
GOTOOLCHAIN=local make build
bash demos/demo-local.sh
```

The local build needs no NKE checkout or registry credentials. `GO_BIN` and `NKDCTL_BIN` can select
installed executables. The resulting script runs on the host; its build executes in a Linux container.
First use can take longer while downloading the toolchain image and compiling the helper.

Prepare the catalog and OS exports before presenting:

```sh
bash demos/demo-team-catalog.sh --non-interactive
bash demos/demo-team-os.sh --non-interactive
bash demos/demo-team-os.sh --arch amd64 --non-interactive
```

Each script prints a new export directory. Load its canonical `plan.json` or `os-plan-*.json` through
**Open JSON** in the explorer. These planning demos need no Docker after the CLI is built.
The catalog script generates and checks a GitHub bundle in scratch space; it does not dispatch CI.

The catalog, OS and release scripts pause by default; `--non-interactive` skips pauses.
The tiny local demo runs straight through by default; `--pause` pauses before each step.
All support `--out NEW_DIR`; its parent must exist and the directory must be new.

## Explain catalogs and changes

Keep three nouns separate: a **component** is a catalog entry with a producer; an **output** is a named
deliverable the Definition requires; an **artifact** is the actual result.

The gallery uses two bundles: `client-bundle` selects `app-cli`; `admin-bundle` selects `app-cli` and
`config-cli`. Compare three independent edits from that same baseline:

- Replace the `app-cli` producer in the default catalog: both bundles change.
- Apply the overlay only to `admin-bundle`: only that bundle changes.
- Make `client-bundle` select `app-cli-alt`: only the client selection changes.

A toolchain substitution is narrower: it replaces the pinned build container for explicitly selected
components while retaining their source and command declarations. A Plan diff shows the effect on
build identities and execution settings. `run generate --check` checks the entire generated bundle.

For an executable comparison of overlays and selection, run the
[authoring lab](../docs/examples/authoring-lab/README.md). Its older fixture uses different component
names; the three replacement operations are the same. It compiles Plans without executing producers.

## Build a real artifact

For the optional execution stop at slide 8, run:

```sh
bash demos/demo-local.sh --pause
```

Show the Definition's one `hello` output. Follow the four printed steps: pin source, compile the Plan,
execute, then check and use the artifact. Point out the completed Run and artifact digest before the greeting.
The script retains all inputs, logs and artifacts in the printed directory.

Run a different version to show why pins matter:

```sh
bash demos/demo-local.sh --version 1.1.0
```

The greeting becomes `Welcome` and the version becomes `v1.1.0`. Both the Build Digest and the artifact
bytes change. See the [local demo guide](quickstart/README.md) for each exported file.
This is real, unqualified local execution with a composition SBOM. It does not sign, publish, or scan dependencies.

## Follow the OS-image materials

At slide 9, open the ARM64 Plan. The consuming producer declares exact mappings such as
`agent: nke-agent`: an input label on the left, a component key on the right.
The disk output's `components` list explicitly includes the root and every transitive material.
NVKE Recipes validates that selection against the catalog graph; the list is not positional wiring.

NVKE Recipes checks and stages the input bytes. NKE's source-owned commands understand the packages and
archives, and Packer assembles the disk. Use the [Materials concept](demo-team-concepts.html#materials)
for the full explanation. The separate OS script compiles and dry-runs the Plan; it does not bake a disk.
Actual disk execution requires the matching host capabilities in the [OS-image guide](../definitions/nke-os/README.md).

## Browse the hosted release

The featured [Run 37736529644](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37736529644)
uses commit `105bb738521c9dbd7f1326ab15e78a699200f4f1` and publication-root v4.
Here **v4 names the publication metadata schema**, not an NVKE Recipes product version. The root binds its Plan
Digest along with the Definition and Environment identities. Earlier team-demo runs emitted v3 metadata;
current NVKE Recipes rejects those roots. Their historical captures remain available in Git history.
The successful consumer command can still emit `nkd.dev/release/v3`: that is the separately versioned
consumer Release description, not the stored publication-root schema.

Use the explorer's **Pipeline Release** view for a repeatable offline presentation.
The saved view contains 34 outputs and 60 evidence records, including both QCOW2 disks.
For a live browse, run:

```sh
export RELEASE_REF='ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases@sha256:2fef87f426734a4427f7694aaf5ac181ba11352ea37522349bbb9cda7d84dc42'
bash demos/demo-team-release.sh --reference "$RELEASE_REF" \
  --output nke-node-image-arm64 --mermaid
```

Live browsing needs existing registry credentials for this release. Preflight access before the session.
For offline browsing, use an authenticated GitHub CLI to download the small metadata carrier once:

```sh
DEMO_METADATA=$(mktemp -d)
gh run download 37736529644 --repo dsx-ai-factory/nke-recipes \
  --name nkd-package-release --dir "$DEMO_METADATA/download"
bin/nkdctl transport-unpack \
  --archive "$DEMO_METADATA/download/package-release.tar" \
  --root "$DEMO_METADATA" --path .nkd/factory/packages/release
bash demos/demo-team-release.sh --reference "$RELEASE_REF" \
  --layout "$DEMO_METADATA/.nkd/factory/packages/release" \
  --output nke-node-image-arm64 --mermaid
```

The Run artifact contains `package-release.tar`. The unpacker admits only its declared
`.nkd/factory/packages/release` subtree; the resulting OCI layout includes metadata and evidence, with no
image layers, executable archives or QCOW2 payloads. Reuse `$DEMO_METADATA/.nkd/factory/packages/release` for subsequent `--layout` calls. Archive extraction alone is not signature or payload verification.

Choose ARM64, select the artifact, then follow an evidence membership to its component, stage and exact
subject. Compare AMD64, a chart and a CLI. Distinguish the OCI manifest digest from the disk's declared
QCOW2 payload digest. Load the script's `release-view.json` into the explorer to inspect your own export.

Native browsing validates bounded metadata and evidence memberships. It does not download payload
layers, verify signatures or apply consumer policy. Opening saved JSON in HTML is a descriptive view;
it does not repeat the CLI's checks. Disk `build-provenance-v1` evidence is not a full installed-software inventory.

## Explain signing without overclaiming

An SBOM describes software for a subject. Production provenance describes its build inputs and workflow.
A signature authenticates the workflow identity making a statement about exact bytes.
**It does not claim ownership of the software.**

Consumer verification is a separate operation against an explicitly accepted identity, issuer, evidence
lane and scope. The supported `pipeline-attested` / `artifact-publication` scope checks signed production
evidence and exact published contents. It does not establish qualified reproduction, full qualified
release acceptance, revocation acceptance or deployment readiness.

The [consumer script](../scripts/demo-release-consumer.sh) runs natively on Linux and uses its pinned
Linux Docker runtime automatically on macOS. Supply a pinned static Linux amd64 cosign executable and
an accepted policy; private access can use an inline-auth Docker config via `--registry-config`.
See the [release quickstart](../docs/release-quickstart.md#verify-against-an-accepted-policy) for exact flags.
Keep this optional operation separate from the short metadata-browsing stop.

The macOS Docker route was exercised successfully against this run’s single-output `nkectl` publication:
`ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases-nkectl@sha256:768b0ef67a09649465a66ff360eb44e74237a22c66da34bcee961247f06bd4d9`.
It accepted the exact `main` workflow identity and GitHub OIDC issuer under the scope above. This check
covers that CLI publication; it does not extend to the combined release or disk payloads.

## Follow-up routes

The [concept gallery](demo-team-concepts.html) includes pin sets, version stamps, overlays, toolchain
substitutions, builder types, generator adapters, graph views, Helm bindings, completeness, SBOMs,
publication identities, verification policy, maintenance and the Go SDK.

Use the [Markdown quickstart](../docs/quickstart.md) for first-time planning, the
[full local NKE demo](m1-local.md) for CLI/image/chart execution and pull-back, and the
[documentation index](../docs/README.md) for task guides. The
[reconciliation record](../docs/adr/reconciliation.md) owns current support and evidence limits.
