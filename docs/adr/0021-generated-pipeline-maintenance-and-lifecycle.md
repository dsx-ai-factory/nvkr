# ADR-21 — Generated-pipeline maintenance & lifecycle

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-21 in the consolidated NVKR ADR set (added to close the day-2 pipeline-maintenance gap).
- **Relates to:** ADR-18 (what is emitted / the runner), ADR-11 (catalog producer overlays), ADR-07
  (runner/profile digest, re-sign), ADR-22 (the pin/lock contract a bump edits), ADR-01 (canonical codec +
  digest provenance), ADR-17 (CLI verbs), ADR-20 (publish phase).
- **Satisfies:** FR-05 (CI portability), FR-21 (standard, replaceable interfaces); supports FR-20 (coded
  errors — the `pipeline-drift` code registers in ADR-03).

## Context

ADR-18 fixes *what* an emitted pipeline is: one IR, provider adapters, and a from-source `nvkr-exec` executing
each node. It does not fix the **lifecycle of those files after they land in an operator's repository**. That is
the day-2 story: a generated pipeline goes stale when pins move, someone hand-edits it, the operator wants a
newer tool, or the operator needs an org-specific step. A stress-test of that journey found no drift gate, no
sanctioned customization seam, no home for the runner/tool upgrade, and an ambiguous owner for "the CI files."
This ADR fixes maintenance.

## Decision

### 1. The emitted pipeline is a generated artifact, not an owned scaffold

`run generate` is the normal owner of committed workflows, generated local actions and invocation data, complete plans and
manifests. Every owned file regenerates byte-for-byte from exact Definition, Environment, catalog pins, generator version and
engine. Drift checks repeat the recorded Definition and Environment inputs without substituting a different
catalog, path or engine. Definition v5 outputs select exact producers directly, with either a native `platform`
or an OCI aggregate `platforms` set. The v5 Plan/artifact graph embeds one producer graph per native build target: a native
output has one target keyed by output name, while an aggregate derives a target for each required platform
and one image-index packaging node that joins every member's exact result and evidence. Run/Release v4
records retain the required aggregate output and its native membership. Separate per-build graph files are retired through
the previous manifest; hosted operations execute the exact complete Plan. The former recipe selection and catalog criteria
workflow are removed. Cluster-composition files and nodes are also removed; regeneration retires former
owned members through the same prior-manifest checks described below. Changed graph/plan and lifecycle records use their new schema majors; old records
cannot be relabeled into current identities.

In production mode, Environment's optional `githubBundle` selects an independent generated GitHub namespace.
Empty preserves `.github/workflows/nvkr-run.yml` and `.github/nvkr-run/{plan,manifest}.json`
byte-for-byte. A nonempty name contains at most 48 lowercase letters, digits and hyphens,
with alphanumeric ends; it derives `.github/workflows/nvkr-run-<name>.yml` and
`.github/nvkr-run-<name>/{plan,manifest}.json`. Authored paths are unsupported. Namespace
selection changes Environment and Plan identity without changing producer Build identity.
The named producer's workflow display name is `nvkr-project-<name>`, disjoint from
the default `nvkr-run-<Definition name>` and development `nvkr-dev-<Definition name>`.
This keeps same-Definition bundles separate under GitHub's workflow-based concurrency
and prevents a named producer from matching the default qualifier's display-name trigger.
That separation is not a destination-wide lock. ADR-20's S3 extension restricts each enrolled physical
namespace to one workflow/bundle, exact ref and evaluated non-canceling group; another bundle or alias
cannot share it under a different owner. The extension's admission remains implementation work.
Workflow file paths and explicit signing identities retain the namespace contract above.
Every generated execution, recovery and utility-container command selects that exact
Plan path; validation rejects a bundle whose paths differ from the admitted Environment.
Signing identity and issuer remain explicit Environment policy and are never rewritten
to accommodate a selected workflow filename. Operators must select the matching policy.
Producer generation emits independent bundles whose entrypoints support manual dispatch, with no cron or
callable trigger. By default every producer job remains in its bundle's entry workflow. The opt-in
`githubExecutionPartitions` projection also owns up to 50 same-commit sibling workers named
`<entry-stem>__partition-00.yml` through `<entry-stem>__partition-49.yml`. Only those exact sibling names
belong to this entry's current manifest; arbitrary reusable workflows and another owner's siblings remain
outside its namespace. Every worker receives the same per-file size and parsing bounds as the entry.
The root manifest binds every worker's exact bytes. Regeneration checks and retirement cover the complete
worker inventory, including changed or unowned leftovers. Workers export per-consumer result fragments so the
entry does not repeat member-by-member projections (ADR-18 §8). This moved most result-forwarding bytes into
workers. The maintained development entry measured 373,817 bytes before the change and 264,038 after, with
the largest worker at 134,308 bytes and unchanged signer jobs. These are local generation measurements.
Workers never contain OIDC jobs. A fixed `<entry-stem>__signer.yml` signer workflow is proposed only
together with ADR-18 §8's explicit certificate-identity and trust-policy migration. It is not an admitted
member name; the reserved child-namespace rules below fail closed on such a file. Workers share one complete Plan and do not add
an authored lifecycle object or a child Release. ADR-18 owns scheduling and result propagation. [Reconciliation](reconciliation.md#output-local-publication-and-metadata-assembly)
records one successful earlier-head full development execution of this opt-in topology; it predates the core
execution-model extraction and per-worker fragment forwarding, and proves neither current-head worker execution,
full-product failure/cancellation recovery nor production qualification. Optional qualification
generation separately emits the admitted `workflow_run` qualifier and fixed `workflow_call` smoke pair (§4).
That pair currently admits the default producer workflow only; named producer
qualification is not part of this namespace contract.
A separately authored downstream CI coordinator may schedule
dispatches of those bundles; its scheduling, exact-run correlation and runtime
ownership are outside generation (ADR-16/18).

The Environment's explicit GitHub development mode selects a second fixed generated layout:
`.github/workflows/nvkr-dev.yml`, `.github/nvkr-dev/plan.json`, and `.github/nvkr-dev/manifest.json`.
The default production layout remains `.github/workflows/nvkr-run.yml` and `.github/nvkr-run/`. Both use the same
compiler, renderer, exact validation and writer. The admitted Environment selects these paths; callers
cannot relocate one bundle into the other layout. Each manifest owns only its own payloads and lock, so
regeneration or retirement of one leaves the other intact. Helper commands and container launches bind the
Plan at that layout and recheck its digest. Development mode changes Environment/Plan identity, not
transport-independent logical Build identity. Its execution and retention policy is owned by ADR-18.
Signed development also binds the disposable namespace and signer policy in this immutable Plan. Actual
run-attempt destinations are derived by the execution owner under ADR-20; runtime substitutions never
rewrite generated inputs or bypass drift checks. The maintained development bundle uses the complete M2
Definition with its separate development Environment.

`githubDevelopment: true` with a nonempty `githubBundle` is unsupported and
returns `CodeEngineCapability` before catalog resolution or generated-output work.
Unnamed development and named production remain separate supported selections.

Execution identity and transaction identity are separate. The Plan binds authored inputs and every executable
operation; the workflow has its own exact-byte digest. The commit manifest lists each owned file's safe relative path, mode and exact whole-file digest,
plus plan and workflow identities. It is written last and never lists itself. No self-referential identity is permitted.

Owned payloads are generated provider/configuration files and canonical graph/plan records. Catalogs, profiles,
vectors, roots and credentials remain external inputs whose exact references/policies are recorded, not copied
and silently claimed as generator-owned. Future artifact/result digests belong in runtime observations; the
generator must not invent them. Changes to input mode, environment or output graph regenerate in one reviewed
change. Generated files are not editable scaffolds.

The GitHub bundle includes shared composite templates beneath `.github/nvkr-run/actions/` and canonical
invocation values in `.github/nvkr-run/action-data.json`, with corresponding members in the development
layout. The generator selects fixed semantic interfaces for
execution, preparation, collection and transfer. Grouped jobs repeat those calls; declared optional slots
and further individual transfer calls handle differing output counts without shape-specific or numbered
variant actions. Invocation values use named operation roles rather than step positions. These interfaces
lower Plan-derived steps without introducing another executable Plan or authored extension surface.
The helper-preparation, execution, bootstrap and controlled-transfer
families and their invocation-data member appear in the same commit manifest as the top-level workflow and
Plan. Their exact bytes, safe relative paths and modes participate in regeneration, drift
checks and prior-manifest retirement. The workflow digest still describes the top-level workflow bytes;
it is not a substitute for validating the complete manifest-owned execution closure.

The producer entry workflow is bounded to 500,000 UTF-8 bytes before output work. Shared action interfaces
and descriptive YAML aliases compact repeated operations without introducing callable producer workflows.
The default flat projection rejects producer child workflows. A current manifest may own only the opt-in
`<entry-stem>__partition-NN.yml` workers described above. Any other file in the entry workflow's
reserved `<entry-stem>__*.yml` namespace is admitted only from a prior v1 manifest, for migration.
Regeneration retires prior-owned files only after checking their exact prior bytes and modes. Changed or unowned siblings fail closed and are preserved;
other bundle namespaces and handwritten workflows retain their separate ownership.

Identical complete semantic calls without provider controls may share descriptive YAML anchors after
structural equality checks, including their parameter selector and credential references. The outer label names
the operation and job-local part, while the job and expanded role labels retain the selected producer. Controlled diagnostic
steps retain their original caller labels. Static expansion resolves aliases into independent copies before
substituting per-job parameters, so one job cannot mutate another's input or credential bindings. Job
conditions and dependencies remain inline. Every job retains its runner, timeout, permissions and protected
runtime selection in the entry workflow. Identical complete host step sequences may also share descriptive
anchors when they contain only native preflight, credential-free
pinned checkout, current-job preparation and one or two uncontrolled node calls. Complete structural
equality is required; source credentials, authored setup and provider-controlled steps cannot enter this
sequence form. This changes presentation only, not execution membership, evidence or the provider's
workflow-size bound.

The entry workflow references the fixed Plan and manifest paths without repeating the Plan digest. Its
provider source-fetch IDs and scheduling order describe the target and actual consumer cohort rather than
changing source pins. Exact executable identities remain in the Plan and generated invocation data.
Templates contain fixed helper operations and pinned provider actions; the runtime descriptor operation
admits the Plan and regenerates the selected values rather than trusting the checked-in data as authority.
Routine pin or Plan changes can therefore leave the workflow bytes unchanged while the complete bundle
and ownership manifest change. This stability is conditional on unchanged topology, labels, action
interfaces and visible execution policy; checkout pins, pins of required inline transports and inline
operator-setup changes remain workflow changes. The closing summary's references keep finalization and
the final Run upload inline, so an upload-action pin change affects the maintained workflow. ADR-18 §8
defines the full boundary. Ordinary bundles require complete regeneration and the same all-member drift
check after every input update. Named production Environments may explicitly select
`githubRuntimePlan: true`: the first bootstrap admits the committed baseline, freezes a fresh Plan from
current pinned inputs and distributes its exact data through same-Run helper artifacts. Only Plan and
invocation data plus the derived manifest may change; every workflow/action byte, member path,
Environment digest and authored input path stays fixed. Source-version updates within that contract need
no regeneration commit. `--runtime --check` validates that contract without writes; ordinary `--check`
still compares the complete current bundle. Secondary bootstraps restore the three runtime JSON files into
a fresh staging directory, then admit their exact rendered bytes and current authored selection against
the complete committed baseline. The generation owner installs the admitted bundle using its existing
lock, per-file atomic replacement and manifest-last semantics; this is not an atomic directory transaction.
Private transport never overwrites checked-out files. ADR-18 owns bootstrap ordering and transport. Neither an
unchanged workflow nor runtime compilation establishes execution or publication acceptance.

Same-input determinism and edit locality are separate contracts. Adding an independent named output must
preserve existing outputs' provider job membership, IDs, dependencies, policy and shared action templates
when their own producer closures and execution policy are unchanged. Ordinary grouping is fixed by the
same BuildTarget's observation-to-SBOM dependency (ADR-18 §8), never global adjacency or chunk boundaries.
The pair may additionally wait for a shared pinned root tool acquisition; no extra source or output
dependency is admitted. Failed tool acquisition prevents pair completion.
Packaging owns a separate job and success condition. Aggregate assembly/publication, finalizer accounting
and the opening Run summary necessarily change with required membership. Source-cohort changes and real
new dependencies remain topology changes. YAML anchor definitions may move to an earlier new owner of an
identical sequence; this bounded presentation change does not change the expanded existing jobs. Generation
derives the projection from the current complete Plan without using the previous workflow as an input.
The dependency projection preserves each existing prerequisite closure and condition. Finalizer collectors
are diagnostic accounting, not execution groups: fixed buckets do not move existing members when a new output is added, and their
complete membership is derived from the current Plan. Crossing the direct-result threshold changes finalizer
accounting only. ADR-18 §8 owns collector bounds, exact result normalization and authored-context preservation.
Producer matrices and general callable producer interfaces remain deferred; the opt-in partition workers
above are generated scheduling structure, not an authored callable interface.

### 2. Drift detection is a fail-closed `--check` twin, run by a separate generated gate

`run generate --check` re-emits in memory using the recorded Definition/Environment selection and compares
against committed files. A mismatch fails closed with a distinct coded error (`pipeline-drift`, ADR-03) naming
missing, extra or divergent owned files. Execution admission verifies the commit manifest before consuming its
graph. The dedicated generated Run directory must contain only expected payloads, its manifest and the
persistent lock inode; unexpected files or directories and symlinks fail the check. The check also inventories
the selected owner's reserved legacy child namespace in `.github/workflows/`; remaining child files fail
closed. Handwritten workflows and other bundle namespaces remain independently owned. Qualification
admission and its prior-manifest validation reject reserved legacy producer child paths before either owner can
write or retire them. The unused qualified `bundle` command, bundle manifest and
transport-plan writer are removed. The opt-in M3 qualification pair has its own narrowly scoped ownership
manifest; it does not replace or reinterpret the producer Plan bundle.

Committed bundle members and `exec-node`'s diagnostic canonical graph are opened as bounded, nonblocking,
no-follow regular file capabilities and revalidated against their live pathname after reading. A symlink, FIFO,
oversized member or inode substitution fails closed before hashing or execution.

A separately generated meta-lint workflow and automatic semantic explanation inside `--check` remain deferred.
The maintained repository gate invokes `run generate --check` for both committed layouts using their exact
recorded inputs and canonical generator stamp. Repository development tooling regenerates these bundles,
checks a clean named branch against the explicit GitHub repository's current commit, and requests manual
dispatch of the development workflow. It does not commit, push, edit deployment policies or authenticate
an eventual provider result. GitHub must first register the workflow on the default branch; a moving remote
branch after preflight remains a provider dispatch race and the resulting Run SHA must be inspected.
Read-only `run diff` separately compares
two fully validated canonical Plans: it reports changed named Build identities, classifies every changed field,
keys named collections without array-shift noise, and preserves command/patch order. Concise text counts all
changes; detailed text and descriptive JSON retain the full comparison. `run graph` exports complete linked
producer/artifact graphs, and `run plan --pretty` emits a display-only view. These views remain outside the
generator-owned directory and do not change stored canonical bytes, Plan identity, or the byte-exact drift gate.

### 3. Churn automation: bump → regenerate → PR, gated by drift

A pin or catalog change moves the IR, which moves the emitted pipeline. The two travel together:

- `catalog bump [--all] --check` previews the lockfile diff without writing (ADR-22).
- `run generate` after `catalog bump` regenerates the pipeline in the **same
  change** as the lock edit. The drift gate (§2) makes a lock change **unmergeable without the matching pipeline
  regen**, so "forgot to regenerate" is structurally impossible to merge.
- `catalogmaintenance` owns an explicitly configured review-candidate refresh: check the existing bundle,
  atomically bump selected independent roots and their declared pin sets, regenerate, and repeat the drift
  check. No-op preserves bytes. `--check` previews only the complete pin transaction, without writes or a
  claim that candidate production was exercised. Locks and generated files retain separate transactions;
  failure after pin commit leaves a disposable checkout for diagnosis, never a publishable candidate.
  Recovery context preserves the generating owner's coded cause, including retryable cancellation and
  non-retryable generated-file drift during either generation pass.
- `catalog update-ci` takes no policy path and renders a separate GitHub workflow that discovers the one
  `inputs/policies/*-catalog-maintenance.yaml` when it runs (several share the repository-root generated bundle
  and are refused), with a reviewed schedule (nightly unless one is given, so pins stay current without an
  operator remembering to run the command; an empty schedule is manual dispatch only), default-branch admission,
  exact base checkout, per-ref canceling concurrency and read-only repository permissions. Credentials are
  passed only to the fixed private-source checkout, not resolution or producer execution. Its Git prefix
  override rejects distinct repository URL collisions before workflow output or pin mutation, and `catalog
  refresh --source-repository` repeats the admission at run time; admission covers selected maintenance source
  closures, every revalidated catalog pin set and compiled fetches. Repeated roles and ordinary
  `.git`/trailing-slash aliases for the same repository remain supported. Changed candidates pass deterministic
  regeneration and then the pull request's required checks, which the App's writes start; `local-run`
  additionally requires execution and independent observation of every required output. Unsupported capabilities
  fail without reducing the Definition. An isolated job that alone holds write permission on the workflow's
  GitHub App installation token (minted in that job for this repository only, with contents, pull requests and
  workflows write, and revoked when the leg ends; the workflow token stays read-only everywhere) opens or
  updates the PR for a changed Definition, leaves a branch that already holds the candidate alone, and closes
  the stale PR and branch of an unchanged one. Only a closed, unmerged PR whose head commit carries the current
  candidate keeps a PR from being recreated, even after its branch is deleted, and every PR lookup ignores PRs
  from fork branches with the same name. The policy names the dependent generated bundles (a development Run, a
  production and development qualification pairs and additional named Runs). Each is checked before any pin update, regenerated through its
  own owner (the runtime-Plan owner for a `githubRuntimePlan` Run, which admits only Plan and action-data
  changes) with that owner's coded failure preserved, and transported on an exact path allowlist; a committed
  generated bundle the policy does not name is refused with `CodeEngineCapability` before pin mutation or
  workflow output. `catalog update-ci` also refuses a generator stamp that differs from the committed Plans
  (`CodePipelineDrift`), before any catalog or source resolution, and a symlinked policy location. Regenerating
  a member does not qualify or accept anything. The deployment-image selection is not part of that closure: a
  release advance must coordinate it in a separate authored change, which maintenance neither performs nor
  checks. The job creates the review commit through the GitHub API with a DCO trailer and no author or
  committer, so GitHub is expected to sign it (not verified on GitHub). Because that token can write workflows,
  a candidate that regenerates a generated workflow in the closure is published like any other generated file;
  patch admission still refuses every other workflow path, and code-owner review gates the merge. The App token
  exchange, revocation and DCO attribution are contract-tested only. The rendered workflow is installed, but no
  scheduled run is claimed as exercised.
- Local candidate verification is diagnostic, not qualified reproduction or hosted release acceptance.
  Qualified changes require reproduction; v1 production uses its explicit evidence contract. Merge to the
  default branch is the v1 release decision (ADR-16), without a reproduction claim. Release-manifest issuance
  remains deferred. Regeneration is a reviewable diff.


GitHub actions are catalog sources with `kind: github-action`. The registry owns the GitHub repository
and semver constraint; `catalog pin` and `catalog bump` write exact release commits into the source lock.
The resolved catalog snapshot and generated Run bind those pins. Regenerate after bumping; the generator
binary does not need rebuilding. The GitHub adapter requires its supported repositories and major versions
before emitting bytes. Missing pins, mutable refs, and unsupported interfaces fail closed. Handwritten
repository CI has its own reviewed action pins; it is not part of the generated product Run.
Native action runtime declarations and transport behavior require external checks; a commit pin alone does
not establish compatibility.

### 4. Deferred customization: extension overlay and callable form

Generated shared composite templates and invocation data are internal lowering artifacts owned by the existing
generator. They accept only generated input bindings and do not admit operator-supplied actions, arbitrary commands or
replacement job policies. Their callers retain visible topology, permissions and credential references;
validation checks the expanded steps and exact bytes of every generated member (ADR-18 §8).

General extensions and reusable callable producer interfaces remain deferred design options. The fixed M3
native smoke worker is the separately admitted reusable workflow, invoked by its generated same-repository
qualifier at the same commit. It exposes no arbitrary steps, runner labels, commands or reusable refs.
The independently selected qualification policy and trusted compiled producer inputs determine its exact
worker identity, native images, executable pins, source-read secret names and SHA-pinned actions. Source
secrets are explicitly mapped and scoped to trusted owner steps; there is no blanket secret inheritance.
Worker permissions allow reads and its own report signature, while registry publication belongs only to
the final qualifier. Reusability here binds a distinct signing identity, not a customization surface.

The final owner runs after a completed worker success or failure so an ordinary smoke failure reaches the
complete per-output refusal diagnostics. Its condition independently repeats trusted successful source
admission, permits only those two dependency results and uses `!cancelled()` to override GitHub's implicit
success check without running after cancellation. Skipped or unknown dependency states are not admitted.
[GitHub's status-condition contract](https://docs.github.com/en/actions/reference/workflows-and-actions/expressions#status-check-functions)
owns that scheduling behavior. Scheduling the owner supplies no authority: exact provider job success and
signed complete worker evidence remain mandatory before publication. Failed worker execution and evidence
upload are never converted to successful job status.

Before rendering, lifecycle joins exact tool/non-tool policy membership and native platforms to the
compiled Plan. Impossible policy scope fails before any workflow write. The worker matrix is exactly the
selected executable platform set, while the final signer remains AMD64 with its independent executable pin.
`run generate --qualification-policy` admits and renders this pair before writes, then composes it with
the normal producer generation. The worker is a generated reusable GitHub Actions workflow, not an additional
handwritten integration. The maintained candidate uses `inputs/policies/nvke-qualification.yaml` to emit
`.github/workflows/nvkr-qualify.yml` and `.github/workflows/nvkr-smoke.yml` alongside its producer bundle.
Its separate manifest owns exactly the two workflows. A persistent kernel lock serializes writes; bounded
stages are synced and the manifest is committed last. Replacement or
retirement requires prior ownership and exact bytes. Recovery may accept only prior-owned bytes or the
newly regenerated bytes; arbitrary edits fail closed. The read-only check creates neither locks nor output
directories. A partial cross-owner generation is detectable drift, not a valid completed generation.
A leftover qualification stage must match a prefix of the selected generation's exact bytes. A policy or
input change that makes the stage disagree returns `CodeWriteConflict` and preserves staged and published
bytes; the writer cannot infer authority to discard another generation. Restore the interrupted generation's
original authored inputs, pins, policy and generator version, repeat generation and confirm `--check`, then
reapply the intended changes and regenerate. If those original inputs cannot be established, preserve the
ambiguous state for investigation; deleting stages or ownership records is not the recovery protocol.
The `nvkr.dev/generated-qualification/v1` manifest records the trusted Plan and full policy digests,
producer/qualifier/worker paths and both workflow byte hashes and modes. Worker and final qualifier admission
verify this manifest, policy binding and exact finite owner directory before candidate or signing work. This
read-only runtime check does not recompile source inputs or repair drift.

The explicit development policy generates a separate fixed manual-dispatch/call pair,
`nvkr-dev-qualify.yml` and `nvkr-dev-smoke.yml`, with ownership under
`.github/nvkr-dev-qualification/`. Production and development owners have independent locks, stages and
manifests. A prior manifest naming the other producer cannot authorize replacement or retirement. The same
exact-byte runtime policy check applies to both pairs. The development source checkout is untrusted input
data and does not replace the qualifier's generated policy or executable owner.

The reviewed policy and generated pair may merge before real hosted acceptance. The fresh producer run and
positive/negative hosted and independent-consumer exercises follow merge; their evidence remains required
before production-readiness claims. Generation, repository checks and merge do not establish hosted behavior
or organizational acceptance, and do not relax source, worker or final-signer admission.

Any broader extension requires explicit input ownership, deterministic identity, credential admission and
transport contracts before implementation. They cannot restore
removed Recipe layers or mixins; catalog overlay v3 remains whole-producer replacement.
Producer matrix scheduling likewise needs an explicit dependency and complete provider-result contract
before admission; the fixed qualification worker matrix does not admit producer matrices, and extracting
existing steps into generated actions does not change their scheduling contract.

Editing generated files directly remains out of contract. Current pipeline changes belong in the Definition,
Environment or owning compiler/renderer and pass the same byte-exact regeneration gate.

### 5. Runner/bootstrap self-upgrade is a profile change, not a catalog change

Helper/runtime upgrades belong to Environment and, for qualified execution, the signed builder profile.
Component toolchains and output-affecting inputs belong to producer/catalog identity. Neither owner can override
the other's pins. From-source helper bootstrap measures the actual helper and bootstrap toolchain; a preexisting
helper must not claim a bootstrap that did not participate (ADR-07/18).

For the qualified source-bootstrap path, `profile repin --runner|--bootstrap-toolchain` writes new expected pins
and re-signs; regeneration accompanies the change and actual execution must reproduce the expected helper and
pass stage two. A pinned helper image is an alternative transport, not automatic qualified admission. Its
qualified path remains unavailable until the exact executable/runtime observation contract is implemented.
The legacy `--toolchain` spelling cannot alter component producer pins.

### 6. Serialized local writes and exact retirement of generated members

Catalog lock writes use original-byte compare-and-swap, a persistent kernel lock inode, same-filesystem
staging, fsync and atomic replacement (ADR-22). Generated Run writes acquire their per-bundle kernel lock,
stage complete files and fsync them, publish payloads, retire obsolete members, and replace the manifest last.
Each named namespace owns its distinct manifest and lock. Write and check reject mixed
namespace paths or members before output work; retirement cannot claim another bundle's
workflow or generated directory. Default and multiple named bundles may coexist. Changing
a bundle name creates a separate owner and does not delete the previously named bundle.
The current generated Run writer serializes same-checkout writers; it does not promise an optimistic CAS
conflict for two separately compiled bundles.

Retirement reads the previous generated-run/v1 manifest as a bounded ownership description. It validates
its schema, engine, workflow path, sorted member paths, digests and modes. A member may be retired only
inside the generator-owned directory or the exact owner's reserved legacy child workflow namespace, and
only when it is a regular file whose current bytes and mode still match that manifest. Missing obsolete
members are tolerated for retry after an interrupted retirement.
Symlinks, changed obsolete members and unlisted directory members fail closed before payload publication.
A missing prior manifest grants no ownership over unexpected files. Empty directories made obsolete by
retired members are removed nonrecursively; unknown files are never swept by a recursive cleanup.

New bundles cannot introduce child destinations; unsupported members fail before filesystem work.
Under the persistent owner lock, a bounded inventory of the reserved sibling namespace rejects files not
covered by the prior manifest's retirement authority before payload publication. A rejected unowned legacy
file may leave the private directory and lock, but no new payloads or manifest. Retained prior-owned members
keep the existing regeneration repair behavior.

Payload replacement and retirement are not atomic across the directory. The writer rechecks retired bytes
before removal, writes the new manifest last, and fsyncs generated directories. A process crash may leave
mixed payloads or incomplete retirement that fails the byte-exact check; regeneration is the recovery path
when the prior ownership record remains available. The writer does not claim a crash-wide transaction or
protection from an arbitrary same-user editor outside its kernel lock. If an older generator already replaced
the ownership manifest while leaving old files behind, recover the authentic prior manifest from version
control before regeneration; the current writer does not guess which unlisted files it may delete.

The persistent owner-only lock inode is opened without following symlinks and is never removed. Process death
releases kernel ownership automatically. Acquisition uses nonblocking attempts with context-aware retries;
unsupported lock platforms fail closed. Staging honors cancellation; after publication begins the small
commit finishes without voluntary cancellation. No daemon or distributed lock is required. Separate branches
and worktrees retain Git's ordinary review and merge-conflict behavior.

### 7. Single-owner rule for "the CI files"

`run generate` is the sole owner of the emitted build pipeline; the legacy `bundle` adapter is removed.
`catalog update-ci` renders only the scheduled catalog-refresh workflow, never the build pipeline. Catalog
maintenance regenerates a candidate by invoking the existing owners (`run generate` and the qualification
generator); it renders none of their files itself. Conflating two "CI" artifacts under one verb is
disallowed; that ambiguity is what makes forgotten-regen drift possible.

## Consequences

**Positive**
- The committed pipeline provably corresponds to a specific IR, and staleness or hand-edits self-detect in CI.
- Bump and regen travel together; forgotten-regen drift is unmergeable.
- Future customization must preserve exact regeneration before it is admitted.
- The tool/runner upgrade has a home that preserves the signed-profile boundary.

**Negative / costs**
- A future extension overlay or meta-lint workflow would add an admission and maintenance contract.
- Generated files are off-limits to direct edits; current changes go through authored inputs or the renderer.

**Neutral**
- The `pipeline-drift` code and its classification are surfaced through ADR-03's coded-error table.
- The pin/lock mechanics a bump operates on are owned by ADR-22; this ADR consumes them.

## Alternatives considered

- **One-time scaffold the operator then owns.** Rejected — silent drift breaks "the pipeline builds these
  selected producers"; the byte-identity claim evaporates.
- **Route the runner/bootstrap bump through `catalog bump`.** Rejected — it puts environment identity in the
  definition and skips the required profile re-sign (§5).
- **Overlay OR emit-callable, pick one.** Rejected — in-pipeline gated steps need the drift-checkable overlay
  and org-owned wrapping needs the callable form; they serve different needs.
- **Rely on Git for concurrent writers in one checkout.** Rejected — Git sees only the last filesystem state and
  cannot detect a pre-commit lost update. Catalog lock CAS and bundle serialization are required; a daemon or global lock is still
  unnecessary (§6).
- **Digest recheck without a commit lock.** Rejected — two writers can both pass the check before either rename.
  The narrowly scoped kernel lock makes check-and-commit atomic without introducing a service.

## De-risking spikes

1. **Deferred extension contract** — any future extension must reproduce byte-identically and remain covered
   by the generation drift check.
2. **Deferred drift classification** — distinguish moved pins from hand edits without weakening exact comparison.
3. **Concurrent writer/crash matrix** — same-checkout racers produce one success plus one coded conflict, and
   interruption at every commit step leaves either the prior valid generation or detectable drift.

## Follow-ups

- **ADR-03:** register `pipeline-drift` in the coded-error table.
- **ADR-03:** allocate and freeze `write-conflict` before the CAS interface is published.
- **ADR-22:** the lockfile diff a bump produces and the `catalog bump --check` dry-run.
- **ADR-07:** `profile repin --runner|--bootstrap-toolchain` writes and re-signs the corresponding environment
  identity attribute.
- **ADR-17:** migrate the legacy ambiguous `profile repin --toolchain` spelling to `--bootstrap-toolchain` with
  an explicit compatibility/deprecation path; it must never alter component T1 pins.
- **ADR-18:** the emitted pipeline whose lifecycle this governs.

## Acceptance evidence

Current generated Run contract evidence in `pkg/nvkr/factory/bundle_io_test.go` covers idempotent generation,
byte-exact drift checks, unchanged obsolete-member retirement, empty-directory cleanup, and preservation of
modified or unlisted files. These tests do not establish a complete crash/kill-point matrix.
Reconciliation records named-bundle contract evidence and its hosted-acceptance limits.

Retired qualified renderer and bundle-transaction tests were removed with their unused implementation. The
maintained Run writer keeps its ownership, retirement, cancellation and drift tests. Retired-engine admission
is checked before authored-input or output I/O. No new provider-executed or qualified acceptance is claimed.

Acceptance requires every owned generated file to participate in the byte-exact check. Substitution of external
profile, evidence or trust inputs must fail at the owning authenticated consumer. A complete process-race and
kill-point matrix for stage/fsync/rename/manifest commit remains deferred; it must prove interruption cannot
appear valid. Future extensions, general callable producer workflows and meta-lint generation require their own evidence
before admission. The fixed M3 worker pair requires its own byte-exact generation, ownership and negative
admission tests; local rendering does not establish provider job names, reusable-workflow certificate claims
or hosted qualification acceptance. Two worktree branches updating the same lock must preserve the ordinary Git merge-conflict path.
The CLI contract test must also prove the legacy `--toolchain` option cannot be mistaken for a component
toolchain update.
