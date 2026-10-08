# Demos

Use these guided examples to explore NKD with a team or at your own pace. For current user
instructions, start with the [documentation](../docs/README.md) and [quickstart](../docs/quickstart.md).

## Team tour

The original presentation follows catalogs, generated pipelines, OS-image materials, and release
navigation. Its featured release is the recorded October 6 M2 candidate; its graphs and results
describe that snapshot. Current NKD requires publication-root v4 and rejects this historical v3 root.
Use the embedded explorer for the recorded release; live browsing with current NKD needs a v4 publication.

| Open | Purpose |
|---|---|
| [13-slide deck](nkd-team-tour.html) | Present the core story |
| [Concept gallery](demo-team-concepts.html) | Explore worked examples from the slides |
| [HTML walkthrough](demo-team-walkthrough.html) | Follow the guided discovery steps |
| [Offline graph explorer](demo-team-explorer.html) | Navigate saved Plans and the recorded release |
| [Facilitator guide](team-guide.md) | Prepare and lead the 25-minute session |

Open the HTML files directly in a browser. They include their assets and need no web server.

## Self-guided scripts

Run from the repository root with a built `nkdctl`. Each script pauses at checkpoints and prints
the location of its exported files; use `--help` for prerequisites and options.

```sh
bash demos/demo-team-catalog.sh --help
bash demos/demo-team-os.sh --help
bash demos/demo-team-release.sh --help
```

The catalog and OS tours inspect inputs, graphs, and dry-runs without building artifacts. The
release tour reads bounded metadata and evidence; payloads, signatures, and consumer policy
remain unverified. The [facilitator guide](team-guide.md) explains the separate verification step.

## Execution and focused examples

| Journey | Start here |
|---|---|
| Build, publish, and recover the local NKE CLI | [Local NKE walkthrough](m1-local.md) and [script](../scripts/demo-m1-local.sh) |
| Bake an OS image on a prepared host | [OS-image routes and prerequisites](../definitions/nke-os/README.md) |
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
rendering, registry availability or NKD execution.

After editing the explorer’s executable inline script, recompute the base64-encoded SHA-256 of its
exact text between `<script>` and `</script>` (including whitespace), and replace the `script-src` hash
in its Content Security Policy. The hash permits only those exact offline script bytes.
