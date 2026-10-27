# Definition, Run, and Release workflow

This reference covers output completeness, immutable attempts, release verification and SDK use.
Start with the [quickstart](quickstart.md#build-only-the-cli) for CLI setup and the [concept reference](user-concepts.md)
for the six public objects. [ADR-17](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/0017-cli-sdk-foundation-and-package-topology.md) owns the
lifecycle contract; [reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md) owns current implementation and evidence.

## Declare outputs

A Definition declares named outputs. Each output selects exact catalog components and a concrete target
platform. Catalog components own reusable build or acquisition
instructions. The compiler derives a closed, pinned build graph for each output and derives its packaging,
publication route, and evidence requirements from the output and selected Environment.

The Build Digest identifies that generated graph's logical content. Output labels and
execution settings remain in their owning records. Two outputs can share a Build Digest while requiring
separate producer jobs, observations and evidence; one successful output cannot satisfy a missing sibling.

The [NVKE Definition](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/examples/nvke-tutorial/definition.yaml) directly selects `nvkectl`, `cwe`, and `nvke-helm`
for three outputs. The Helm output explicitly depends on the NVKE management-plane image output (`cwe`). No reusable membership group,
criteria query, inheritance chain, or mixin sits between an output and its producers.

Deployment topology and placement are owned by the system that consumes the artifacts;
Definitions and Releases contain no cluster views, deployment roles or categories.

## Select an Environment

The [local Environment](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/examples/nvke-tutorial/environment-local.yaml) supplies execution settings and a local
registry destination. The [GitHub Environment](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/examples/nvke-tutorial/environment-github.yaml) supplies the
corresponding hosted-run settings. Environments own runner routes and may declare named execution classes with
host platform, resource capacity, exposed capabilities, and timeout ceilings. The compiler deterministically
binds a requested build to one satisfying class and records that exact route in the Plan. A class declaration is
planning input, not proof that a runtime supplied the declared capacity or capability. Each execution adapter
admits only its supported requirement bindings; unsupported requests fail before execution. Local direct-Packer
and the explicit native ARC route retain their own admission boundaries.

Environments also own the publication namespace, credentials, and signing identity policy. Catalog sources and
locks own tool versions and byte pins; Definition selects the scanner, signer, and generators by name.
Output-affecting build tools remain part of build identity.

A destination or signing-setting change can change the prepared execution and generated workflow while
preserving build identity. A dry-run signing mode records intended operations and never supplies a signature.

## Inspect and generate

With `bin/nvkr` built as described in the quickstart, inspect the three-output NVKE example:

```sh
bin/nvkr definition validate \
  --definition docs/examples/nvke-tutorial/definition.yaml

bin/nvkr run plan \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-local.yaml
```

For saved generated Plans, `run plan --plan <path>` prints the required outputs and instructions;
`--pretty` provides an indented display view. `run graph` exports complete linked producer/artifact graphs,
and `run diff` explains changes between canonical Plans. See [Plan and graph inspection](plan-and-graph-inspection.md).

Generation writes provider files and their commit manifest from the same admitted inputs. This example
uses a temporary root: the repository's committed bundle selects the maintained NVKE Definition, not the three-output Definition.
The matching `--check` invocation regenerates in memory and fails if generated files differ:

```sh
NVKR_LIFECYCLE_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-lifecycle.XXXXXX")
bin/nvkr run generate \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --engine github --root "$NVKR_LIFECYCLE_ROOT"

bin/nvkr run generate \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --engine github --root "$NVKR_LIFECYCLE_ROOT" --check
```

Use the [maintained NVKE Definition's instructions](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/nvke/README.md) to regenerate the committed bundle.
Generated CI defaults to generic hosted runners; ADR-18 separately admits the explicit native ARC disk
route. Publication and signing belong to the GitHub v1 lane. Generation does not prove provider execution.

## Run and verify

Inspect a local attempt without creating its workspace or executing nodes:

```sh
bin/nvkr run local \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-local.yaml \
  --attempt review-001 --dry-run
```

The dry-run remains pending with no observed outputs. Actual execution uses an explicit attempt name under the
selected attempts root; the default root is `.nvkr/runs`. The attempts root must be a clean repository-relative
path under `.nvkr`. Publication is a separately enabled operation. The
implementation record distinguishes contract tests from external execution.

### Choose local Run presentation

`run local --ui=auto` selects a terminal dashboard only with interactive standard
input/output, a usable terminal and no nonempty `CI` environment variable.
Pipes, redirected output, CI auto mode and `--ui=plain`
retain scrolling output. The dashboard lists required named outputs, selected
producer operations and dependencies, observation counts and bounded public
activity. Producer completion and fresh activity do not establish output
observation, finalization, release verification or runtime health.

```sh
bin/nvkr run local \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-local.yaml \
  --attempt build-001 --ui=plain
```

Choose `--ui=tui` explicitly to require an interactive dashboard; incompatible
terminal/JSON choices fail before input or execution work. `--no-color` or a
nonempty `NO_COLOR` selects monochrome presentation with terminal-default colors.
NVKR and `nvke-demo` share a dark blue canvas, pale text and cyan pane titles and
focus borders. A tab strip identifies Outputs, Details and Activity; named frames
distinguish their contents. Tab
or 1/2/3 changes panes, arrows navigate the focused pane, Enter opens details,
Esc goes back, `/` searches output names, `f` changes the filter and `?` shows help.
The initial screen uses one row per output. Details and Activity open when
selected; a wide terminal pairs the list with Details only after inspection is
requested. Selection stays bound to the same named output while execution updates. Activity scrolling pauses following new
entries; End resumes it. On a small terminal the view uses one pane at a time.

During a live Run, q opens a cancellation confirmation. Press y to cancel;
n, Esc or Enter keeps the Run open. Ctrl+C requests cancellation immediately.
Cancellation waits for owned work to stop. Esc otherwise navigates. The dashboard
has no force-exit or implicit deployment cleanup action. A successful or failed
command restores the terminal and returns to the shell with its original owning
outcome; a reporting failure can separately
fail the command. A complete retained Run with incomplete cleanup keeps its
observed outputs while returning the coded failure.

While the dashboard is open, a shared memory buffer retains the last 128 KiB of
producer stdout and stderr. If the command fails, the captured bytes are replayed
to ordinary stderr after terminal restoration, owned-work joining and the safe
final summary. Recovered presenter/worker panic diagnostics use the same private
bounded sink. A marker
identifies truncation. Raw producer text never enters the safe dashboard panes.
This failure replay does not create a durable general-purpose log; successful
Run output is not replayed. Existing Packer diagnostic retention is separate.

`run local --json` emits one deterministic, safe final-result document on stdout
under `nvkr.dev/local-run-result/v1`. It includes frozen required outputs, observed
membership, status, lane, code, exit status and record/cleanup availability.
Producer diagnostics go to stderr. It omits private paths, transport references
and raw failure messages; it is a description, not a resumable Run or verification
receipt. Existing `run plan --json` still emits exact canonical Plan bytes.

Auto presentation stays plain for `--dry-run`. Explicit `--dry-run --ui=tui` first
admits the normal dry-run, then opens a static read-only view; it executes no
producers and creates no attempt. Closing that view does not cancel another Run.

### Browse public command results

The following public commands accept `--ui=tui` and `--no-color`: Definition validation
and inspection; Run planning, graph, comparison, inspection, generation and local
execution; producer resolution; all catalog inspection and maintenance commands;
and Release inspection, browsing, export, pulling and verification. Other than the interactive
local Run default described above, commands default to `--ui=plain` and keep their
existing text, canonical JSON or diagram output. On commands with a result
browser, `--ui=auto` selects it when the terminal is eligible. Explicit JSON or Mermaid output
and `--ui=tui` are incompatible and fail before input, network or write work.
`validate`, `run check-artifacts` and `run qualify` retain canonical machine-readable diagnostics and do not offer
presentation flags.

```sh
bin/nvkr definition inspect --definition docs/examples/nvke-tutorial/definition.yaml --ui=tui
bin/nvkr run graph --plan .github/nvkr-run/plan.json --ui=tui
bin/nvkr catalog list --data inputs/catalogs/nvke/module-tags --ui=tui
```

Graph and release browsers start with an output overview when their supplied
results also contain internal nodes or evidence entries. Press `g` to toggle all
supplied entries; `/` searches the complete supplied set. Producer-only graph
views keep their supplied producers. The default list uses one row per entry,
with readable output names; repeated static states are omitted. Release rows add compact kind/platform or
chart-version summaries when the complete row fits. Full digests and command context live in
Details.

Results and Details show projected names, identities, counts and dependencies;
`/` searches result names, navigation identities, kinds and projected metadata fields, and `f` cycles the
available All, Changed and Failed filters. Searches match a case-insensitive substring across the supplied
entries; global command context is outside item search. Run progress keeps its output-name search.
Static views offer Activity only when activity is supplied; live Runs retain it.
Tab cycles the available panes, and 1/2/3 selects a pane when available.
Details group navigable dependencies before item metadata and command context.
A path shows each drill-down step; Esc returns to the exact previous selection.
A pinned hint
identifies the dependency Enter will follow even when its row is scrolled away.
Enter follows an available dependency and Esc returns. q asks before
closing; y closes, while n, Esc or Enter stays. Small terminals use one pane.
Below 40 columns or 12 rows, browsing pauses until the terminal is resized;
close confirmation and Ctrl+C remain available.

The result browser opens after the command's existing operation finishes. Catalog
maintenance and generation retain their normal write/check behavior; closing the
browser neither repeats nor undoes it. Ctrl+C closes this read-only browser and
preserves the completed operation's owning result. Verification results reflect only the
owning verifier's accepted lane and scope. Authenticity results explicitly state
that signatures and evidence were authenticated while payload integrity was not
checked; artifact rows describe authenticated publisher metadata. Release browsing checks metadata;
local Release inspection additionally checks closure membership and payload
integrity. Neither operation authenticates signatures or consumer policy. Differences still return status 3 when requested. Raw changed
values, source URLs, private paths, signer expressions and error messages are
excluded from safe result panes; use existing plain output for the complete
machine or diagram representation. Hidden execution and CI helpers keep their
existing interface.

### Dashboard screenshots

These controlled Plan views show the shared NVKR/NVKE demo styling; they do not
represent executed producers or human usability acceptance.

![Named output overview](https://raw.githubusercontent.com/dsx-ai-factory/nvkr/93715c6931a35824dd14e1221e2f129cbacc59a9/docs/assets/nvkr-dashboard/overview.png)

![Selected output details](https://raw.githubusercontent.com/dsx-ai-factory/nvkr/93715c6931a35824dd14e1221e2f129cbacc59a9/docs/assets/nvkr-dashboard/details.png)

![Exact dependency drill-down](https://raw.githubusercontent.com/dsx-ai-factory/nvkr/93715c6931a35824dd14e1221e2f129cbacc59a9/docs/assets/nvkr-dashboard/drilldown.png)

![Quit confirmation defaults to staying](https://raw.githubusercontent.com/dsx-ai-factory/nvkr/93715c6931a35824dd14e1221e2f129cbacc59a9/docs/assets/nvkr-dashboard/quit.png)

![Wide terminal details](https://raw.githubusercontent.com/dsx-ai-factory/nvkr/93715c6931a35824dd14e1221e2f129cbacc59a9/docs/assets/nvkr-dashboard/wide-details.png)

![Monochrome presentation](https://raw.githubusercontent.com/dsx-ai-factory/nvkr/93715c6931a35824dd14e1221e2f129cbacc59a9/docs/assets/nvkr-dashboard/monochrome.png)

### Inspect a recorded attempt

```sh
bin/nvkr run inspect --record .nvkr/runs/build-001/lifecycle.json
bin/nvkr run inspect --record .nvkr/runs/build-001/lifecycle.json --json
bin/nvkr run inspect --record .nvkr/runs/build-001/lifecycle.json --ui=tui
```

Inspection defaults to plain output. The bounded lifecycle-owned loader rejects
symlinks in every path component, malformed canonical bytes, inconsistent named
output metadata, unavailable qualified lanes and an attempt name that does not
match its containing directory. Use a physical path (for example, start from
`pwd -P`); macOS `/tmp` is a symlink, so use its physical `/private/tmp` path.
A missing directory or record returns a coded verification failure. A disk marker
without a record cannot distinguish unfinished live work from interruption; no
interrupted Run is inferred. A present record describes its sibling disk-commit
marker separately, without performing cleanup.

q asks before closing a read-only viewer; y or Ctrl+C closes it, while n, Esc or
Enter keeps it open. Closure does not execute, change a record, or perform cleanup.
Stored observations establish no current payload availability, signature
verification or health; file modification times are not live freshness. The SDK
exposes the same detached description through `Client.InspectRun`.

For a completed local `disk-image` output, lifecycle retains the exact observed QCOW2 as
`<attempts-root>/<attempt>/outputs/<output-name>.qcow2` and prints that path. The file is read-only, is committed
only after held-source digest/size revalidation and successful Run binding, and remains local unqualified output.
An attempt transaction prevents an interrupted disk/record pair from being treated as complete. It is not an OCI
or object-store publication and the caller cannot redirect it outside the immutable attempt workspace. Marker
removal plus attempt-directory sync is the durable success point; after successful rollback, errors before it
record failure.

A Run records one attempt against frozen inputs and expected output slots. Execution must match independently
observed package contents to those slots, including selected build identities and exact component membership.
An omitted or conflicting output cannot make the Run complete. Once execution has prepared an attempt, its
failure record retains the required outputs; that record cannot be deserialized into release authority.
Hosted finalization also requires a running finalizer and its generated helper. A bootstrap failure, unavailable
runner, or interrupted artifact transfer can prevent the Run record from being produced at all. Consult the
provider's job state in that case; a missing record is never evidence of success.

A Release supplies one pinned reference to immutable selected artifacts and evidence. Consumer verification
takes that reference and a Verification Policy and authenticates the complete selected closure. The policy
names the accepted evidence lane and identities. Qualified authorization, v1 pipeline evidence, and dry-run
descriptions remain distinct. Unsupported authority adapters return a coded capability error.

The policy explicitly selects what acceptance means. `authenticity` checks signed membership, the root and
every required output's signatures and production provenance, and required signed SBOMs without downloading
artifact payload layers. `artifact-publication` checks the selected payload bytes as well as evidence.
Full v1 release acceptance adds qualification and revocation requirements. Each result retains its scope;
an unavailable stronger scope never falls back to weaker verification.

For disk outputs, `VerificationPolicy.minimumDiskEvidence` is included in the policy digest. Pipeline-attested
verification defaults to `build-provenance-v1`, accepting signed producer/material evidence without an inspected
disk inventory. Use `--minimum-disk-evidence strict` to require inspected evidence. The
SDK exposes `DiskEvidenceStrict` and `DiskEvidenceBuildProvenance`. Qualified verification defaults to strict
and cannot opt into the producer-only mode. Authenticity enforces the same minimum against signed evidence
and metadata; it does not inspect or re-hash the disk.

The CLI expresses build-evidence acceptance as `--scope artifact-publication`. On Linux, with a pinned cosign
executable and an existing signed publication in a public registry, the consumer invocation is:

```sh
bin/nvkr release verify \
  --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope artifact-publication \
  --identity "$BUILD_IDENTITY_REGEXP" --issuer "$OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST" \
  --anonymous
```

To authenticate release evidence without downloading artifact payloads, change the scope to `authenticity`:

```sh
bin/nvkr release verify \
  --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope authenticity \
  --identity "$BUILD_IDENTITY_REGEXP" --issuer "$OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST" \
  --anonymous
```

The result reports `payloadIntegrityVerified: false`. It authenticates the complete metadata/evidence
closure, including each required output and SBOM bundle, but cannot authorize payload export or exact-input
matching. `--definition`, `--environment`, and `--plan` are rejected with this scope before external work.
Stock verification remains Linux-only with the same pinned cosign requirement.

An independent consumer can additionally require the publication to realize
exact authored and compiled inputs under `artifact-publication` by supplying all three paths:

```sh
bin/nvkr release verify \
  --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope artifact-publication \
  --identity "$BUILD_IDENTITY_REGEXP" --issuer "$OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST" \
  --definition definition.yaml --environment environment.yaml --plan plan.json \
  --anonymous
```

The three inputs are admitted together before signature or registry work. A
successful response includes a package-issued exact-input description bound to
the publication subject, consumer policy, and its Definition, Environment, Plan
and content digests. This narrows what the consumer accepted; it does not grant
a stronger lane, full Release scope, qualification, or deployment acceptance.
The Plan is generated pipeline state; an unreadable or undecodable Plan reports
pipeline drift, while a decoded Plan that fails validation preserves its validation
error rather than being reclassified by the command adapter.

For `artifact-publication` without the three exact-input flags, `release verify` writes the verified `Release`
description directly:

```json
{
  "schemaVersion": "nvkr.dev/release/v4",
  "subject": "registry.example/releases@sha256:<release-digest>",
  "lane": "pipeline-attested",
  "scope": "artifact-publication",
  "artifacts": [
    {
      "name": "nvkectl",
      "kind": "executable",
      "digest": "sha256:<artifact-digest>",
      "reference": "registry.example/releases-nvkectl@sha256:<artifact-digest>",
      "buildDigest": "sha256:<build-digest>",
      "components": ["nvkectl"]
    }
  ],
  "contentSubject": "sha256:<content-digest>",
  "policyDigest": "sha256:<policy-digest>"
}
```

With all three exact-input flags, the output is a wrapper containing that same
verified `Release` description and the defensive exact-input description:

```json
{
  "schemaVersion": "nvkr.dev/release-verification-result/v1",
  "release": {
    "schemaVersion": "nvkr.dev/release/v4",
    "subject": "registry.example/releases@sha256:<release-digest>",
    "lane": "pipeline-attested",
    "scope": "artifact-publication",
    "artifacts": [
      {
        "name": "nvkectl",
        "kind": "executable",
        "digest": "sha256:<artifact-digest>",
        "reference": "registry.example/releases-nvkectl@sha256:<artifact-digest>",
        "buildDigest": "sha256:<build-digest>",
        "components": ["nvkectl"]
      }
    ],
    "contentSubject": "sha256:<content-digest>",
    "policyDigest": "sha256:<policy-digest>"
  },
  "exactInputs": {
    "releaseSubject": "registry.example/releases@sha256:<release-digest>",
    "policyDigest": "sha256:<policy-digest>",
    "definitionDigest": "sha256:<definition-digest>",
    "environmentDigest": "sha256:<environment-digest>",
    "planDigest": "sha256:<plan-digest>",
    "contentSubject": "sha256:<content-digest>"
  }
}
```

Artifact entries retain their type-specific optional fields, such as `disk` for
disk-image outputs. Exact-input mode changes only the top-level JSON shape; it does
not replace or weaken the verified Release description. The opaque receipt remains
live and process-local and is never serialized by the CLI.

`RELEASE_REF` is a registry reference pinned with `@sha256:…`; the identity and issuer belong to the
consumer's trusted build policy. Signature and production-attestation verification are both required by this
scope. Authenticity separately requires these checks for the root and every required named output, along
with cryptographic SBOM verification. The SDK retains a live, policy-bound capability until closed.
The stock verifier remains Linux-only, and unit tests do not establish live signing-service
acceptance. Full v1 release scope (`--scope release`) and qualified acceptance remain capability-gated.

The scoped consumer and exact output/content bindings have positive and negative contracts. Full v1
release acceptance still requires its deferred qualification, membership, and revocation adapters; the scoped
result cannot substitute for them. The ADR-04 stock-cosign consumer contract remains independent of the
convenience NVKR reader.

## Authenticate a qualification candidate

On Linux, `nvkr run check-artifacts` checks the already-published bytes against one exact producing Run
attempt and an independently selected trust policy:

```sh
nvkr run check-artifacts \
  --run https://github.com/OWNER/REPO/actions/runs/ID/attempts/N \
  --policy qualification.yaml
```

Replace `OWNER/REPO`, `ID` and `N` with the intended Run and attempt. An attempt is mandatory; the command
never selects the latest retry implicitly. Configure the trusted producer and verifier once in
`qualification.yaml`, then reuse that policy across approved Runs:

```yaml
schemaVersion: nvkr.dev/qualification-policy/v1
source:
  repository: OWNER/REPO
  workflow: .github/workflows/nvkr-run.yml
  defaultBranch: main
cosign:
  # Replace this placeholder with the SHA-256 of the independently trusted executable.
  digest: sha256:0000000000000000000000000000000000000000000000000000000000000000
```

The zero digest is a placeholder, not an accepted executable pin. The verifier defaults to `cosign` on
`PATH`; optional `cosign.path` selects another executable, with relative paths resolved beside the policy.
Its bytes must match the independent digest. Neither the producer's Environment nor downloaded files can
select consumer trust. The optional `--workspace` defaults to `.nvkr/qualification`.
`NVKR_GITHUB_TOKEN` supplies GitHub read credentials; private registry reads use `NVKR_REGISTRY`,
`NVKR_REGISTRY_USER` and `NVKR_REGISTRY_PASSWORD`. This check needs no qualification signing credentials
or identity token. macOS rejects it before input or network work.

The source owner discovers the commit from GitHub's exact attempt endpoint, checks successful completion,
approved repository and workflow, and the repository's actual default branch. It then downloads the single
`nvkr-qualification-ID-N` artifact for that attempt. Direct HTTPS access to `api.github.com` and approved
artifact download endpoints is required. The owned transport ignores proxy environment variables; an
admitted artifact download location receives a fresh request without GitHub API credentials.

Generated production real-signing `per-output-v1` workflows save one `handoff.json` after complete finalization. It contains the
original Plan, the compiler-read Definition and Environment, base and output-specific catalogs, overlays,
registries, locks, patches and directory facts, plus the original predicate bytes keyed by the root and every
delivered payload reference. The collector joins bounded aggregate metadata and saved output-route records
without reading payload packages. The handoff contains no consumer policy. Development, unsigned, dry-run and legacy aggregate workflows do not produce it;
an older Run without a complete handoff fails instead of rebuilding or reconstructing missing evidence.
Legacy aggregate builds/publication remain available, but they lack invocation-bound release metadata and
the per-output route records needed for qualification; direct qualification rejects that capability before publication work.

The lifecycle owner recompiles the captured authored bytes with their original logical paths and requires
the exact original Plan identity. Authored reads have no checkout fallback. Catalog-owned authentication
of pinned upstream Git trees remains required and may use its existing credentials and bounded network
access; this is not offline compilation. Unknown fields, duplicate keys, noncanonical encodings, unsafe
paths, incomplete inputs and oversized transport fail closed. The handoff is descriptive generated
transport, not proof that any artifact passed. `lifecycle.Consumer.CheckQualification` still verifies the
complete publication, original predicates, signatures, subjects and input bindings.

For programmatic use, `lifecycle.CheckQualificationRun` owns Run selection, policy loading and handoff
retrieval. `lifecycle.CheckQualificationHandoff` borrows a provider-issued source receipt. The existing
`lifecycle.CheckQualificationFiles` adapter remains available for explicit saved-file diagnostics; normal
CLI use does not require those files or separately entered source-commit fields.
All three adapters scope explicit registry pull credentials to the independently configured canonical
`ConsumerOptions.Credential.Registry` host (the corresponding runtime credential for Run/handoff calls).
A candidate or Environment cannot choose that destination; missing or mismatched hosts fail before
registry access. Anonymous pulls and registry-scoped credential keychains remain available.

At the consumer API boundary, source and Plan validation precede adapter-capability checks. A request with
both malformed inputs and unavailable adapters reports the input error first. Neither case reaches registry
or signature-verification I/O.

Every selected output is checked for schema/membership, exact Run and inputs, signed evidence, build
signature/provenance, applicable payload pins and any authored embedded declarations. Failed checks return coded
diagnostics; only a complete passing set produces an opaque `ArtifactCheckReceipt`. Its description is
defensive data and grants no authority. Hosted checks verify every output's complete payload in sequence,
releasing non-executable scratch after each output. A successful receipt retains verified controls, evidence,
exact executable bundle bytes and a frozen Plan until closed; it cannot export discarded payloads. Closure revokes
all shallow copies, cancels and joins registered operations, then releases held bytes. This lifetime does not
itself authorize smoke execution. The direct consumer and handoff
methods borrow the caller's source Run receipt, whose closure also revokes the check receipt. The Run and
file adapters transfer their internally created source owner to the returned receipt and close it with that
receipt.
If the source is revoked during verification, the final source check refuses receipt issuance. Each output
then includes a separate failed `source-lifetime` diagnostic and an overall failure; completed signature,
pin and other artifact checks keep their actual results. This extra diagnostic appears only on final
source-admission failure. Successful reports retain the six artifact checks above. Always inspect the
returned error or command exit status; diagnostic data alone never proves receipt issuance.
Payload digests remain distinct from OCI delivery-container subjects, and disk results explicitly retain
producer-only scope without content inspection.

An incomplete evidence traversal is labeled as a failure of the complete publication in every output's
signed-evidence diagnostic. Output-specific signature failures remain visible alongside that global failure;
no output receives a passing evidence verdict merely because its own signature check did not fail.

Production qualification accepts authenticated publications from earlier attempts of the same producer workflow run.
The selected successful attempt anchors completion; each output retains its original preparation and signing
identities and exact predicate bytes. Preparation must precede or equal signing, output signing must precede
or equal aggregate assembly, and aggregate assembly/signing must precede or equal source completion.
All subjects must match the same exact Plan, inputs and workflow builder. Missing origins, other runs,
future attempts, changed inputs and invalid or incomplete evidence still fail. An earlier attempt may have
failed unrelated jobs; its successfully authenticated outputs need not be rebuilt. The CLI writes
check diagnostics and returns the stable failure code; its serialized output cannot restore a live receipt
or grant qualification signing authority.

The [pinned embedded declaration](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/0010-pluggable-builder-types.md)
profile is optional for source producers; no producer must supply it. Omission yields `not-applicable`.
Any authored declaration must match its referenced pinned upstream metadata, and a missing, invalid or
disagreeing reference fails. The check reads
raw source metadata; it does not apply patches, configuration or material substitutions and cannot establish
effective customized or installed composition. Source, patch, configuration and material identity/provenance
checks remain required. The maintained candidate needs no new upstream component manifests.

Delivered-tool smoke still requires the complete native worker results described below. An A receipt alone
cannot sign evidence or authorize release. Local tests do not establish real hosted signing or complete-set
consumer acceptance.
[Reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md#m3-mvp-scope-and-acceptance-boundary) records contract coverage and
the outstanding hosted signing, exact-input candidate and consumer acceptance evidence.

## Publish qualification evidence

Hosted qualification uses one command:

```sh
nvkr run qualify --policy qualification.yaml
```

The generated workflow supplies its environment and credentials. The command discovers its source only
from the authenticated `workflow_run` event, verifies the source's exact successful attempt, and downloads
its original handoff. There are no source-commit, candidate-file, verdict-file or skip-smoke flags.
A manually dispatched qualifier or the source build's own identity is rejected.

The generated production qualifier adds `--issue-manifest`:

```sh
nvkr run qualify --policy qualification.yaml --issue-manifest
```

This keeps the live artifact-check receipt through complete qualification publication and manifest
issuance. The same independently loaded policy selects the exact default-branch qualification identity
and policy digest; evidence cannot select its own trusted signer. The issuer verifies the exact canonical
qualification evidence on every artifact, attests the manifest to the publication root, then verifies its
attached bytes. Development policies refuse this option before provider or publication work; their
generated qualifier publishes development qualification only.

With this option, canonical diagnostics contain the qualification result and, after successful issuance,
the manifest, its digest and subject. A manifest failure returns a nonzero exit status and preserves the
qualification diagnostics, including a successful qualification whose later manifest step failed. Earlier
attachments may remain; failure does not imply rollback. Without the option, the command retains its
qualification-only output shape. Neither mode creates a release tag, moves a channel, issues qualified-lane
clearance or establishes full-release consumer acceptance. Hosted OIDC and registry acceptance remain
subject to the evidence recorded in reconciliation.

Add `--qualification-policy qualification.yaml` to the normal `run generate` invocation to generate the
qualifier and its native smoke worker alongside the producer. The worker is a generated reusable GitHub
Actions workflow; the operator does not write or maintain a separate worker implementation. The maintained
candidate supplies `inputs/policies/nvke-qualification.yaml` and emits `.github/workflows/nvkr-qualify.yml`
and `.github/workflows/nvkr-smoke.yml`. The same invocation with `--check` compares
all owned bytes without writing. The separately owned qualification manifest records both workflow files;
hand edits and unowned files are not silently overwritten. The worker is a fixed same-repository reusable
workflow at the caller's exact commit. It is generated plumbing, not another operator command to configure
for each Run. Before candidate or signing work, both commands check the trusted checkout against that
manifest and the full loaded policy; policy or workflow drift requires regeneration.

The independent policy extends the artifact-check policy with these decisions:

| Policy field | What it controls |
|---|---|
| `qualificationWorkflow` | The default-branch workflow allowed to publish qualification evidence |
| `cosign.version` and `cosign.platformDigests` | An exact release locator and independently approved executable bytes for every worker platform plus the AMD64 final signer |
| `smoke.workflow` | A separate same-repository worker identity, distinct from both builder and final signer |
| `smoke.images` | Exact native execution-image references, pinned by digest |
| `smoke.tools` | Exact component names mapped to fixed nvkectl, Helm, kubectl, Kind, clusterctl or ORAS version-check profiles |
| `smoke.nonToolOutputs` | Exact OCI/APT output names, kinds and components explicitly outside loose-tool smoke |
| `smoke.nonToolMembers` | Exact output/member pairs for non-executable runtime-assets archives in mixed executable bundles |
| `outsideMVPSuites` / `requiredSuites` | Unevaluated external suites, or a requirement that currently blocks qualification |

The [maintained-candidate policy](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/policies/nvke-qualification.yaml)
contains concrete verifier/native-image pins and exact output scope. Review those independent trust selections
with the generated workflows before merge. Other Definitions need their own matching scope and native pins;
the candidate policy is not a wildcard approval. The trust policy belongs to the operator, not the producing
Run. Concrete configuration does not establish organizational approval or successful hosted execution.

For hosted generation, replace the earlier single `cosign.digest` field with `cosign.platformDigests`;
using both is rejected. Map keys are exactly `linux/amd64` and `linux/arm64`. Set `cosign.version` to the
approved stable `vX.Y.Z` download locator and leave `cosign.path` absent or `cosign`. Images must cover exactly
the executable platforms selected by the Definition. An AMD64-only product generates one worker. An
ARM64-only product still needs an AMD64 cosign pin because the final signer runs on AMD64. The maintained
candidate selects both platforms. Generation validates all tool profiles, versions and non-tool membership
against the Plan before writing either workflow; missing or extra scope is an error.

`smoke.nonToolOutputs` is a list of `output`, `kind`, `components` and `reason` records. The admitted pairs
are `kind: oci` with `reason: deployment-image`, and `kind: apt-repository` with
`reason: package-repository`. Each record must match the complete named output's component set. The
candidate policy contains all 25 OCI records, alongside three profiles covering its four native
tool invocations. It contains no Helm/disk exemptions because those formats have a fixed non-tool rule.

For a mixed bundle, `smoke.nonToolMembers` accepts records with `output`, `member` and
`reason: runtime-assets-archive`. Each must select a source-built `tarball` producer in that exact
output; the delivered member must have no executable mode bits. Sibling tools still require their
fixed profiles, and each executable output must contain at least one tool. The archive remains
subject to artifact and provenance checks. This does not assert that its contents contain no tools.
Missing, extra, duplicate and tool-overlapping member rules fail closed.

Use independently approved native images containing `/bin/sh`, `cat`, `wc`, `sha256sum`, `chmod` and
`/usr/bin/env`. They must have no declared volumes or extra inherited environment. The payload environment
is fixed to `PATH=/usr/bin:/bin`, `HOME=/work`, `TMPDIR=/work`, `LC_ALL=C`, `TZ=UTC` and
`CLUSTERCTL_DISABLE_VERSIONCHECK=true`. The last setting disables clusterctl's optional network update
check after its version command; version/source checks and network isolation remain required. Image configuration
must not add or conflict with it. Builder images and pins are not automatically trusted smoke images.
The generated route also requires a GitHub-signing producer, its generated source workflow, and GHCR
publication under the same repository owner with the existing `github:GITHUB_TOKEN` credential reference.

The fixed profiles include `nvkectl-version-json`, `helm-version`, `kubectl-client-version-json`,
`kind-version`, `clusterctl-version-json`, `oras-version`, `nicosim-version-json` and
`nicosim-patched-version-json`. NICoSim profiles require exact version, source commit and platform,
with `clean` for unpatched inputs or `patched` for declared authenticated patch inputs. Dirty or unknown
tree stamps fail. Expected versions and source commits come from the authenticated frozen Plan. The policy cannot supply a
replacement expected version or arbitrary executable arguments. Every member of every executable bundle
must be covered. OCI deployment-image and APT package-repository exemptions require exact membership;
there is no wildcard exemption for an artifact format. Disk and Helm outputs use their explicit non-tool
format rule. Disk content remains uninspected.

Each native worker independently performs artifact checks, streams the held delivered member into a
network-disabled nonroot container, checks its native ELF identity and version response, and joins cleanup
before signing its report. The payload receives no credentials, host mounts, daemon socket or inherited
host environment. The worker can read artifacts and sign its own report; it cannot publish qualification
attestations. Its generated upload contains exactly four files under an attempt-specific artifact name.

A failed worker report lists every known native member: completed checks retain their actual pass result,
the failed attempt is marked `fail`, and checks never reached are `not-evaluated` with a coded reason.
Cleanup or revocation can fail the overall report even after all member checks passed. These diagnostics
cannot be signed as successful evidence. If the final job cannot authenticate the delivered smoke set,
every artifact receives a failed `delivered-tool-smoke-authentication` verdict while its genuine artifact
check results remain intact; this does not claim the payload itself was executed and failed.

After workers complete with success or failure, the final job repeats artifact checks and attempts to
authenticate the complete smoke set. This produces refusal diagnostics when a worker failed. Canceled,
skipped or untrusted source invocations do not enter this final job. For each platform, publication selects
the latest worker job in the same qualifier run, up to the current attempt, and requires it to succeed.
Successful workers from earlier attempts can be reused; a later failed or unfinished job blocks fallback.
The original attempt's artifact, report signature, worker certificate SAN and GitHub issuer are verified.
The same verified leaf must bind the worker's original top-level workflow, workflow commit and Run attempt.
The final owner requires unchanged source artifact checks, commit, full independent policy, native images
and complete expected member identities/results. A missing architecture or member, another run or workflow,
future attempt, changed policy or failed check prevents signing. The final qualification evidence binds the
current invocation and preserves every worker's original invocation.

`qualification.PublishTriggeredRun` owns this orchestration. The lower-level publisher consumes live A,
B and invocation receipts; serialized reports cannot restore them. The old `qualification.PublishFiles`
publishing adapter returns a capability error before I/O; saved-file artifact diagnostics remain available
through `lifecycle.CheckQualificationFiles`.

The canonical predicate uses schema and URI `nvkr.dev/qualification-evidence/v2` and
`https://nvkr.dev/qualification-evidence/v2`, with typed family `nvkr.qualification-evidence`. Version 2
requires the full smoke policy and member-level worker evidence. It also binds the complete artifact-check
report, source and qualifier identities, input identities, per-output verdicts and external-suite status.
Every selected output receives the same complete-set predicate under its own exact subject. An
outside-MVP suite is `not-evaluated`, never passed. [Independent consumption](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/publication-architecture.md#independent-consumption)
explains distinct builder/qualifier trust and complete-set rejection requirements. Requiring an external suite fails before provider or
artifact work until authenticated integration exists.

The publisher collects all results before signing and rechecks receipt lifetimes before constructing the
signer, each attachment and successful return. An operational failure can leave earlier attachments;
the command returns a failure code and retains those subjects in its diagnostics. Their presence does
not establish successful qualification or release authorization.

The maintained candidate's reviewed policy and generated workflows may merge before real acceptance runs.
After merge, a complete producer run must provide its original evidence and a handoff for its successful
completion attempt; authenticated partial retries are permitted. The hosted
worker/qualifier and independent consumer then exercise positive and negative cases. These observations
remain unproved and are required before production-readiness claims. Local native-container, race and protocol
tests do not establish GitHub's actual reusable-job names, OIDC claims, production attachment/read-back or
independent complete-set consumer acceptance. Merge does not waive any runtime verification;
[reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md#m3-mvp-scope-and-acceptance-boundary)
records their scope and status.

## Use the SDK

Configure the client with tool and registry settings once, then pass a reference and policy for each Release.
The accepted identity and issuer belong only to the policy. This example uses a public registry:

```go
package consumer

import (
	"context"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
)

func configuredClient(cosignPath, cosignDigest string) *client.Client {
	options := client.ReleaseConsumerOptions{
		CosignBinary: cosignPath,
		CosignDigest: cosignDigest,
	}
	options.Registry.Anonymous = true
	return client.New(client.WithReleaseConsumer(options))
}

func inspectRelease(ctx context.Context, c *client.Client, reference, identity, issuer string) (client.Release, error) {
	policy := client.VerificationPolicy{
		Lane: "pipeline-attested", Scope: "artifact-publication",
		IdentityRegexp: identity, Issuer: issuer,
	}
	verified, err := c.VerifyRelease(ctx, reference, policy)
	if err != nil {
		return client.Release{}, err
	}
	defer verified.Close()
	return verified.Description()
}
```

Use `AuthenticateRelease` with an `authenticity` policy for the metadata/evidence-only operation. It returns
a distinct closeable `AuthenticatedRelease`; its description records that payload integrity is unchecked.
It exposes no payload export or exact-input receipt and cannot be passed as a fully verified Release.

The returned Release is a description of the accepted contents. Its serialized form does not
recreate the live verified capability. Verification still requires the supported platform, pinned tool, and
evidence described above; compiling this example does not exercise those external dependencies.

## Inspect internal records when needed

Plans and artifact graphs explain execution dependencies. Required/observed membership explains completeness.
Typed evidence explains reproduction, compatibility, conformance, or qualification claims for exact subjects.
Clearance and publication roots explain authorization and transport. These records are useful diagnostic views;
normal product authors do not construct and join them manually.

Maintained inputs under `inputs/` use Definition and Environment; teaching selections live in `docs/examples/`. Superseded factory authoring APIs
and their migration fixtures are removed; backward compatibility is not a requirement.
The direct-output model uses Definition v5, catalog v3 and producer IR v4. Generated Run Plans use v5; Run and Release use v4. Older recipe layouts and cluster/placement fields
are rejected before work. Current migration checks and maintained-input build evidence are tracked in reconciliation;
historical Runs retain the authoring and wire contracts exercised at the time.
