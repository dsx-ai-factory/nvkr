# Combined NKE pipeline

One maintained Definition builds the NKE CLI, complete upstream CWE runtime image and quickstart Helm bundle
from NKE commit `ed9fb09b04fd40bf331d25df2222206ffaca593e`. The generated workflow observes all three outputs,
publishes them to GHCR, signs and verifies their publication envelopes, and must finalize a complete Run.

## Implementation

`definitions/nke-v1/definition.yaml` selects two catalog Recipes: `nke-cli` produces `nkectl`, and
`nke-control-plane` produces `cwe` plus `nke-helm`. Environment inputs select Docker, publication repositories
and signing. The seven public objects and Definition → Run → Release lifecycle are unchanged.

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

The Helm producer archives the complete pinned `generated/aicr/quickstart/helm` tree with sorted names, fixed
timestamps and numeric ownership. Its 48 files contain three local charts and versioned external chart
references; external charts are not vendored. The Definition declares `imageOutput: cwe` and
`chart: 007-controller`, creating an explicit package dependency. Packaging binds the chart's unique controller
Deployment to the verified native image manifest, removes the development tag/default repository and refreshes
all 47 checksum entries. Missing, ambiguous or already-templated Deployment inputs fail closed.

Consumers supply the verified CWE repository as `image.repository`. The manifest digest is embedded in the
Deployment template and cannot be replaced through `image.digest`. Rendering without a repository fails.
See the [Definition README](../definitions/nke-v1/README.md) for generation and consumption commands.

Generated Docker utility jobs use the runner's UID/GID so private package directories remain readable by later
host transport steps. Each source fetch transports its exact receipt file, never the shared receipt directory.
This preserves no-replace restoration when independent fetch jobs join at finalization. Receipt authentication
remains owned by the pipeline module.

## Validation

The full local `make check` gate passed after the receipt fix: formatting, vet, golangci-lint, shellcheck and
race-enabled tests, with 64.2% statement coverage. Linux pipeline/sign race checks, actionlint, yamllint and
byte-exact generation checks passed. The corrected graph's 35 artifact paths have no exact or ancestor overlap.

Contract tests cover native-image byte/platform/diff-ID validation, unsafe archives, substituted producer
results, source Helm cardinality, image dependency lowering, Deployment ambiguity and checksum regeneration.
A transport regression restores two independent source receipts into the same consumer workspace.

The real-input Helm test rendered all three local charts, rejected a missing repository and proved that a
values digest override cannot replace the image binding. `TestPinnedNKEHelmBinding` is opt-in through
`NKD_HELM_ARCHIVE` and `NKD_CWE_IMAGE_LAYOUT`; a skipped invocation does not prove external acceptance.

## Hosted attempts

| Run | Result and correction |
|---|---|
| [34430414883](https://github.com/njhensley/nke-recipes/actions/runs/34430414883), `d61ce88` | CLI and Helm source builds passed; CWE rejected the default Podman runtime. Both maintained Environments now select Docker, and graph admission rejects incompatible settings. The [failed Run record](evidence/2026-09-10-combined-first-failed-run.json) retains all three required outputs. |
| [34430831748](https://github.com/njhensley/nke-recipes/actions/runs/34430831748), `16bbddb` | All three builds passed; CWE took 8m19s. Root-owned private image directories blocked host archive handoff. Generated Docker utilities now use runner ownership. A real Linux-volume experiment reproduced the failure and verified the correction. |
| [34431910080](https://github.com/njhensley/nke-recipes/actions/runs/34431910080), `00656b2` | All builds, six package jobs, publication, signing and hosted verification passed. Independent consumers passed for all three repositories. Finalization failed because two source fetches restored the same receipt directory. Exact receipt-file carriers correct that collision. |
| [34433727700](https://github.com/njhensley/nke-recipes/actions/runs/34433727700), `3c2afb2` | CWE exhausted runner disk space at the Go linker under the forced native snapshotter. The launcher now requires overlayfs; the receipt fix remains in place. No publication succeeded in this attempt. |

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
The miniature storage test does not prove that the complete upstream CWE build fits a hosted runner; the next
full hosted attempt must establish that separately.

## Acceptance boundary

Completion requires a successful combined hosted Run and independent consumption of that attempt's three
published outputs. Earlier CLI-only runs and successful partial attempts are insufficient. No Kubernetes
deployment, database-backed CWE service startup, external chart availability or qualified reproduction is
claimed. The OS-image build continues to own the complete worker payload; individual worker packages are
outside this pipeline change.
