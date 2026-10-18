# NVKR from your first artifact to the NVKE demo

> **Preview:** NVKR is available as a preview. Commands, input schemas and behavior can change between releases, and we do not recommend NVKR for production use. See [Preview status and known limitations](overview/preview-status.md).

NVKR takes a description of what you want to ship and turns it into a build you can inspect and run.
It connects the resulting files, images, and charts to their exact inputs and evidence. NVKE consumes
those artifacts to install and run the product.

Start with one small executable on your laptop. See how its inputs become an artifact, change its
version, then use the same workflow for NVKE. Start with [how NVKR works](#how-nvkr-works), then run
each command as you go.

## How NVKR works

The public lifecycle is **Definition → Run → Release**:

1. **Define what to produce.** Declare named outputs and select the catalog components that produce them.
2. **Review and run the work.** NVKR resolves the pinned inputs into a Plan. A Run executes that work
   and records the artifacts, evidence, and failures for each required output.
3. **Publish and consume.** A Release connects selected artifacts and evidence through one pinned
   reference. Consumers verify its supported claims against their own policy.

You supply three inputs:

| Input | Question it answers | In the first example |
|---|---|---|
| Catalog and pins | How is each component produced, and from which exact inputs? | Build a greeting script from a pinned Git commit in a pinned Alpine container. |
| Definition | What must this Run produce? | One executable output named `hello`. |
| Environment | Where and how will the work run? | Use local Docker. |

A **component** supplies a recipe, an **output** names a required deliverable, and an **artifact** is
the actual file or image produced. The Plan is generated from your inputs; you do not write it yourself.
The first example ends with a local artifact. CI, signing, and Releases come later.

## Build the CLI

Use an existing NVKR checkout, or clone one:

```sh
git clone https://github.com/dsx-ai-factory/nvkr.git
cd nvkr
```

You need Git 2.38 or later, Make, and the exact Go version recorded in `.go-version`.
Run the remaining commands from the repository root in the same terminal.

```sh
cat .go-version
GOTOOLCHAIN=local make build
bin/nvkr --version
```

Building may download Go modules. If Make reports a version mismatch, put the Go version shown by
`cat .go-version` on your `PATH`; see
[Development](https://github.com/dsx-ai-factory/nvkr/blob/main/DEVELOPMENT.md) for setup.

## Build and use your first artifact

Start Docker and make sure `python3` is on your `PATH`. No NVKE source checkout or registry credentials
are needed. The first run may pull its pinned Alpine image; later runs can use Docker's cache.
If Docker is not available, skip to [preview the NVKE outputs](#preview-the-nvke-outputs).

Keep both versions of the example beneath one fresh workspace:

```sh
mkdir -p .nvkr
NVKR_QUICKSTART_DIR=$(mktemp -d "$PWD/.nvkr/quickstart.XXXXXX")
bash demos/demo-local.sh --out "$NVKR_QUICKSTART_DIR/hello-v1"
```

The script creates a tiny local Git repository, locks its source version, and prints the `nvkr`
commands it runs. Add `--pause` for a guided stop before each stage. Expect:

```text
1 / 4  Pin the source
2 / 4  Choose the output and see the plan
3 / 4  Run it
4 / 4  Check the result and use it
  Run: complete | output: hello | lane: unqualified-local-execution
  Artifact bytes match the recorded SBOM subject.
Hello, team! This is hello v1.0.0, built by NVKR.
```

That greeting comes from the artifact Docker built. Its target is `linux/amd64`, so Docker on Apple
Silicon needs Linux AMD64 emulation. The result is a portable shell script that also runs on your host.

### Connect the inputs to the result

The [example Definition](https://github.com/dsx-ai-factory/nvkr/blob/main/demos/quickstart/definition.yaml)
selects one output:

```yaml
outputs:
  - name: hello
    components: [hello]
    platform: linux/amd64
    format: executable
```

The catalog supplies the build command: substitute the pinned version into the greeting script and
make it executable. NVKR derives the remaining work. Inspect the saved Plan and run the artifact again:

```sh
bin/nvkr run plan --plan "$NVKR_QUICKSTART_DIR/hello-v1/plan.json"
"$NVKR_QUICKSTART_DIR/hello-v1/.nvkr/runs/demo/hello/out/hello/hello" world
```

Look for one required output in the Plan and `Hello, world!` in the greeting. Under `hello-v1`, keep
these records together:

| File | What it tells you |
|---|---|
| `catalog/pins.lock.yaml` | The exact source commit selected for the build. |
| `plan.json` | The intended inputs, operations, and Build Digest. |
| `.nvkr/runs/demo/lifecycle.json` | What the Run actually observed and whether it completed. |
| `logs/run.log` | The full execution log, including failures. |

The demo checks that the artifact's bytes match its recorded SBOM subject. This is **unqualified local
execution**: nothing is signed or published, and the composition SBOM is not a dependency scan.
The [example guide](https://github.com/dsx-ai-factory/nvkr/blob/main/demos/quickstart/README.md) maps the remaining files.

### Change one input and compare

Build the second source version into another fresh directory:

```sh
bash demos/demo-local.sh --version 1.1.0 --out "$NVKR_QUICKSTART_DIR/hello-v2"
bin/nvkr run diff \
  --before "$NVKR_QUICKSTART_DIR/hello-v1/plan.json" \
  --after "$NVKR_QUICKSTART_DIR/hello-v2/plan.json"
```

The greeting now starts with `Welcome` and reports `v1.1.0`. The source pin and Build Digest change.
A Build Digest identifies the logical build inputs; the artifact digest identifies the bytes that
execution produced. Each invocation requires a new output directory, so use fresh names when repeating
these commands.

## Preview the NVKE outputs

The same model scales from one greeting script to the NVKE CLI, a container image, and a Helm chart.
This small NVKE Definition requests `nvkectl`, `cwe`, and `nvke-helm`; the chart uses the image's digest.
These preview commands need only the CLI you built above, with no Docker, registry, or NVKE source checkout:

```sh
bin/nvkr definition validate --definition docs/examples/nvke-tutorial/definition.yaml
bin/nvkr run plan \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-local.yaml
bin/nvkr run local \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-local.yaml \
  --attempt quickstart-preview --dry-run
```

Expect three required outputs in the Plan. The dry-run reports `pending`, `static-unqualified`, and
zero observed outputs: it creates no artifacts or attempt workspace. NVKR has described the work;
each named output still needs its own successful result.

The [three-output walkthrough](https://github.com/dsx-ai-factory/nvkr/blob/main/demos/m1-local.md)
can build those artifacts and pull the CLI back from a local registry. Continue below to run the
larger NVKE demo with a real workload cluster.

## Extend the workflow into the NVKE demo

The NVKE demo adds a downstream step: **NVKR builds the artifacts → NVKE's installer consumes them →
you use the running cluster**. It builds a larger selection including tools, images, charts, and an OS
disk. Kind hosts the management services; the workload cluster's control-plane and worker are real QCOW2 virtual machines.

This needs a dedicated **Ubuntu ARM64 host**, with eight CPUs, 24 GiB RAM, and about 90 GiB free on
native ext4 storage. An Apple Silicon Mac can host that Linux environment through Colima; Docker
Desktop alone does not provide the workload cluster's virtual-machine runtime. Allow substantially more time and downloads than
the greeting demo.

First follow the
[Ubuntu ARM64 setup guide](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/local-nvke-demo.md#1-choose-your-linux-execution-host).
Complete steps 1–3 **and the preparation in step 4**, stopping before its first `e2e` command.
That includes the Kind network setup and required runtime-image pulls. Bootstrap builds the CLI inside
its pinned Go container, so this host does not need its own Go installation. Stay in that Linux terminal,
at the NVKR repository root, with the guide's `NVKE_DEMO` variable set.

For this walkthrough, explicitly select **upstream OVN**. The default NVIDIA OVN selection has a
documented startup blocker. The upstream alternative has a recorded successful E2E at a specific
revision on October 11, 2026; it does not validate the NVIDIA fork or later revisions. The setup guide
links that evidence and owns the current prerequisites and limitations.

```sh
"$NVKE_DEMO" e2e \
  --config demos/local-nvke.yaml \
  --repo "$PWD" \
  --attempt quickstart-upstream-01 \
  --out "$PWD/.nvkr/quickstart-upstream-01-demo" \
  --jobs 2 \
  --network upstream-ovn
```

The command supplies the example's paths; no YAML edits are needed. For a rerun, choose unused values
for both `--attempt` and `--out`.

Success returns `retained-ready` and prints live cluster checks plus commands for accessing the
management cluster and the workload cluster, which the demo labels "Site" and "Tenant". Copy the exact
`DEMO`, `SESSION`, `KUBECTL`, and kubeconfig assignments it prints. Use the printed workload cluster
commands to inspect nodes and pods, and check the retained session with:

```sh
"$DEMO" status --session "$SESSION"
```

When finished exploring, clean up that session:

```sh
"$DEMO" cleanup --session "$SESSION"
```

Cleanup removes the owned demo resources and retains Run records and logs. For an automated run that
cleans up immediately, add `--cleanup` to `e2e` and require exit zero with the upstream-specific `PASS`
message. Readiness alone does not establish cleanup. This remains an unqualified local diagnostic;
it does not produce a signed Release.

## Take the same inputs to CI and Releases

Once the local flow is familiar, follow [pipeline generation](generate-pipeline.md) to generate and
check GitHub Actions in a scratch directory. Keep output selection in the Definition and execution,
publication, and signing settings in the Environment. Edit those inputs and regenerate the workflow.

For a published Release, follow [browse and verify a Release](release-quickstart.md). Browsing finds
outputs and evidence; verification checks the supported claim against your accepted signer and policy.
Use the [concept reference](user-concepts.md) when you need more detail on any step.
