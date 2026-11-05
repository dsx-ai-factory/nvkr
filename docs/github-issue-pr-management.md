# GitHub issue, discussion, and pull request management

This document explains how the NVKR repository routes and reviews issues, discussions, and pull
requests. It does not turn [best-effort community support](../SUPPORT.md) into a fix promise.

## Intake and routing

| Request | Route |
|---|---|
| Reproducible defect | [Bug form](../.github/ISSUE_TEMPLATE/bug_report.yml) |
| Concrete enhancement | [Feature form](../.github/ISSUE_TEMPLATE/feature_request.yml) |
| Documentation correction or request | [Documentation form](../.github/ISSUE_TEMPLATE/documentation_request.yml) |
| Usage question | [Discussions Q&A](https://github.com/dsx-ai-factory/nvkr/discussions/categories/q-a) |
| Open-ended design idea | [Discussions Ideas](https://github.com/dsx-ai-factory/nvkr/discussions/categories/ideas) |
| Suspected vulnerability | [Private NVIDIA PSIRT reporting](../SECURITY.md), never a public issue or PR |

The issue chooser exposes the three work-item forms and the security reporting
link. Questions and ideas go to Discussions through [SUPPORT.md](../SUPPORT.md).
A discussion that becomes an actionable defect or accepted feature can be
linked from a new issue. Do not post embargoed security details in an issue,
discussion, or pull request.

## Weekly triage

The [maintainers](../MAINTAINERS.md) review new and open issues and discussions
at least weekly. At each review they:

1. Check for duplicates, the right public or private channel, a usable
   reproduction or problem statement, and the affected component. Security
   reports are redirected privately without repeating sensitive details.
2. Assign an owner and apply labels. Collaborators with Triage or higher access
   can apply labels; the issue forms apply type labels, not priority. A
   reporter's impact selection is evidence for triage, **not** an assigned
   priority.
3. Record the next action and link any related pull request or discussion.
   Blocked and unassigned items are revisited at the next review. Duplicates and
   invalid requests are closed with a reason.

Maintainers also review the latest [vulnerability scan](cve-management.md) at
this cadence. A report forwarded by NVIDIA PSIRT is triaged promptly when received.

The [triage workflow](../.github/workflows/triage.yaml) adds `needs-triage` to new
open issues and infers one `area/*` label using the precedence below. It never
infers an area from free text and never creates labels: a missing label fails the
job before any label is applied. Type labels and inferred labels do not complete
triage; maintainers correct the area if needed and remove `needs-triage` after
review and recording the next action.

Intake preserves an existing area, then prefers the exact Component, an explicit
mapped title scope, and finally the `documentation` label as a docs fallback.
Bare `factory` scopes need review because they span Run and pipeline work.
Generated-workflow capacity and transport belong to pipeline; Run planning and
execution belong to run. Authored Definition/Environment work belongs to
definitions, and publication or consumer acceptance belongs to release. Maintainers
correct mismatched form selections and documentation labels after reading the
acceptance scope. Focused regression tests retain the behavior's area; tests is
for shared harnesses and fixtures.

Each issue has one primary area, chosen by the owning behavior. For cross-area
work, record secondary owners in the next action. Use the closest existing area
and state the routing assumption when no exact label exists, such as observation
or validation. Resolve disputed ownership with a maintainer; labels do not change
module authority. Preserve valid type labels, including `enhancement` on tests or
refactors, and propose a type when one is missing. Preserve legacy topical labels
such as `github_actions` unless a maintainer approves removal. An actionable issue
with `question` may need a design decision and remains an issue.

Queues predating the taxonomy rollout need review even when `needs-triage` is
absent. Review every open issue, propose one area, and propose `needs-triage` on
unresolved/manual-review items. Do not add and remove the label just to replay
intake on classified rows. Maintainer acceptance and a recorded next action
complete review; a recommendation report alone does not.

Before recommending closure, reconcile every acceptance item with merged code/tests
or exact operational evidence at the level required. Closing keywords, resolved
review threads and design merges do not prove completion. Keep partially completed
issues open and propose removing premature closing keywords, or obtain maintainer
acceptance to transfer every remaining item to a named surviving issue. Duplicate,
invalid and superseded dispositions also need an item-by-item mapping to the
surviving issue or decision.

For old references to the monolithic ADR ledger, find the current owner through
the [reconciliation index](adr/reconciliation/README.md) and cite the per-ADR or
topic file. Check stale prerequisite statements against current PR merge state
and owning code. Propose corrections in the owning ledger file while preserving
dated evidence and pending ratification; a merged prerequisite alone does not
prove downstream acceptance.

The [nvkr-triage skill](../.agents/skills/nvkr-triage/SKILL.md) supports this review
with recommendations and verification of authorized edits. Its default scope is
issues only, with excluded open PRs counted and listed. It does not assume a
Projects board or assign priority from a reporter's impact selection.

## Pull requests

Contributors use the [PR template](../.github/PULL_REQUEST_TEMPLATE.md), link
the relevant issue or discussion, and follow [CONTRIBUTING.md](../CONTRIBUTING.md)
for the local gate, DCO sign-off, and cryptographic commit signatures. Code
owners review their paths; maintainers handle review, requests for changes, and
merge decisions under [GOVERNANCE.md](../GOVERNANCE.md). Draft PRs are not ready
for maintainer review.

When PR triage is requested, read complete bodies and comment/review threads,
current labels and assignees, base/head identities, paginated changed-file lists,
additions/deletions, current-head checks/reviews, draft state and merge conflicts.
Report unknown or unreadable states for manual review. Propose path areas from
the labeler configuration and sizes from the changed-line thresholds in
[triage.cjs](../.github/scripts/triage.cjs), never from an issue's effort estimate.
Keep PR verdicts separate from issue counts and apply the same authorization and
verification rules to both.

The [PR labels workflow](../.github/workflows/pr-labels.yaml) computes `area/*`
labels from [changed paths](../.github/labeler.yml) and one `size/*` label from
the changed line count. It runs on `pull_request` with read-only permissions,
including for forks, and uploads a JSON request. The
[label writer](../.github/workflows/pr-labels-apply.yaml) runs on `workflow_run`
completion with a PR write token. It executes only default-branch code and treats
the artifact as untrusted data outside the checkout. It validates the schema and
config-derived label allow-list, binds the request to the run's head repository,
branch and commit and the current PR, and recomputes labels from current API data
before writing. It preserves manual area/type labels and replaces stale sizes.
Incomplete file lists, unknown labels, missing repository labels and mismatched
requests fail before any write. Closed PRs and PRs retargeted away from `main`
are skipped. Label writes are not atomic; rerunning the producer repairs partial
application after a write failure.

Both same-repository and fork PRs use this split. Fork runs remain subject to
GitHub's normal approval requirements. The writer becomes active after its
workflow reaches the default branch; stale requests need a new producer run.
This automation needs no Actions policy exception for `pull_request_target`.

The separate
[title check](../.github/workflows/pr-title-lint.yaml) validates the Conventional
Commit format on title edits as well as new commits, and the
[label guard](../.github/workflows/pr-label-guard.yaml) fails while a PR carries
`do-not-merge`, including when an edit retargets the PR to `main`. These two
checks use `pull_request` with read-only tokens and check out the base commit's
[scripts](../.github/scripts/triage.cjs). As with other `pull_request` checks,
GitHub does not run them while a PR has merge conflicts. Their workflow
definitions can be changed by the PR, so they remain advisory checks.
`make test-workflow-scripts` tests the decisions and artifact validation locally;
it does not prove GitHub's fork approval or event delivery behavior.
Both checks report independently of the required
`gate` check; this automation does not change branch rules or make either check
required.

## Inactive issues

The [stale-issues workflow](../.github/workflows/stale-issues.yaml) marks an
issue with no activity for 90 days as `lifecycle/stale`. If there is no further
activity, it closes the issue after another 30 days. New activity removes the
stale label. A maintainer can apply `lifecycle/frozen` to exempt an issue that
must remain open, such as accepted, assigned, or deferred work that goes long
periods without comments; assignment alone does not exempt an issue from
automatic closure. The workflow creates both labels if needed.

Pull requests are not closed automatically.
