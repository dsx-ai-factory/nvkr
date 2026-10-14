# Present the Go CLI demo in fifteen minutes

The story is **recipe/catalog and Environment → build → publication → cluster**. This is spoken
narration for the existing `nvkr` and `nvke-demo` Go CLIs, not a shell wrapper. Keep the
[runnable command guide](nvkr-demo.md) open. Prepare binaries and select actual Run/recording evidence
before the meeting; a large build should start before the presentation slot.

## Explain what the ARM demo actually does

Use this short explanation while introducing the recording:

1. **Check the host.** “The Go CLI checks the native ARM64 Docker setup, required images, administrator
   access and build resources. Docker and QEMU/libvirt are host prerequisites; VM tools are checked before
   provisioning.”
2. **Prepare pinned tools and inputs.** “It pulls the pinned build images, fetches selected sources and
   dependencies, and builds the tools selected by the catalog. These include Kind, kubectl, Helm,
   clusterctl and ORAS.”
3. **Build the artifacts.** “NVKR executes dependency-ready work in parallel. It produces the CLI,
   container images, charts and ARM64 QCOW2 disk. Packer uses a temporary QEMU build VM while assembling
   that disk; this build VM is separate from the later tenant.”
4. **Start management in Kind.** “The demo extracts and checks the selected Kubernetes tools, then creates
   one Kind management cluster in Docker. It delivers selected images through a local registry and installs
   NVKE controllers, Cluster API and backing services.”
5. **Create the tenant in QEMU.** “NVKE sends provisioning requests through the local infrastructure
   simulator. Its libvirt backend starts two QEMU VMs from the produced QCOW2 disk: one control plane and
   one worker. These VMs form the tenant Kubernetes cluster.”
6. **Show the cluster working.** “The demo waits for both tenant nodes to become Ready, runs its existing
   workload and DNS check, and displays read-only kubectl observations of nodes, pods, rollouts and the Job.”
7. **Delete and clean up.** “With cleanup selected, NVKE deletes the tenant through its normal workflow.
   The demo waits for deletion and stops its owned management, registry and simulator resources, then restores
   the host networking it changed.”

Kind hosts management services in Docker containers. The tenant runs in separate QEMU VMs managed by
libvirt. NVKR owns build and artifact delivery; NVKE owns installation and the tenant lifecycle.

## 0:00–3:00 — Recipe, catalog, pins and Environment

Say: “Start with what we want to deliver. The Definition selects named outputs from reusable catalog
producers. The catalog tells NVKR how each component is produced; pins identify its exact source,
artifact and tool inputs. The Environment supplies where and how the work executes.”

```sh
bin/nvkr --version
bin/nvkr catalog list --data definitions/nvke-local-dev-arm64/cli-catalog
bin/nvkr catalog source-pin --data definitions/nvke-local-dev-arm64/cli-catalog nvke
bin/nvkr definition inspect --definition definitions/nvke-local-dev-arm64/definition.json
```

Open `definitions/nvke-local-dev-arm64/definition.json` first, then the selected catalog stacks'
`registry.yaml` and `pins.lock.yaml`, the authored `os-tcg.json` overlay, and
`environment-local-docker.json`. The Go `e2e --walkthrough` opening lists these paths relative to `--repo`. Say: “A customer can choose components, update pins or replace a producer,
then review the effect before execution. Here the full Definition requests 29 named outputs: OS, CLI, services, providers, charts, tools, profiles and simulator.”

If asked about recipes: “The reusable recipe is a catalog producer. The current schema selects it
through Definition outputs; there isn't another authored Recipe object to synchronize.”

## 3:00–6:00 — Build with the Go CLI

After preparation, show this attempt's generated Plan and its graph, separate from the authored Definition:

```sh
bin/nvkr run plan --plan ".nvkr/laptop-bootstrap/$NVKR_ATTEMPT/selected-plan.json"
bin/nvkr run graph --plan ".nvkr/laptop-bootstrap/$NVKR_ATTEMPT/selected-plan.json" --view complete --format mermaid
```

Say: “NVKR derives the operations and dependencies. A Build Digest identifies the logical build inputs;
an artifact digest identifies the bytes actually produced. Every required output needs its own result.”

Point to the prepared build dashboard or recording. The full product entry point is:

```text
nvke-demo e2e --repo <selected checkout> --nvke-clone <source clone>
  --attempt <fresh attempt> --out <fresh directory> --jobs <admitted concurrency>
  --network ovn --walkthrough --cleanup
```

Say: “This Go command prepares pinned build tools and sources, builds the complete selected artifact
Run, then passes those exact outputs to NVKE. Jobs controls concurrency among dependency-ready operations.
The OS-image producer uses Packer and a temporary QEMU VM to assemble the QCOW2 disk.” The placeholder command is narration;
use the runnable invocation and exact paths from the guide for execution. The walkthrough prints the concept
and documentation opening without pauses, retains the default TUI, then adds read-only observations after
the unchanged workload/DNS check.

Inspect the selected actual completed artifact Run:

```sh
: "${NVKR_RUN_RECORD:?Set the exact lifecycle.json path}"
bin/nvkr run inspect --record "$NVKR_RUN_RECORD"
```

Show the complete required-output set and observed artifact digests. The Plan explains intended work;
the completed Run records what this attempt produced. Keep that distinction to one clear sentence.

## 6:00–8:00 — Publish the resulting artifacts

Say: “Now the build outputs become distributable packages, identified by their actual bytes.”
For a selected completed CLI package, the direct Go CLI demonstration is:

```sh
bin/nvkr publish root \
  --bundle "nvkectl=$NVKR_COMPLETED_RUN/packages/nvkectl" \
  --out "$NVKR_REVIEW_DIR/cli-publication"
bin/nvkr publish inspect --layout "$NVKR_REVIEW_DIR/cli-publication"
```

Set the variables from the guide beforehand and use an absent publication directory. Say:
“This local root validates and packages the exact CLI bytes. It is an unqualified diagnostic. An explicitly
authorized registry push returns an immutable reference; signing and consumer policy are separate.”

For the cluster path, say: “The Go demo distributes selected images through its own local registry,
checks the returned descriptors and hands the exact image references and chart bytes to NVKE.”
Point to the accepted recording's publication/readback stage or the corresponding retained evidence.
Do not confuse this temporary registry with a public signed software Release.

## 8:00–12:00 — Show NVKE consuming the artifacts as a cluster

Narrate the accepted recording or actual owned live session: management services in Kind, two separate
QCOW2 tenant nodes, the workload and cluster DNS, then natural tenant deletion and joined cleanup.

Say: “Kind runs the management Kubernetes cluster in Docker. NVKE's local infrastructure simulator
provisions the tenant through libvirt: two QEMU VMs boot the produced QCOW2 disk, one control plane and
one worker. We check both nodes Ready and execute the existing workload/DNS check inside the tenant.
Finally NVKE deletes the tenant through its normal workflow.”

Name the actual networking selection. NVIDIA OVN is the default. If the evidence uses upstream OVN, say:
“This is the explicitly selected upstream diagnostic; it does not validate the NVIDIA fork.”

Show only read-only cluster observations using the current owned session's exact printed private
kubeconfig locators, or their observations in the accepted recording. Explain the visible node, pod,
rollout and already-run Job results. Do not rerun the workload to manufacture success.

If full runtime has not passed, keep the narration brief and concrete: “The complete artifact Run passed;
the cluster attempt stopped at <verified stage>. The current blocker is <verified observation>.”
Show the completed artifact/publication parts and the actual blocked stage. Do not substitute an old
cluster or invent a cause. The user still sees what worked and exactly where the remaining gate sits.

## 12:00–15:00 — Customer use, verification and questions

Say: “The customer edits the Definition and Environment, updates pins through NVKR, reviews the Plan,
then runs the supported Go command. Generated pipelines and recorded artifacts carry the exact selection
forward. NVKR owns builds and delivery; NVKE owns installation and runtime.”

For an existing exact published Release, `nvkr release browse` navigates metadata. Verification separately
checks the supported claim under the customer's accepted signer/issuer policy and pinned verifier.
The CLI introduction includes these source-pinned links; open one if a customer wants to go deeper:

- [Concepts: Definition, Environment, Run, Artifact, Release and Verification Policy](https://github.com/dsx-ai-factory/nvkr/blob/68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f/docs/user-concepts.md).
- [Catalog producers, materials, pins and customer authoring](https://github.com/dsx-ai-factory/nvkr/blob/68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f/docs/catalogs-and-components.md).
- [Documentation and task map](https://github.com/dsx-ai-factory/nvkr/blob/68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f/docs/README.md).
- [Full ARM demo prerequisites and support boundaries](https://github.com/dsx-ai-factory/nvkr/blob/68b09b8e71048fdbcbd4a11b4b7d1264a1e04c6f/docs/local-nvke-demo.md).

Point to [runnable commands](nvkr-demo.md), [catalog authoring](catalogs-and-components.md),
[pipeline generation](generate-pipeline.md) and [release verification](release-quickstart.md).
Take questions about the demonstrated workflow and the concrete remaining work.

## Presenter preparation

A Plan or rendered workflow is not execution. A local diagnostic publication is not a signed Release.
A signature is not qualification or production readiness. Full tenant acceptance needs the same fresh
Run's exact artifacts, two real Ready nodes, zero-retry workload/DNS, natural deletion, joined cleanup
and natural exit zero. Use an accepted recording only; an incomplete attempt remains incomplete.
Readiness is not cleanup, and source-defined commands are not proof that the video visibly showed them.
Keep addresses, credentials, kubeconfigs and raw provider logs private. State a verified blocker plainly;
don't turn the presentation into a failure analysis or promise an unverified ETA.
