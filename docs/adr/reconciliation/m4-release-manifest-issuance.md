## M4 release manifest issuance

M4-C adds the `pkg/nvkr/releasemanifest` owner and the
registered `nvkr.dev/release-manifest/v1` schema and `nvkr.release-manifest` family (ADR-01). Issuance takes
the live M3-A artifact-check receipt, the exact canonical v2 qualification evidence and an independently
selected qualification identity and policy digest. It refuses failed, incomplete, noncanonical, mismatched,
wrong-identity or wrong-policy evidence before signer construction, then authenticates the exact evidence on
every member subject before attesting the manifest to the publication root under the qualification identity
and verifying the exact attached bytes. The artifact-check receipt now also projects each output's source lane
and exact predicate digest from its already verified production provenance. `pkg/nvkr/sign` adds the distinct
`https://nvkr.dev/release-manifest/v1` attestation type over the same exact-predicate attach and verify path
used by qualification evidence.

`pkg/nvkr/releasemanifest/manifest_test.go`, `pkg/nvkr/releasemanifest/issue_test.go`,
`pkg/nvkr/lifecycle/qualification_release_members_test.go` and `pkg/nvkr/sign/release_manifest_test.go` cover
the strict schema and deterministic identity, exact provenance coverage and closed source lanes, pre-signer
refusals, a later member's missing or wrong-signer evidence despite earlier surviving attachments, manifest
attachment and verification failures, receipt revocation and cancellation between steps, and type, subject,
identity and issuer substitution in the cosign adapter. These are contract tests with fakes and a stubbed
cosign runner.

M4-B connects issuance through `run qualify --issue-manifest` and the generated production qualifier.
The qualification owner retains the same live source, invocation, artifact and smoke receipts through
publication and issuance, and freezes the exact canonical published evidence independently of mutable
diagnostics. The manifest adapter derives its expected qualification identity and policy digest from the
single independently loaded policy snapshot. Development policies refuse production issuance before
provider or publication work. A later manifest failure preserves qualification diagnostics and returns a
nonzero outcome; no surviving attachment is reported as a completed release. CLI and orchestration
contracts cover terminal failure reporting, receipt lifetime and lane isolation; generation contracts
require issuance only in the production final signer. These are local contract tests, not hosted acceptance.
No hosted Run, OIDC manifest attestation, registry read-back or independent consumer verification is
recorded. The tag rule, tag creation, publisher exclusion, consumer verification instructions and
interruption demonstrations remain M4-D.

`run qualify --issue-manifest --out` also writes an unsigned qualification outcome for each attempt it
qualifies, and the generated production qualifier uploads it, best effort and kept seven days (ADR-18 §8), for
any result except a canceled job. An attempt that ends earlier (a failed setup step), that the command's
admission refuses, or whose command times out before writing (a caller's work deadline, or the runner
stopping the step at its timeout) writes none, and the job's `outcome-written` output is `false`; a canceled
job writes none and leaves it empty.
A record that cannot be written after qualification never changes the exit status. Contract tests
cover the status mapping, the event fallback for errored attempts, admission before any work, redaction
(including URL-escaped and base64 forms of the step's secrets), times kept outside the diagnostics, the size
bound, an unchanged stdout and exit status, and the generated steps. No hosted Run has produced an outcome.

`nvkr-results-publish` selects the outcomes a producer Run publishes and writes them to the proposed ADR-31
results index; it is implemented and contract-tested, not externally exercised. Contract tests cover selection
(event admission, the qualify job's result and report, collection, fail-closed membership and built errored
outcomes), the bucket layout, the TestGrid metadata, golden JUnit, the create-only protocol (order, idempotent
second pass, resumed partial attempts, differing bytes, size refusal, store errors), store conformance for the
in-memory and local-directory stores and, through the SDK, for a local fake of the object store, the
object-store client's retries and error classes, token-exchange failures through the auth library, error text
free of the bucket name, the SDK boundary and the clock and host scan. No generated job runs the helper, and no
outcome has been published.
