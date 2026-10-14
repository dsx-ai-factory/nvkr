# Use the Go CLI to demonstrate NVKR and NVKE

Run **`nvkr`** for catalog, input, Plan, artifact and release operations. Run **`nvke-demo e2e`** for the
complete Linux ARM64 build and tenant demonstration. These are the existing Go entry points; no new
shell wrapper is required. The [presenter script](nvkr-presenter.md) is spoken narration, not a program.

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
were checked against source `68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f` plus the local strict
18-argument launcher compatibility fix and optional walkthrough in this change; select a reviewed revision and use
that revision's help. Never silently mix a binary, inputs and runtime evidence from different revisions.

## Open the complete demo's authored inputs

The full ARM demo selects **29 named outputs** in one Definition. Start with its inputs,
not the separate three-output tutorial. Paths below are relative to the checkout selected by `--repo`.
For NVIDIA OVN, open these in this order:

| File | What to explain |
|---|---|
| `definitions/nvke-local-dev-arm64/definition.json` | The complete named output selection, including per-output catalog stacks. |
| Each selected stack's `registry.yaml` | Reusable build/acquisition producers, dependencies and instructions. |
| Each selected stack's `pins.lock.yaml` | CLI-managed exact source, artifact and tool identities. |
| `definitions/nvke-local-dev-arm64/os-tcg.json` | The explicitly selected OS producer overlay; it is an authored file. |
| `definitions/nvke-local-dev-arm64/environment-local-docker.json` | Native Linux ARM64 Docker execution; publication and signing are absent. |

The default catalog is `cli-catalog`; other outputs select their own complete stacks. The Go
`e2e --walkthrough` introduction enumerates the selected catalog and overlay paths from the actual
Definition and prints the actual output count. For explicit `upstream-ovn`, it instead reads
`definition-upstream-ovn.json`. It never changes the NVIDIA default to make a demo pass.

These read-only commands inspect the NVIDIA selection:

```sh
bin/nvkr catalog validate --data definitions/nvke-local-dev-arm64/cli-catalog
bin/nvkr catalog list --data definitions/nvke-local-dev-arm64/cli-catalog
bin/nvkr catalog source-pin --data definitions/nvke-local-dev-arm64/cli-catalog nvke
bin/nvkr definition validate --definition definitions/nvke-local-dev-arm64/definition.json
bin/nvkr definition inspect --definition definitions/nvke-local-dev-arm64/definition.json
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

## Run the full Linux ARM64 NVKE demo with its Go CLI

The full path is **NVKR builds → NVKE consumes those exact outputs → real tenant → workload/DNS →
natural tenant deletion → joined cleanup**. It is a separate, explicitly requested host-mutating operation.
Follow the existing [Ubuntu ARM64 preparation guide](local-nvke-demo.md): native storage, Docker/Buildx,
libvirt/QEMU, kernel modules, source access, runtime images and administrator capability checks.
Use a prepared dedicated Linux ARM64 host. A Darwin CLI build or Linux hosted inside Colima is not proof
of native Mac NVKE deployment. No VM workaround is selected by these commands.

Set the existing local source clone path, then choose a fresh attempt and output directory:

```sh
: "${NVKE_LOCAL_CLONE:?Set the absolute path of the local NVKE source clone}"
bin/nvke-demo e2e \
  --config demos/local-nvke.yaml \
  --repo "$PWD" \
  --nvke-clone "$NVKE_LOCAL_CLONE" \
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

This change adds optional `--walkthrough` (or `run.walkthrough: true` in explicit YAML). For `e2e`,
it prints the selected inputs, concepts, generated-record locators and source-pinned documentation links
after typed configuration admission, before the dashboard. This introduction is read-only presentation;
it does not establish platform support, execute producers or grant runtime admission. The unchanged
runtime checks still decide whether work can proceed. After the original workload/DNS check, bounded
read-only site/tenant observations show nodes, pods, rollouts and the existing Job's zero-retry result
before cleanup. The Job is never rerun to create success. No input pauses are added and TUI remains the default.

The existing Go entry point is retained. The private launcher contract requires exactly 18 arguments,
including its internal `--ui=plain`; missing, replaced, split or extra arguments remain rejected.
That internal child setting does not disable the outer demo's TUI.

The current runtime validation has not established a new complete accepted demonstration. Artifact
completion alone is insufficient. Full acceptance requires the same fresh Run's complete required-output
set, exact consumed bytes, two distinct real Ready tenant nodes, zero-retry workload/DNS success,
natural tenant deletion, joined owned cleanup and natural exit zero. Preserve failed originals and use
fresh resource inventory before recovery. Never call an incomplete recording a successful demo.

The full output retains `demo-inputs.json`, `site/site-inputs.json`, `site/tenant-result.json` and private
logs. The Run remains under `.nvkr/runs/<attempt>/lifecycle.json`. Keep credentials, node addresses,
private kubeconfigs and provider logs off the shared screen. Native Mac deployment is separate feature
work; the Darwin operator and Linux tenant runtime have distinct support and acceptance gates.
Do not merge their evidence.

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
**unqualified diagnostic** publication root, not a signed Release or the full 29-output deployment.
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

During `nvke-demo e2e`, the Go adapter separately starts its owned temporary registry, copies exact
selected runtime images, reads their descriptors back and installs the selected artifacts into the site.
This is local artifact distribution for the cluster, not release signing. Its retained `site/site-inputs.json`
records the selected outputs, references, observed deployment bindings and chart digests. Do not publish
private registry addresses or raw provider logs in a customer report.

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
These pages explain the baseline contracts; the walkthrough addition itself remains a local change
until reviewed and committed. Native Mac deployment is separate work and is not claimed by this guide.
