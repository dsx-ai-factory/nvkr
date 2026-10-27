# Generate a GitHub Actions pipeline

Turn a Definition and Environment into a reviewable pipeline bundle. A **Definition** selects the named
outputs to build; an **Environment** supplies execution, publication, and signing settings. This walkthrough
uses the three-output NVKE tutorial: a CLI executable, a container image, and a Helm chart.

Start at the repository root with `bin/nvkr` built as described in the [quickstart](quickstart.md#build-only-the-cli).
Run these commands in the same terminal. They write to a temporary directory, leaving the repository's
generated pipeline untouched.

## Inspect the intended work

Create a canonical Plan, then read its output list and build instructions:

```sh
NVKR_PIPELINE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-pipeline.XXXXXX")
bin/nvkr run plan \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --json > "$NVKR_PIPELINE_DIR/plan.json"
bin/nvkr run plan --plan "$NVKR_PIPELINE_DIR/plan.json"
```

The Plan freezes the selected inputs and describes the work. It is generated from your authored files,
not another configuration file to maintain. `--json` preserves its exact canonical bytes; `--pretty` is
only a display format and cannot replace that saved Plan.

Export a diagram to trace producer instructions, artifacts, evidence, and publication dependencies:

```sh
bin/nvkr run graph --plan "$NVKR_PIPELINE_DIR/plan.json" \
  --view complete --format mermaid > "$NVKR_PIPELINE_DIR/plan.mmd"
```

Open the `.mmd` file in a Mermaid-compatible viewer. A planned signing or verification step describes
intended work; it does not establish that the step succeeded.

## Generate and check the bundle

```sh
mkdir "$NVKR_PIPELINE_DIR/generated"
bin/nvkr run generate \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --engine github --root "$NVKR_PIPELINE_DIR/generated"
bin/nvkr run generate \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --engine github --root "$NVKR_PIPELINE_DIR/generated" --check
```

Under that scratch root, inspect:

- `.github/workflows/nvkr-run.yml`: named GitHub Actions jobs, dependencies, runners and credential policies.
- `.github/nvkr-run/actions/`: shared actions with fixed interfaces for execution, preparation and transport.
- `.github/nvkr-run/action-data.json`: generated invocation parameters selected by stable job/segment keys.
- `.github/nvkr-run/plan.json`: the canonical Plan consumed by the workflow.
- `.github/nvkr-run/manifest.json`: the generated bundle's ownership and content record.

Those are the default production paths when the Environment omits `githubBundle`
or sets it to an empty string and leaves `githubDevelopment` false. To generate
a separate production project bundle, author `githubBundle` in that Environment.
A name contains at most 48 lowercase letters, digits and
hyphens, with alphanumeric ends. Its paths are derived by the generator:

| Environment selection | Workflow | Generated records directory |
|---|---|---|
| Production, omitted or empty `githubBundle` | `.github/workflows/nvkr-run.yml` | `.github/nvkr-run/` |
| Production, `githubBundle: <name>` | `.github/workflows/nvkr-run-<name>.yml` | `.github/nvkr-run-<name>/` |
| `githubDevelopment: true`, empty `githubBundle` | `.github/workflows/nvkr-dev.yml` | `.github/nvkr-dev/` |

Named production workflows display as `nvkr-project-<name>`. Default production
retains `nvkr-run-<Definition name>` and development retains
`nvkr-dev-<Definition name>`. These distinct names separate workflow-based concurrency
and keep named producers outside the default qualifier's display-name trigger, even
when the bundles select the same Definition. File paths remain as listed above.

Each records directory contains its own `plan.json` and `manifest.json`. Default
and named bundles can coexist; each owns only its files and retirement. The
selection changes Environment and Plan identity without changing component Build
identity. Set the signing policy for the chosen workflow explicitly; selecting a
bundle name never rewrites that policy or permits authored generated paths.
Development uses its separate branch, signing, publication and retention policy;
it does not authorize production acceptance. Combining `githubDevelopment: true`
with a nonempty `githubBundle` returns `CodeEngineCapability` before catalog
resolution or generated-output work. See the [feature-branch development loop](https://github.com/dsx-ai-factory/nvkr/blob/98ef3ec595d86d017345a0e2d635b388d1429b49/DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch).

`--check` regenerates in memory and compares exact bytes without writing files. Use the same CLI build
and inputs for both commands; the generator version contributes to generated content. Change authored
inputs and regenerate instead of editing generated files.

Named production Environments can opt into `githubRuntimePlan: true`. Generate their initial complete
bundle normally. On each dispatch the first bootstrap compiles the repository's current catalog pins
with `run generate --runtime`, freezes the resulting Plan, and carries that exact data to every job.
Secondary helper bootstraps restore the three JSON files into fresh staging, validate them against the
committed bundle and current authored inputs, then install them through the generated-file writer.
Routine source-version updates therefore do not require a regenerated bundle commit. CI uses the same
inputs with `--runtime --check`: it checks baseline integrity and unchanged workflow/action bytes,
membership and Environment policy without writing files. Changes outside that contract require normal
regeneration and review. This mode neither follows floating upstream versions nor changes the default
or development bundle contract; ordinary `--check` remains a complete byte comparison.

Generated GitHub transfers keep validated metadata in Actions artifacts using the workflow identity.
Provenance, SBOMs, signing/verification records and control layouts must pass their exact schemas and
complete file inventories; metadata layouts cannot contain output payload blobs. Their exact bytes and
Run bindings remain verified. Binaries, tools, source archives, unknown/mixed contents and raw diagnostics
use private/internal `ghcr.io/<owner>/<repository>/ci-artifacts` storage. Configure separate repository secrets
`NVKR_CI_STORAGE_READ_TOKEN` and `NVKR_CI_STORAGE_WRITE_TOKEN` with access only to that private/internal
storage package, plus repository variables `NVKR_CI_STORAGE_READ_USER` and `NVKR_CI_STORAGE_WRITE_USER`
for the corresponding token owners. The package must have no linked repository; public, unknown, inaccessible or linked
policy fails closed. A missing package can receive a metadata-only seed before the same checks are repeated.
There is no repository-token or public Actions payload fallback. The repository token separately authenticates
repository metadata; storage credentials belong only to transfer operations and do not add package permissions
to build jobs. Producer commands do not receive them.

The Go transport stores ordered chunks no larger than 1 GiB. Single-file uploads stream deterministic archive
framing around the held carrier, without another full archive on disk. Downloads extract directly from the
ordered chunks into private staging and verify each chunk, the complete archive, exact size and source EOF
before exposing restored files. The v2 locator format replaces the unshipped single-layer draft; old v1
locators require a new run. Final hosted GHCR publication separately checks every destination package with
its publication credential and binds the push to the inspected metadata root. Provider locator retention
does not expire private carrier blobs; configure registry retention separately.

Downloads can restart an incomplete extraction for a narrowly identified transient body-read failure,
with at most three attempts under the original deadline. Each retry closes the failed response, removes
its private staging and rechecks the entire archive from fresh responses; it adds no archive copy.
Invalid bytes, unsafe archive entries, filesystem or cleanup failures stop immediately. Publication and
destination replacement are outside the retry boundary. Error reports include only a fixed category,
chunk number and byte counts, keeping provider URLs and response bodies out of logs.

Production registry access uses separate credentials from CI storage. Configure repository secret
`NVKR_REGISTRY_READ` and variable `NVKR_REGISTRY_READ_USER` for a package reader covering release,
toolchain and smoke-image packages. Configure secret `NVKR_REGISTRY_WRITE` and variable
`NVKR_REGISTRY_WRITE_USER` in the protected `nvkr-release` environment for publication, signing and
qualification. Use separate accounts and tokens with read-only versus write package scopes; copying a
write-capable PAT into the reader secret does not narrow its authority. The existing CI storage secrets
remain unchanged. Native image preparation consumes its reader only in the preparation step.

The repository-level `NVKR_REGISTRY_READ` secret and `NVKR_REGISTRY_READ_USER` variable are also
required for private native toolchain and smoke-image pulls in development and legacy
`github:GITHUB_TOKEN` output-publication profiles. Development output credentials do not replace this
shared image reader. A missing reader refuses the pull; there is no workflow-token fallback.
The entry workflow passes both reader values explicitly into composite actions, scoped to preparation.

For each existing storage, release, toolchain and smoke-image package, an administrator must:
1. Keep visibility private (or internal only when enterprise-wide access is intended).
2. Disable repository permission inheritance and remove the source-repository connection.
3. Remove the source repository under **Manage Actions access**, including explicit grants.
4. Grant the dedicated reader/writer accounts only their required package roles.

The package API verifies visibility and repository linkage, but does not prove absence of separate Actions
grants. Record that settings audit and verify denial for anonymous users, unrelated users, and fork workflows
before changing repository visibility. Replacing a token does not revoke existing package grants.
Do not revoke grants needed by an active old workflow: migrate credentials, merge and validate the new
workflow, then retire the old access. Existing dispatched runs retain their original workflow code.

To enter credentials securely from a local terminal (the commands prompt without printing the secret):

```sh
gh secret set NVKR_REGISTRY_READ --repo dsx-ai-factory/nvkr
gh variable set NVKR_REGISTRY_READ_USER --repo dsx-ai-factory/nvkr --body READER_ACCOUNT
gh secret set NVKR_REGISTRY_WRITE --repo dsx-ai-factory/nvkr --env nvkr-release
gh variable set NVKR_REGISTRY_WRITE_USER --repo dsx-ai-factory/nvkr --env nvkr-release --body WRITER_ACCOUNT
```

Before making the source public, verify hidden package grants and anonymous, unrelated-user and fork access
for storage, toolchain and release packages. Previously retained Actions artifacts, caches, active runs and
old workflow refs also require separate handling; this change does not migrate or delete historical payloads.
Compiled-output caches and CodeQL database uploads are disabled. Candidate OS builds retain private files
only for the exact trusted workflow commit, so arbitrary candidate code cannot access a later storage writer.
The nightly coordinator attaches its constructed cycle summary; raw suite report files are not public artifacts.

Generated workflows admit at most 2,000 record uploads per attempt, including conditional diagnostic and
finalization uploads. The helper looks up exact selected names and fetches their record ZIPs serially,
with bounded, cancellable waits for GitHub rate limits. Exact-name discovery avoids truncating a growing
inventory at a fixed page count. Each ZIP must match its provider digest and contain one bounded canonical
record for the selected Run and attempt. Public metadata is limited to 4,096 entries, 8 MiB per file,
16 MiB total text and 32 MiB encoded JSON; the complete downloaded index is capped at 128 MiB.
Oversized or invalid metadata fails closed. Metadata-only transfers need no registry token or package API
checks. Private payload restoration follows its existing checks. The
generator rejects larger bundles before writing them; signed development retries require a fresh complete Run.

The generator rejects workflows above 500,000 bytes or 50,000 expanded YAML parsing events. YAML aliases
reduce file size but do not reduce the parser's expanded count. Maintained rendering tests reserve five
percent headroom. Compatible offline observation, SBOM and packaging steps share their producer job;
signing, publication and remote verification keep separate jobs and permissions. Payloads consumed only
inside their producing job are not packed or uploaded unless finalization needs them. This avoids shipping
an intermediate disk package as well as its prepared publication layout.

Native disk producers prepare access to `/dev/kvm` before checkout or helper/input downloads. Missing,
symlink and non-character devices fail immediately. If the runner cannot open the device read/write, one
fixed noninteractive `sudo chmod 0666 /dev/kvm` attempt runs under a fifteen-second timeout and five-second
kill grace. A failed change or subsequent open rejects the job; an already-accessible device is unchanged.
Full native capacity and held KVM API admission still occurs in the helper with the same producer identity.
Finalizers placed on ARC for disk retention do not use KVM and omit this step. The separately authored
OS-validation preflight remains read-only and can therefore report an inaccessible baseline that the
generated producer would attempt to repair.

The operator-trusted ARC policy explicitly accepts this bootstrap: changing a host-backed/shared device
to mode 0666 can affect access outside the runner job. It does not establish SLSA Build L3 isolation or
upgrade the evidence lane. Provisioning the correct numeric device group on runners avoids the mutation.
The generated bootstrap has not yet completed a real full build; see [reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md)
for the separately observed NVKE bootstrap and the remaining NVKR acceptance evidence.

The per-output pipeline gives each remaining payload transfer a specific purpose:

| Handoff | Bytes moved | Purpose |
|---|---|---|
| Preparation producer → private CI storage → publisher | Original prepared payload and its metadata | Cross the producer/publication credential boundary; validate and publish the native output closure |
| Preparation producer → SBOM attester → publisher | Bounded metadata, canonical SBOM documents and signed bundles | Keep OIDC outside the producer while binding evidence to unchanged subjects |
| Publisher → product registry | Complete validated output closure | Publish the independently consumable native artifact |
| Publisher → artifact signer | Exact-root-bound metadata and reference | Derive signing subjects and verify evidence without another payload download |
| Product registry → independent verifier | Complete served payload closure | Verify the bytes actually published, plus signatures, provenance and SBOMs |
| Verifier → release assembly/finalization | Metadata, references and evidence records | Authenticate membership and completeness without retrieving payloads |

The publisher authenticates the attested metadata against the original preparation, verifies each bundle
and its exact decoded SBOM, and moves unchanged blobs into the final layout before full-byte publication
checks. The SBOM and signing jobs do not download or re-upload output payloads. Metadata inspection alone
is not payload verification; the independent remote readback remains required. This is the existing
`pipeline-attested` claim, not qualification or a new SLSA level.

Generated stage restoration streams the nested factory carrier directly from private registry chunks into
its extracted files. The admitted Plan selects the complete locator set, exact carrier members and permitted
inner roots before payload transfer. Both TAR layers and every chunk are checked, including complete outer
size, digest and source EOF, before any extracted root becomes visible. No full carrier TAR is stored beside
the extracted payload. Atomic no-replace publication moves the private staging roots within the workspace;
it does not copy their contents. Bootstrap/helper transfers and finalizer recovery retain their separate paths.

Disk publication reads the existing QCOW2 through a held no-follow descriptor instead of copying it into a
temporary one-image layout. Each upload body checks its exact size, digest and unchanged inode metadata;
independent bodies support HTTP retries without reopening a pathname. A final full rehash is mandatory even
when the registry skips the body because it already has that digest. Remote immutable-reference checks remain.
Independent disk verification can restart an interrupted one-layer download for the typed HTTP/2 peer-reset
failure observed in development. Recovery is bounded to three attempts under one deadline and removes the
failed layout before retrying. It rechecks the complete immutable closure; it does not retry publication or signing.

This is not a zero-copy pipeline. Native disk collection still stages the container archive and its
extracted payload; packaging captures the authenticated disk into a private OCI layout, and preparation
imports that layout. Ordinary nondisk publication also retains its temporary layout. These remaining copies
support the current ownership and mutation checks; they are implementation choices, not SLSA requirements.
Further removal needs equivalent bounded reads or exclusive ownership transfer, with unchanged digest,
cleanup and atomic-completion guarantees. Direct pathname reuse or hardlinks alone do not provide them.

For the native `build-provenance-v1` disk route, the remaining producer-side materializations are:

| Materialization after Packer writes the original QCOW2 | Current purpose | Requirement for removing it |
|---|---|---|
| Container output → Docker TAR file → sealed Linux memory file | Bound container output, join the Docker command and freeze its complete archive before extraction | Stream the bounded archive into private extraction while retaining complete archive validation and command stop/join ownership |
| Held TAR → extracted QCOW2 | Validate the exact archive member and create the held disk owner | Retain one authenticated final payload; the archive staging above is the removable duplication |
| Live disk owner → export temporary → OCI wrapper blob | Bind export to the exact Plan/output, then commit a complete verified OCI transaction | Let the authenticated export write directly into the transaction's final staged blob |
| OCI wrapper blob → prepared publication blob | Give the publication transaction independent ownership and verify its complete closure | Consume exclusively owned blobs with atomic same-filesystem moves and complete closure checks |
| Prepared publication → inner transfer TAR | Produce a deterministic carrier for private registry transport | Stream deterministic TAR bytes over held, verified source files with bounded replay and unchanged integrity checks |

This accounts for six payload-scale filesystem writes and one sealed memory snapshot after the original
QCOW2 on that route. It is an inventory of writes, not seven simultaneous disk copies or a measured peak.
These are implementation costs to eliminate where the replacement preserves the stated guarantees;
SLSA does not require these intermediate files. The replacement paths in the last column are not implemented.

The generated producer emits a manual `workflow_dispatch` pipeline, with no cron
or callable producer trigger. A separately authored downstream CI coordinator can
schedule dispatches; that scheduling and runtime-test ownership are separate
from generated bundle ownership.

Adding `--qualification-policy` also generates the admitted qualifier and its
fixed native smoke worker. The qualifier uses `workflow_run`; the worker uses
`workflow_call` from that same-repository qualifier at the same commit. Their
independent policy, credentials and ownership are separate from the producer
bundle and the downstream nightly E2E adapter. They add no generated cron or
general callable producer interface.
Current qualification policy admission selects the default producer workflow,
`.github/workflows/nvkr-run.yml`; named-producer qualification is not an admitted
configuration. The named nightly producers instead use the separate downstream
E2E adapter.

Read the top-level workflow to review which jobs can run, their dependency order, permissions, protected
Environments and time limits. Follow a job's local `uses: ./.github/nvkr-run/actions/...` reference to inspect
the shared operations it calls, then find its invocation key in `action-data.json` for exact node and carrier
values. Calls such as `execute-node`, `execute-source` and `collect-node-outputs` name the operation rather
than a generated shape. Grouped jobs reuse the node interface for each operation; optional slots and further
individual upload calls preserve every output without adding output-count variants. Invocation parameters
have named roles. The helper recomputes those values from the admitted Plan at runtime; this data is generated,
not another file to author. Templates contain fixed commands
and pinned actions, not scripts supplied as parameters. Native preflight, checkout, operator setup and timed
producer execution remain inline. A controlled transfer delegates to an action containing one upload or
download, with its condition, timeout and failure policy still visible on the caller. Steps whose outcomes
or outputs are referenced remain inline, including finalization and the final Run upload read by the closing
summary. Recovery uses the shared `recover-inputs` action before that inline finalization. Source credentials are passed explicitly and consumed only by the
original source step, separately from packing and uploads.
Repeated native preflight, checkout and helper/parameter preparation steps use descriptive aliases such as
`*check-native-linux-amd64`, `*checkout-workflow-source` and `*prepare-stage-<runner-artifact>`.
Preparation selects the current job through `github.job`; job policies, dependencies and operation calls
remain explicit.

The header points to fixed Plan and manifest files. Provider source-fetch names and ordering follow the
target and its consuming components, so routine source, artifact, toolchain or generator
updates can leave this workflow unchanged while generated data and action bodies change. This holds when
topology, labels, action interfaces and visible policy stay the same. A checkout-pin update, a split or merge
of shared source consumers, or a change to runners, runtime containers/services, permissions, secret
references, time limits, qualification or inline setup can still change the workflow. Transport pins remain
outside the workflow only when their calls can be extracted; the required inline final Run upload makes
upload-action pin updates visible in the maintained workflow. Always regenerate
and check the complete bundle after an input change, even when the workflow file has no diff.

The shared actions and invocation data are part of the same bundle: commit them with the workflow, Plan and
manifest when adopting a generated pipeline. `--check` covers every template and data file, and regeneration
retires unchanged obsolete actions through the previous manifest. Editing an action directly produces drift just like editing the
workflow. Producer jobs retain explicit dependencies and separate results; producer matrices and general
callable workflows remain deferred. The optional qualification bundle has its own fixed reusable native
worker and selected-platform matrix (ADR-21).

## Recover interrupted qualification generation

When generation includes `--qualification-policy`, an interrupted write can leave files such as
`.github/nvkr-qualification/manifest.stage`. `--check` reports the incomplete generation without repairing
it. Changing policy or other inputs before retrying can produce `CodeWriteConflict`: the saved stage does
not match the newly requested generation, so the writer preserves it and the published files.

1. Restore the exact Definition, Environment, catalogs and pins, qualification policy, and CLI build used
   by the interrupted command. Keep the same paths and output root.
2. Repeat that original `run generate` command without `--check`, then run it with `--check` to confirm
   recovery completed.
3. Reapply the intended policy or input changes, regenerate, and check again.

Do not delete stage files or ownership manifests to force an update. If the original inputs cannot be
established, retain the files for investigation instead of guessing their ownership. The producer and
qualification bundles have separate owners; their writes are not one atomic directory transaction.

Generation neither submits the workflow to GitHub nor builds, signs, or publishes artifacts. Continue with
[Plan inspection and comparison](plan-and-graph-inspection.md) or the [Run lifecycle](lifecycle.md).
