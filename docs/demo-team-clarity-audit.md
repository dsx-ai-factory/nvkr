# NKD team tour clarity audit

Reviewed October 7, 2026 for an audience new to NKD. The audit covers all 33
[HTML concept examples](demo-team-concepts.html), the
[13-slide introduction](nkd-team-tour.html), and the
[guided walkthrough](demo-team-walkthrough.html).

## Findings and changes

| Confusion | Change made |
|---|---|
| Components, outputs and artifacts appeared interchangeable. | Introduce them as a named piece with a recipe, a promised deliverable, and the actual produced bytes. Use `doctor` and `diagnostics` to show the distinction. |
| Overlay replacement and replacement scope were explained together. | Separate what the overlay changes from where the Definition applies it. Show the exact field location next to before/after output comparisons. |
| “Substitution” covered several unrelated edits. | Compare a default-catalog overlay, an output-specific catalog stack and selection of an existing alternate component. Explain toolchain substitutions and version stamps separately. |
| Examples changed names and contexts without introduction. | Use one small catalog example throughout the authoring explanation; explicitly introduce the real NKE examples for execution and release browsing. |
| Important explanations were hidden behind numbered buttons. | Keep the scenario, reading guide, three explanation points and expected result visible. Put complete schema details and longer command excerpts in labeled disclosures. |
| Digests and internal terminology arrived before the question they answer. | Explain Build Digests as fingerprints of build inputs, then distinguish them from payload and registry-manifest digests. Introduce acronyms in context. |
| Generator/builder, Plan/graph and signature/provenance/SBOM distinctions were unclear. | Give each a concrete purpose and comparison in its own example. |
| Some captions described earlier versions of the examples. | Correct file ownership, omitted-field notes and fixture scope. Correct the version-stamp result: `.ModuleTag` strips `cli/`, leaving `v0.1.0-rc.2`. |

## Catalog comparison

The baseline has `diagnostics` selecting `doctor` and `tools` selecting `doctor`
plus `status`. The catalog also offers `doctor-alt`, unused at baseline. The lab
uses small shell scripts as stand-ins for these commands.

Each row starts independently from that baseline:

| Edit | diagnostics uses | tools uses | Changed Build Digests |
|---|---|---|---|
| Add `supplier.json` to the Definition's default catalog | Supplier doctor | Supplier doctor + status | diagnostics, tools |
| Add the same overlay only to `tools.catalog` | Original doctor | Supplier doctor + status | tools |
| Change `diagnostics.components` to `[doctor-alt]` | Existing doctor-alt | Original doctor + status | diagnostics |

The overlay replaces the whole producer behind the name `doctor`, changing a
source build to acquisition of pinned supplier bytes. Its placement determines
which outputs use that recipe. Choosing `doctor-alt` selects another existing
catalog entry and uses no overlay. All three variants retain the output names;
the other five fixture outputs retain their Build Digests.

“Changed” here describes build inputs and instructions. The lab does not build
or compare final artifact bytes. The interactive comparison makes that limit
visible next to its results.

## Validation

- Replayed `bash docs/examples/authoring-lab/run.sh --nkdctl bin/nkdctl` on the
  PR #108 CLI. All four comparisons passed: source-pin changed the four app
  outputs; whole-producer changed diagnostics/tools; output-catalog changed
  tools; output-selection changed diagnostics. The lab also passed its rejected-input checks.
- Checked all 33 concept routes at 1600px and 390px widths, including expanded
  technical examples: no document overflow or browser page errors.
- Checked the three replacement controls against the expected output matrix,
  keyboard activation and retained focus, exact clipboard contents, offline
  topic navigation, and return links to slide 5.
- Checked all 13 slides at 1280×720 and 1600×900: no clipped or scrolling slide
  content. Checked keyboard and button navigation.
- Checked the walkthrough at desktop/mobile widths and inspected rendered
  catalog, replacement and slide examples.
- Passed inline JavaScript syntax, SVG XML, unique IDs, local HTML links,
  embedded rendering assets, source-path existence and `git diff --check`.

The existing full `make check GO_TEST_FLAGS=-count=1` result for this PR #108
implementation is recorded in [walkthrough validation](demo-team-guide.md#walkthrough-validation).
This clarity pass changes presentation documents only; it does not change the
Go implementation, scripts, catalog inputs, generated pipeline or retained
release evidence. These checks add no execution, signing or consumer-verification
claim beyond the scopes already recorded.
