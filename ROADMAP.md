# NVKR roadmap

This is what the NVIDIA Kubernetes Recipes (NVKR) team is building next. NVKR
builds the node OS images, packages and components that NVKE runs.

The roadmap shows direction, not commitments. Items can move between stages,
change scope or come off the list as we learn more. We don't publish dates.

| Stage | What it means |
|---|---|
| **Coming soon** | Being built now. Expect it in an upcoming release. |
| **Working on it** | Funded and in design or early development. |
| **Exploring** | On our list, but not yet scheduled. Your feedback shapes what happens next. |
| **Not yet staged** | Tracked work whose issues have not been assigned a roadmap stage. |

## Coming soon

| Item | What it delivers |
|---|---|
| [One composite Release for the NVKE GPU site](https://github.com/dsx-ai-factory/nvkr/issues/326) | Assemble the family Definitions and prove hosted composite publication and independent consumer verification. |
| [Controller profiles and an offline CAPI repository](https://github.com/dsx-ai-factory/nvkr/issues/324) | Include the controller setup artifacts in the NVKE core Release. |

## Working on it

| Item | What it delivers |
|---|---|
| [Hosted release proof](https://github.com/dsx-ai-factory/nvkr/issues/367) | Record family Releases, independent consumer verification and Ubuntu 24.04 disk boot evidence. |
| [Hosted E2E acceptance record](https://github.com/dsx-ai-factory/nvkr/issues/353) | Document scheduled main-branch end-to-end validation and its acceptance scope. |
| [AICR bundles from pinned release tools](https://github.com/dsx-ai-factory/nvkr/issues/304) | Replace source-built renderers with pinned executables and publish AICR's manifest unchanged for the GB300 and GB200 NVKE bundles. |

## Exploring

| Item | What it delivers |
|---|---|
| [Signed revocation markers](https://github.com/dsx-ai-factory/nvkr/issues/359) | Let consumer verification reject revoked releases. |
| [Movable release channels](https://github.com/dsx-ai-factory/nvkr/issues/360) | Point a channel at an accepted release. |
| [One-command node-image verification](https://github.com/dsx-ai-factory/nvkr/issues/363) | Check a node image and its signed evidence before use. |
| [Host packages in node images](https://github.com/dsx-ai-factory/nvkr/issues/365) | Sign and publish Debian packages and place one in a built OS image. |
| [Installed-package SBOMs](https://github.com/dsx-ai-factory/nvkr/issues/362) | List what the finished disk contains, not only who produced it. |

## Not yet staged

| Item | What it delivers |
|---|---|
| [S3 delivery in maintained workflows](https://github.com/dsx-ai-factory/nvkr/issues/384) | Configure an approved S3 destination for the complete selected artifact set. |
| [S3 delivery acceptance](https://github.com/dsx-ai-factory/nvkr/issues/383) | Prove complete hosted publication and independent consumption from S3. |
| [S3 verification through the CLI and SDK](https://github.com/dsx-ai-factory/nvkr/issues/385) | Let consumers authenticate an S3 publication through supported CLI and SDK entry points. |

## Tell us what you need

Open a [feature request](https://github.com/dsx-ai-factory/nvkr/issues/new?template=feature_request.yml).

For the management plane, clusters and nvkectl, see the
[NVKE roadmap](https://github.com/dsx-ai-factory/nvke/blob/main/ROADMAP.md).
