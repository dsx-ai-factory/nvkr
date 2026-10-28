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

## Coming soon

| Item | What it delivers |
|---|---|
| Signed, verifiable releases from the hosted pipeline | Signed release manifests under immutable and channel tags, plus revocation. |

## Working on it

| Item | What it delivers |
|---|---|
| Image provenance validation | Check a node image against its recipe, SBOM, signatures and provenance before use. |
| Add your own host packages to node images | Build Debian packages from source, with an SBOM, into the node image. |

## Exploring

| Item | What it delivers |
|---|---|
| Generate AI runtime charts and manifests with AICR | Render accelerated-runtime charts and manifests from AICR recipes. |
| SBOMs that list what's installed on each node image | List what the finished disk contains, not only who produced it. |

## Tell us what you need

Open a [feature request](https://github.com/dsx-ai-factory/nvkr/issues/new?template=feature_request.yml).

For the management plane, clusters and nvkectl, see the
[NVKE roadmap](https://github.com/dsx-ai-factory/nvke/blob/main/ROADMAP.md).
