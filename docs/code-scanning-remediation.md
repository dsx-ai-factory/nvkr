# Code-scanning remediation

Scope: the 13 open CodeQL alerts retrieved for `NVIDIA/nke-recipes` on `main` on
2026-09-11. GitHub alert closure requires a subsequent CodeQL analysis of the
merged changes.

| Alerts | Finding | Change |
|---|---|---|
| 5–12 | Allocation-size arithmetic in composition merges | Capacity hints use one existing collection length, eliminating addition that could overflow. Maps continue to grow as needed; merge and sorting semantics are unchanged. |
| 13 | Allocation-size arithmetic in recorded signing arguments | Start with the executable and append arguments without computing a combined capacity. The same arithmetic was also removed from OCI environment construction. |
| 3 | OCI archive extraction through filesystem paths | Hold an `os.Root` for staging and create directories and files relative to it. Explicitly reject nonlocal member names in addition to the existing strict OCI member allowlist. |
| 1, 2, 4 | Transport archive directory, file and symlink creation | Hold an `os.Root` for staging and use its directory, file, symlink and permission operations. Preserve existing declared-root, symlink-target, parent, duplicate and no-replace publication checks. |

## Regression coverage

`TestExtractImageArchiveConfinesWrites` supplies valid OCI archives with either
blob directory replaced by a symlink to an outside directory. It requires a coded
verification failure and no writes outside staging. Both cases fail against the
original implementation using a temporary Go source overlay, and pass with the
confined extractor. This tests the internal extraction boundary under a
preexisting-parent substitution; it does not demonstrate an archive-only exploit
of the public import operation, which creates private staging.

The transport rejection table additionally covers absolute symlink targets and
directory/symlink entries beneath a symlink parent. Every rejection in that table
now asserts `CodeVerifyFailed` and verifies that no declared destination was
published. Existing deterministic transport and OCI image round trips exercise
valid inputs.

These are local contract tests. No alerts were dismissed, and no hosted build,
publication, or CodeQL rescan was performed as part of this change.

## Validation

- Full native `go test ./... -count=1` and `go test -race ./... -count=1` passed.
- `PATH=/Users/nhensley/go/bin:$PATH make check` passed: formatting, vet,
  golangci-lint, ShellCheck and the full race/coverage suite (70.0% coverage).
  This selects the installed golangci-lint 2.13.1 built with Go 1.27; the default
  PATH selected an older build that could not analyze this repository's Go version.
- Linux `go test -race ./pkg/nkd/sign ./pkg/nkd/pipeline -count=1` passed in the
  existing Go 1.27 container with init, network disabled and read-only source and
  module-cache mounts.
- `git diff --check` passed.
