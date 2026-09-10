# GitHub configuration audit

Reviewed 2026-09-09 against the current Definition → Run → Release lifecycle.

| File | Disposition |
|---|---|
| `.github/workflows/ci.yml` | Retain repository formatting, analysis, race tests and build checks. Replace the obsolete POC header. |
| `.github/dependabot.yml` | Retain Go and action updates; exclude the renamed generated workflow because its pins are owned by generator inputs. |
| `.github/workflows/nkd-run.yml` | Regenerate from the NKE Definition and GitHub Environment. Replaces `nkd-factory.yml`. |
| `.github/nkd-run/manifest.json` | Generated file membership and byte digests. Replaces the old factory manifest. |
| `.github/nkd-run/plan.json` | Generated execution plan with current internal CompileSpec and ExecutionSettings terminology. |
| `.github/nkd-run/graphs/nke-cli.json` | The sole graph selected by the maintained NKE Definition. |
| Former GPU/AICR graphs and GPU composition under `.github/nkd-factory/` | Remove. They were leftovers outside the manifest, including retired individual worker builds. AICR retains its separate Definition and demo. |

The generated directory is now checked for unexpected files and directories, including symlinks. Previously,
the drift check compared expected members but ignored leftover graphs. Other handwritten workflows remain
outside that directory's ownership.

The GitHub Environment now names this repository's GHCR destination and workflow signer identity. Signing
is now configured for the exact pinned cosign executable and the `nkd-release` deployment environment.
Real-service acceptance is tracked in the [signed Run evidence](evidence/2026-09-10-github-signed-run.md). All pull requests perform only
bootstrap and regeneration checks. Execution requires manual dispatch on the default branch, with bounded
job timeouts and concurrency that does not cancel an in-progress publication.

The internal `factory execute-node` helper and `.nkd/factory` scratch paths remain compiler/executor mechanics;
they are not separate authored public objects. The public entry point and generated files use Run terminology.

## Operational boundary

The [first hosted Run](evidence/2026-09-09-github-run.md) passed bootstrap and regeneration but failed
fetching the internal NKE source without a cross-repository credential. It uploaded a failed Run record.
The subsequent [authenticated fork Run](evidence/2026-09-09-github-authenticated-run.md) passed every job,
including the CLI build, GHCR publication and complete Run finalization. The deployment environment now restricts execution to `main`, and only the signing job receives OIDC minting
permission. The first real signing exercise is pending; automatic default-branch publication remains deferred.

## Validation

The updated generator passed the complete local `make check` gate with golangci-lint 2.13.1 built using
Go 1.27, the uncached unit suite, and Linux/arm64 race checks for signing, pipeline and provenance. Both
workflows pass Actionlint; the workflows and Dependabot config pass YAML linting with generated long lines
allowed. NKE and AICR generation checks pass, including regression coverage for leftover payloads and changed
execution guards. Changed-document relative links and `git diff --check` pass.

The current NKE plan digest is
`sha256:afbf72d8d16cb5c5e0a6346fa7d6638874e432b18dbc5535ff8cae25f889734a`.

GitHub Actions is enabled. Commit `3c55dcf` promoted the implementation and generated workflow to `main`;
manual dispatch then exercised the hosted failure path described above.

Commit `c15e534` added Environment-owned source mirroring and fetch-step authentication. Its generated Run
and repository CI both passed on GitHub; the execution evidence records exact identities and dry-run limits.
