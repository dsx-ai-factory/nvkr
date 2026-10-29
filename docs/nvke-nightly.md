# Nightly NVKR builds and NVKE end-to-end tests

The authored [GitHub workflow](../.github/workflows/nvkr-nightly.yml) runs daily at
03:17 UTC and supports manual dispatch. It resolves NVKE `main` once, dispatches
the existing generated NVKR producer, and tests the artifacts that producer
built and published. The first maintained runtime target is NVIDIA OVN on the
native ARM64 `arc-arm64-kvm` runner.

The four coordinator jobs select a candidate, dispatch and join its producer,
select a completed candidate, and run its native Go E2E test:

1. Pass the cycle's exact NVKE commit to the existing
   `nvkr-run-nvke-nightly-arm64.yml` producer. Its admitted runtime source policy
   selects that commit for every source-built NVKE component, including disk
   materials and the installer. Independent dependency pins retain their owners.
2. Build the Definition's 34 outputs through the generated producer's existing
   execution, private transport, publication, signing and verification stages.
   Runtime preparation and finalization bind the same frozen source selection;
   checked-in catalog pins need no nightly version-bump commit.
3. Retrieve the exact producer Run, frozen Plan and aggregate publication locator.
   `PrepareExactInputs` and `VerifyExactInputs` authenticate the selected closure
   under its signing policy. The native consumer requires the exact-input receipt
   and producing invocation, and rejects a Plan with a different cycle commit.
4. Run `TestNightlyPublishedCandidate`. Existing Go owners extract the authenticated
   inputs and invoke the built `nvkectl bootstrap site`, using a temporary
   kind/Lima backend and a real QEMU/KVM site-controller VM.
5. Check the live Kubernetes API `/readyz`, the identified Ready node and native
   VM evidence, then stop and join owned VM, backend and network cleanup.

Manual `tenant_checks` additionally creates a tenant and checks workload and DNS.
The nightly consumer builds no product artifacts. The separate local
`TestEndToEnd` test retains its build-and-test developer flow; the nightly does
not invoke it. Published native conformance remains unsupported.

## Inputs and access

The maintained producer uses
[`arm64-nvidia-kvm.json`](../inputs/definitions/demo/arm64-nvidia-kvm.json) and
[`github-nightly-nvidia.json`](../inputs/environments/github-nightly-nvidia.json).
The Environment's `githubRuntimeSource` policy admits the exact NVKE commit
through the existing `run generate --runtime --source-commit` option. The
generator, source-pin and finalization owners validate that selection. Git
ancestry admission requires the selected commit to belong to `main`, allowing
the branch to advance during the cycle. A branch name, caller path or detached
digest alone grants no artifact authority.

Finalization repeats the proof against the then-current `main`. A delayed retry
outside the 64-commit fetch window, or a ref rewrite that removes the frozen
commit, fails closed before a Run record is issued even if signing and publication
already completed.

The coordinator's `NKE_SOURCE_TOKEN` resolves NVKE `main`; producer source access
uses its existing credential policy. Dispatch and metadata reads use separate
job-scoped GitHub token permissions. Product publication remains in the generated
producer. The runtime job receives only `NVKR_REGISTRY_READ` and
`NVKR_REGISTRY_READ_USER` for publication reads, and
`NVKR_CI_STORAGE_READ_TOKEN`/`NVKR_CI_STORAGE_READ_USER` for private CI transport.
The native runtime owners start candidate processes with their separate native
environment. Source code and the existing network helper remain trusted with
sudo and Docker access; this is no independent sandbox.

Binary bundles, OCI images, QCOW payloads and raw diagnostics use private storage
under the existing fail-closed transport/publication policies. Coordinator
Actions uploads contain only constructed selection JSON, exact producing-Run
locator JSON and the cycle verdict JSON. The runtime job has no registry writer
or OIDC signing permission, and Go's shared Actions cache is disabled.

No additional AMD64 producer bundle is introduced. A public AMD64 runner lane
requires its own admitted artifact production support before the coordinator
can select it. The upstream-OVN producer remains a separate manual diagnostic;
the maintained native consumer currently selects NVIDIA OVN only.

## Timeouts and evidence

Selection and completed-candidate selection have ten-minute job limits. Producer
dispatch/join and native runtime jobs each have a 360-minute limit; subprocesses
and setup operations have smaller bounds. The runtime Go test receives the time
remaining below a deadline set five minutes before its job limit. It reserves
125 minutes for native cleanup, verdict reporting and margin, and refuses to
start work when that reserve would consume the remaining time. SIGINT/SIGTERM
cancel work through the same owned cleanup lifecycle.

The Go cycle verdict is an unsigned repository integration result, outside
qualified release acceptance. Local contracts cover exact producer/source joins,
publication receipts, native input selection, deadlines and orderly signals.
They do not establish a completed hosted build, publication, bootstrap and cleanup
cycle. That full current ARM64 cycle remains unverified until a recorded run
completes.
