# Team presentation validation, October 6–7, 2026

These are the recorded browser/script checks for the retained [deck](../../demos/nkd-team-tour.html),
[concept gallery](../../demos/demo-team-concepts.html), [walkthrough](../../demos/demo-team-walkthrough.html),
[explorer](../../demos/demo-team-explorer.html) and [facilitator guide](../../demos/team-guide.md).
Consolidation and relocation do not claim a fresh browser or production replay.

## PR #108 walkthrough

The CLI source was exact PR #108 head `00c71385580e798eb3d7f74b88fc694225cfe175`,
reporting `00c71385 (00c71385)`. The default catalog script passed validation, canonical
Plan export, native text/pretty inspection, Mermaid/DOT/JSON graphs, isolated generation,
byte-exact check and no-change comparison. Both OS architecture scripts passed graph exports
and dry-runs: pending, static-unqualified, zero observed outputs. The three scripts passed ShellCheck.

Release-script inspection of the exact Run 37533806963 metadata carrier matched the retained native
JSON. Its separate live registry replay returned the same 34-output/60-evidence bytes;
[the M2 capture](2026-10-06-team-demo-pipeline.md#pr-108-metadata-browsing-replay) retains its
source, commands, hashes, 184.875-second result and metadata-only limits. The private-source
`--deploy` route and independent consumer verification were not exercised.

Browser checks covered all thirteen slides at 1280×720 and 1600×900 without clipping; both disk
selections, the eleven-member evidence jump, offline Plan navigation, and the complete 348-operation
graph. HTML/JavaScript/SVG, local-link and script checks passed. Prior checks at
`61ec66fa4ffaf75fee291450f7572d244f2e624f` were not transferred to this PR head.

## October 7 concept and clarity checks

The presentation-only expansion supplied 33 concepts and four walkthrough chapters.
At 1600px and 390px widths, every concept route—including expanded technical examples—had no
horizontal overflow or browser page error. Checks covered search, visible reading guides,
explanation steps, offline navigation, originating-slide return links, exact clipboard contents
and manual-copy fallback, keyboard activation/focus, mobile diagrams and checklist/reset controls.
All thirteen slides still fit both presentation resolutions; browser shortcuts remained available.

The original shell-script lab retained its presentation names: `diagnostics = doctor`,
`tools = doctor + status`, with unused `doctor-alt`. Each comparison started independently:
a default supplier overlay changed diagnostics/tools Build Digests; an output-specific stack changed
tools; selecting the alternate changed diagnostics. Source-pin maintenance changed the four app
outputs. The PR #108 lab replay passed those four comparisons and rejected-input checks.
Those results describe build inputs, not produced artifact bytes.

The clarity pass separated components/outputs/artifacts, producer replacement from application
scope, and toolchain substitution from component selection. It corrected `.ModuleTag` to
`v0.1.0-rc.2` after stripping `cli/`. JavaScript syntax, SVG XML, unique IDs, source paths,
embedded assets and local links passed. The exact final markup revision was not separately recorded;
these are dated presentation observations, not proof for arbitrary later edits.

The earlier full PR-head repository gate was separately recorded as passing. These browser and
authoring checks add no artifact execution, signing, full-disk inventory or consumer-policy claim.

## Retained unsigned demo fixture

The explorer's two-architecture sample also retains the older
[unsigned loopback JSON](2026-10-03-release-navigation/remote.json) and
[source identities](2026-10-03-release-navigation/source.json). That October 3 snapshot used
base `d1a4ec9403a936b154b16f4c53e3d3f6daa4c739` plus local browser changes, NKE CLI source
`ed9fb09b04fd40bf331d25df2222206ffaca593e`, and descriptive fixture input identities.
Its temporary registry was stopped. This preserved presentation sample is not signed release
acceptance or evidence of current registry availability.
