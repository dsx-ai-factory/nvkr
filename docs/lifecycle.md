# Definition, Run, and Release workflow

This reference covers output completeness, immutable attempts, release verification and SDK use.
Start with the [quickstart](quickstart.md) for CLI setup and the [concept reference](user-concepts.md)
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

The [NVKE Definition](https://github.com/dsx-ai-factory/nvkr/blob/main/definitions/nvke-v1/definition.yaml) directly selects `nvkectl`, `cwe`, and `nvke-helm`
for three outputs. The Helm output explicitly depends on the CWE image output. No reusable membership group,
criteria query, inheritance chain, or mixin sits between an output and its producers.

Deployment topology and placement are owned by the system that consumes the artifacts;
Definitions and Releases contain no cluster views, deployment roles or categories.

## Select an Environment

The [local Environment](https://github.com/dsx-ai-factory/nvkr/blob/main/definitions/nvke-v1/environment-local.yaml) supplies execution settings and a local
registry destination. The [GitHub Environment](https://github.com/dsx-ai-factory/nvkr/blob/main/definitions/nvke-v1/environment-github.yaml) supplies the
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
  --definition definitions/nvke-v1/definition.yaml

bin/nvkr run plan \
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-local.yaml
```

For saved generated Plans, `run plan --plan <path>` prints the required outputs and instructions;
`--pretty` provides an indented display view. `run graph` exports complete linked producer/artifact graphs,
and `run diff` explains changes between canonical Plans. See [Plan and graph inspection](plan-and-graph-inspection.md).

Generation writes provider files and their commit manifest from the same admitted inputs. This example
uses a temporary root: the repository's committed bundle selects M2, not the three-output Definition.
The matching `--check` invocation regenerates in memory and fails if generated files differ:

```sh
NVKR_LIFECYCLE_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-lifecycle.XXXXXX")
bin/nvkr run generate \
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-github.yaml \
  --engine github --root "$NVKR_LIFECYCLE_ROOT"

bin/nvkr run generate \
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-github.yaml \
  --engine github --root "$NVKR_LIFECYCLE_ROOT" --check
```

Use the [M2 instructions](https://github.com/dsx-ai-factory/nvkr/blob/main/definitions/nvke-m2/README.md) to regenerate the committed bundle.
Generated CI defaults to generic hosted runners; ADR-18 separately admits the explicit native ARC disk
route. Publication and signing belong to the GitHub v1 lane. Generation does not prove provider execution.

## Run and verify

Inspect a local attempt without creating its workspace or executing nodes:

```sh
bin/nvkr run local \
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-local.yaml \
  --attempt review-001 --dry-run
```

The dry-run remains pending with no observed outputs. Actual execution uses an explicit attempt name under the
selected attempts root; the default root is `.nvkr/runs`. The attempts root must be a clean repository-relative
path under `.nvkr`. Publication is a separately enabled operation. The
implementation record distinguishes contract tests from external execution.

### Choose local Run presentation

`run local --ui=auto` selects a terminal dashboard only with interactive standard
input/output and a usable terminal. Pipes, redirected output and `--ui=plain`
retain scrolling output. The dashboard lists required named outputs, selected
producer operations and dependencies, observation counts and bounded public
activity. Producer completion and fresh activity do not establish output
observation, finalization, release verification or runtime health.

```sh
bin/nvkr run local \
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-local.yaml \
  --attempt build-001 --ui=plain
```

Choose `--ui=tui` explicitly to require an interactive dashboard; incompatible
terminal/JSON choices fail before input or execution work. `--no-color` or a
nonempty `NO_COLOR` selects monochrome presentation. A tab strip identifies
Outputs, Details and Activity; separator lines distinguish their contents. Tab
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
to ordinary stderr after terminal restoration and owned-work joining. A marker
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

Every public command accepts `--ui=tui` and `--no-color`: Definition validation
and inspection; Run planning, graph, comparison, inspection, generation and local
execution; producer resolution; all catalog inspection and maintenance commands;
and Release inspection, browsing, export and verification. Other than the interactive
local Run default described above, commands default to `--ui=plain` and keep their
existing text, canonical JSON or diagram output. On commands with a result
browser, `--ui=auto` selects it when the terminal is eligible. Explicit JSON output
and `--ui=tui` are incompatible and fail before input, network or write work.

```sh
bin/nvkr definition inspect --definition definitions/nvke-v1/definition.yaml --ui=tui
bin/nvkr run graph --plan .github/nvkr-run/plan.json --ui=tui
bin/nvkr catalog list --data definitions/nvke-v1/catalog --ui=tui
```

Graph and release browsers start with an output overview when their supplied
results also contain internal nodes or evidence entries. Press `g` to toggle all
supplied entries; `/` searches the complete supplied set. Producer-only graph
views keep their supplied producers. The default list uses one row per entry,
with readable output names; repeated static states are omitted. Full digests and command context live in
Details.

Results and Details show projected names, identities, counts and dependencies;
`/` searches names and `f` cycles the available All, Changed and Failed filters.
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
browser neither repeats nor undoes it. Verification results reflect only the
owning verifier's accepted lane and scope. Release browsing checks metadata;
local Release inspection additionally checks closure membership and payload
integrity. Neither operation authenticates signatures or consumer policy. Differences still return status 3 when requested. Raw changed
values, source URLs, private paths, signer expressions and error messages are
excluded from safe result panes; use existing plain output for the complete
machine or diagram representation. Hidden execution and CI helpers keep their
existing interface.

### Inspect a recorded attempt

```sh
bin/nvkr run inspect --record .nvkr/runs/build-001/lifecycle.json
bin/nvkr run inspect --record .nvkr/runs/build-001/lifecycle.json --json
bin/nvkr run inspect --record .nvkr/runs/build-001/lifecycle.json --ui=tui
```

Inspection defaults to plain output. The bounded lifecycle-owned loader rejects
symlinked directories/files, malformed canonical bytes and inconsistent named
output metadata. It describes the sibling disk-commit marker separately. A marker
without a record, an interrupted attempt without a record, and a complete record
with incomplete cleanup have different descriptions. q asks before closing a
read-only viewer; y closes it, while n, Esc or Enter keeps it open. Closure happens
without execution, record changes or cleanup. Stored observations establish no
current payload availability, signature verification or health; file modification
times are not live freshness. The SDK exposes the same description through
`Client.InspectRun`.

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

Maintained inputs under `definitions/` use Definition and Environment. Superseded factory authoring APIs
and their migration fixtures are removed; backward compatibility is not a requirement.
The direct-output model uses Definition v5, catalog v3 and producer IR v4. Generated Run Plans use v5; Run and Release use v4. Older recipe layouts and cluster/placement fields
are rejected before work. Current migration checks and maintained-input build evidence are tracked in reconciliation;
historical Runs retain the authoring and wire contracts exercised at the time.
