# NKE Recipes

NKE Recipes turns pinned NKE components into traceable, signed software releases. Its goal is to bring
NKE's CLI, container images, Helm charts, operator tools, and node images into one declarative build and
delivery workflow, connecting the delivered bytes to their inputs and evidence.

This repository contains **NKD**, the build and release engine exposed through **`nkdctl`**, alongside
reusable NKE catalogs and product Definitions. Release engineers declare what to ship; NKD derives the
dependency graphs and generates the pipelines that build or acquire, package, sign, and publish it.
Consumers can navigate a release's artifacts and evidence, then verify a supported claim against their
own policy. NKE and downstream systems own installation and deployment.

## From components to releases

The public lifecycle is **Definition → Run → Release**:

1. **Define the deliverables.** Catalogs describe reusable component producers and pinned sources,
   artifacts, and tools. A Definition selects the components, platform, and format for each named output.
   Overlays replace producers or substitute toolchains for a chosen scope. An Environment supplies
   execution, registry, and signing settings.
2. **Review and run the pipeline.** Inspect the generated Plan, visualize producer and artifact graphs,
   and compare changes before execution. Run locally or generate GitHub Actions from those inputs.
   Each Run records its required outputs, observed artifacts, evidence, and failures.
3. **Publish and consume a release.** One digest-pinned OCI reference connects the published outputs
   to their SBOMs and provenance. Browse the release to find an output and follow its evidence;
   apply a Verification Policy to authenticate the supported artifact and build-evidence claim.

## What signed attestations do

An **attestation** is a statement about a specific artifact, bound to its digest: a fingerprint of its
content. For signed GitHub releases, NKD signs production provenance recording the Run, selected inputs,
and producing workflow, and SBOMs recording the components observed during inspection. It also signs
the published artifacts and release root. These signatures let a verifier authenticate the signer and
detect changes to the signed statements; the digest binds each claim to its intended artifact.

Signing an artifact or its attestations does **not claim ownership** of the software or its included
components. It identifies the signer making the recorded production or publication claim.

The consumer still chooses whom to trust. A Verification Policy supplies the accepted workflow identity
and **issuer**, the service that issued that identity; verification also requires a pinned verifier.
NKD checks the required signatures and production attestations against that policy, then checks the
referenced artifact and evidence bytes. Missing evidence, mismatched digests, or an unaccepted signer
fail verification. A valid signature authenticates a claim; it does not establish complete inspection,
freedom from vulnerabilities, independent reproduction, qualification, or deployment readiness.
See the [release quickstart](docs/release-quickstart.md) to apply your policy.

## Start here

Start with the [quickstart](docs/quickstart.md) to validate the small three-output NKE example and preview
a Run. You need Git, Make, and the Go version in [.go-version](.go-version); these planning commands work
on macOS and Linux without Docker, a registry, or an NKE source checkout. From the repository root:

```sh
GOTOOLCHAIN=local make build
bin/nkdctl definition validate --definition definitions/nke-v1/definition.yaml
bin/nkdctl run plan --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-local.yaml
```

Planning does not build or publish artifacts. The [documentation map](docs/README.md) connects the next steps:

| Task | Guide |
|---|---|
| Select components, pins, and overlays | [Catalogs and components](docs/catalogs-and-components.md) |
| Visualize dependencies and generate GitHub Actions | [Pipeline generation](docs/generate-pipeline.md) |
| Understand node-image materials and execution | [Node images](docs/node-images.md) |
| Find outputs and verify a published claim | [Release quickstart](docs/release-quickstart.md) |
| Look up terminology and features | [Concept reference](docs/user-concepts.md) |

The [tutorial Definition](definitions/nke-v1/README.md) selects the CLI, CWE image, and Helm bundle.
The [combined NKE candidate](definitions/nke-m2/README.md) selects 34 outputs, including AMD64 and ARM64
container and node images, and supplies the repository's generated GitHub workflow.

## Current scope

GitHub is the maintained hosted generator and signing/publication provider. Actual builds require their
declared runtime, source access, and capabilities; the combined candidate's hosted disk jobs use explicit
KVM routes. Consumer verification requires Linux, pinned tools, and an explicit identity and issuer policy.

The [October 6 candidate Run](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963) completed all 34 outputs,
publication, keyless signing, workflow verification, and Run finalization, including both disk images.
The record covers that validation branch and does not establish independent full-payload consumer
acceptance or general production readiness.

Release browsing inspects metadata without downloading payloads or verifying signatures. The supported
consumer claim is `pipeline-attested` / `artifact-publication`. It does not establish exact-byte reproduction,
full disk inventory, or deployment readiness; full release qualification remains unavailable. See the
[implementation and evidence record](docs/adr/reconciliation.md) for current boundaries.

## Contribute

See [DEVELOPMENT.md](DEVELOPMENT.md) for setup and `make check`, and
[CONTRIBUTING.md](CONTRIBUTING.md) for contribution rules.

The [documentation map](docs/README.md) connects architecture, engineering decisions, and evidence.
The [demo index](demos/README.md) provides presentations and runnable walkthroughs.
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) covers community expectations.

## Support

Use [GitHub Discussions](https://github.com/dsx-ai-factory/nke-recipes/discussions/categories/q-a) for usage
questions and [GitHub Issues](https://github.com/dsx-ai-factory/nke-recipes/issues) for reproducible bugs,
feature requests, and documentation gaps. See [SUPPORT.md](SUPPORT.md) for support channels. Report
security vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## License

NKE Recipes is licensed under the Apache License 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
