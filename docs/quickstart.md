# NVKR quickstart

Validate a product Definition, inspect its Plan, and preview a Run using the three-output NVKE tutorial.
The example declares three outputs: the `nvkectl` executable bundle, the `cwe` container image, and the
`nvke-helm` chart. The chart uses the image's digest.

These steps work on macOS or Linux without Docker, a registry, or a private NVKE source checkout.
Run every command from the NVKR repository root, keeping the same terminal open.
The repository's committed workflow uses the larger [M2 candidate](https://github.com/dsx-ai-factory/nvkr/blob/main/definitions/nvke-m2/README.md).

## Open a checkout

Use your existing checkout, or clone the repository:

```sh
git clone https://github.com/dsx-ai-factory/nvkr.git
cd nvkr
```

## Build the CLI

You need Git, Make, and the exact Go version recorded in `.go-version`. Building may download Go modules.
For toolchain setup, see [Development](https://github.com/dsx-ai-factory/nvkr/blob/main/DEVELOPMENT.md).

```sh
cat .go-version
GOTOOLCHAIN=local make build
bin/nvkr --version
```

If Make reports a Go version mismatch, install the version shown by `cat .go-version` and put it on `PATH`.

## Validate the Definition

```sh
bin/nvkr definition validate \
  --definition definitions/nvke-v1/definition.yaml
```

Expect the name `nvke-v1`, three outputs, and a Definition digest. The
[Definition](https://github.com/dsx-ai-factory/nvkr/blob/main/definitions/nvke-v1/definition.yaml) chooses the outputs; its
[catalog](https://github.com/dsx-ai-factory/nvkr/blob/main/definitions/nvke-v1/catalog/registry.yaml) supplies their producer instructions and pins.

## Save and inspect a Plan

```sh
NVKR_QUICKSTART_DIR=$(mktemp -d)
bin/nvkr run plan \
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-local.yaml \
  --json > "$NVKR_QUICKSTART_DIR/plan.json"
bin/nvkr run plan --plan "$NVKR_QUICKSTART_DIR/plan.json"
```

Look for `expected outputs: 3` and the `Outputs` section in the summary. Later sections list the planned
operations and their inputs. The Environment supplies runtime, publication,
and signing settings; selecting it here does not start Docker or a registry. Keep the canonical JSON
unchanged so you can inspect, graph, or compare the same Plan later.

## Preview a Run

```sh
bin/nvkr run local \
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-local.yaml \
  --attempt quickstart-preview --dry-run
```

Expect `pending`, `static-unqualified`, and zero observed outputs. The preview builds no artifacts and
creates no attempt workspace. You now have validated inputs and a saved description of the intended work.

Continue with [catalogs and components](catalogs-and-components.md),
[pipeline generation](generate-pipeline.md), or the [concept reference](user-concepts.md).
