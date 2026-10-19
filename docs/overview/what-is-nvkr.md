# What is NVKR

> **Preview:** NVKR is available as a preview. Commands, input schemas and behavior can change between releases, and we do not recommend NVKR for production use. See [Preview status and known limitations](preview-status.md).

NVIDIA Kubernetes® Recipes (NVKR) is open-source software that turns pinned source into traceable software releases for NVIDIA Kubernetes Engine (NVKE). You declare the outputs you want to ship, and NVKR resolves their pinned inputs, builds or acquires each artifact, and connects each delivered artifact to its inputs and evidence. Its CLI is `nvkr`.

NVKR builds NVKE's CLI, container images, Helm charts, operator tools and node OS images. You can run the build on your own host, or generate a GitHub Actions workflow that builds, signs and publishes a Release to registries you control. Only a Release from the hosted pipeline is signed and carries build provenance; a local Run is unsigned. NVKE and other downstream systems install and deploy the artifacts. NVKR is licensed under Apache 2.0.

## Why NVKR

To deliver software you can audit, you need to know which inputs produced each artifact and to check that claim yourself. NVKR provides these features:

- **Declarative outputs.** A Definition names each output and selects the catalog components, platform and format that produce it. NVKR derives the build graph and the pipeline.
- **Pinned inputs.** Catalogs pin every source commit, artifact and tool. Overlays replace a producer or substitute a toolchain for a chosen scope without editing the catalog.
- **Reviewable plans.** Before anything runs, you inspect the generated Plan, visualize producer and artifact graphs, and compare two Plans.
- **Evidence per output.** For each output the Definition requires, a Run records the artifact it observed, the evidence and any failure. One successful output never stands in for a missing sibling.
- **Releases you verify under your own policy.** One digest-pinned OCI reference connects the published outputs to their SBOMs and provenance. `nvkr release verify` checks a Release against your Verification Policy: the signer identity, issuer and evidence you accept.

## Who NVKR is for

| You are | You use NVKR to | Start with |
|---|---|---|
| **An AI factory operator** who runs NVKE | Build the node images and NVKE components you deploy, publish them to your registries, and verify each Release before it reaches a cluster. | [Quickstart](../quickstart.md) |
| **A release engineer** | Change what a Definition selects, repin catalogs, and generate the pipeline that builds and publishes it. | [Catalogs and components](../catalogs-and-components.md) |
| **A consumer of a published Release** | Browse its outputs and evidence, and verify a supported claim against your own policy. | [Browse, export, pull and verify a release](../release-quickstart.md) |

## Key terms

- **Definition.** Your declaration of the named outputs to produce, their catalog producers, target platforms and packaging.
- **Environment.** The execution settings, registry namespace, credential references and signing identity policy for a Run. You select it separately from the Definition.
- **Run.** One execution attempt against pinned inputs, for the outputs the Definition requires. It records progress, observed artifacts, evidence and failures.
- **Artifact.** An immutable file or image produced or acquired for delivery, such as an executable, container image, Helm chart or node image.
- **Release.** An immutable selection of artifacts and evidence, accessible through one pinned reference, with an explicit evidence lane.
- **Verification Policy.** Your requirements for accepting a Release: the identities, issuer and evidence you trust. You write it; NVKR checks a Release against it.
- **Evidence lane.** The kind of claim a result supports. A dry-run is `static-unqualified`, a local Run is `unqualified-local-execution`, and a signed Release from the hosted pipeline is `pipeline-attested`. A fourth lane, `qualified`, is not available at preview.

[Concepts](../user-concepts.md) defines every term NVKR uses.

## How NVKR works

The lifecycle is **Definition → Run → Release**:

1. **Define the deliverables.** Catalogs describe reusable component producers and their pinned sources, artifacts and tools. A Definition selects components for each named output. An Environment supplies execution, registry and signing settings.
2. **Review and run.** NVKR resolves the pinned inputs into a Plan. You run it locally or generate a GitHub Actions workflow from the same inputs.
3. **Publish and verify.** The hosted pipeline signs the published artifacts, their attestations and the release root. Consumers verify those claims under their own policy.

| Stage | What happens | Read more |
|---|---|---|
| **Define** | You select components and pins, and apply overlays. | [Catalogs and components](../catalogs-and-components.md) |
| **Plan** | NVKR generates the Plan and graphs for review. | [Plan and graph inspection](../plan-and-graph-inspection.md) |
| **Run** | You build locally, or generate and run the GitHub workflow. | [Generate a pipeline](../generate-pipeline.md) |
| **Release** | You browse, export, pull and verify the published outputs. | [Browse, export, pull and verify a release](../release-quickstart.md) |

## What NVKR does not do

- **Install or deploy software.** NVKE and other downstream systems own installation, deployment topology and node placement.
- **Claim ownership of what it signs.** A signature identifies the signer of a production or publication claim, not the owner of the software or its components.
- **Prove more than a claim states.** A valid signature authenticates a claim. It does not establish complete inspection, freedom from vulnerabilities, independent reproduction, qualification or deployment readiness.

## NVKR and NVKE

NVKE is the Kubernetes management system that runs the artifacts NVKR builds. NVKR publishes them as Releases; you verify each Release and give it to the NVKE install. See the [NVKE documentation](https://github.com/dsx-ai-factory/nvke) for installing and operating NVKE.

## Next steps

- **Build your first artifact:** [Quickstart](../quickstart.md).
- **Let your agent drive NVKR:** [Use NVKR with your AI agent](../use-with-agents.md).
- **Check what preview supports:** [Preview status and known limitations](preview-status.md).

## Trademarks

Kubernetes® is a registered trademark of The Linux Foundation in the United States and other countries. NVIDIA and the NVIDIA logo are trademarks and/or registered trademarks of NVIDIA Corporation in the U.S. and other countries. Other company and product names may be trademarks of the respective companies with which they are associated.

The Linux Foundation has registered trademarks and uses trademarks. For a list of trademarks of The Linux Foundation, please see its [Trademark Usage page](https://www.linuxfoundation.org/legal/trademark-usage).
