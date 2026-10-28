# Demos

Use these guided examples to explore NVKR with a team or at your own pace. Start with the
[quickstart](../docs/quickstart.md) for a local three-artifact NVKE build, or
[build only the CLI](../docs/quickstart.md#build-only-the-cli) for planning and inspection.
The [concept reference](../docs/user-concepts.md) explains the core ideas. The [documentation map](../docs/README.md) connects deeper tasks.

## Team tour

The refined team demo explains NVKR from catalogs and pins through real execution, generated pipelines,
OS-image materials and release navigation. Its 14-slide overview uses diagrams and short CLI snippets,
with links to 34 worked concepts and an offline graph explorer. Allow 12–15 minutes for the deck and
extra time for optional demos. The featured hosted release comes from Run 37736529644 and uses publication-root v4.

| Open | Purpose |
|---|---|
| [14-slide deck](nvkr-team-tour.html) | Present the core story |
| [Concept gallery](demo-team-concepts.html) | Explore worked examples from the slides |
| [HTML walkthrough](demo-team-walkthrough.html) | Follow the guided discovery steps |
| [Offline graph explorer](demo-team-explorer.html) | Navigate saved Plans and the featured release |
| [Facilitator guide](team-guide.md) | Present the concepts and optional live stops |

Open the HTML files directly in a browser. They include their assets and need no web server.
The deck’s CLI snippets illustrate the operations using your input paths; the walkthrough supplies full setup and runnable examples.

## Self-guided scripts

Run from the repository root with a built `nvkr`. The catalog, OS and release scripts pause at
checkpoints; the local demo pauses with `--pause`.
Each prints its export directory. Use `--help` for prerequisites and options.

```sh
bash demos/demo-local.sh --help
bash demos/demo-team-catalog.sh --help
bash demos/demo-team-os.sh --help
bash demos/demo-team-release.sh --help
```

The tiny local demo builds a real executable in Docker and runs the portable result on the host;
start with its [guide](quickstart/README.md).
The catalog and OS tours inspect inputs, graphs, and dry-runs without building artifacts. The
release tour reads bounded metadata and evidence; payloads, signatures, and consumer policy
remain unverified. The [facilitator guide](team-guide.md) explains the separate verification step.

## Execution and focused examples

| Journey | Start here |
|---|---|
| Build your first artifact in seconds after warm-up | [Tiny local demo](quickstart/README.md) and [script](demo-local.sh) |
| Build, publish, and recover the local NVKE CLI | [Local NVKE walkthrough](m1-local.md) and [script](../scripts/demo-m1-local.sh) |
| Bake an OS image on a prepared host | [OS-image routes and prerequisites](../inputs/definitions/diagnostics/README.md) |
| Understand the native Linux/Mac site plus two-guest topology under validation | [Shared native path](../docs/local-nvke-demo.md#native-linux-and-mac-path) |
| Build and deploy a real local ARM64 QCOW2 tenant | [Ubuntu ARM64 guide](../docs/local-nvke-demo.md) and [declarative demo inputs](local-nvke.yaml) |
| Compare catalog overlays and component selection | [Runnable authoring lab](../docs/examples/authoring-lab/README.md) |
| Preview and apply source-pin changes | [Catalog change script](../scripts/demo-catalog-change.sh) |
| Explore digest and containment rejection | [Tamper detection script](../scripts/demo-tamper-detection.sh) |
| Verify a signed publication under a consumer policy | [Release consumer script](../scripts/demo-release-consumer.sh) |

Operational scripts stay in `scripts/`. Their [usage guide](../scripts/README.md) explains runtime,
credentials, storage ownership, and cleanup. The [facilitator guide](team-guide.md) and
[reconciliation](../docs/adr/reconciliation.md) describe the subjects and limits of recorded demonstrations.

## Before sharing an updated tour

Check local links and heading anchors in the deck, sibling HTML pages and Markdown guides. Open each
linked page and confirm its referenced section exists; exercise query/fragment routes separately. Check
remote historical references separately from local links, and retain their exact source/schema limits.
Run `make license-header-check`, `make shellcheck` and `git diff --check`. Syntax-check inline JavaScript
with `node --check` after extracting each executable script. These static checks do not establish browser
rendering, registry availability or NVKR execution.

After editing the explorer’s executable inline script, recompute the base64-encoded SHA-256 of its
exact text between `<script>` and `</script>` (including whitespace), and replace the `script-src` hash
in its Content Security Policy. The hash permits only those exact offline script bytes.
