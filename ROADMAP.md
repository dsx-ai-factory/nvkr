# NVKR roadmap

This is what the NVIDIA Kubernetes Recipes (NVKR) team is building next. NVKR
builds the node OS images, packages and components that NVKE runs.

The roadmap shows direction, not commitments. Items can move between stages,
change scope or come off the list as we learn more. We don't publish dates.
The linked issues carry the current roadmap stages; the
[implementation and evidence ledger](docs/adr/reconciliation/README.md) records
what has been implemented and exercised.

## Merged implementation

Merged code, contract tests and operational acceptance are separate milestones.

| Item | Current scope and remaining work |
|---|---|
| [Composite Definitions](https://github.com/dsx-ai-factory/nvkr/pull/272) | Combine pinned member Definitions into one Run and Release. The [implemented scope](docs/adr/reconciliation/adr-26.md) has contract coverage; hosted publication and independent consumer verification remain part of the GPU-site work below. Composite qualification and member Release reuse are deferred. |
| [AICR deployment bundles](https://github.com/dsx-ai-factory/nvkr/pull/239) | Build bundles for GB300 and GB200 NVKE selections. The [recorded local runs](docs/adr/reconciliation/aicr-deployment-bundles.md) do not establish hosted delivery or hardware acceptance. Moving to pinned release tools and preserving AICR's manifest remain below. |
| [S3 artifact delivery](https://github.com/dsx-ai-factory/nvkr/pull/341) | Copy complete signed artifact sets and their original evidence to required S3 destinations. The [implementation boundary](docs/adr/reconciliation/object-store-publication.md) has contract coverage; maintained configuration and live provider/consumer acceptance remain outstanding. |

S3 follow-ups cover [maintained workflow configuration](https://github.com/dsx-ai-factory/nvkr/issues/384),
[live publication and independent consumption](https://github.com/dsx-ai-factory/nvkr/issues/383),
and [CLI/SDK verification](https://github.com/dsx-ai-factory/nvkr/issues/385).
These issues do not yet have roadmap stage labels.

## Planned work

| Stage | What it means |
|---|---|
| **Coming soon** | Being built now. Expect it in an upcoming release. |
| **Working on it** | Funded and in design or early development. |
| **Exploring** | On our list, but not yet scheduled. Your feedback shapes what happens next. |

### Coming soon

| Item | What it delivers |
|---|---|
| [One composite Release for the NVKE GPU site](https://github.com/dsx-ai-factory/nvkr/issues/326) | Assemble the family Definitions and prove hosted composite publication and independent consumer verification. |
| [Controller profiles and an offline CAPI repository](https://github.com/dsx-ai-factory/nvkr/issues/324) | Include the controller setup artifacts in the NVKE core Release. |

### Working on it

| Item | What it delivers |
|---|---|
| [Hosted release proof](https://github.com/dsx-ai-factory/nvkr/issues/367) | Record family Releases, independent consumer verification and Ubuntu 24.04 disk boot evidence. |
| [Hosted E2E evidence](https://github.com/dsx-ai-factory/nvkr/issues/353) | Record the [passing scheduled main Run](https://github.com/dsx-ai-factory/nvkr/actions/runs/41766922589) in reconciliation. Its unsigned E2E result does not establish signed Release or qualification acceptance. |
| [AICR bundles from pinned release tools](https://github.com/dsx-ai-factory/nvkr/issues/304) | Replace source-built renderers with pinned executables and publish AICR's manifest unchanged for the GB300 and GB200 NVKE bundles. |

### Exploring

| Item | What it delivers |
|---|---|
| [Signed revocation markers](https://github.com/dsx-ai-factory/nvkr/issues/359) | Let consumer verification reject revoked releases. |
| [Movable release channels](https://github.com/dsx-ai-factory/nvkr/issues/360) | Point a channel at an accepted release. |
| [One-command node-image verification](https://github.com/dsx-ai-factory/nvkr/issues/363) | Check a node image and its signed evidence before use. |
| [Host packages in node images](https://github.com/dsx-ai-factory/nvkr/issues/365) | Sign and publish Debian packages and place one in a built OS image. |
| [Installed-package SBOMs](https://github.com/dsx-ai-factory/nvkr/issues/362) | List what the finished disk contains, not only who produced it. |

## Tell us what you need

Open a [feature request](https://github.com/dsx-ai-factory/nvkr/issues/new?template=feature_request.yml).

For the management plane, clusters and nvkectl, see the
[NVKE roadmap](https://github.com/dsx-ai-factory/nvke/blob/main/ROADMAP.md).
