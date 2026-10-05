# Generated CI steps and logs

The GitHub renderer names each logical operation and identifies its exact Plan node in the log and job
summary. A build also names the component, selected output, producer instruction, platform, network and source
commit where present. Utility operations identify their package format or publication route and list their
exact declared input and expected output artifacts, including producers.

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
dumping its execution environment. Summary formatting and provider/shell expression syntax are escaped.

Summaries are optional diagnostics. If GitHub does not provide a summary path, or that file cannot be written,
the original operation status remains authoritative for the step. A job that never starts, is canceled or
loses its runner may have no summary. A successful summary is not an authenticated Run or Release verdict.

## Understanding transfers and recovery

A consumer job downloads one exact set of external input carriers and restores them in one or more steps,
splitting only when the provider script limit requires it. The log names the current carrier before each unpack. A missing or invalid carrier stops the job at that exact
artifact; the next consumer cannot run. The download action can match a partial set, so every mandatory
unpack remains essential. Artifacts produced earlier in the same grouped job use the existing local
workspace and are not downloaded again.

Each operation archives its declared outputs in one step, then uploads each carrier independently. The
carrier names, paths, payload verification, evidence bindings and upload failure behavior remain intact.
If preparing one output fails, that operation's subsequent upload steps do not run.

The finalizer uses one exact all-carrier download followed by bounded recovery batches. Its table records each
artifact as `job-failed`, `missing`, `restore-failed` or `restored:success`. It continues recording outcomes
after returned restore failures. These are the same per-carrier files consumed by the finalizer: a batch
step's success does not establish completeness, and a carrier from a failed grouped job cannot become a
successful output. The finalization gate still authenticates output membership, observations and evidence.

## Measured generated workflow

Regenerating the maintained six-output `nke-deploy` Definition at base `d1a4ec9` gives:

| Item | Before | After |
|---|---:|---:|
| Jobs | 23 | 23 |
| Logical execution operations | 33 | 33 |
| Visible steps | 338 | 212 |
| Download actions | 75 | 36 |
| Upload actions, including helper and final Run record | 44 | 44 |
| Finalizer steps | 49 | 10 |

Recovery blocks stay intact within each batch. Every generated shell step is admitted against a conservative
21,000-byte ceiling, matching GitHub's [21,000-character run limit](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstepsrun).
The largest step here is 20,408 bytes; the 42 carrier outcomes occupy three recovery batches. An indivisible
oversized script fails with a capability error before a bundle is returned.

The Plan digest and canonical Plan bytes are unchanged. Workflow bytes and their manifest digest change;
regeneration remains the sole owner of those files. The wrappers add explicit diagnostic script text, so
fewer visible steps do not mean a smaller generated YAML file.

[Recorded local evidence](evidence/2026-10-03-ci-log-readability.md) covers injected operation and carrier
failures, exact primary exit status, partial recovery, shell/provider/Markdown escaping, summary write
failure and byte-exact regeneration. The changed workflow has not been dispatched on GitHub. The counts
are static measurements, not a hosted duration, performance, release or qualification claim.
