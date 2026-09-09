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
remains `dry-run` and the resulting evidence remains `static-unqualified`. All pull requests perform only
bootstrap and regeneration checks. Execution requires manual dispatch on the default branch, with bounded
job timeouts and concurrency that does not cancel an in-progress publication.

The internal `factory execute-node` helper and `.nkd/factory` scratch paths remain compiler/executor mechanics;
they are not separate authored public objects. The public entry point and generated files use Run terminology.

## Operational boundary

Local validation does not prove a hosted run. The current workflow has not exercised GitHub runner source
access, GHCR publication or real OIDC signing. Protected-environment integration and automatic default-branch
publication remain deferred in ADR-18. No signed publication exists for positive Release-consumer acceptance.

## Validation

The updated generator passed the complete local `make check` gate with golangci-lint 2.13.1 built using
Go 1.27, the uncached unit suite, and Linux/arm64 race checks for signing, pipeline and provenance. Both
workflows pass Actionlint; the workflows and Dependabot config pass YAML linting with generated long lines
allowed. NKE and AICR generation checks pass, including regression coverage for leftover payloads and changed
execution guards. Changed-document relative links and `git diff --check` pass.

The current NKE plan digest is
`sha256:04a2fd55836d9bef3d3b34b52d5df6b9fb65a8ff6a6a265089203cc8a500d504`.

GitHub inspection confirmed Actions is enabled, but default branch `main` is still template commit
`a2e9c71ca5556aa5958410134d5b0703d733b8bc` and GitHub reports no registered workflows. The implementation is on
`github-baseline`; the latest cleanup is local. A hosted dispatch therefore needs the implementation and
generated workflow promoted to the default branch first.
