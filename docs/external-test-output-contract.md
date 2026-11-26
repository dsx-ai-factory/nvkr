# External test output contract

`pkg/nvkr/testresults` describes outputs supplied by external test producers. Its lane is **none
(descriptive)**. It supplies an interface, wrapper validation and deterministic serialization, with no
producer implementation, execution, report parser, file collector, signing or qualification integration.
[Reconciliation](adr/reconciliation/README.md) owns implementation and disabled-path status.

## Producer boundary

`Producer.Produce(context.Context) (Output, error)` is the output implementation interface.
[ADR-16 §6](adr/0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
owns its execution and authority boundary. The fields below describe its output; execution requires the separate explicit diagnostic profile below.

`Output` uses schema `nvkr.dev/test-results-output/v1`. It describes `ProducerIdentity`,
`SuiteSelection`, optional `HarnessIdentity`, `Reports []ReportReference`,
`Environment []EnvironmentFact`, `CaptureStatus` and optional NVKR `Binding`. Suite
name/selection/revision and harness name/version stay separate. Raw credentials do not belong in output.

`CaptureStatus` is `complete` or `partial`. Complete requires at least one report reference; partial
may contain none. This declares capture availability, not suite success, format validity or passing tests.
A normal nonzero harness exit can accompany complete capture: original bytes, terminal wrapper cleanup,
matching observations and live subject bindings must all be retained. The observed `harness/process.exitCode`
fact has an unknown cause and cannot distinguish failed tests from a late harness failure. CLI success means
capture completed, not that reports are valid, a suite completed or tests passed. Signals, launch failure,
timeout/cancellation, capture I/O errors, missing results, identity drift and failed wrapper cleanup remain
partial/error. An error may accompany partial diagnostic output. Metadata status is `declared`, `observed`, `unknown`
or `unavailable`; unknown/unavailable carries no value. Environment facts have explicit scope and
`before`, `during`, `after` or `unknown` phase. Observed metadata is descriptive, not independently
authenticated. Missing data stays absent or unknown; never fabricate a Run/attempt, timestamp,
installed artifact, successful cleanup or passing result.

## Original report references

A producer may reference original JUnit XML, CTRF JSON, or both. `ReportReference` declares format,
profile, opaque location, exact SHA-256 and byte length. Validation never opens that location or
recomputes its digest. A future collector must independently check the original bytes. Independent
JUnit/CTRF files need no conversion link. Optional `DerivedFrom` names another included original
reference, with one derivation step only; it proves no conversion. No report bytes are rewritten.

Formats are `junit-xml` and `ctrf-json`. JUnit requires a named producer dialect; no universal JUnit
schema or planned adapter is admitted. CTRF requires profile `ctrf-0.2.0`, referring to
[working draft 0.2.0](https://github.com/ctrf-io/ctrf/blob/e325eb33c7662a208bfa4d093a41cc55439d6e6a/spec/ctrf.md)
and its normative
[Draft-07 JSON Schema](https://github.com/ctrf-io/ctrf/blob/e325eb33c7662a208bfa4d093a41cc55439d6e6a/schema/ctrf.schema.json),
pinned to commit `e325eb33c7662a208bfa4d093a41cc55439d6e6a` from October 10, 2026.
The upstream [license](https://github.com/ctrf-io/ctrf/blob/e325eb33c7662a208bfa4d093a41cc55439d6e6a/LICENSE)
is MIT. Referencing that source establishes neither dependency approval nor format validation.

Authoritative format validation is deferred. The dependency set has no JSON Schema validator, and
no JUnit profile parser is supplied. Wrapper validation does not interpret result counts, evaluate
coverage or judge results passing. A format label is not a validity claim.

## Run configured diagnostic checks

`nvkr validate` delegates through SDK `Client.Validate` to `validation.RunFiles`. No argument runs
the profile's configured supported checks; one positional name selects that configured check. Supply
flags before the optional check name, following the existing CLI parser convention:

```sh
nvkr validate --config validation.json --workspace .nvkr/validation
nvkr validate --config validation.json --workspace .nvkr/validation cluster-conformance
```

This route requires Linux execution support, a complete embedded HTTPS kubeconfig, and an explicitly
acknowledged existing disposable local cluster. That acknowledgement proves neither locality nor exclusive
ownership; selected tools execute on the host with cluster credentials, without a sandbox. See
[ADR-18 §3](adr/0018-pipeline-ir-and-ci-adapter-emission.md#3-helper-owned-isolation-and-narrowly-admitted-native-arc-disks).
For `source.kind: published`, product and tools roots must be signed `per-output-v1` hosted publications carrying a qualification handoff;
unsigned local/dry-run and legacy aggregate outputs cannot supply this capability. Both policy paths name
independently selected qualification-policy documents in the [M3 policy format](lifecycle.md#authenticate-a-qualification-candidate).
Set `NVKR_GITHUB_TOKEN` for producing-Run and handoff reads. Trust policy and verifier pins remain independent
of downloaded records.

The profile explicitly selects a published source and independently selected policy. Replace the
reference placeholder with the immutable publication root; there is no automatic latest lookup:

```json
{
  "source": {
    "kind": "published",
    "reference": "<publication-repository>@sha256:<reviewed-root-digest>",
    "policy": "qualification.yaml"
  },
  "checks": [{"name": "cluster-conformance", "suite": "kube-conformance", "harness": "hydrophone", "config": "conformance.json"}]
}
```

Registry authentication is explicit. The CLI selects anonymous access only when `NVKR_REGISTRY`,
`NVKR_REGISTRY_USER` and `NVKR_REGISTRY_PASSWORD` are all absent. Otherwise supply the complete
host-scoped tuple; partial credentials fail owning admission. SDK callers must set
`ValidationOptions.Registry.Anonymous = true` or supply complete matching scoped credentials.
Registry reads use the selected anonymous or host-scoped transport, and OCI cosign children receive private
Docker/Podman configuration rather than ambient configuration. This does not disable every cosign provider
keychain: ambient cloud/provider variables and helper lookup may still affect cosign verification. Anonymous
transport selection is not proof of anonymous cosign provider access. Provider read credentials remain
independently explicit; registry/provider secrets are not forwarded in the cluster tools' restricted environment.

Relative policy and check-configuration paths resolve beside the profile. Lifecycle owns publication,
source-Run lookup and artifact authentication; customers need no GitHub Run URL. An unsigned local
Run or record cannot restore that hosted receipt. The separate explicit local diagnostic route below
captures local bytes through its own concrete lifecycle owner. This route supports native
Linux tools and Darwin ARM64 tools. Darwin uses owner-private executable snapshots with byte
checks before/after commands and joined cleanup; it does not provide Linux sealed execution
against a hostile same-user writer. Published-source validation remains Linux-only.
Linux OCI suite images and exact Run/output bindings are unchanged.
Check `name` is a configurable unique instance name; `suite` and `harness` select separate supported
protocols. The initial suite/harness pair is `kube-conformance` / `hydrophone`. Unsupported pairs,
including user/AI adapters, return `CodeUnsupported` before provider or harness work. New producers
can implement the shared interface but require a concrete supported adapter; no plugin framework, shell
configuration, Definition/product detector or implicit applicability rule is implemented.

The producer configuration independently selects its tools publication and policy:

```json
{
  "toolsSource": {
    "reference": "<tools-publication-repository>@sha256:<reviewed-root-digest>",
    "policy": "tools-qualification.yaml"
  },
  "runner": {
    "hydrophone": {"output": "<executable-output>", "member": "<bundle-member>", "platform": "linux/arm64"},
    "kubectl": {"output": "<executable-output>", "member": "<bundle-member>", "platform": "linux/arm64"},
    "conformanceImage": {"output": "<conformance-image-output>", "platform": "linux/arm64"},
    "busybox": {"output": "<busybox-image-output>", "platform": "linux/arm64"},
    "kubeconfig": {"path": "cluster.kubeconfig", "digest": "sha256:<access-file-digest>"}
  },
  "timeouts": {"run": "2h", "command": "1m", "cleanup": "5m", "startup": "5m"}
}
```

This fragment shows source/member selection; the remaining runner fields are required: `context`,
`cniSelector`, optional `cniNamespace` (defaults to `kube-system`), declared `imageVersion`, `suiteName`, `role: local`, `disposableCluster: true` and explicit `targets`. Each target specifies
`output`, `namespace`, `labelSelector`, `container`, and positive `requiredReplicas`.
The complete profile's source and suite/harness selections are admitted before execution; each
referenced producer configuration is admitted when its check starts. Invalid settings for a later
check can therefore stop execution after earlier checks have run. Outputs retain the producer's
suite name and follow execution order; profile instance names are not serialized into the output.
Invoke one named check when a consumer requires an explicit invocation-to-result association.

Environment overrides are unsupported. Relative tools-policy and kubeconfig paths resolve beside
this configuration. Product and tool sources are independently selected and may name the same
publication root. `conformanceImage` and `busybox` select exact native OCI image outputs from the same authenticated tools publication; raw
caller image references are not accepted. `imageVersion` is declared protocol metadata checked against
server major/minor, not source/version proof. Harness version remains explicitly unknown.

Tools use ordinary catalog-produced executable outputs, authenticated by the existing lifecycle
owner before exact member export, holding both publication receipts through use. No globally installed
Hydrophone/kubectl or caller-supplied executable path/digest is selected. Member exports undergo
native static Linux executable checks. The example names are placeholders, not an approved tool
catalog or published candidate. Tool source/security approval must be established for the selected
publication; exact-byte authentication alone supplies no such approval. Kubeconfig is a self-contained,
owner-only pinned access input for an existing disposable cluster. The acknowledgement is operator
responsibility, not proof of locality or exclusive ownership.

The current binding supports selected native OCI images only. Before/after API checks match PodSpec
and image-ID to the authenticated native manifest selected by node platform, and compare
the `kube-system` namespace UID as a cluster-identity surrogate, plus mapped pod/node/container
identities and restart counts. They describe those two snapshots, not
continuous identity during execution, installed-byte attestation or coverage of unmapped outputs/index
children. No cluster is provisioned. Site/tenant execution is unavailable. The previously reviewed
[Hydrophone `5fe47daaff5fe025215cb257118dc2a0e643e1d6`](https://github.com/kubernetes-sigs/hydrophone/blob/5fe47daaff5fe025215cb257118dc2a0e643e1d6/cmd/root.go)
has name-based automatic normal cleanup; that source review does not establish the revision of a
selected published executable. Wrapper recovery retains UID preconditions.

Hydrophone directly runs the upstream Ginkgo conformance suite and fetches original JUnit. The initial
adapter selects that narrow route. [Sonobuoy](https://sonobuoy.io/docs/v0.56.7/plugins/) offers broader
plugin orchestration and [archive/result processing](https://sonobuoy.io/docs/v0.56.7/results/); it was
not rejected because of a demonstrated technical failure. That additional control plane is not required
by this initial adapter. No report parsing, CTRF conversion or passing verdict is supplied. The CLI
emits a canonical array of output descriptions, preserving partial outputs and original errors. These
outputs never activate qualification signing or publication; external execution of this rewritten route
is not yet established.

## Plan what to validate for a release

`nvkr validate --plan` describes what to validate for one published release and runs nothing. It
authenticates the release under a qualification policy, reads the outputs from that authenticated
description, and builds a validation plan (`nvkr.dev/validation-plan/v1`) from the planners an authored
directory lists:

```sh
nvkr validate --plan \
  --reference <repository>@sha256:<reviewed-root-digest> \
  --policy qualification.yaml \
  --planners inputs/testing/planners \
  --planner-var nvke-qualify.nvke-root=./nvke-source \
  --planner-var nvke-qualify.overrides=os-image-amd64=https://images.example.test/node-amd64.qcow2 \
  --plan-out validation-plan.json
```

The plan is a list of targets sorted by id. Each target names the executor that runs it and the inputs that
executor takes, so independent workflow jobs can run the targets in parallel. A target's inputs list every
component the executor is given, each with the version, the digest-pinned registry reference or served URL
that applies to it, and an origin: `release` for a value read from the authenticated release, `declared` for a
version a reviewer wrote down, and `resolved` for a value the planner's own strategies produced, which the
release does not vouch for. The executor pulls by digest and never reads the release again.

`--plan` takes no check name, `--config` or `--workspace`, and the plan flags are refused without `--plan`.
`--planner-var` is `planner.key=value`. Only that planner receives it, and each planner rejects a variable it
does not take and owns the form of its values. Any failure returns a coded
error and writes nothing. The same release and inputs produce byte-identical output.

Each planner also makes a plan in its own format. `--planner-out <dir>` writes it to
`<dir>/<planner>.json` in canonical encoding, and each target's `source.planDigest` names its typed digest
(`nvkr.planner-plan/v1`), which is recomputed by decoding the file and canonicalizing the value and is not a hash
of the file's bytes. The validation plan does not carry these plans, and an executor takes everything it needs
from its target. The planner plans are written before the plan, so a written plan never names a missing file.

A planners directory holds `config.yaml` and each planner's own files. Each entry pairs a built-in planner
with the executor that runs its targets and the planner's own configuration. Planners are a closed set
compiled into `nvkr`, and executors are workflow jobs that this repository does not run. The
`nvke-qualify` planner resolves the release through NVKE's qualification planner. The maintained inputs are
under [inputs/testing/planners](../inputs/testing/planners/README.md).

A plan is descriptive and carries `qualificationAuthority: "none"`. It issues no receipt, signs nothing,
and gates no release. Planning establishes neither that a target passes nor that its inputs exist where an
executor would look. Only fixture-based contract tests exercise it. No real published release has been
planned, and no executor consumes a plan yet.

### Plan for a local Run

`nvkr validate --plan --local-run <attempt directory>` plans for a complete local Run instead of a published
release, so planning can be tested without a registry, credentials or `cosign`. The existing local owner checks
the Run's Plan, compiler inputs, output membership and held payloads, and signs and publishes nothing. A local
output has no registry location, so the plan names each one as `local/<attempt>/<output>@<digest>`, which no
executor can fetch. `--local-run` replaces `--reference` and `--policy`, and the command refuses a mix.

The Run has to retain its compiler inputs, so build it with `--capture-inputs-root=.` and repository-relative
Definition and Environment paths. [demos/hello/planners](../demos/hello/planners) maps the hello demo's one
output to a component. From the repository root, with Docker running:

```sh
bash demos/demo-local.sh                      # prints a workspace such as .nvkr/demo-local.XXXXXX
W=.nvkr/demo-local.XXXXXX
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_COUNT=1 \
  GIT_CONFIG_KEY_0="url.$PWD/$W/source.insteadOf" GIT_CONFIG_VALUE_0=https://demo.example.invalid/hello
bin/nvkr run local --definition $W/definition.yaml --environment $W/environment.yaml \
  --attempt hello-captured --attempts-root .nvkr/runs --runner-source . --capture-inputs-root=. --ui plain
bin/nvkr validate --plan --local-run .nvkr/runs/hello-captured --planners demos/hello/planners \
  --planner-var nvke-qualify.nvke-root=/tmp
```

`nvke-root` can be any existing directory here, because the hello registry uses no NVKE-tree strategy. This
exercises the lifecycle capture, the planner and the plan encoding. It does not exercise hosted
authentication, a registry, or the output names of a real NVKE release.

## Optional post-publication bindings

Standalone outputs need no NVKR binding. A supplied `Binding` may describe input identities, named
subjects, or both. `Inputs` reuses `lifecycle.ExactInputDescription`: it is a **post-publication**
description requiring `PolicyDigest`, `DefinitionDigest`, `EnvironmentDigest`, `PlanDigest`,
`ContentSubject` and an exact immutable `ReleaseSubject` together. It cannot describe a partially
populated pre-publication Plan. `PolicyDigest` remains a descriptive policy reference; validation
neither authenticates that policy nor applies its enforcement rules.

Named subjects use `SubjectDescription { ref, digest }` and optional concrete native platform.
They are descriptions, not signing subjects or authenticated artifact receipts. Absent platform
and native platform descriptions cannot be mixed for one named output. Reference shape, input
identities and subject digests are validated by their existing owners without authenticating referents.

## Validation and serialization

`Output.Validate()` checks wrapper shape and declarations; success does not guarantee the aggregate
document fits the serialized byte limit. `Encode(Output)` validates, defensively
copies and sorts descriptions through the existing canonical codec. `Decode([]byte)` admits at most
16 MiB, validates exact canonical bytes and rejects non-normalized collection order. Selection,
report and subject collections are bounded to 1,024 entries each; environment facts to 10,000.
Optional empty collections have one omitted wire representation. These operations perform no file
I/O, original-byte hashing, result parsing or execution. Malformed descriptions use `CodeUsage`;
unsupported versions, enums and profiles use `CodeUnsupported`. Nested owner errors retain their
coded cause and context.

Deterministic serialization supplies no new digest family or structured identity. Output is
**producer-supplied descriptive data**, not an authored lifecycle object, authenticated receipt or
publisher evidence. Existing validation-vector v3 (including `ConformanceClaim` and
`ValidationVectorRef`) and qualification-evidence v2 remain unchanged. This output cannot authorize
artifact verification, qualification signing, promotion or release, nor establish current cluster health.
The configured diagnostic route supplies capture and selected-subject observations. Format adapters,
coverage policy and independently authenticated external qualification handoff remain unimplemented;
contract tests establish no external runtime acceptance.

## Complete local diagnostic Runs

Use `source: {"kind":"local","workspace":".nvkr/runs/<attempt>"}` to test an
explicit disposable local cluster against a complete `unqualified-local-execution` Run.
Run paths are canonical repository-relative paths; invoke `nvkr validate` from that repository.
The original Plan, compiler inputs, complete output membership and exact held payloads are checked.
A local record cannot substitute for a signed publication, and this route signs no qualification.

The check configuration uses `toolsWorkspace` instead of `toolsSource` and the same `runner`
and `timeouts` descriptions. `bindings.productImages` maps an exact output to its deployed canonical
OCI alias; `bindings.toolImages` maps each tool-image output to a canonical digest reference.
Both arrays contain objects with `output` and `reference`. Tool image digests must match held output
bytes. A product alias alone proves nothing: selected live PodSpec aliases and native runtime
manifest identities must match the held outputs before and after execution. Repository-relative
Run paths are independent of configuration-relative kubeconfig paths. Product and tools may name
the same Run; it is captured once and kept alive through execution and joined cleanup.

Use the shared [native demo](local-nvke-demo.md#conformance-on-the-fresh-ubuntu-tenant) for a
version-matched configuration generated from its fresh tenant. Retained JSON and original JUnit
are diagnostics. Inspect the original suite results; complete capture and zero process exit do not
establish suite completeness, a passing test verdict, qualification or promotion.
