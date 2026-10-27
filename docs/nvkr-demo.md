# Use the Go CLI to demonstrate NVKR and NVKE

Run **`nvkr`** for catalog, input, Plan, artifact and release operations. The Go demo CLI's
`run` and `e2e` commands use one shared native deployment path: one QEMU site and two tenant guests.
NVKE's host-native `nvkectl` uses a temporary Kind bootstrap, then deploys the permanent site stack
and creates the tenant. Kind is removed before tenant startup; it is not the permanent site.
The [operational guide](local-nvke-demo.md#native-linux-and-mac-path) explains the native topology,
current validation limits and host prerequisites. `e2e` builds fresh artifact and native-tool Runs;
`run` consumes explicitly selected completed Runs. Each invocation retains its own evidence.

## Prepare the Go binaries

Use the selected NVKR checkout and the exact Go patch in `.go-version`:

```sh
cat .go-version
GOTOOLCHAIN=local make build demo
bin/nvkr --version
bin/nvke-demo e2e --help
```

`make build` produces `bin/nvkr`; `make demo` produces `bin/nvke-demo`. Record the full source commit
with `git rev-parse HEAD`. A working CLI does not prove an accepted tenant deployment. These commands
must use one reviewed source revision and that revision's help. Keep the binary, inputs and
runtime evidence bound to that revision.

## Open the complete demo's authored inputs

The maintained local ARM demo selects **32 named outputs**, or **38 with conformance**, in one Definition. Start with its inputs,
not the separate three-output tutorial. Paths below are relative to the checkout selected by `--repo`.
For NVIDIA OVN, open these in this order:

| File | What to explain |
|---|---|
| `inputs/definitions/demo/arm64-nvidia-local-kvm.json` | The complete local named output selection, including per-output catalog stacks. |
| `inputs/definitions/demo/arm64-nvidia-local-kvm-conformance.json` | The same selection plus version-matched Kubernetes conformance tools and images. |
| Each selected stack's `registry.yaml` | Reusable build/acquisition producers, dependencies and instructions. |
| Each selected stack's `pins.lock.yaml` | CLI-managed exact source, artifact and tool identities. |
| `inputs/catalogs/node-images/release` | The shared release OS and material producers. |
| `inputs/catalogs/node-images/variants/native-nvidia.json` | NVIDIA OVN producers over the shared release catalog. |
| `inputs/catalogs/node-images/variants/native-local-kvm.json` | Native agent stamp, MAC-stable DHCP staging and KVM settings. |
| `inputs/environments/local-arm64-kvm.json` | Native Linux ARM64 Docker/KVM build execution; publication and signing are absent. |

The default catalog is `inputs/catalogs/nvke/module-cli-arm64`; other outputs select their own complete stacks. The Go
`e2e --walkthrough` introduction enumerates the selected catalog and overlay paths from the actual
Definition and prints the actual output count. For explicit `upstream-ovn`, it instead reads
`inputs/definitions/demo/arm64-upstream-local-kvm.json`. It never changes the NVIDIA default to make a demo pass.

These read-only commands inspect the NVIDIA selection:

```sh
bin/nvkr catalog validate --data inputs/catalogs/nvke/module-cli-arm64
bin/nvkr catalog list --data inputs/catalogs/nvke/module-cli-arm64
bin/nvkr catalog source-pin --data inputs/catalogs/nvke/module-cli-arm64 nvke
bin/nvkr definition validate --definition inputs/definitions/demo/arm64-nvidia-local-kvm.json
bin/nvkr definition inspect --definition inputs/definitions/demo/arm64-nvidia-local-kvm.json
```

A customer saying “recipe” usually means a reusable catalog producer. The current schema selects
producers through Definition outputs; it does not require another authored Recipe object. Customers
edit authored Definition, catalog and Environment inputs, use `catalog pin`/`bump` for locks, and
regenerate owned files. See [catalog authoring](catalogs-and-components.md).

## Inspect the selected attempt's generated Plan and Run

The Go demo prepares a snapshot under `.nvkr/laptop-bootstrap/<attempt>/`, including `definition.json`
and `selected-plan.json`. This generated attempt selection is separate from the authored inputs above.
After preparation has actually written it, inspect that exact Plan:

```sh
: "${NVKR_ATTEMPT:?Set the exact fresh demo attempt name}"
bin/nvkr run plan --plan ".nvkr/laptop-bootstrap/$NVKR_ATTEMPT/selected-plan.json"
bin/nvkr run graph --plan ".nvkr/laptop-bootstrap/$NVKR_ATTEMPT/selected-plan.json" \
  --view complete --format mermaid
```

After execution has written the Run record:

```sh
bin/nvkr run inspect --record ".nvkr/runs/$NVKR_ATTEMPT/lifecycle.json"
bin/nvkr run inspect --record ".nvkr/runs/$NVKR_ATTEMPT/lifecycle.json" --json
```

The Plan explains intended work; the Run records actual results. A Build Digest identifies logical
inputs; artifact digests identify produced bytes. A missing record is not success. Preserve failed
originals and keep private diagnostics off the shared screen. For the smaller general-purpose tutorial,
use [the M1 walkthrough](../demos/m1-local.md); it is a different selection.

## Run the full native ARM64 NVKE demo with its Go CLI

The full path is **NVKR builds → NVKE consumes those exact outputs → real tenant → workload/DNS →
natural tenant deletion → joined cleanup**. It is a separate, explicitly requested host-mutating operation.
Follow the [native Mac and Ubuntu ARM64 preparation guide](local-nvke-demo.md#native-linux-and-mac-path).
Both hosts execute the Go demo and `nvkectl` locally; their virtualization and temporary
container-backend prerequisites are platform-specific.
The native Linux bootstrap integration is still under validation; successful artifact builds alone do not
establish a working deployment.

Choose a fresh attempt and output directory. The default fetches pinned dependency sources from
their catalog URLs; pre-existing dependency repositories are not required:

```sh
bin/nvke-demo e2e \
  --repo "$PWD" \
  --attempt presentation-arm-01 \
  --out "$PWD/.nvkr/presentation-arm-01-demo" \
  --jobs 2 \
  --network ovn \
  --walkthrough \
  --cleanup
```

`ovn` is **NVIDIA OVN**, the default. Changing that explicit argument to `upstream-ovn` selects the
alternate diagnostic; it never validates the NVIDIA fork. The Go CLI does not silently switch suppliers.
The existing CLI chooses its outer TUI in an interactive terminal; `--plain` selects scrolling progress.
`--jobs` bounds dependency-ready operations, not the number of required outputs. Set it for the admitted
host's actual capacity. `--cleanup` requests natural tenant deletion and joined cleanup before return.
Omitting it instead retains a Ready environment and is not full cleanup acceptance.

For `e2e`, optional `--walkthrough` (or `run.walkthrough: true` in explicit YAML)
prints the selected inputs, concepts, generated-record locators and source-pinned documentation links
after typed configuration admission, before the dashboard. This introduction is read-only presentation;
it does not establish platform support, execute producers or grant runtime admission. The unchanged
runtime checks still decide whether work can proceed. After the original workload/DNS check, bounded
read-only native site/tenant observations show API readiness, node roles and anonymous pod status/restarts
before cleanup. The host workflow enforces the preceding zero-retry workload/DNS
acceptance. The Job is never rerun to create success. No input pauses are added
and TUI remains the default.

The inner NVKR command uses `--ui=plain` so its progress can feed the outer demo dashboard.
That child setting does not disable the outer TUI. Explicit build inputs also capture compiler inputs
through NVKR's existing input owner, including the closure needed by local conformance reporting.

For conformance on either native host, add `--conformance`. NVKR builds the version-matched tools in the same fresh
artifact Run, then runs them against that invocation's tenant before cleanup. Retain the original JUnit
and inspect its failure/error counts. This local report does not upload results to TestGrid automatically.

The current runtime validation has not established a new complete accepted demonstration. Artifact
completion alone is insufficient. Full acceptance requires the same fresh Run's complete required-output
set, exact consumed bytes, two distinct real Ready tenant nodes, zero-retry workload/DNS success,
natural tenant deletion, joined owned cleanup and natural exit zero. Preserve failed originals and use
fresh resource inventory before recovery. Never call an incomplete recording a successful demo.

The native output retains prepared assets and a private `runtime` directory with site/tenant kubeconfigs,
observations and the native result when completed. The artifact Run remains under
`.nvkr/runs/<attempt>/lifecycle.json`; the native-tool Run is under `.nvkr/native-tools/runs/<attempt>`.
Keep credentials, node addresses, private kubeconfigs and provider logs off the shared screen.
Mac and Linux share source and orchestration, while each requires its own complete runtime result.

## Publish and inspect an exact local diagnostic package

After a completed Run, set a fresh review directory and the actual completed Run path:

```sh
NVKR_REVIEW_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-demo.XXXXXX")
NVKR_REVIEW_DIR=$(cd "$NVKR_REVIEW_DIR" && pwd -P)
```

The Go CLI can create and inspect a diagnostic OCI publication from its exact CLI
package. Select the actual completed Run directory; the commands do not rebuild it:

```sh
: "${NVKR_COMPLETED_RUN:?Set the absolute completed Run directory}"
bin/nvkr publish root \
  --bundle "nvkectl=$NVKR_COMPLETED_RUN/packages/nvkectl" \
  --out "$NVKR_REVIEW_DIR/cli-publication"
bin/nvkr publish inspect --layout "$NVKR_REVIEW_DIR/cli-publication"
```

The output directory must be absent. This validates the selected package and creates a local
**unqualified diagnostic** publication root, not a signed Release or the full deployment.
To push that diagnostic to an explicitly selected existing development registry, use:

```sh
: "${DEMO_PUBLICATION_TARGET:?Set the explicitly owned development registry/repository:tag}"
bin/nvkr publish push --layout "$NVKR_REVIEW_DIR/cli-publication" \
  --to "$DEMO_PUBLICATION_TARGET" --allow-diagnostic
```

This is an external write; run it only for the chosen destination and authorized demo. TLS is default.
Add `--plain-http` only for a supported loopback development registry. Registry setup and credentials
remain customer-owned; these commands do not start a registry or obtain authorization implicitly.
The push prints a digest-pinned reference. It does not create a signature or qualification.

During the native demo, the adapter distributes the selected verified local image archives to the
bootstrap backend and site startup. NVKE consumes those images for its controller, providers and runtime
services. This is local artifact distribution for the cluster, not release signing. Retain the selected Run
and native input/output records; keep private endpoints and raw provider logs out of customer reports.

## Verify an existing published Release

For an existing published release, obtain the exact publication-root reference from its publisher:

```sh
: "${RELEASE_REF:?Set the exact digest-pinned publication-root reference}"
bin/nvkr release browse --reference "$RELEASE_REF"
bin/nvkr release export --reference "$RELEASE_REF" > "$NVKR_REVIEW_DIR/release-description.json"
```

Browsing/export validates metadata but does not verify signatures or payload bytes. Follow
[release verification](release-quickstart.md) with an independently accepted signer/issuer and pinned
verifier for the supported policy scope. Local Runs above do not sign or publish a Release; rendered
hosted signing/publication jobs have not executed merely because generation passed. Do not claim
qualification, conformance, reproduction, production readiness or NVIDIA-fork acceptance from a Plan,
local artifact or alternate-network result. NVKR owns the build and artifact handoff; NVKE owns deployment.

## Source-pinned explanations

The Go introduction links directly to the reviewed source baseline's
[concept reference](https://github.com/dsx-ai-factory/nvkr/blob/68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f/docs/user-concepts.md),
[catalog authoring](https://github.com/dsx-ai-factory/nvkr/blob/68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f/docs/catalogs-and-components.md),
[documentation map](https://github.com/dsx-ai-factory/nvkr/blob/68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f/docs/README.md) and
[ARM demo prerequisites](https://github.com/dsx-ai-factory/nvkr/blob/68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f/docs/local-nvke-demo.md).
These links identify the historical narration baseline. Use the documentation at the selected source
revision to assess current implementation and recorded validation; historical explanations do not establish
a fresh successful Mac or Linux invocation.
