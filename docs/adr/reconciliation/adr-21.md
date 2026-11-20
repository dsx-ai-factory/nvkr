## Support by owner

Interactive catalog-family bumping prepares the explicit multi-catalog closure before writing working files.
Local Git fixtures cover cross-catalog release consistency, excluded cohorts, unchanged dry-run files and
second-catalog preparation failure. Injected writer tests cover rollback, cancellation and concurrent edits,
including retained recovery backups when another writer changed a committed member. Additional fixtures
inject edits immediately before/after withdrawal, change unchanged inputs during publication, and
validate edited workflow/policy bytes captured after initial admission. Publication checks the actual
displaced bytes and uses no-replace links; paths may briefly be absent. These contracts do not
establish cross-file crash atomicity, hosted family maintenance or producer execution. Scheduled refresh
retains its existing source selection and separate owner transactions.
Family commitment holds the shared Plan-store guard, publishes manifests before retiring captured
objects removed by ordinary staged generation, and writes the derived index last. Injected tests cover
retirement rollback, cancellation and concurrent saves. Directory additions or retirements require
ordinary authored regeneration and are refused before commitment.
The family path generates static authored bundles without freezing runtime source heads, then repeats
owner checks. Runtime-source fixtures retain capture refusal and exact-commit dispatch admission; family
generation supplies neither a frozen snapshot nor a source commit. Explicit source-bound Ubuntu24 copies
and their predecessor chains regenerate through the overlay owner while preserving producer contracts.
The real default-family no-op dry run checked the existing closure and resolved every selected pin; the
no-change path does not run staged generation or commitment. Changed-candidate preparation, second-pass
generation and rollback are covered by local fixtures, not an externally exercised family version advance.

Explicit producer-overlay maintenance now checks the existing generated baseline before pin mutation,
copies complete producers from named catalogs, and derives ordered predecessor bindings before Run
generation. The NVIDIA/upstream TCG copies and their two dependent local KVM bindings are enrolled;
the KVM replacement producers remain frozen. The maintenance workflow admits only the configured
destination paths in its patch. Deterministic/no-write checks, stale-writer refusal, selection/order errors
and lock-only propagation have local contract coverage in `producer_overlays_test.go`. Lock, overlay-set
and Run-bundle commits remain separate transactions; interrupted regeneration requires rerunning the
owners and all checks. No hosted refresh or producer execution validates this migration. The
[maintenance guide](../../catalog-maintenance.md#maintain-producer-copies) owns recovery and the excluded-copy inventory.

Native image maintenance (#243) adds an authored main-branch workflow owned by `njhensley`, with Monday
06:00 UTC preparation and relevant-source change triggers. It reuses native preparation and verification,
retains separate image archives, admits a complete four-image set against reviewed per-image exceptions,
publishes only to existing internal candidate packages, and requires independent-reader read-back plus
native pulls before proposing catalog/overlay/Environment changes and owner regeneration. Offline contracts
cover invalid/expired exceptions, incomplete sets, source-attempt mismatches, archive/configuration and
measurement substitution, overlay rebinding and profile updates, and review-branch retry/refusal behavior.
The current authored-input inventory has a read-only repin preview; that is not a live refresh.
Refresh notification contracts cover reuse of one labeled or legacy-title issue,
create-once retry routing, lookup failures, and silence for unchanged successful
measurements and successful publication. Native image push paths are checked against
the module-local inspector dependency graph for both Linux architectures. Manifest
comparison and routing are diagnostic only; these offline tests do not exercise
GitHub issue writes, hosted matrix outputs or a live refresh.

The [publication attempt](https://github.com/dsx-ai-factory/nvkr/actions/runs/42939744937)
admitted preparation `42904976235` attempt 1, published all four approved candidate subjects, and passed
independent-reader native verification on AMD64 and ARM64. Its proposal failed after repinning:
generated overlays had new toolchain pins while their frozen producer policy retained the old pins,
so catalog-refresh generation returned `pipeline-drift`. Clean main at `d4cb377b` passed both overlay
and catalog-refresh checks before repinning; the failure was introduced by the proposal path.
Repin now updates the named producer-overlay policy, and the proposal regenerates overlays before
dependent workflows. A contract fixture reproduces the old drift and verifies that regeneration
preserves the new pins without editing exception records. Workflow-order and proposal-path contracts
retain the pre-repin drift check and restrict policy edits to that one owner file.
Explicit native substitutions also bind local configuration-ID replacements to the approved
image IDs. The overlay regression covers base/frozen producers and substitution sources; ambiguous
cross-architecture mappings return a coded error before any input is written. The same fixture
checks local diagnostic predecessor bindings; the proposal admits only their exact authored file
under `toolchains/`, leaving image build locks outside its write set.
Maintained consumer pins and local AMD64 image IDs select these published subjects; disk producer
contracts require Packer `1.16.2`, matching the candidate source lock. Frozen/local overlays and generated
bundles follow those inputs. [Development Run 43774985236](https://github.com/dsx-ai-factory/nvkr/actions/runs/43774985236)
stopped all eight disk builds at `packer validate`: the pinned NVKE project requires Packer `= 1.15.4` and
QEMU plugin `= 1.1.6`. Disk producers that select Packer `1.16.2` now carry a content-addressed catalog patch that moves both pins to
`1.16.2` and `1.1.8`. A local `packer validate` in both pinned images accepts the patched project.
The patched disk Build identities have not been executed end to end.
The AMD64 local demo used by `nvkr-nightly.yml` has a separate diagnostic selection:
Packer `1.15.4` and QEMU plugin `1.1.6`, matching NVKE main at `2f31480b360cbd7fc9d9db7a6ba274a8d0fd5049`.
The existing builder selects those pinned archives and executables only with `--nightly`;
its default maintenance inputs, published image pins and ARM64 selection are unchanged.
This variant carries no version-pin patch. The other disk producers declare Packer `1.16.2` and carry
the catalog patch above.
The AMD64 variant's committed toolchain pins retain the `1.16.2` candidate selection.
The launcher must replace them with its fresh diagnostic image to satisfy the variant's
`1.15.4` contract; executing the variant with its committed pins fails closed at preflight.
Local contracts check selection, admission and build/validation arguments. A successful hosted
build/site/tenant/cleanup cycle with this rebuilt toolchain remains unrecorded.
The corrected hosted proposal/App exchange remains unexercised; this publication attempt does not
demonstrate a human environment-approval pause or complete refresh acceptance. GitHub rulesets,
protected-environment review, private carrier retention, review-record authenticity and registry
credential access remain operational prerequisites. Exceptions
are candidate-only; source-lock general license/security gates remain unresolved. The historical
[Go-pin rebuild](https://github.com/dsx-ai-factory/nvkr/actions/runs/39521422867) failed on both native
architectures at Docker archive export before validation; it supplies no publication evidence. Current
main already selects the container-backed Buildx driver for that export path.

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 21 Generation | Exact recorded input selection, native output utility placement from per-platform tool/container pins, independent fixed production/development layouts, manifest-owned semantic action interfaces and transient Plan-derived invocation data with expanded-step validation, output-local ordinary job membership with addition/removal and failure-accounting contracts, stable result-collector membership and exact retirement of legacy producer children, manifest-last ownership, deterministic checks/retirement, canonical Plan inspection, linked graphs, semantic diffs, atomic pin maintenance plus regeneration and review patch workflows; separately owned qualification pair and runtime policy drift admission; optional named production namespaces independently own their manifest, lock, paths and retirement; opted-in named runtime Plans compile current source pins within the complete reviewed workflow/action and Environment contract and distribute frozen data through same-Run helpers; provider artifacts contain JSON locators while private CI carriers use a digest-bound GHCR transport with package visibility and linked-repository admission; generated bootstrap and stage admission re-emits only through the core projection, never the top-level `factory.Render` seam (seam trap and committed-closure inventory contract tests) | Producer generation emits manual-dispatch workflows, not cron; optional qualification generation emits its fixed event/call pair. Authored downstream scheduling has separate ownership. Combining development with a nonempty named bundle is rejected before catalog resolution or generated-output work. The inline layout has guarded dispatch admission and isolated exact-result/failure diagnostics; complete production execution, automatic PR publication and scheduled/private-source service acceptance remain unexercised. The runtime Plan handoff and private CI carrier transport have local contracts; a hosted private-carrier producer-to-E2E cycle remains unexercised. Public-repository execution requires an independently admitted storage credential/access policy; public-linked storage fails closed. Historical Actions payloads and caches are not migrated by this implementation. The workflow `catalog update-ci` renders (installed as `catalog-refresh.yaml`) opens PRs with a GitHub App installation token minted in its `pull-request` job by the SHA-pinned `actions/create-github-app-token` action for this repository only, with contents, pull requests and workflows write, and revoked by the action when the job ends; the workflow token stays read-only (shape, action inputs, bot sign-off identity and step behavior contract-tested with a stubbed `gh`; a manual dispatch on 2026-10-16 failed at the earlier hand-rolled token step with an unattributed 404). No scheduled run, token exchange, push, PR, automatically started PR check, close or branch deletion has been exercised, so M4 acceptance item 1 is implemented but not demonstrated and M4 is not claimed. The review commit is created through the GitHub API with a DCO trailer for the App's bot user (contract-tested; GitHub signing and attribution not verified). Pin-only fixtures assert the production, development and qualification workflows stay identical; a topology, label, interface or action-pin change that regenerates a closure workflow is published in the same PR. One enrolled policy is supported until generated bundle roots are per-Definition. A branch that already holds the candidate is left alone, a closed unmerged PR whose head commit carries the same candidate stays closed even after its branch is deleted, fork PRs from a branch with the same name are ignored, a lookup page that could be truncated fails the step, and an unchanged Definition closes its stale PR and deletes its branch (contract-tested with a stubbed `gh` that applies each call's real filter). The ruleset, last-push approval and code-owner settings that protect the bot branch are repository settings that this workflow does not enforce; the release owner or an administrator must validate them before scheduled writes are relied on. The policy names the dependent development Run, qualification pair and additional named Runs (the two nightly bundles, checked and regenerated through the runtime-Plan owner because they compile current pins at dispatch); refresh admits each additional Run's catalogs before compiling it, and checks, regenerates and transports every member through its owner on an exact allowlist, refuses an unnamed committed bundle and a generator stamp that differs from the committed Plans (contract-tested with real local Git for the production and development bundles, and through the real qualification owner on a fixture with synthetic trust pins that contacts no GHCR or cosign; deployment-image selection is outside the closure and must be coordinated by hand before a moving release), and `make workflows-check` compares the installed workflow with `catalog update-ci` output (not exercised in the hosted gate). Producer matrices and general callable workflows remain deferred. Qualification generation does not make both owner writes one atomic transaction; lock and generated files are separate transactions; complete crash matrix remains |
| 21 Projection pilot (diagnostic) | Hidden `run generate --projection-pilot=in-process` selects the same-module GitHub or Tekton reference projection behind `factory.Render`. Describe/Emit binds engine, release, schema and ABI before Plan delivery; exact core validation admits all returned bytes before writing. Tekton's default built-in generation uses the same contract. Equivalence, malformed-emission, wrong-description and generation-boundary tests remain. The out-of-process transport, framing, serving API and hidden serving command are removed | Diagnostic only; no third-party executable selection or discovery. Runtime refresh refuses selection, and generated stages use core admission. No hosted Run has used a selected pilot. In-process equivalence is not external execution evidence. `Project` and placement remain deferred until semantic groups exist |

Stable worker paths are derived from the current Plan without previous-output input. Numbered
`__partition-NN.yml` workers are prior-manifest retirement members only; new workers use
`__w-<lowest member job id>.yml`. Namespace and generator retirement contracts cover the migration.
The rename changes generated workflow bytes and ownership manifests, not Plan or Build identity.
Hosted execution of the renamed bundles remains unrecorded.

GitHub Plan storage follows [ADR-21's shared-store contract](../0021-generated-pipeline-maintenance-and-lifecycle.md#1-the-emitted-pipeline-is-a-generated-artifact-not-an-owned-scaffold).
Local contracts cover exact canonical round trips, malformed references, missing or altered objects, unowned objects, cross-bundle retirement, whole-bundle removal and rename, interrupted-write recovery, concurrent writers and readers, aggregate bounds and runtime pin refresh.
The loader does not render workflows or actions; complete closure checks remain with generation and runtime bootstrap.
Packed inputs, CLI JSON and Tekton retain the canonical representation.
The Plan schema and digest contracts are unchanged.
The 2026-11-17 three-folder simplification preserves the exact reconstructed canonical bytes, three Plan Digests and 261 named Build Digests of the retained development, NVKE and nightly NVKE bundles at `b5109e62dbf95de245669fafd3e76968bd90ac9a`.
The store contains 22 Plan records, 87 step records with inline component configurations and source pins,
62 byte-identical scripts and one shared navigation index. Its only object folders are `plan`, `steps` and `scripts`.
Across the store and three private bundle directories, the change reduces files from 441 to 236,
nested directories from 218 to 187 and bytes from 3,460,553 to 2,977,305 (excluding workflows and local locks).
The store itself grows from 2,166,150 to 2,668,669 bytes because configurations are inline;
removing duplicated per-bundle indexes and reducing manifest membership saves 985,767 bytes elsewhere.
Named object paths remain unchanged when neighboring bundles are added, removed or reordered;
the shared index groups variants and records their full hashes and bundle users.
One graph object contains the unchanged 941 nodes and 1,537 artifacts shared by all three bundles.
Hosted execution of the simplified layout remains unrecorded.

Invocation data is regenerated from the Plan and is not a bundle member;
`TestGitHubActionDataIsTransientAndRetired` covers omission, prior-owned retirement and refusal of
modified or unowned copies. Hosted execution of the regenerated bundles remains unrecorded.

The generated shared `index.json` links each GitHub execution job/step to its frozen producer record,
exact script files and owning bundle workflows. Identical operations share one entry across bundles;
exact command operands remain in the step record. Plan loads do not consult this descriptive index.
`TestGitHubScriptIndexConnectsJobsToExactCommands` covers flat/partition routing, byte-exact script references,
shared job and bundle-user lookup, and separate object/navigation drift without adding a Plan-loader dependency.
Bundle independence contracts cover damaged neighboring private Plans and workflows; each bundle checks its own job mapping.
The maintained all-bundle check covers the complete navigation index. Legacy workflow retirement retains its owning manifest without admitting retired workflows into navigation.
Runtime pin-refresh contracts retain exact executable programs while regenerating descriptive record links.
`TestPlanStoreNamedPathGrammar` and `TestPlanStoreFullDigestDisagreementDoesNotPublish` directly cover the
named-path predicate and both conflicting-full-digest refusal branches. These are local contracts;
hosted execution of the navigation layout remains unrecorded.

## GitLab generated bundle

GitLab generation implements the fixed `.gitlab/nvkr-run/` pipeline/Plan/manifest namespace through the
shared generated-file owner. Its Plan retains packed canonical bytes; the shared Plan store above is
GitHub-only. Root `.gitlab-ci.yml` remains operator-owned; runtime admission requires its
single exact local include and the project's default root CI configuration.
The exact generated transport includes the native Docker-helper switch suppressing Runner's default
`umask 0000`, the native fresh-clone strategy and the token-free Git URL flag before all job checkouts.
Current workflow rules mirror the exact ref/source policy, require protected refs for publication and
end with refusal. These later policy changes do not inherit the earlier provider-run evidence below. Contract tests cover all-job
inheritance and coherent missing/disabled/substituted policy refusal. Shared manifest `0644` mode checks
remain unchanged. Fresh job-owned source checkout avoids the reproduced unchanged-file mode retention in
reused Runner storage. The historical [real server/Runner runs](adr-18.md#gitlab-ordinary-unsigned-profile)
exercised that policy across all eight jobs in their recorded source snapshots, with unchanged source
and zero retries; they do not validate this upstream integration.

Exact projection, conditional dependencies, custody/tampering, coexistence, ownership/retirement and symlink-refusal contracts are included
in the [validation record](adr-18.md#gitlab-ordinary-unsigned-profile). Controlled contracts, the real ARM64
recipe Run and full repository checks have separate scopes; none grants signing or qualification.
GitLab named namespaces, arbitrary root
customization, runtime/source refresh, qualification generation, catalog maintenance and projection-pilot
selection remain outside the admitted profile.
