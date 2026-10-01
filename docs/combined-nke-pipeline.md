# Combined NKE pipeline

One maintained Definition builds the NKE CLI, complete upstream CWE runtime image and controller Helm chart
from independently pinned NKE CLI and controller tags. The generated workflow observes all three outputs,
publishes one release index to GHCR, signs and verifies that entry point and each deliverable manifest, and finalizes a Run only after its
required checks pass. The [single-entry layout](publication-architecture.md) has been rehearsed locally with
actual NVIDIA payloads; its new hosted signing/finalization path still needs an authorized run.
The [successful hosted run and independent consumption](evidence/2026-09-10-combined-run.md) establish this
combined path at the `pipeline-attested` / `artifact-publication` scope for those recorded personal-namespace
inputs. It predates the current NVIDIA migration and catalog/tool/action changes; it is not evidence of a new
hosted Run from the current checkout.

## Implementation

`definitions/nke-v1/definition.yaml` selects `nkectl`, `cwe`, and `nke-helm` directly on their named outputs.
Each output has a compiler-derived build graph; the Helm package explicitly depends on the image output. Environment inputs select Docker, one publication namespace
and signing. The six public objects and Definition → Run → Release lifecycle are unchanged. Definition v4 contains no
cluster views, deployment roles or categories. The generated Run still freezes all required outputs and binds
each exact named output, build identity, observed bytes and evidence into the publication content subject.

The CLI and source-chart archive build offline with the pinned Go toolchain. CWE wraps the upstream
`controller/Dockerfile` using rootless BuildKit v0.33.0, pinned to
`moby/buildkit@sha256:80b15f0735e87bab7bf59ec4d695dfb4a7cfb25521cf56dc75d6f256285b63ef`.
Its explicit network access supports base images and Alpine packages. The rootless launcher requires overlayfs
to avoid full-tree snapshot copies; unsupported kernels fail explicitly. This is the `pipeline-attested` wrapped
build lane; it does not claim hermeticity or exact image reproduction. Unsupported runtimes fail admission.

The delivery module imports the exact observed OCI archive through bounded, safe extraction. It verifies the
single-platform native image index, manifest, config, compressed layers and uncompressed layer identities,
rejecting platform mismatches, ambiguity and unreachable blobs. Lifecycle observation independently reopens
the producer archive. Publication preserves the runnable image manifest and consumer checks bind its platform.

The maintained Helm producer archives `controller/deploy/charts/controller` at `controller/v0.1.1-rc.9`,
with sorted names, fixed timestamps and numeric ownership. `helm.images: {controller: cwe}` creates an
explicit package dependency. Packaging writes the verified image manifest digest to `image.digest` and
refreshes checksums; templates and other values remain unchanged. Operators supply the image repository
at deployment. Helm digest overrides remain possible. Historical runs below used the older quickstart
wrapper and template-rewriting behavior; they do not validate these updated inputs.
See the [Definition README](../definitions/nke-v1/README.md) for generation and consumption commands.

Release metadata references the CLI, native CWE and Helm manifests in their own repositories. The release
index contains only metadata, referencing four stage-bound SBOM attestation bundles in the artifact repositories. One metadata document owns names, digests and evidence membership; there is no nested Helm root or
separate lifecycle-binding object. Source build indexes remain internal job handoffs. The final Helm inventory
is generated after binding and is distinct from the source-archive inventory. The SBOM-attestation stage verifies the bundles before publication; the root signature covers their exact
identities and required membership through metadata; it does not sign the subsequent Run completion record.

Generated Docker utility jobs use the runner's UID/GID so private package directories remain readable by later
host transport steps. Each source fetch transports its exact receipt file, never the shared receipt directory.
This preserves no-replace restoration when independent fetch jobs join at finalization. Receipt authentication
remains owned by the pipeline module.

## Validation

The original receipt-fix validation recorded the full local `make check` gate: formatting, vet, golangci-lint, shellcheck and
race-enabled tests, with 64.2% statement coverage. Linux pipeline/sign race checks, actionlint, yamllint and
byte-exact generation checks passed. The corrected graph's 35 artifact paths have no exact or ancestor overlap.

Current single-entry checks and real-output results are in the [publication validation summary](publication-architecture.md#local-validation).

Contract tests cover native-image byte/platform/diff-ID validation, unsafe archives, substituted producer
results, source Helm cardinality, image dependency lowering, value-path ambiguity and checksum regeneration.
A transport regression restores two independent source receipts into the same consumer workspace.

Real Helm rendering of the source controller chart and template preservation are recorded in the
[values-binding validation](evidence/2026-10-01-declarative-helm-bindings.md). A new complete build and
hosted publication of the updated inputs remain unexercised.

## Hosted attempts

| Run | Result and correction |
|---|---|
| [34430414883](https://github.com/njhensley/nke-recipes/actions/runs/34430414883), `d61ce88` | CLI and Helm source builds passed; CWE rejected the default Podman runtime. Both maintained Environments now select Docker, and graph admission rejects incompatible settings. The [failed Run record](evidence/2026-09-10-combined-first-failed-run.json) retains all three required outputs. |
| [34430831748](https://github.com/njhensley/nke-recipes/actions/runs/34430831748), `16bbddb` | All three builds passed; CWE took 8m19s. Root-owned private image directories blocked host archive handoff. Generated Docker utilities now use runner ownership. A real Linux-volume experiment reproduced the failure and verified the correction. |
| [34431910080](https://github.com/njhensley/nke-recipes/actions/runs/34431910080), `00656b2` | All builds, six package jobs, publication, signing and hosted verification passed. Independent consumers passed for all three repositories. Finalization failed because two source fetches restored the same receipt directory. Exact receipt-file carriers correct that collision. |
| [34433727700](https://github.com/njhensley/nke-recipes/actions/runs/34433727700), `3c2afb2` | CWE exhausted runner disk space at the Go linker under the forced native snapshotter. The launcher now requires overlayfs; the receipt fix remains in place. No publication succeeded in this attempt. |
| [34434756868](https://github.com/njhensley/nke-recipes/actions/runs/34434756868), `4557f64` | All 20 jobs passed, including all three builds, publication, real signing, verification and complete Run finalization. Independent cosign/NKD consumers and downloaded CLI/image/Helm checks passed. |

An offline replay of the second attempt's successful producer carriers also passed all six package/archive
handoffs. It verifies the packaging path, not new hosted execution or signatures.

The third attempt published the common envelope digest
`sha256:77b8f4f754820a0f59901e5f69a1ad1d49322f6d744cd9116ceb057ebf8e3504` to the `cwe`, `helm` and `nkectl`
repositories under `ghcr.io/njhensley/nke-recipes`. Independent cosign signature and production-attestation
checks and the public NKD `pipeline-attested` / `artifact-publication` consumer passed for all three references.
Its native CWE manifest
`sha256:38b30b65d4e8c333d9823f3376f9d623846c0474837e62182d973e4c8b7adecf` passed offline executable,
SPIFFE helper, UI, profiles, license and CA checks, reporting `cloud-workflow-engine 0.0.0-ged9fb09b04fd`.
The extracted CLI reported `nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)`.
The downloaded Helm tree passed all checksums and rendered all three local charts with that exact CWE digest.
These successful publication checks do not change the third attempt's failed Run status.

## Hosted storage regression

The fourth attempt failed with `mapping output file failed: no space left on device`. The forced native
snapshotter copied the large source/toolchain tree across Dockerfile steps. A real Docker regression using
the launcher's exact settings and a 16 MiB repeated-copy fixture failed a 64 MiB storage budget at 164,436 KiB.
After switching only the snapshotter to overlayfs, it passed at 33,256 KiB. The test runs in about two seconds:

```sh
NKD_BUILDKIT_TEST_IMAGE=moby/buildkit@sha256:80b15f0735e87bab7bf59ec4d695dfb4a7cfb25521cf56dc75d6f256285b63ef \
  go test ./pkg/nkd/pipeline -run TestBuildKitSnapshotStorageBudget -count=1 -v
```

The [pinned BuildKit rootless documentation](https://github.com/moby/buildkit/blob/v0.33.0/docs/rootless.md)
requires Linux 5.11+ or an Ubuntu kernel for unprivileged overlayfs. The launcher does not add privileged mode,
a host socket, credentials or a FUSE device. Unsupported hosts fail instead of silently using native storage.
The miniature storage test isolates the storage regression. The subsequent full hosted attempt separately
passed the complete CWE build in 4m40s and finalized the combined Run.

## Acceptance boundary

The completed combined Run and all three independent consumers are recorded in the
[acceptance evidence](evidence/2026-09-10-combined-run.md), with exact Run and Release descriptions. No Kubernetes
deployment, database-backed CWE service startup, external chart availability or qualified reproduction is
claimed. The OS-image build continues to own the complete worker payload; individual worker packages are
outside this pipeline change.
