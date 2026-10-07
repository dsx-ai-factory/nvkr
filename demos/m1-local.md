# Local NKE walkthrough and M1 evidence

The script name is historical: `demo-m1-local.sh` now runs the combined NKE Definition, producing the NKE
CLI, CWE runtime image, and quickstart Helm bundle. **M1 itself requires only the CLI vertical slice.** The
script builds all three outputs, publishes one release index, then extracts and consumes its CLI artifact. It does not
independently pull or smoke-test the CWE image or render the Helm charts.

Plans are `static-unqualified`; executed local Run records use `unqualified-local-execution`. Dry-run signing executes no cosign and produces no authenticated
signature or attestation. The CLI build is offline; the CWE wrapped build uses network access. Calling the
whole journey hermetic, signed, or M1-complete would be incorrect.

Real hosted signing and independent consumption were [recorded for the combined outputs](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-09-10-combined-run.md)
in September. The [producer observations](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#local-rendering-and-packaging)
retain the earlier local execution's CLI pull-back and version checks. These dated inputs do not establish
this checkout's combined local journey or transfer acceptance to newer wire schemas.

## Prepare and run

Requires the repository's Go toolchain, Docker, Git, curl, an NKE clone containing both pinned module commits,
and network access for pinned tool downloads, container acquisition, and the CWE build's upstream dependencies.
The Docker Linux VM must support rootless BuildKit with overlayfs; the CWE build needs substantially more
disk, memory and time than the CLI alone. Docker must support execution of linux/amd64 images; an arm64 laptop uses
emulation. The default registry binds loopback port 5000. Its `registry:3` service image is tag-selected demo
infrastructure, not a digest-pinned build input.

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
| Check hosted generation | Fresh scratch bundle and matching drift check | The selected example regenerates exactly; the committed M2 workflow is untouched and no provider job runs |
| Execute | `lifecycle.json` in the new attempt | All three required outputs must complete; observations and failures are recorded |
| Inspect SBOM | CLI output `nkectl/sbom/index.json` and component documents | The CLI bill of materials names the observed binary digest and includes configured dependency inspection |
| Inspect signing | `signatures/`, `predicates/`, `verifications/` | Dry-run records name the published digest and exact predicate bytes; signature verification remains false |
| Pull by digest | Published reference and extracted `nkectl` | Registry bytes round-trip and the recovered binary digest matches the SBOM subject |
| Execute recovered binary | Version output in the pinned amd64 container | The published package contains a runnable NKE CLI with the expected upstream version |

The script prints the actual reference, digests, paths and timings. Use those values during a presentation;
historical node counts, workspace names and digests are not stable expectations for the current model.

Inspect [the NKE inputs](../definitions/nke-v1/README.md) to show the authorship split: Definition owns the
output selection and packaging; Environment owns runtime, publication namespace, credentials and signing identity. The catalog owns source,
tool and action pins; the Definition selects tools by purpose. Compiler output
contains the package graph and publication envelope, so users do not wire those records manually. There are no
cluster-view, deployment-role or category inputs; output completeness and exact evidence membership remain
mandatory. Deployment topology belongs to the downstream consumer.

## Production acceptance

M1 acceptance includes a green generated hosted Run, real keyless signing and production provenance,
independent consumer verification, and the review/governance requirements in the
[acceptance specification](../docs/epics-v1-spec.md#epic-m1--vertical-slice). Hosted signing and consumption have been
recorded in the personal namespace; they are not still wholly unimplemented. A run under the current NVIDIA
repository and identity is not yet recorded.

The implementation admits default-branch manual dispatch and makes all pull requests generation-only.
The acceptance specification still calls for a default-branch **push** run and same-repository pull-request
builds. Its per-job helper digest negative demonstration is also not established by the recorded run.
These remain acceptance gaps; this documentation audit does not relax them or declare M1 complete.

For other journeys, use the [demo index](README.md): pin changes, tamper rejection and
policy-scoped publication verification each demonstrate a distinct behavior.
