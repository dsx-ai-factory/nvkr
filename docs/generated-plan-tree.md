# Audit generated scripts and adapt a Plan

A generated Plan is a frozen record of the selected inputs and operations. GitHub stores it as a tree for review; the canonical Plan remains the adapter boundary. The tree does not grant execution authority. [ADR-21](adr/0021-generated-pipeline-maintenance-and-lifecycle.md#1-the-emitted-pipeline-is-a-generated-artifact-not-an-owned-scaffold) owns storage and recovery; [ADR-18](adr/0018-pipeline-ir-and-ci-adapter-emission.md) owns execution and adapter contracts.

## Follow a job to its script

Start at an execution job in `.github/workflows/nvkr-run-nvke.yml`. Its `Producer commands` comment names `.github/nvkr-plan-store/index.json` and the job key. In that index:

1. Find the job key under `jobs`. Each variant lists its `workflows`: bundle names and actual workflow files, including partition workers. Identical operations share one variant across bundles.
2. Each `operations` entry names the factory `node` and execution `step`. A disk job has several operations under its shared `run-disk` step. Composite actions can contain the step; follow the workflow's local `uses` path to inspect its wrapper.
3. `target`, `producer` and `record` identify the frozen producer instruction. `scripts` lists its exact raw script files. Open `record` for the ordered command operands, inline component configuration, source pins, environment, working directory, network, material and toolchain bindings. Operations without a producer execute NVKR's typed helpers rather than a catalog script.
4. Find a script's store-relative `file` under `objects` in the same index to see its full SHA-256 and every bundle sharing those bytes. Different suffixes are different variants, even when their logical names match.

For example, inspect a job's variants, then find all users of a selected file:

```sh
jq --arg job 'JOB_ID' '.jobs[$job]' .github/nvkr-plan-store/index.json
jq --arg file 'scripts/COMPONENT/build@SUFFIX.sh' \
  '.objects[][] | select(.file == $file)' .github/nvkr-plan-store/index.json
```

The index describes producer commands; orchestration shell remains visible in workflow `run` fields and generated local actions. Read those wrappers too: they select the Plan, restore inputs and dispatch the executor. Runtime-refresh workflows may freeze newer admitted source pins at dispatch. Audit the exact Run's packed Plan for its commands; the committed tree describes the reviewed baseline, not a claim that every future Run uses those bytes.

## Layout and verification

| File | Purpose |
|---|---|
| `.github/<bundle>/plan.json` | Root index: scalar metadata, ordered build references and distinct catalogs keyed by catalogDigest |
| `.github/<bundle>/manifest.json` | Owned paths, modes, full exact-byte hashes and Plan/workflow identities |
| `.github/nvkr-plan-store/steps/<selection>/<target>/<platform>.json` | Build record with inline resolved configuration and execution graph, including source commits |
| `.github/nvkr-plan-store/plan/<kind>-<selection>.json` | Shared frozen Plan fields with stable names |
| `.github/nvkr-plan-store/plan/targets-<selection>-<bundle>.json` | Environment bindings, publication prefix, target repository suffixes and derived inventory Build Digest |
| `.github/nvkr-plan-store/plan/catalog-<name>@<12 hex>.json` | Catalog snapshot variants |
| `.github/nvkr-plan-store/scripts/<name>/<operation>@<12 hex>.sh` | Exact multiline operand bytes, with no added shebang or newline |
| `.github/nvkr-plan-store/index.json` | Shared job-to-step-to-script navigation, variants and bundle users |

Only `scripts/`, `steps/` and `plan/` are store directories. A step filename uses a platform label such as `linux-arm64`; its record retains the full original target name and platform. Component and source configuration stay inline, so reading a build does not require separate pointer-only records. Each distinct catalog snapshot is stored once per catalogDigest and shared across bundles.

Plan and step names remain stable when their bytes change. Environment records include the bundle name. Static Environments can share graph and output records when only their bindings differ. For `githubRuntimePlan` Environments, all stable Plan and step records additionally include the bundle name. Nightly keeps separate records so dispatch refresh and carrier installation can update it beside unchanged production files. Catalogs and scripts keep short content suffixes because multiple variants may coexist. The short suffix is a locator. Full manifest hashes check file integrity; the typed Plan Digest binds the reconstructed canonical Plan. The shared index is descriptive, not authorization. `nvkr run plan --plan .github/nvkr-run-nvke/plan.json --json` verifies the manifest-listed tree and returns canonical bytes without resolving catalogs or rendering workflows. Complete adapter-file drift is checked separately by `make workflows-check`; each bundle check verifies its navigation and the shared object inventory. Checking all maintained bundles covers the entire navigation index without reading another bundle's private Plan or workflows. Edit authored inputs or generator code, then regenerate with `make workflows-generate`. A change to a shared stable record requires all its owners in that coordinated generation; a single-bundle write that would leave another owner stale is refused. `make nvke-generate` and `make pipeline-dev-generate` also regenerate the complete maintained set. Static Environments that change the graph structure (for example signed versus unsigned publication) need distinct Definition names; a coordinated batch with incompatible stable contents returns a capability error. The store's `plan/` and `steps/` directories are generator-owned: unmanifested stable JSON files there are swept, so keep authored files elsewhere. An interrupted update fails exact-byte checks; retry the same complete selection, or restore the committed store and manifests before changing the inputs. Do not edit frozen scripts or indexes.

## Consume from another CI adapter

Use `factory.LoadPlanContext(ctx, path)` to load a tree or packed Plan, or export canonical JSON with `run plan --json`. Loading needs the root, its manifest and the shared objects that manifest lists; it does not require another bundle's manifest or the descriptive index. Transporting a packed Plan removes filesystem references and preserves Plan and Build Digests.

An adapter consumes the validated graph, named outputs and execution requirements. It must preserve dependency edges, exact operation membership, runner/tool capabilities, isolation, credentials and result/evidence bindings. Provider job grouping is an adapter choice: GitHub job and step names in the shared index are navigation aids, not portable scheduling instructions. Do not execute stored `.sh` files as standalone jobs; their interpreter arguments, environment and authority come from the admitted Plan and executor.

The existing Tekton renderer consumes the canonical Plan representation through its own admitted Environment and adapter path. A new provider must implement and validate the ADR-18 adapter contract before execution; this file layout does not add a plugin-discovery mechanism or admit a new engine. See [pipeline generation](generate-pipeline.md) for supported entry points and [Plan inspection](plan-and-graph-inspection.md) for graph views.
