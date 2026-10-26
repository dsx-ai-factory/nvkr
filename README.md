# NVIDIA Kubernetes Recipes (NVKR)

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27.3-00ADD8.svg)](.go-version)

**Build NVKE artifacts from pinned inputs and trace releases back to their sources.**
NVKR gives release engineers a CLI, reusable component catalogs and declarative
Definitions for NVIDIA Kubernetes® Engine (NVKE). Select the executables, container
images, Helm charts or node images to produce; NVKR derives the dependency graph
and runs local builds or generates GitHub Actions workflows. The hosted workflows
build, sign and publish releases with evidence consumers can verify against their
own policy.

[NVKE](https://github.com/dsx-ai-factory/nvke) installs and operates clusters.
NVKR owns the build and release workflow that supplies their artifacts.

> **Preview:** NVKR is available as a preview. Commands, input schemas and behavior can change between releases, and we do not recommend NVKR for production use. See [Preview status and known limitations](docs/overview/preview-status.md).

[Try it](#build-your-first-artifact) · [Documentation](docs/README.md) ·
[Contribute](CONTRIBUTING.md) · [Get help](SUPPORT.md)

## Build your first artifact

Build the NVKE CLI, controller container image and Helm chart locally, with dependency SBOMs.
You need **Git 2.38 or later, Bash and running Docker**. The pinned Go compiler is required; the launcher asks before installing it locally if needed. No Make or host Python is required.
Use Docker Desktop on macOS, or Docker on Linux.
NVKR runs on your host; the NVKE CLI and image it builds target Linux.
The launcher checks prerequisites, selects the required Go compiler and builds NVKR from source for your CPU architecture.
Git must already be installed. Supported Docker installation actions and local Go setup are shown for approval before proceeding.

Open a terminal in a directory without an existing `nvkr` folder and copy the whole block:

```sh
git clone https://github.com/dsx-ai-factory/nvkr.git &&
cd nvkr &&
bash scripts/nvkr-quickstart.sh
```

Wait for `status: complete` and `observed outputs: 3`. The block lists the payload files
and prints the full path to the artifacts and SBOMs. The native CLI is `bin/nvkr`.
The launcher runs in a child shell, so a failed build returns an error without changing your SSH shell settings.
Nothing is published or deployed; the local build is unsigned.

The [quickstart](docs/quickstart.md) explains the output files and next steps.
This builds three selected artifacts; OS images belong to the larger NVKE selection.

## From components to releases

The public lifecycle is **Definition → Run → Release**:

1. **Define the deliverables.** A Definition selects named outputs, their
   components, platforms and formats. Catalogs supply pinned sources, artifacts
   and tools; an Environment supplies execution, registry and signing settings.
2. **Inspect and run.** NVKR generates a Plan and dependency graphs you can
   review before execution. Run locally or generate GitHub Actions. Each Run
   records its required outputs, observed artifacts, evidence and failures.
3. **Publish and verify.** A digest-pinned OCI registry reference connects a
   Release's outputs to their SBOMs and provenance. Consumers choose a
   Verification Policy and authenticate the supported claims before using the artifacts.

Start with the [tutorial Definition](docs/examples/nvke-tutorial/README.md),
which selects the NVKE CLI, management-plane image and Helm bundle. The
[combined NVKE candidate](inputs/definitions/nvke/README.md) expands that selection
to container and node images for AMD64 and ARM64. The [input index](inputs/README.md)
explains the available Definitions, catalogs and execution settings.

## Understand the evidence

A signature authenticates who made a statement about an artifact; a digest binds
that statement to specific bytes. Your Verification Policy chooses the accepted
workflow identity and issuer. Signing does not claim ownership of the included
software, and a valid signature alone does not establish deployment readiness.

| Operation | What it establishes |
|---|---|
| Browse a Release | Inspects metadata; does not verify signatures or download artifact payloads. |
| Pull an output | Retrieves and checks that output's payload integrity; does not verify signatures or consumer policy. |
| Verify `authenticity` | Authenticates signatures and evidence under your policy without downloading artifact payloads. |
| Verify `artifact-publication` | Authenticates the publication claim under your policy and checks the referenced artifact payloads. |

Both verification scopes use the `pipeline-attested` lane. Verification requires
Linux, pinned tools and an explicit policy; the [release quickstart](docs/release-quickstart.md)
walks through the commands and Docker-based verification from macOS.

**No NVKR Release is qualified at preview.** Supported verification does not
establish exact-byte reproduction, freedom from vulnerabilities or deployment
readiness. The maintained node-image SBOMs record producer and material subjects;
they do not inspect installed disk contents. See the
[implementation and evidence record](docs/adr/reconciliation.md) for recorded
Runs and the limits of what each establishes.

## Choose your next step

| Task | Guide |
|---|---|
| Select components, pins and overlays | [Catalogs and components](docs/catalogs-and-components.md) |
| Inspect Plans, graphs and changes | [Plan and graph inspection](docs/plan-and-graph-inspection.md) |
| Generate a hosted build and publication workflow | [Pipeline generation](docs/generate-pipeline.md) |
| Build NVKE node images | [Node images](docs/node-images.md) |
| Browse, pull or verify a published Release | [Release quickstart](docs/release-quickstart.md) |
| Work with an AI agent | [Use NVKR with your AI agent](docs/use-with-agents.md) and the [operator skill](.agents/skills/nvkr-operator/SKILL.md) |
| Look up terminology | [Concept reference](docs/user-concepts.md) |

GitHub Actions is the maintained hosted generator and signing/publication provider.
Builds require their declared runtime and source access. Hosted node-image disk
jobs additionally need the explicit native Linux KVM runner routes described in
the [preview limitations](docs/overview/preview-status.md).
The [documentation map](docs/README.md) and [demo index](demos/README.md) connect
all the guides and examples.

## Contributing and support

Contributions, bug reports and documentation improvements are welcome.
[DEVELOPMENT.md](DEVELOPMENT.md) covers setup and `make check`;
[CONTRIBUTING.md](CONTRIBUTING.md) explains contribution and review requirements.

- **Questions:** [GitHub Discussions](https://github.com/dsx-ai-factory/nvkr/discussions/categories/q-a).
- **Chat:** the `#nvke` channel in the DSX AI Factory Slack.
  [Get an invite](https://inviter.co/dsx-ai-factory).
- **Bugs and feature requests:** [GitHub Issues](https://github.com/dsx-ai-factory/nvkr/issues).
- **Security vulnerabilities:** follow [SECURITY.md](SECURITY.md) and report privately.
- **Community:** [Code of Conduct](CODE_OF_CONDUCT.md), [Governance](GOVERNANCE.md),
  [Maintainers](MAINTAINERS.md) and [Support](SUPPORT.md).
- **Source releases:** [Release process](RELEASE.md), including approval and recovery.

## License

NVKR is licensed under the Apache License 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE),
and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Trademarks

Kubernetes® is a registered trademark of The Linux Foundation in the United States and other countries.
NVIDIA and the NVIDIA logo are trademarks and/or registered trademarks of NVIDIA Corporation in the U.S. and
other countries. Other company and product names may be trademarks of the respective companies with which
they are associated.
