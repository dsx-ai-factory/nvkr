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

The policy explicitly selects what acceptance means. Authenticating build evidence for a selected artifact
publication checks its exact bytes, build identity, and production provenance. Full v1 release acceptance adds
qualification and revocation requirements. A result for the first scope cannot satisfy the second, and an
unavailable stronger scope never falls back to weaker verification.

For disk outputs, `VerificationPolicy.minimumDiskEvidence` is included in the policy digest. Pipeline-attested
verification defaults to `build-provenance-v1`, accepting signed producer/material evidence without an inspected
disk inventory. Use `--minimum-disk-evidence strict` to require inspected evidence. The
SDK exposes `DiskEvidenceStrict` and `DiskEvidenceBuildProvenance`. Qualified verification defaults to strict
and cannot opt into the producer-only mode.

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

An independent consumer can additionally require the publication to realize
exact authored and compiled inputs by supplying all three paths:

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

Without the three exact-input flags, `release verify` writes the verified `Release`
description directly:

```json
{
  "schemaVersion": "nvkr.dev/release/v3",
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
    "schemaVersion": "nvkr.dev/release/v3",
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
scope. The SDK retains a live, policy-bound capability until closed.
The stock verifier remains Linux-only, and unit tests do not establish live signing-service
acceptance. Full v1 release scope (`--scope release`) and qualified acceptance remain capability-gated.

The scoped consumer and exact output/content bindings have positive and negative contracts. Full v1
release acceptance still requires its deferred qualification, membership, and revocation adapters; the scoped
result cannot substitute for them. The ADR-04 stock-cosign consumer contract remains independent of the
convenience NVKR reader.

## Authenticate an M3 candidate

On Linux, `nvkr run check-artifacts` checks the already-published M2 bytes against the producing Run's
saved inputs and evidence. Supply the original Definition, Environment, Plan and canonical candidate
transport record, plus the independently expected source identity:

```sh
nvkr run check-artifacts \
  --definition "$DEFINITION" --environment "$ENVIRONMENT" \
  --plan "$ORIGINAL_PLAN" --candidate "$CANDIDATE" \
  --source-repository "$SOURCE_REPOSITORY" --source-run "$SOURCE_RUN" \
  --source-attempt "$SOURCE_ATTEMPT" --source-commit "$SOURCE_COMMIT" \
  --source-workflow "$SOURCE_WORKFLOW" --default-branch main \
  --cosign "$COSIGN_BINARY" --cosign-digest "$COSIGN_DIGEST"
```

`SOURCE_WORKFLOW` is the approved repository-relative workflow path. `COSIGN_DIGEST` pins exact executable
bytes. Optional `NVKR_GITHUB_TOKEN` supplies read access to GitHub; private registry reads use
`NVKR_REGISTRY`, `NVKR_REGISTRY_USER` and `NVKR_REGISTRY_PASSWORD`. No qualification signing credentials
or identity token are required by this check. macOS rejects the operation before input or network work.

GitHub source authentication requires direct HTTPS access to `api.github.com`. Its owned transport ignores
`HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`, and refuses redirects. Environments that require an outbound
proxy are not currently supported for this operation.

The candidate transport uses `schemaVersion: nvkr.dev/qualification-candidate/v1`, a digest-pinned root
`reference`, and `predicates`, each containing its exact subject `reference` and original canonical predicate
bytes in the JSON base64 `predicate` field. It includes one predicate for the root and every named output.
It is a bounded internal handoff, not an independently authored lifecycle input or proof of success.
Unknown fields, duplicate JSON keys, unsupported versions and noncanonical encodings are rejected. The
current command consumes this saved handoff; it does not manufacture missing build evidence.

The lifecycle file owner recompiles the exact supplied inputs and requires the original Plan identity to
match. `qualificationorigin.GitHub.AuthenticateSource` separately authenticates a completed successful
GitHub Run attempt, exact commit, expected workflow, intended repository and its actual default branch.
The module-level `lifecycle.Consumer.CheckQualification` then authenticates the publication and original
predicates. Descriptions and candidate files remain untrusted until their exact bytes, subjects, signatures,
provenance and Plan bindings verify.

At the consumer API boundary, source and Plan validation precede adapter-capability checks. A request with
both malformed inputs and unavailable adapters reports the input error first. Neither case reaches registry
or signature-verification I/O.

Every selected output is checked for schema/membership, exact Run and inputs, signed evidence, build
signature/provenance, applicable payload pins and embedded declarations. Failed checks return coded
diagnostics; only a complete passing set produces an opaque `ArtifactCheckReceipt`. Its description is
defensive data and grants no authority. Callers close the receipt after use; closure revokes all shallow
copies. The direct consumer method borrows the caller's source Run receipt, whose closure also revokes the
check receipt. The file adapter creates and transfers its source owner to the returned check receipt, so
closing that receipt also closes its internally created source.
Payload digests remain distinct from OCI delivery-container subjects, and disk results explicitly retain
producer-only scope without content inspection.

An incomplete evidence traversal is labeled as a failure of the complete publication in every output's
signed-evidence diagnostic. Output-specific signature failures remain visible alongside that global failure;
no output receives a passing evidence verdict merely because its own signature check did not fail.

Qualification requires the root and every output predicate to name that exact source attempt. When output
metadata records publication origin, both preparation and signing invocations must also match. Earlier-attempt
publication reuse remains available to the existing publication retry protocol, but cannot satisfy this MVP
qualification gate. Earlier bytes are never relabeled as produced by the current attempt. The CLI writes
check diagnostics and returns the stable failure code; its serialized output cannot restore a live receipt
or grant qualification signing authority.

Wrapped source producers require the [pinned embedded declaration](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/0010-pluggable-builder-types.md)
profile. Current M2 candidate catalogs have not supplied those declarations and therefore cannot pass
the gate. M3-B's authenticated smoke receipt is not available, so required delivered-tool smoke blocks
qualification signing. An A receipt alone cannot sign evidence or authorize release. The C publisher's
contract and local tests do not establish real hosted signing or complete-set consumer acceptance.
[Reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md#m3-mvp-scope-and-acceptance-boundary) records contract coverage and
the outstanding hosted signing, exact-input candidate and consumer acceptance evidence.

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
The direct-output model uses Definition v4, catalog v3 and generated IR v4. Run and Release use v3. Older recipe layouts and cluster/placement fields
are rejected before work. Current migration checks and maintained-input build evidence are tracked in reconciliation;
historical Runs retain the authoring and wire contracts exercised at the time.
