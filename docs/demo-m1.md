# Local NKE demo

Run the production NKE Definition through one local attempt: build the pinned NKE CLI, package and publish it
to a loopback OCI registry, inspect its SBOM and dry-run signing records, then pull by digest and execute the
recovered binary. The script uses the current Definition and Environment inputs and preserves each Run attempt.

This is unqualified local execution. Dry-run signing executes no cosign and produces no authenticated signature
or attestation. The [2026-09-08 evidence](evidence/2026-09-08-m1-public-lifecycle-local.md) records a passing
current-model run. The separate [2026-09-04 evidence](evidence/2026-09-04-m1-local.md) preserves the earlier
version. Current checks are listed in
[reconciliation](adr/reconciliation.md#repository-validation).

## Prepare and run

Requires the repository's Go toolchain, Docker, Git, curl, access to the pinned images, and an NKE clone
containing the pinned source. Docker must support execution of linux/amd64 images; an arm64 laptop uses
emulation. The default registry binds loopback port 5000.

From the repository root:

```sh
NKE_LOCAL_CLONE=/path/to/nke scripts/demo-m1-local.sh
```

The clone override changes Git transport for this process only. The committed catalog continues to name the
upstream repository and its exact pins. The script starts or reuses its labeled demo registry. A container with
the same name and a different owner is rejected.

Each execution chooses a new attempt under `.nkd/m1-local/`. To choose the name explicitly:

```sh
NKE_LOCAL_CLONE=/path/to/nke scripts/demo-m1-local.sh --attempt walkthrough-01
```

Reusing an existing attempt is an error. The separate consume directory is disposable scratch space.
`--stop-registry` runs the demo and removes its registry at successful completion; it is not a standalone
cleanup command. See `--help` for the path options.

## Walkthrough

| Step | Inspect | What it establishes |
|---|---|---|
| Build and validate | Definition, catalog pins and local Environment | The current checkout accepts the intended pinned inputs |
| Check hosted generation | Committed GitHub bundle drift check | Hosted workflow bytes match the current inputs; no provider job is run |
| Execute | `lifecycle.json` in the new attempt | Frozen required outputs, one attempt's observations and any failures |
| Inspect SBOM | Recipe `sbom/index.json` and component documents | The bill of materials names the observed binary digest |
| Inspect signing | `signatures/`, `predicates/`, `verifications/` | Dry-run records name the published digest and exact predicate bytes; signature verification remains false |
| Pull by digest | Published reference and extracted `nkectl` | Registry bytes round-trip and the recovered binary digest matches the SBOM subject |
| Execute recovered binary | Version output in the pinned amd64 container | The published package contains a runnable NKE CLI with the expected upstream version |

The script prints the actual reference, digests, paths and timings. Use those values during a presentation;
historical node counts, workspace names and digests are not stable expectations for the current model.

Inspect [the NKE inputs](../definitions/nke-v1/README.md) to show the authorship split: Definition owns the
output selection and packaging; Environment owns execution, destination and signing settings. Compiler output
contains the package graph and publication envelope, so users do not wire those records manually.

## Production acceptance

M1 still requires a green generated GitHub Run with real keyless signing, production attestation and independent
cosign consumption. That exercise also establishes source access, hosted execution, registry interoperability,
identity policy and protected-environment behavior. Contract tests and this local dry run do not establish those
external integrations.

For other journeys, use the [demo index](../scripts/README.md): GPU cluster planning, hosted generation,
pin changes, tamper rejection and policy-scoped Release consumption each demonstrate a distinct behavior.
