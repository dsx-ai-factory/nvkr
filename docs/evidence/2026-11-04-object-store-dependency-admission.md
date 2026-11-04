# Object-store dependency admission evidence, 2026-11-04

The AWS SDK dependency set in PR #341 is pinned by `go.mod` and `go.sum`: core v1.47.5,
credentials v1.20.13, S3 v1.114.6, STS v1.51.6 and Smithy Go v1.28.7, plus the required
transitive modules. The linked modules are official upstream modules with Apache-2.0 notices
retained by the repository generator. No SDK ambient configuration, profile, SSO or transfer-manager
module was added. These engineering checks do not establish organizational license-allowlist
approval, provider qualification or operator IAM approval.

At exact head `4baeed35efdb08e7893d36ba92ad17c8dfdd13f9`, the repository-pinned
`govulncheck ./...` ran with Go 1.27.3 on Linux amd64 and reported **No vulnerabilities found**
at 2026-11-04 14:48:00 UTC. The [completed vulnerability job](https://github.com/dsx-ai-factory/nvkr/actions/runs/41851021721/job/126647134707)
and [notice regeneration job](https://github.com/dsx-ai-factory/nvkr/actions/runs/41851021721/job/126647134898)
both passed. Separately, `GOTOOLCHAIN=local go mod verify` was run manually on 2026-11-04 at checkout
`33383d7f2cc3746346eaa9b5c13fbca5a5a4ff76` with unchanged `go.mod`/`go.sum`, and returned `all modules verified`. This local module-integrity
check is not part of `make check` or Merge Gate and was not a job in the cited CI run.
The review-fix patch retains that dependency set; a previous scan does not replace checks on a changed head.
This is a dated engineering admission record, not a promise about future vulnerability-database results.
