# Reviewable catalog refreshes

Catalog maintenance combines existing atomic pin updates with matching generated Run files. It does not
choose a new release policy: each selected independent source still uses the exact constraint in its
registry. The maintained policy,
[`inputs/policies/nvke-catalog-maintenance.yaml`](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/policies/nvke-catalog-maintenance.yaml),
selects `nvke` and `nvke-helm`, whose constraints remain `v0.21.0`. A normal upstream check therefore preserves
the current pins. Broader constraints or a different exact release require an authored, reviewed registry change.
The explicit controller chart package/app-version remains an authored producer input; maintenance does
not infer or rewrite it.

## Policy and source roles

A maintenance config names one catalog, explicit source roots, a Definition and its Environment. Paths
are relative to the repository root; run the commands there. This is an operational configuration, not
an additional public lifecycle object. The file contains exactly one YAML or JSON object; additional documents,
unknown fields, duplicate keys, YAML anchors and aliases fail before refresh or workflow generation. Path
escapes, symlinks in selected input paths, unknown sources and missing release constraints fail closed.

Other committed generated bundles can depend on the same pins: a development Run, a qualification pair (whose
manifest binds the production Plan digest) and named Runs compiled from other Definitions, such as the nightly
bundles. The optional `developmentEnvironment`, `qualificationPolicy` and `additionalRuns` (a list of
`definition` and `environment` pairs) fields name them. They are generation inputs for the existing owners, not
authority and not a way to relocate a bundle: the development Environment must select the development layout,
and the policy is the same qualification policy `run generate --qualification-policy` takes. Every named Run
must select its own bundle layout; a repeated selection or a shared layout fails with `NVKR-E-USAGE`. Refresh
checks every named member before it resolves a pin, regenerates each through its own owner (the Run generator,
or the qualification generator for production plus the pair), and repeats every check. A named Run whose
Environment sets `githubRuntimePlan` (the nightly bundles) compiles current pins when it is dispatched, so its
committed Plan may lag the lock: refresh checks and regenerates it through the runtime-Plan owner, which
authenticates every committed byte and then admits only Plan and action-data changes, never a workflow or action
change. An owner's coded failure
is preserved: drift stays `NVKR-E-PIPELINE-DRIFT` and a stage conflict stays `NVKR-E-WRITE-CONFLICT`. A
committed `.github/nvkr-*/manifest.json` bundle the policy does not name, including a qualification manifest
without a `qualificationPolicy`, is refused with `NVKR-E-ENGINE-CAPABILITY` before any pin mutation and before
workflow output, because a pin change would leave it stale and the bot could not transport the repair.
Regenerating a member does not qualify anything; qualification and release acceptance stay with their own runs.

`sameRelease` optionally groups independent source roles with identical repository, tag prefix and
constraint text. The catalog transaction checks that they resolve the same tag, commit, tree and epoch
before committing any pins. Toolchains and vendoring can still differ. The relationship applies to this
maintenance transaction; it neither adds `pinnedBy` nor invents upstream provenance. A directly invoked
`catalog bump` without the maintenance policy does not inherit the relationship.

Existing declared pin sets keep their existing meaning. Updating an umbrella resolves its source and
acquired members together, validates exact upstream membership, and writes the complete lock using the
existing compare-and-swap transaction. The NVKE umbrella derives its four external chart pins. A changed
upstream chart list, failed download, conflicting same-release selection, or concurrent lock edit fails
without committing a partial pin set. No source constraints, toolchain declarations or component commands
are rewritten by the refresh command.

Maintenance also rejects an existing version that now resolves to different source identity or acquired
member bytes, before either preview or commit. A moved tag or republished chart is an error, never an
automatic refresh of an exact pin. A newer umbrella may retain a member version and advance only that
member's `derivedFrom` provenance when its bytes are unchanged. This stricter maintenance policy is
invocation-scoped; explicitly invoked interactive pin/bump commands retain their existing repinning behavior.

## Preview and apply locally

Build the CLI and supply existing Git access to the canonical sources, then run:

```sh
go build -trimpath -ldflags '-X main.version=dev -X main.commit=unknown' -o bin/nvkr ./cmd/nvkr
bin/nvkr catalog refresh --config inputs/policies/nvke-catalog-maintenance.yaml --check --json
bin/nvkr catalog refresh --config inputs/policies/nvke-catalog-maintenance.yaml --json
```

The owner first compiles and validates the full current Plan, then checks the existing generated bundle.
This detects unrelated generation drift before any pin update. The CLI version must match the
`generatorVersion` recorded in the committed Plan; the commands above use the maintained bundle's
`dev (unknown)` generator identity. A Git-stamped `make build` has a different identity and correctly
reports drift until its own bundle is deliberately regenerated. The command uses a bounded context
(default 15 minutes); `--timeout` can raise it for a larger upstream closure.

`--check` resolves the complete candidate pin transaction and emits descriptive changes. It writes no
lock or generated files and returns status 3 (`NVKR-E-DIFF`) when pins would change. It does not claim that
the candidate has compiled or executed. Applying commits the atomic pin transaction, invokes the normal
Run generator, then repeats compilation and the byte-exact generated-file check. An unchanged transaction
preserves lock and generated bytes and returns `changed: false`; registry outages remain errors, not no-ops.

Use a disposable branch/checkout for applying updates. Lock replacement and generated-bundle replacement
are separate owner transactions. If generation fails after the lock commit, the command reports failure
and leaves the checkout for diagnosis. The original coded failure is retained: cancellation remains retryable
`NVKR-E-TIMEOUT` (31), while a generated-file mismatch remains `NVKR-E-PIPELINE-DRIFT` (51), including
the final regeneration check. It does not roll back another process's work or claim cross-file
crash atomicity. Regenerate after correcting the inputs, or discard that disposable checkout. Never publish
a candidate from a failed refresh. Concurrent lock updates retain the existing coded write-conflict behavior;
workflow concurrency serializes scheduled runs, not human editors.

## Generate the maintenance workflow

Run the generator from the repository root. It takes no policy path: the workflow it renders discovers the one
`inputs/policies/*-catalog-maintenance.yaml` itself when it runs.

```sh
make catalog-refresh-generate
actionlint .github/workflows/catalog-refresh.yaml
```

The target builds the generator with the canonical stamp, renders to a temporary file and replaces the
installed workflow only when rendering succeeds. Do not redirect the generator straight into the installed file:
the shell truncates it before the generator runs, so a refused render (source, stamp or closure admission)
would leave it empty. By hand, build the canonical generator, render to a temporary file and move it into place
only on success. A `make build` binary carries a different stamp and is refused with `NVKR-E-PIPELINE-DRIFT`:

```sh
make workflow-generator
bin/nvkr-exec catalog update-ci --source-checkout dsx-ai-factory/nvke:NKE_SOURCE_TOKEN > /tmp/catalog-refresh.yaml &&
  mv /tmp/catalog-refresh.yaml .github/workflows/catalog-refresh.yaml
```

Change the generator or its flags, never the installed file: edit the renderer, then regenerate and review the
diff. The runner image's `yq` and `jq` are required by the rendered steps; both ship on GitHub's hosted Ubuntu
images, and the steps read only the documented fields.

| Flag | Meaning |
|---|---|
| `--schedule` | Reviewed cron for the scheduled run; defaults to nightly `0 2 * * *`, empty means manual only. |
| `--source-checkout` | `owner/repository:SECRET_NAME` of the private source; omit it for public sources. |
| `--app-secrets` | `CLIENT_ID_SECRET:PRIVATE_KEY_SECRET` of the GitHub App that publishes; defaults to `RELEASE_BOT_CLIENT:RELEASE_BOT_SECRET`. |
| `--engine` | Only `github` is supported. |
| `--timeout` | Bounds resolution and rendering while the policies are admitted. |

Each `--schedule` field is `*` or one in-range decimal integer, and the minute must be an integer; lists, aliases,
steps and ranges are rejected. `--source-checkout` adds the one step that receives the private-source secret and
passes the repository to every `catalog refresh` as `--source-repository`.

The generator reads the policy that matches the glob only to refuse output the generation owner could not
produce: it must load, plan and render its product Run, and the transported source repository is admitted
against its complete source closure. It fails with a coded error and no output when none matches, more than
one matches (`NVKR-E-ENGINE-CAPABILITY`, because every refresh checks and rewrites the one repository-root
generated bundle), a directory name is outside `[a-z0-9-]`, the policy is invalid, a committed generated bundle
is not named by the policy, or the generator stamp differs from the one recorded in a committed Plan
(`NVKR-E-PIPELINE-DRIFT`: a workflow rendered by another build could never pass its baseline check, so render
with the canonical `make workflow-generator` build), or the policy's Definition directory or file is a symlink.
It never changes the product Run. The workflow writes with a GitHub App: the two `--app-secrets` repository
secrets hold its client ID and PEM private key, and the App must be installed on the repository with contents,
pull requests and workflows write. The repository's own `GITHUB_TOKEN` stays read-only. Review the rendered
bytes before installing them. Re-render after the catalog's action pins change. The rendered header records the
flags that reproduce the file.

### Create the publishing GitHub App

An organization owner sets up the App once, before the workflow is installed:

1. [Register a GitHub App](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app)
   owned by the organization, with the webhook inactive and installation limited to this account.
2. Grant these [repository permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app):

   | Permission | Access | Used to |
   |---|---|---|
   | Contents | Read and write | Create the review commit and move or delete the `catalog-maintenance/<definition>` branch. |
   | Pull requests | Read and write | Open, update and close the pull request. |
   | Workflows | Read and write | Publish a candidate that regenerates a generated file under `.github/workflows/`. |

3. [Install the App](https://docs.github.com/en/apps/using-github-apps/installing-your-own-github-app) with
   **Only select repositories**, choosing this repository.
4. Copy the App's client ID, [generate a private key](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps),
   and store both as the [repository secrets](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets)
   named by `--app-secrets`:

   ```sh
   gh secret set RELEASE_BOT_CLIENT --body '<client-id>'
   gh secret set RELEASE_BOT_SECRET < '<private-key>.pem'
   ```

The rendered workflow has the four jobs described under [Scheduled catalog refresh](#scheduled-catalog-refresh):
`discover`, `refresh`, `select` and `pull-request`. It admits only the repository's default branch and checks out
the exact triggering commit. Its token has repository-content read permission, and a newer run for the same ref
cancels one still in progress. Checkout, Go setup and artifact transfer use the admitted catalog action commits.
The maintenance CLI is rebuilt with the exact validated generator version and commit label supplied during
rendering, preserving the Plan's drift-check inputs.

When a source checkout is requested, a fixed checkout action receives the secret, fetches the source with its
tags and does not persist credentials. Only later steps get a Git URL override pointing the canonical source
at that local repository, so they read source bytes without the token. Git's URL override matches prefixes, so
generation and refresh reject a distinct repository such as `example/nvke-tools` when `example/nvke` is the
`--source-checkout` repository. The rendered `refresh` step passes that repository to `catalog refresh
--source-repository`, so the same admission runs before any pin mutation. Admission covers selected
maintenance roots and their source pin-set members, all pin sets that the selected catalogs revalidate (for an
additional Run, every catalog its Definition selects, admitted before it is compiled), and required compiled
fetches (including output-specific catalogs and overlay replacements). Repeated roles using
the same repository, with an optional `.git` suffix or trailing slash, remain supported; unselected ordinary
sources outside that closure do not block generation. Rejection returns `NVKR-E-ENGINE-CAPABILITY` before
workflow output or pin mutation. Missing private-source access fails closed; no pull-request trigger or
privileged fork path is generated.

If no pin changes exist for a Definition, its validation and description steps are skipped; it still uploads a
`changed` marker so `select` can tell a refreshed no-op from a failure. A changed Definition's patch holds only
its lock and the generated files of the policy's closure (the production Run and, when named, the development
Run, the qualification pair and the additional Runs); unrelated files and private source contents are excluded.
The `pull-request` job alone writes, with a GitHub App installation token minted for this repository only,
limited to contents, pull requests and workflows, and revoked when the job ends. For a changed Definition it
applies the patch to `catalog-maintenance/<definition>` and opens or updates the PR; for an unchanged one it
closes a stale PR and deletes its branch. The `refresh` job writes the PR body with `jq` from the `catalog
refresh --json` result, so the body is part of the rendered workflow. Unlike `GITHUB_TOKEN` writes, the App's
branch updates and PR events start the PR's own workflow runs without a maintainer approving them. Because the
token can write workflows, a candidate whose closure regenerates a generated workflow file (`nvkr-run.yml` and,
when named, `nvkr-dev.yml`, `nvkr-qualify.yml`, `nvkr-smoke.yml` or an additional Run's workflow) is published
like any other generated file; patch admission still refuses every other workflow path. An ordinary source or
artifact pin change leaves every generated workflow byte-identical (it updates the lock, Plans and manifests);
the pin-only fixtures assert this for the production, development and qualification workflows. A topology,
label, interface or action-pin change can modify them. The output is contract-tested only.

Migration: the policy file no longer carries `schedule`, `pullRequest` or `sourceCheckout`, and `catalog
update-ci` no longer takes `--config`. Move the schedule to `--schedule` and the private source to
`--source-checkout`; a policy that still has one of the removed fields fails strict decoding with
`NVKR-E-INVALID-CATALOG`. The checkout is no longer pinned in the policy: the exact lock and source identities
still decide what is built, so a default-branch fetch of the private repository grants no unpinned authority.

## Candidate build policy

| `validation` | Candidate gate | Meaning |
|---|---|---|
| `static` | Byte-exact generation, then the pull request's own checks | No producers executed |
| `local-run` | Static checks plus a complete candidate `run local` | Diagnostic execution, every output observed |

A `local-run` candidate runs `run local` with the candidate Definition and Environment, and independently observes
every required output.

For `local-run`, the shared factory owner first checks the complete Plan's static local capabilities,
without probing the rendering host. A hosted-only Environment is rejected before workflow output or pin
mutation. Workflow generation also admits the complete product Run through its actual GitHub renderer.

`local-run` uses the existing local executor on the generic hosted Linux runner, with no dry-run and no
publication flag. All named outputs remain required. Unsupported VM capabilities, unavailable architecture
execution, dependency failures or missing observations fail the candidate. Maintenance does not reduce the
Definition or substitute a less demanding environment to obtain a green result. Review the Environment and
its runner requirements before enabling this gate; large or multi-architecture builds may need a supported
execution route before they can pass.

The maintained NVKE example deliberately selects `static`: its production Environment declares hosted
`sourceAccess`, which the local adapter already rejects. The dual-architecture CLI integration additionally
uses `githubRunners`, also rejected by the local adapter. Therefore a `static` candidate opens a review pull
request whose gate is byte-exact regeneration and the repository's own PR checks; it cannot claim complete
candidate execution. A `local-run` candidate is diagnostic: its execution is not release acceptance. Selecting
`local-run` with that Environment fails before any maintenance workflow bytes are emitted.

The next integration slice must admit a separately authored candidate-validation Environment while keeping
the production Environment for generation. The maintenance owner must bind that candidate to the same
Definition, source locks, complete named-output requirements and exact logical Build identities, validate
its local execution capabilities, and require observation of every output before a candidate is reported as
executed. That slice is future work and is not delivered. It must not silently derive an Environment, drop an
output, or weaken native hosted admission. Local execution would remain diagnostic; actual native hosted
signing and release verification still need their own run.

A local candidate Run is not qualified reproduction, hosted keyless signing, or release acceptance. Those
claims remain with the production Run and consumer policy. Even a green candidate requires ordinary review.

## Scheduled catalog refresh

`.github/workflows/catalog-refresh.yaml` is installed in this repository. It is the output of `catalog update-ci
--source-checkout dsx-ai-factory/nvke:NKE_SOURCE_TOKEN`, and it is regenerated rather than edited. It runs
nightly at 02:00 UTC and on manual dispatch from the default branch. The nightly default keeps the pins
current without an operator remembering to run the command; an empty `--schedule` restores manual dispatch only.
A newer run for the same ref cancels one still in progress. The next run moves the review branch to the current
candidate (leaving it alone when it already holds that candidate) and opens or edits the pull request, so an
interruption between the two converges on the next run. The one file matching
`inputs/policies/*-catalog-maintenance.yaml` enrolls the Definition its `definition` field names; its file-name
prefix `<d>` only names the review branch `catalog-maintenance/<d>`. Adding or removing such a file is a reviewed
change. Exactly one policy can be enrolled until each Definition owns a generated bundle root, because
`catalog refresh` always checks the bundle at the repository root; rendering and the `discover` job both fail on
a second policy. Today only `nvke` is enrolled, for the `nvke-m2` Definition. Its policy selects the NVKE
umbrella and `nvke-helm`, so the NVKE release and its derived chart pins move together within the registry
constraints. It cannot widen them.

- **`discover`.** Lists the matching files. It has no secrets and runs no repository code. It rejects a
  policy name outside `[a-z0-9-]` and anything but exactly one policy, so finding none fails the run
  instead of looking like "no changes". A policy whose `definition` path is unusable fails the `refresh` leg.
- **`refresh` (one leg, for the one enrolled Definition).** Read-only token. It fetches NVKE objects with
  the source secret in one checkout step, then resolves pins through a local Git URL override that carries no
  token. It regenerates the Run and repeats the byte-exact check. Any failure fails that leg and uploads
  nothing, so nothing is published for it. Every successful leg uploads a `changed` marker; a changed leg also
  uploads the patch and the PR body. Each upload also names its Definition in a `definition` file.
- **`select`.** No secrets, no repository code. It lists every Definition whose refresh succeeded, each with a
  `changed` flag, and fails on a result without a Definition marker or with a name outside `[a-z0-9-]`; the
  `pull-request` job checks the name again before it writes. It runs when a leg failed (`!cancelled()`). The
  download action extracts one artifact directly into the target and several into one directory each, so it
  finds the markers anywhere and reads each Definition from the `definition` file. A matrix leg cannot gate its
  own job, so this job decides which `pull-request` legs exist. A failed refresh leaves no result, so its pull
  request is neither updated nor closed. The `pull-request` job runs once per listed Definition and is skipped
  only when no refresh succeeded or `select` itself failed.
- **`pull-request` (one leg per listed Definition).** The only job that writes. Its `GITHUB_TOKEN` stays
  `contents: read`, like every other job's. The SHA-pinned `actions/create-github-app-token` action mints an
  installation token limited to this repository with contents, pull requests and workflows write, and revokes it
  when the job ends, whatever the leg's outcome. A following step derives the App's bot user for the DCO
  sign-off. The token reaches `gh` only through `GH_TOKEN`, never as a process argument. The job runs no repository or producer
  code, and it first refuses a Definition name outside `[a-z0-9-]`. For a changed Definition it admits only the
  lock named by the base checkout's policy and the exact generated closure that policy names (the production Run
  directory and workflow, plus the development Run and qualification pair when configured), only as regular
  files (mode 100644) or deletions. It creates the commit through the GitHub API (blobs, tree, commit, then the
  ref) with the base commit as its parent and a `Signed-off-by` trailer for the App's bot user in the message.
  It sends no author or committer, so GitHub attributes the commit to that bot and is expected to sign it; that
  signature is not verified here. CI commits fall outside the rule that commits are authored as a human, which
  binds coding agents rather than this workflow. The branch `catalog-maintenance/<d>` is a bot-owned branch and
  must never carry human commits. Explicit lookups decide every branch and PR action, and a failed lookup fails
  the step instead of falling through to a write.
  - A branch whose commit already holds this candidate tree on this base commit is left alone: no new commit, no
    force update and no PR edit, so its head, its checks and its approvals survive a nightly run that changes
    nothing. Otherwise the branch is created or force-updated, and the open PR is edited or a PR is created.
  - A PR that was closed without merging for this very candidate is not recreated; it stays closed until the
    candidate changes, even if its branch was deleted, because GitHub keeps a closed PR's head commit. This is
    decided before any write. Only a closed PR whose head commit holds this tree on this base commit counts, so
    a PR closed for an older candidate does not stop a retry after an interrupted run, and a branch that holds
    the candidate but never had a PR still gets its PR.
  - Every PR lookup keeps only PRs whose head branch is in this repository. A fork PR from a branch with the
    same name is never edited, closed or counted. Lookups request up to 1000 PRs and fail when a page could be
    truncated, so enough fork PRs cannot hide the bot's own open or declined PR.
  - For an unchanged Definition the job closes the open PR for the branch with a comment, then deletes the
    branch. If the deletion fails, the next run finds no open PR and deletes it then.
- **PR body.** `refresh` renders it with `jq` from the `catalog refresh --json` output; the command itself
  writes no description. For every changed pin: old and new tag, commit, tree hash and derived-from provenance
  for source pins; old and new per-architecture digests, derived-from provenance and transport (chart repository
  and URL) for artifact pins; and, for both, the names of the fields that changed, so a provenance- or
  transport-only change is visible. Rows are sorted by name, and the Run plan digest is included. A value
  outside printable ASCII fails the step.
- **Approval and checks.** The workflow requests review; it is not a candidate gate. Its own gate is
  byte-exact regeneration, plus `run local` for a `local-run` policy; the patch and PR body are saved before
  `run local` executes, so nothing it does to the workspace reaches the PR. The App's writes start the PR's
  `pull_request` runs, and the required checks are a merge precondition; they are repository settings and are
  not enforced by this workflow. The maintained
  Run stays manual and default-branch-only, so candidate execution and qualification are separate runs that
  nothing here performs or claims. A `static` policy such as `nvke-m2` is validated by the PR checks only;
  `local-run` cannot execute its Environment.
- **Repository settings the workflow does not enforce.** These are hard prerequisites. The release owner or a
  repository administrator must validate them before anyone relies on scheduled writes; this repository does
  not verify them, and nothing here changes them:
  - The App installed on this repository with contents, pull requests and workflows write, and its client ID
    and private key in the `--app-secrets` repository secrets.
  - A ruleset on `refs/heads/catalog-maintenance/**` that restricts creation and updates to the App, so the
    bot branch cannot carry human commits.
  - `require_last_push_approval` on the protected default branch, so a person who pushes to a PR branch cannot
    also supply its approving review. GitHub's `require_extra_approval_for_unattributed_changes` has undocumented
    semantics and is not relied on.
  - Code-owner review on the default branch. The App opens the PR and can write workflows, so an independent
    code-owner approval is what keeps a bot change, including a regenerated workflow, from merging unreviewed.
  Merging the PR is the v1 release decision, so a missing prerequisite lets one person change `pins.lock.yaml` or
  the generated Run files through that merge. The workflow fails closed on its own inputs, not on these settings.

The renderer's tests parse the rendered workflow and assert its job graph and gates (`if` and `needs` per job),
a read-only `GITHUB_TOKEN` in every job, the App secrets reaching only the token action's inputs and the App
token only the bot identity, publish and cleanup steps, the source secret reaching exactly one credential-free
checkout step with no other secret in any `env` or script, `persist-credentials: false` and the triggering
commit on every default-branch checkout, the exact private-source Git rewrite, the generated header and
SHA-pinned actions. They also run the rendered `discover`, `select`, Definition-name, patch-admission,
description, bot identity, publication and cleanup steps against fixtures: a failed bot user lookup, a slug or
bot id that could reach the step outputs, zero, one and several downloaded artifacts, unexpected
files, executable files and symlinks, a table of paths the allowlist must reject, a real `Refresh` result with
hostile values (backticks, pipes, newlines, bidirectional, zero-width, control and other non-ASCII characters, a
missing plan digest, no change), a new, a stale, a current and a human-closed branch and PR, and failing lookups
at every call, with the token absent from Git arguments. The committed policies are loaded by a test, and the
`catalog refresh --source-repository` flag is tested at the command with an invalid spelling and a prefix
collision. The `select` step is run with missing and malformed Definition markers, the policy step with valid
`static` and `local-run` policies and with missing, malformed and output-injecting ones, and the description
step with typed artifact-only, mixed, transport-only and source-provenance-only results. The publication and
cleanup steps run beside a newer fork PR from a branch with the same name, with a closed PR for an older
candidate after an interrupted run, with a declined candidate whose branch was deleted, and with a lookup page
that could be truncated. The `discover` step and the renderer agree that nested policies never enroll and hidden
names are unsupported. The rendered steps also pass under Bash 3.2: their bracket expressions spell characters
out instead of using ranges, which follow locale collation there. These rendered-step tests skip locally when
`bash`, `git`, `jq` or `yq` is missing; with `NVKR_REQUIRE_SHELL_TOOLS=1` or `CI` set they fail instead, and the
merge gate sets the variable. Rendering needs access to the private NVKE source, so the unit tests do not
compare installed bytes with the renderer's output. `make workflows-check` does, through `make
catalog-refresh-check`: it renders `catalog update-ci` with the canonical generator stamp and fails on any
difference from `.github/workflows/catalog-refresh.yaml`. The merge gate's regeneration job runs it, and a
change to the installed file triggers that job. The policy file is a CODEOWNERS trust root, because it decides
which pins move and which bundles are regenerated. A changed shared lock is tested against the production and
development bundles with real local Git fixtures, and a pin-only change is asserted to leave `nvkr-run.yml`
byte-identical. A runtime-Plan member whose committed Plan lags the lock passes the closure check through its
own owner, an edited workflow byte in it is still refused before pin work, and its regeneration moves the Plan
but not the workflow. An additional Run's colliding pin-set root is refused before compilation fetches it. A
changed-pin refresh is also driven through the real qualification owner, on a fixture that signs and publishes
with synthetic trust pins: the regenerated manifest binds the new production Plan digest, every changed path is
a transported regular `0644` file, the pair rechecks clean, a second refresh is a no-op, and the owner's drift
and unpinned-input failures keep their codes before any pin work. That fixture does not contact GHCR or run
cosign, so it is not live qualification or signing acceptance.

### Coordinate deployment images before a moving release

The maintained Definition's deployment images are selected outside the maintenance policy. Its image catalog
(`inputs/catalogs/runtime-dependencies`) pins a `nvke-deployment-selection` source to the NVKE commit the controller
catalog pins, declares each image reference, and locks each image's native digests; the Definition lists one OCI
output per image and platform, and the qualification policy names those outputs as deployment images. A refresh
moves only the `nvke` and `nvke-helm` pins and regenerates the bundles, so it changes none of these inputs.
Today the exact `v0.21.0` constraints keep refreshes a no-op. Before a constraint allows the NVKE release to
move, review and update together, in one authored change: the selection source pin, the image references and
their native digest locks, the Definition's image outputs, the qualification policy's `nonToolOutputs`, and the
expected images in `pkg/nvkr/buildgraph/nvke_m2_images_test.go`. That test fails, naming this section, when the
selection commit and the controller's NVKE commit diverge. Maintenance does not perform or check this
coordination; it is a documented limit, not a capability.

Not exercised against GitHub: the scheduled trigger, matrix selection, the App token exchange and revocation,
the API commit, its signature and DCO attribution, PR creation, the PR's automatically started checks, closing
and branch deletion.

## Validation evidence

Contract tests exercise real local Git tags and pinned-tree extraction: an independent same-release pair,
an umbrella-derived source, complete preview with no writes, successful bump plus deterministic regeneration,
unchanged byte preservation under exact constraints despite a newer upstream tag, rejection of stale
generated bytes before resolution, and the separate pre-commit/post-commit failure boundaries. Local TLS
fixtures exercise republished acquired member refusal with no partial lock write and unchanged member bytes
under new umbrella provenance. Additional tests
cover malformed configs, unsafe paths, cancellation after a real pin commit and at the final generation
check, prefix collisions and same-repository Git aliases, cohort disagreement and workflow trigger/permission
boundaries. These fixtures do not build NVKE or contact private registries. The scheduled workflow and any
candidate production builds have not been exercised against external services by these tests.
