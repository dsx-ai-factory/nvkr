# Six worked journeys

> **Preview:** NVKR is not recommended for production use. See [Preview limits](overview/preview-status.md).

Run commands from an NVKR checkout. Start with the small build, or enter at the journey matching your task.
If you enter later and need `bin/nvkr`, follow [CLI-only setup](quickstart.md#build-only-the-cli) first.
The [Preview limits](overview/preview-status.md) page owns shared capability and evidence limits;
the prerequisites below apply to the command immediately beside them. Detailed guides remain in the
[reference index](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/README.md#reference-index).

## Build a small artifact

You need Git, a running Docker daemon on native AMD64 or ARM64, and network access for pinned images
and dependencies. The launcher builds NVKR and offers to install the checkout's pinned Go compiler.

```sh
NVKR_JOURNEY_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-journey.XXXXXX")
bash demos/demo-local.sh --out "$NVKR_JOURNEY_DIR/hello-v1"
```

Expect one observed executable, `status: complete`, and:

```text
Hello, team! This is hello v1.0.0, built by NVKR.
```

Next: [change the component's source version](#change-a-component), using the same terminal.
The launcher prints and retains the exact workspace path. Keep that directory if you return later.

This is a real container build with a local Git source, not a simulation. It needs no registry account
and publishes nothing. [Hello's input reference](https://github.com/dsx-ai-factory/nvkr/blob/main/demos/hello/README.md#read-the-inputs)
explains the four small files. For unattended use, `--install-go` explicitly permits the compiler download.

## Change a component

After the small build, keep Docker running and use the same terminal so `NVKR_JOURNEY_DIR` is set.
Choose the demo component's second source version in a fresh workspace:

```sh
: "${NVKR_JOURNEY_DIR:?Set this to the directory created in the small-build journey}"
bash demos/demo-local.sh --version 1.1.0 --out "$NVKR_JOURNEY_DIR/hello-v2"
```

Expect the greeting to become `Welcome, team! This is hello v1.1.0, built by NVKR.`
The new source pin changes the printed Build Digest; the first Run and its files remain available.
Next: [inspect both Runs](#inspect-a-run).

This changes one intentional input: the selected source tag. The launcher uses `catalog pin` to write
the lock; do not edit it by hand. For a change to your own producer, follow
[catalog pinning and component replacement](catalogs-and-components.md) after this small example.
The [authoring lab](examples/authoring-lab/README.md) covers replacement and rejection cases in more depth.

## Inspect a Run

You need the retained workspace from either hello build and the CLI at `bin/nvkr`. Inspection needs
no running container. Set the directory again if you opened a new terminal.

```sh
: "${NVKR_JOURNEY_DIR:?Set this to the directory created in the small-build journey}"
bin/nvkr run inspect --record "$NVKR_JOURNEY_DIR/hello-v1/.nvkr/runs/demo/lifecycle.json"
bin/nvkr run plan --plan "$NVKR_JOURNEY_DIR/hello-v1/plan.json"
bin/nvkr run graph --plan "$NVKR_JOURNEY_DIR/hello-v1/plan.json" --format mermaid
```

Expect the recorded Run to report one observed `hello` output and the local execution lane.
The Plan describes the intended work; the Mermaid graph shows its dependencies. Replace `hello-v1`
with `hello-v2` to inspect the changed source selection. Next: [build a larger selection](#build-a-larger-selection).

Use [Plan and graph inspection](plan-and-graph-inspection.md) for filtering and comparisons, and
[Run inspection](lifecycle.md) for status, records and interactive presentation. Reading a completed
record is not signature verification or a new execution.

## Build a larger selection

Use a built CLI and network access. Preview the three-output NVKE selection for your native AMD64 or
ARM64 host first; this step does not need Docker or Go on `PATH`:

```sh
case "$(uname -m)" in
  arm64|aarch64) NVKR_JOURNEY_ARCH=arm64 ;;
  x86_64|amd64) NVKR_JOURNEY_ARCH=amd64 ;;
  *) echo 'This example requires native AMD64 or ARM64'; false ;;
esac &&
bin/nvkr run local \
  --definition "docs/examples/nvke-tutorial/definition-$NVKR_JOURNEY_ARCH.yaml" \
  --environment "docs/examples/nvke-tutorial/environment-local-$NVKR_JOURNEY_ARCH.yaml" \
  --attempt journey-preview --dry-run
```

Expect `pending`, `static-unqualified`, and zero observed outputs. To build the CLI, controller image
and chart, start Docker and run:

```sh
bash scripts/nvkr-quickstart.sh
```

Expect `status: complete`, three observed outputs, and a printed result directory containing payloads
and SBOMs. The controller build needs Docker's Linux kernel to support unprivileged overlayfs.
Next: [publish through a configured hosted workflow](#publish-a-release).

The [quickstart](quickstart.md) owns setup and troubleshooting. For more outputs, choose a maintained
[Definition and matching Environment](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/README.md).
Node-image builds require the explicit native Linux KVM route; increasing the selection does not
grant that capability. The [node-image guide](node-images.md) keeps those prerequisites beside its commands.

## Publish a Release

Start with `bin/nvkr` built and network access. Generate the three-output hosted example in scratch
space to review its publication policy before adopting it:

```sh
NVKR_PUBLISH_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-publish.XXXXXX")
bin/nvkr run generate \
  --definition docs/examples/nvke-module-tags/definition.yaml \
  --environment docs/examples/nvke-module-tags/environment-github.yaml \
  --engine github --root "$NVKR_PUBLISH_DIR"
bin/nvkr run generate \
  --definition docs/examples/nvke-module-tags/definition.yaml \
  --environment docs/examples/nvke-module-tags/environment-github.yaml \
  --engine github --root "$NVKR_PUBLISH_DIR" --check
```

Expect a workflow, shared actions, Plan and ownership manifest under that directory's `.github/`.
The check should report no drift. This creates a reviewable bundle; it does not publish the completed
local Run or submit anything to GitHub. Next: adopt and run the bundle as follows, then
[verify its resulting Release](#verify-or-consume-a-release).

The example's repository prefix and signer identity belong to `dsx-ai-factory/nvkr`. This source-built
example requires a publishing repository containing the NVKR source (including `.go-version` and
`cmd/nvkr`) and all authored inputs at their generated relative paths; use an NVKR fork for this journey.
The runner bootstrap builds from that checkout. Before using your fork, author its exact destination and accepted workflow identity in your Environment,
declare image access, configure the protected `nvkr-release` Environment to admit only `main`, configure
registry permissions, and install the
separate private CI-storage credentials described in [pipeline generation](generate-pipeline.md).
Regenerate from those authored inputs and commit the complete bundle through your repository's review
process. Do not copy a scratch workflow over an existing generated bundle or edit rendered YAML.

Once the reviewed bundle is installed on the repository's admitted default branch, a maintainer with
GitHub CLI access can dispatch it (for this example, the workflow is `nvkr-run.yml` on `main`).
Dispatch runs whichever bundle is committed at that ref. The maintained production workflow in
`dsx-ai-factory/nvkr` is `nvkr-run-nvke.yml`, which selects the NVKE composite. This scratch example
uses `nvkr-run.yml` in your configured fork. Check its committed Plan and output membership before dispatch:

```sh
: "${NVKR_PUBLISH_REPO:?Set the configured publishing repository as owner/name}"
gh workflow run nvkr-run.yml --ref main --repo "$NVKR_PUBLISH_REPO"
```

Expect a newly queued Run in that repository's Actions view; a protected Environment can leave it waiting
for approval. Successful dispatch does not mean the build has succeeded. Wait for Run completion. Find
the job whose display name ends in **release** (this scratch example uses **Attest, publish, sign and verify
release**; a per-output bundle can use **Assemble, publish, sign and verify release**). In its publication
step, find `Published immutable reference:` and select the value ending in `/releases@sha256:...`. Copy
the complete `configured-prefix/releases@sha256:...` value into `RELEASE_REF` for the next journey.
Use the release reference, not a per-output reference from another job. A failed or incomplete Run is
not a successful publication, even if one operation printed a reference.
[Publication architecture](publication-architecture.md) owns destination, signing and recovery details;
This journey uses GitHub keyless signing. [Tekton execution](generate-pipeline.md#tekton-cluster-execution)
has a separate cluster-key trust contract and is not covered by the verification example below.

## Verify or consume a Release

Obtain `RELEASE_REF` from a completed publication: a repository followed by `@sha256:` and its digest,
not a mutable tag or an individual output manifest. Current readers require publication-root v4 (plain
Definitions) or v5 (composites); historical v3 roots are rejected. Registry read access and `bin/nvkr` are required.

```sh
: "${RELEASE_REF:?Set the exact publication-root reference from the producing Run}"
bin/nvkr release browse --reference "$RELEASE_REF"
```

Expect named outputs with their formats, Build Digests and evidence. Browsing reads metadata without
authenticating the publisher. Next: verify the same reference under an independently accepted policy.

On Linux, supply your trusted static Linux cosign executable and SHA-256 digest, plus the accepted
signer identity pattern and OIDC issuer. Do not derive the accepted identity from an untrusted candidate.

```sh
: "${ACCEPTED_BUILD_IDENTITY_REGEXP:?Set the independently accepted signer identity pattern}"
: "${ACCEPTED_OIDC_ISSUER:?Set the independently accepted issuer}"
: "${COSIGN_PATH:?Set the static Linux cosign executable path}"
: "${COSIGN_DIGEST:?Set its independently trusted SHA-256 digest}"
bin/nvkr release verify --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope artifact-publication \
  --identity "$ACCEPTED_BUILD_IDENTITY_REGEXP" --issuer "$ACCEPTED_OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST"
```

Expect success only when payload integrity, signatures and production evidence satisfy that policy.
For macOS, use the [pinned Linux Docker verification route](release-quickstart.md#verify-against-an-accepted-policy).
Then select an exact output name from the listing and pull to a new directory:

```sh
: "${OUTPUT_NAME:?Set an exact output name from the verified Release}"
NVKR_CONSUME_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-consume.XXXXXX")
bin/nvkr release pull --reference "$RELEASE_REF" \
  --output "$OUTPUT_NAME" --out "$NVKR_CONSUME_DIR/output"
```

Expect that output's files under `output/`; pulling neither installs nor executes them. Keep the exact
verified reference. Pull checks payload bytes but does not replace signature verification. For a public
registry, add `--anonymous` consistently. Continue with the [format and export reference](release-navigation.md).

## What these examples establish

The hello launcher checks the produced artifact and exact greeting; `make demo-hello` exercises its
default build when Docker is available. The current three-output hosted example has no recorded hosted Run;
its [historical record](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/examples/nvke-module-tags/README.md)
predates the current inputs. Other Definitions' successful publications do not validate this example.

Your dry-run describes intended work without producing artifacts. Generating and checking a scratch
bundle establishes reproducible generation for those selected inputs. Neither establishes registry,
signer, runner or deployment acceptance.
Publication and verification here require your configured repository and accepted consumer policy.
The [Preview limits](overview/preview-status.md) page owns shared restrictions, while
[reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/README.md)
keeps the exact scope of external evidence.
