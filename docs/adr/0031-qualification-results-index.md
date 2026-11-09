# ADR-31 — Qualification results index

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** The qualification outcome (ADR-17 §4), the selection of what a Run publishes and
  the publisher below are implemented and contract-tested. The generated publish job and its Environment
  declaration are not implemented, so nothing publishes yet; see `reconciliation/README.md`.
- **Acceptance-test maturity:** Contract tests only, against in-memory and local-directory stores, a scripted
  object-store client, and the SDK against a local fake of the object store and its token exchange. No hosted
  Run has published an outcome, and no TestGrid view has read one.
- **Deciders:** Pending; release-engineering and qualification owners must be named before ratification.
- **Index:** Proposed ADR-31 supplements ADR-01–22; it remains outside the consolidated contract set until
  accepted. It is tracked in #279.
- **Relates to:** ADR-01 (canonical codec), ADR-03 (coded errors and exit codes), ADR-16 §5 (the qualification
  evidence and release manifest an outcome points at), ADR-17 §4 (package ownership), ADR-18 (generated jobs),
  ADR-20 §6 (pipeline-attested publication, which this is not), ADR-21 (regeneration).
- **Scope:** Production qualification by the generated release qualifier. Development qualification,
  Definitions without a release qualifier, back-filling earlier Runs and a cross-Definition matrix are excluded.

## Context

Release qualification leaves signed evidence on every output and a signed release manifest (ADR-16 §5). It
leaves no record across Runs and no trace of a failure: the qualifier reports its verdict only in the job log,
and it runs only after a successful producer, so a failed release build leaves nothing behind. A Definition's
qualification history can only be read one Run log at a time.

The production qualifier writes an unsigned qualification outcome for each attempt (`run qualify --out`, owned
by `qualificationoutcome`). It describes the attempt and points at the evidence and manifest by digest. This
decision publishes those outcomes, failures included, to an object store that a TestGrid dashboard reads.

## Decision

### 1. A description, never authority

Each outcome is published twice: as its canonical record under
`results/<group>/<definition>/<source run>-<attempt>/<qualifier run>-<attempt>/outcome.json`, and as a TestGrid
build under `groups/nvkr/<group>/<definition>/<build>/`, which holds `started.json`, `artifacts/junit.xml` and
`finished.json`. The group is the Definition's top-level directory under `inputs/definitions/`, and the
definition is its name. Each must be a path segment a Definition name allows (lowercase letters, digits, `.`,
`_` and `-`, alphanumeric at both ends) of at most 128 characters. The build's rows come from the outcome's typed
checks, never from its opaque diagnostics.

Both entries are unsigned descriptions. They grant no qualification, manifest, release or clearance authority,
and nothing in NVKR reads them back as input. The evidence digest names the evidence the attempt computed; a
failed attempt may have attached it to no member, or to only some. A consumer verifies the signed attestations
(ADR-16 §5), never the index.

### 2. One outcome for each attempt that ran

A qualifier attempt publishes the outcome it wrote: passed, failed (checks or manifest) or errored. A producer
Run that failed, timed out or failed to start publishes one errored outcome, built from the triggering event and
marked as unauthenticated.

The publish job passes on the qualify job's result and its report: the run attempt it last ran in and whether
that attempt wrote an outcome. Re-running only failed jobs keeps a successful qualify job's result and report,
so the attempt says which outcome they are about. When a failed attempt wrote nothing, the publisher builds one
errored outcome for it, whose error says the job wrote no outcome. The outcome of a passed attempt, or of one
that reports writing it, is a record: when it cannot be found it was lost, and publishing fails instead. A
canceled or skipped qualify job, and canceled, skipped and other producer conclusions, publish nothing.

Every outcome of the Run is published, in ascending attempt order, so a partly published earlier attempt is
completed. An invalid outcome fails closed, and so does one that describes another Run, producer attempt or
commit, comes from a later qualifier attempt, or has a stage the producer's conclusion contradicts. Nothing is
fabricated in its place.

### 3. Create-only and resumable

Every object is derived from the outcome alone and encoded canonically, so a re-run projects the same bytes.
Writes are create-only. An existing object must read back byte-equal, within the outcome's size bound, or
publishing fails with `CodeImmutableViolation`; a second or resumed pass writes only what is missing. An object
larger than that bound could never be read back, so it is refused before anything is written. Each
outcome's objects are written in a fixed order, the record, `started.json`, `junit.xml`, then `finished.json`,
so a TestGrid build is complete only when its last object exists. Publishing stops at the first error.

### 4. Times come from the outcome

TestGrid reads builds in lexical name order and skips names below the newest it has read, so a build name
starts with the outcome's finish time in zero-padded nanoseconds. The `started.json` timestamp is the outcome's
start in seconds, and every header value is a string in `finished.json`. The publishing packages observe no
clock or host. Times come from the qualifier's clock or the triggering event's Run times; an outcome built
after qualify failed finishes at the publisher's clock, so it sorts after builds already read.

### 5. The cloud SDK stays out of the release CLI

`qualificationoutcome` (selection) and `resultspublish` (projection and protocol) are SDK-free and hold no
credential; they write through a create-only store interface. The object-store SDK is linked only into
`nvkr-results-publish`, a separate helper that the publish job builds from the same commit. It is not part of
the release CLI and is not shipped. A dependency boundary test keeps the SDK, its client libraries and gRPC out
of `cmd/nvkr` and both packages.

### 6. Errors identify nothing internal

The bucket name comes from the job environment, set from a GitHub Environment secret. Every error is coded,
with a fixed message that names at most a relative object path; store client error text, which can carry the
bucket name, is never wrapped. A failed exchange through the auth step's credential file is classified by the
HTTP status of its own last request, never by its text, which can name the identity pool; on a credential route
that bypasses that client, such as a metadata server, a failed token fetch reports no usable credentials. Every
message ends by saying the qualification verdict is unaffected.

| Condition | Code (exit status) |
|---|---|
| Missing or refused credentials, a bucket that does not exist, or an event from outside the admitted producer | `CodeTrustPolicyMissing` (25) |
| An existing object differs or exceeds the read-back bound | `CodeImmutableViolation` (47) |
| A 408, 429 or 5xx response, or none, from the store or the token exchange, after four attempts; any other store failure | `CodeUnavailable` (30) |
| Deadline or cancellation | `CodeTimeout` (31) |
| An invalid, foreign or lost outcome, or a projected object too large to read back | `CodeVerifyFailed` (22) |
| No job environment or event | `CodeProvenanceMissing` (45) |
| Invalid invocation, including an unset bucket | `CodeUsage` (2) |
| Writing a local file failed | `CodeInternal` (1) |

### 7. A separate publish job

Publishing runs in its own generated job of the release qualify workflow, never in the qualify job, which holds
the registry credential. The job gets only a short-lived OIDC identity, bound to a dedicated GitHub Environment
that holds the store references as secrets.

The job runs `select`, uploads any outcome it built as an outcome artifact, then runs `publish` over the same
directory with the same report. `publish` repeats `select`'s admission and plan, and it refuses if the built
outcome is missing, so a re-run publishes the same record instead of building another. A publish failure never
changes the qualification verdict.

The target is declared in the Environment, outside `publication` (ADR-20). This job is not implemented; its
trigger allowlist and whether its failure turns the Run red are decided with it.

## Onboarding

A Definition is indexed once it has a generated release qualifier and its Environment declares the results
target. A new group also needs the store's owners to admit its path prefix and dashboard. To add one, or to
report a problem with the index, open an issue in this repository.

## Known gaps

- A build without `finished.json` shows as running for 24 hours, then freezes as failed. Re-running the failed
  jobs within 24 hours completes a partial publish.
- A build published after TestGrid has read a newer one can stay hidden.
- A Definition with no qualification for longer than the dashboard's tab age drops off the dashboard until its
  next Run; its records remain under `results/`.
- If a built outcome's artifact is gone when a later attempt publishes, it is built again and differs: a rebuilt
  qualify record finishes at a new time and conflicts with any of its objects already written, so that publish
  fails, while a rebuilt producer record names the new attempt and is published as a second build.
- Re-running only the failed jobs cannot recover a lost qualify outcome, because a successful qualify job is not
  re-run; publishing fails until the qualify job itself runs again.
- An outcome is built only for the qualify job's latest attempt. An earlier failed attempt that wrote nothing,
  and whose built outcome never reached an artifact, is not indexed.
- The object-store SDK reopens a read whose response body breaks off, without backoff, inside one store attempt;
  only that attempt's timeout bounds it.
- Nothing alerts on a publish that never runs; the dashboard's staleness marker is the only signal.

## Consequences

- A Definition's qualification history, failures included, is visible without reading Run logs.
- The module graph grows by the SDK's dependencies, and every generated job's CI bootstrap, which downloads the
  root module graph, downloads them too. The release CLI's linked modules are unchanged, and the third-party
  notices cover the helper.
- The bucket layout and the TestGrid header keys are a public contract: renaming a group, a Definition or its
  input directory moves its index.
- Re-running failed jobs is the recovery for every transient publish failure.

## Alternatives considered

- **A hidden `nvkr` subcommand.** The SDK needs gRPC, which the release CLI excludes. Rejected.
- **Calling the object-store JSON API directly.** It avoids the SDK but reimplements its retries and credential
  handling. Not chosen.
- **Publishing from the qualify job.** That job holds the registry credential. Rejected.
- **Passing outcomes through job outputs.** Job outputs are capped at 1 MB, and a real outcome is about
  435 KiB. Rejected.
- **Publishing only the newest outcome.** It strands a partly published earlier attempt. Rejected.
- **Fabricating an outcome when one is invalid or lost.** It publishes a permanent wrong result. Rejected.
- **The target in the qualification policy or in `publication`.** The first changes the policy digest; the second
  is artifact delivery configuration (ADR-20). Rejected.

## Follow-ups

- The generated publish job, its Environment declaration and the catalog admission of its auth action (#279).
- A monitor for publishes that never run.
- A cross-Definition qualification matrix.
