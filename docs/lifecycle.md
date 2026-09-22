# Definition, Run, and Release workflow

The public model has six objects: **Definition, Environment, Run, Artifact, Release, and Verification
Policy**. [ADR-17](adr/0017-cli-sdk-foundation-and-package-topology.md) defines their contract. The
[implementation record](adr/reconciliation.md) records contract verification,
separately from external operational acceptance.

## Declare outputs

A Definition declares named outputs. Each output selects exact catalog components and a concrete target
platform. Catalog components own reusable build or acquisition
instructions. The compiler derives a closed, pinned build graph for each output and derives its packaging,
publication route, and evidence requirements from the output and selected Environment.

The Build Digest identifies that generated graph's logical content. Output labels and
execution settings remain in their owning records. Two outputs can share a Build Digest while requiring
separate producer jobs, observations and evidence; one successful output cannot satisfy a missing sibling.

The [NKE Definition](../definitions/nke-v1/definition.yaml) directly selects `nkectl`, `cwe`, and `nke-helm`
for three outputs. The Helm output explicitly depends on the CWE image output. No reusable membership group,
criteria query, inheritance chain, or mixin sits between an output and its producers.

Deployment topology and placement are owned by the system that consumes the artifacts;
Definitions and Releases contain no cluster views, deployment roles or categories.

## Select an Environment

The [local Environment](../definitions/nke-v1/environment-local.yaml) supplies execution settings and a local
registry destination. The [GitHub Environment](../definitions/nke-v1/environment-github.yaml) supplies the
corresponding hosted-run settings. Environments own runner routes and may declare named execution classes with
host platform, resource capacity, exposed capabilities, and timeout ceilings. The compiler deterministically
binds a requested build to one satisfying class and records that exact route in the Plan. A class declaration is
planning input, not proof that a runtime supplied the declared capacity or capability; current execution adapters
still reject selected builds with requirements until they can authenticate and enforce the selected binding.

Environments also own the publication namespace, credentials, and signing identity policy. Catalog sources and
locks own tool versions and byte pins; Definition selects the scanner, signer, and generators by name.
Output-affecting build tools remain part of build identity.

A destination or signing-setting change can change the prepared execution and generated workflow while
preserving build identity. A dry-run signing mode records intended operations and never supplies a signature.

## Inspect and generate

The new command surface starts with these read-only operations:

```sh
go run ./cmd/nkdctl definition validate \
  --definition definitions/nke-v1/definition.yaml

go run ./cmd/nkdctl run plan \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-local.yaml
```

Generation writes provider files and their commit manifest from the same admitted inputs. The matching
`--check` invocation regenerates in memory and fails if committed files differ:

```sh
go run ./cmd/nkdctl run generate \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml \
  --engine github --root .

go run ./cmd/nkdctl run generate \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml \
  --engine github --root . --check
```

Generated CI uses generic hosted runners. Publication and signing
belong to the GitHub v1 lane. Generating a workflow does not prove that a provider ran it.

## Run and verify

Inspect a local attempt without creating its workspace or executing nodes:

```sh
go run ./cmd/nkdctl run local \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-local.yaml \
  --attempt review-001 --dry-run
```

The dry-run remains pending with no observed outputs. Actual execution uses an explicit attempt name under the
selected attempts root; the default root is `.nkd/runs`. The attempts root must be a clean repository-relative
path under `.nkd`. Publication is a separately enabled operation. The
implementation record distinguishes contract tests from external execution.

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

The CLI expresses build-evidence acceptance as `--scope artifact-publication`. On Linux, with a pinned cosign
executable and an existing signed publication in a public registry, the consumer invocation is:

```sh
bin/nkdctl release verify \
  --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope artifact-publication \
  --identity "$BUILD_IDENTITY_REGEXP" --issuer "$OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST" \
  --anonymous
```

`RELEASE_REF` is a registry reference pinned with `@sha256:…`; the identity and issuer belong to the
consumer's trusted build policy. Signature and production-attestation verification are both required by this
scope. The CLI prints the verified description; the SDK retains a live, policy-bound capability until closed.
The stock verifier remains Linux-only, and unit tests do not establish live signing-service
acceptance. Full v1 release scope (`--scope release`) and qualified acceptance remain capability-gated.

The scoped consumer and exact output/content bindings have positive and negative contracts. Full v1
release acceptance still requires its deferred qualification, membership, and revocation adapters; the scoped
result cannot substitute for them. The ADR-04 stock-cosign consumer contract remains independent of the
convenience NKD reader.

## Use the SDK

Configure the client with tool and registry settings once, then pass a reference and policy for each Release.
The accepted identity and issuer belong only to the policy. This example uses a public registry:

```go
package consumer

import (
	"context"

	client "github.com/NVIDIA/nke-recipes/client/v1"
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
