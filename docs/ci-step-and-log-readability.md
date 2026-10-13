# Generated CI steps and logs

The GitHub renderer names each logical operation and identifies its exact Plan node in the log and job
summary. A build also names the component, selected output, producer instruction, platform, network and source
commit where present. Utility operations identify their package format or publication route and list their
exact declared input and expected output artifacts, including producers.

## Recognize the operations

Jobs describe the operation; steps identify the selected component or output. Fetch and build steps obtain
the pinned source and execute its producer. Offline processing verifies observed artifacts, creates SBOMs,
and packages outputs. Helm binding embeds the selected image digest before packaging the chart. Publication,
signing/attestation, and signature verification have separate isolation and credentials.

The top-level workflow shows jobs, dependencies, permissions and protected Environments in ordinary YAML.
After native preflight and checkout, local composite calls point into `.github/nvkr-run/actions/` for shared
execution and transport interfaces. Calls such as `execute-node`, `execute-source` and `collect-node-outputs`
name their operation. Grouped jobs repeat the same node interface; differing output counts use declared
optional slots and further individual upload calls, without output-count or group-size action variants.
Stable invocation keys select named node and carrier parameters in `.github/nvkr-run/action-data.json`.
The helper admits the Plan and recomputes those values before the fixed operations use them. Parameters
carry data, never shell scripts.
A shared action prepares the downloaded helper. Operator setup
and timed producer execution retain their caller context inline. Controlled transport wrappers contain one
pinned upload/download; their conditions, timeouts and continue-on-error policy stay on the caller. Diagnostic
paths from another step pass through explicit inputs, and transfers whose outputs are referenced stay
inline. These templates and invocation values are regenerated and checked with the workflow; they do not introduce another
authored configuration surface.
Repeated native preflight, pinned checkout and helper/parameter preparation steps use descriptive aliases.
Preparation selects the current provider job with `github.job`; permissions, dependencies and execution or
transport calls stay explicit.

Workflow headers refer to fixed Plan and manifest paths. Source-fetch job names and ordering follow the
target and actual consuming components; resolved commands and logs still identify exact pin-bound Plan nodes.
Routine pin updates can change the Plan, invocation data, shared actions and manifest without changing the workflow. Topology,
labels/interfaces and visible policy must remain the same; checkout pins, runtime containers/services,
runners, credentials, timeouts and inline setup can still require workflow changes. The full bundle must
be regenerated and checked after every input update.

The finalizer recovers artifact and evidence carriers and checks the complete frozen output set, even after
a producer failure. Each operation logs its Plan node, result, exit code, and elapsed time. A publication
reference appears after it is persisted; a failed step retains the child process's original error.
These diagnostic messages support investigation; authenticated records establish the release claim.

## Finding a failure

Start with the failed job's summary. Each executed operation records its result, exit status, elapsed seconds
and last phase. For example, `execute package/controller-chart` identifies the failed logical operation;
`restore sbom/cwe` identifies a transport failure before the consumer could execute. Expected outputs are
explicitly labeled; only a successful operation prints `Produced` for its outputs.

The corresponding named step retains child-process output and the helper's original error. Stable execution
step IDs still bind the exact Plan operation. Grouped offline processing jobs list the packaged outputs in
the job name and mention Helm image binding when applicable. Builds, fetches, publication and signing retain
separate jobs and their existing isolation, credentials and permissions.

Descriptions exclude raw recipe arguments, environment values, credentials, source/destination URLs and file
contents. Raw arguments can contain literal authentication values; the canonical Plan retains those exact
instructions for deliberate local inspection. Safe compiled context explains which instruction ran without
dumping its execution environment. Runtime descriptions never become provider expressions or shell commands; summaries escape control text and Markdown.

Summaries are optional diagnostics. If GitHub does not provide a summary path, or that file cannot be written,
the original operation status remains authoritative for the step. A job that never starts, is canceled or
loses its runner may have no summary. A successful summary is not an authenticated Run or Release verdict.

## Understanding transfers and recovery

A consumer job downloads one exact set of external input carriers and restores them in one helper step.
The log names the current carrier before each unpack. A missing or invalid carrier stops the job at that exact
artifact; the next consumer cannot run. The download action can match a partial set, so every mandatory
unpack remains essential. Artifacts produced earlier in the same grouped job use the existing local
workspace and are not downloaded again.

Each operation archives its declared outputs in one step, then uploads each carrier independently. The
carrier names, paths, payload verification, evidence bindings and upload failure behavior remain intact.
If preparing one output fails, that operation's subsequent upload steps do not run.

The finalizer uses one exact all-carrier download followed by one recovery helper. Its summary records each
artifact as `job-failed`, `missing`, `restore-failed` or `restored:success`. It continues recording outcomes
after returned restore failures. These are the same per-carrier files consumed by the finalizer: a batch
helper's success does not establish completeness, and a carrier from a failed grouped job cannot become a
successful output. The finalization gate still authenticates output membership, observations and evidence.

## Compact workflow commands

The shared templates resolve their invocation parameters through `factory github describe-action` and invoke
the execution and transport operations against the canonical Plan. The helper derives exact input
and output paths, execution settings, diagnostic context and recovery membership; the workflow retains
meaningful step names, pinned provider actions, credentials, permissions, dependencies and conditions.
The generated invocation-data member is not an authored transport manifest or an alternative Plan. Source-fetch credentials stay on `run-node`, while
`pack-outputs` remains a separate step.

These excerpts illustrate the expanded controller-chart operations and finalizer. Exact names and node IDs
come from the selected Plan. Composite shell steps preserve the original default with `bash -e {0}` on
the host or `sh -e {0}` in a job container. Caller-only
expressions such as `toJSON(needs)` are passed as explicit inputs and mapped to the original consuming field.
Source credentials likewise reach only the source step's environment, never the packing or upload steps.

```yaml
      - name: 'Build nvke-controller-chart (controller-chart)'
        id: nvkr-controller-chart-build-nvke-controller-chart
        run: |
          ./nvkr-exec factory github run-node --plan .github/nvkr-run/plan.json --node controller-chart/build-nvke-controller-chart
      - name: 'Prepare output carriers: Build nvke-controller-chart (controller-chart)'
        run: |
          ./nvkr-exec factory github pack-outputs --plan .github/nvkr-run/plan.json --node controller-chart/build-nvke-controller-chart
```

```yaml
      - name: Recover planned carriers
        id: recover-artifacts
        env:
          NVKR_JOB_RESULTS: ${{ toJSON(needs) }}
        run: ./nvkr-exec factory github recover --plan .github/nvkr-run/plan.json
```

The recovery helper bounds and validates the provider JSON before touching carriers. It requires exactly the
expected stage and bootstrap jobs, rejects duplicate or unknown fields and statuses, and ignores string-valued
job outputs without logging them. It records ordinary carrier failures and continues; cancellation or a
failure to persist outcomes stops recovery. The finalizer still authenticates completeness and evidence.

## Historical six-output workflow comparison

The October 4 compact-helper review used the six-output `nke-deploy` Definition (now `nvke-deploy`) and identical canonical Plan bytes.
These measurements describe that review, rather than the current M2 workflow:

| Item | Initial PR #90 (`582ab01`) | Compact helper |
|---|---:|---:|
| Workflow lines | 5,079 | 1,145 |
| Workflow bytes | 345,920 | 59,933 |
| Jobs | 23 | 23 |
| Logical execution operations | 33 | 33 |
| Visible steps | 212 | 209 |
| Download actions | 36 | 36 |
| Upload actions, including helper and final Run record | 44 | 44 |
| Finalizer steps | 10 | 7 |
| Largest decoded run script, bytes | 20,408 | 412 |

Repeated shell traps, status loops and inline utility launch commands now live in the Go helper. The
conservative 21,000-byte limit still applies to every generated run script, including explicit operator setup.
Workflow and manifest bytes change; the Plan digest and bytes remain unchanged. Transport count and payload
size are unchanged by this refactor, including the remaining work to minimize OS-image transfer.

Current contracts live in [compact-helper tests](../pkg/nvkr/factory/github_compact_test.go),
[reporter tests](../pkg/nvkr/factory/github_reporter_test.go), and
[finalization tests](../pkg/nvkr/factory/github_finalize_test.go). They cover generated commands and failure
handling; neither these tests nor the historical static counts establish hosted duration or Release acceptance.
