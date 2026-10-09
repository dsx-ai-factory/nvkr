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

The generated workflow invokes four operations against the canonical Plan. The helper derives exact input
and output paths, execution settings, diagnostic context and recovery membership; the workflow retains
meaningful step names, pinned provider actions, credentials, permissions, dependencies and conditions.
There is no new authored transport manifest. Source-fetch credentials stay on `run-node`, while
`pack-outputs` remains a separate step.

These excerpts illustrate a controller-chart operation and finalizer. Exact names and node IDs come from
the selected Plan; the repository currently generates the 34-output M2 workflow.

```yaml
      - name: 'Build nke-controller-chart (controller-chart)'
        id: nkd-controller-chart-build-nke-controller-chart
        run: |
          ./nkd-exec factory github run-node --plan .github/nkd-run/plan.json --node controller-chart/build-nke-controller-chart
      - name: 'Prepare output carriers: Build nke-controller-chart (controller-chart)'
        run: |
          ./nkd-exec factory github pack-outputs --plan .github/nkd-run/plan.json --node controller-chart/build-nke-controller-chart
```

```yaml
      - name: Recover planned carriers
        id: recover-artifacts
        env:
          NKD_JOB_RESULTS: ${{ toJSON(needs) }}
        run: ./nkd-exec factory github recover --plan .github/nkd-run/plan.json
```

The recovery helper bounds and validates the provider JSON before touching carriers. It requires exactly the
expected stage and bootstrap jobs, rejects duplicate or unknown fields and statuses, and ignores string-valued
job outputs without logging them. It records ordinary carrier failures and continues; cancellation or a
failure to persist outcomes stops recovery. The finalizer still authenticates completeness and evidence.

## Contract checks

The conservative 21,000-byte limit applies to every generated run script, including explicit operator setup.

Current contracts live in [compact-helper tests](../pkg/nkd/factory/github_compact_test.go),
[reporter tests](../pkg/nkd/factory/github_reporter_test.go), and
[finalization tests](../pkg/nkd/factory/github_finalize_test.go). They cover generated commands and failure
handling; these tests do not establish hosted duration or Release acceptance.
