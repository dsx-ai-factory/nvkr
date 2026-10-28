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

## Pull requests

Contributors use the [PR template](../.github/PULL_REQUEST_TEMPLATE.md), link
the relevant issue or discussion, and follow [CONTRIBUTING.md](../CONTRIBUTING.md)
for the local gate, DCO sign-off, and cryptographic commit signatures. Code
owners review their paths; maintainers handle review, requests for changes, and
merge decisions under [GOVERNANCE.md](../GOVERNANCE.md). Draft PRs are not ready
for maintainer review.

## Inactive issues

The [stale-issues workflow](../.github/workflows/stale-issues.yaml) marks an
issue with no activity for 90 days as `lifecycle/stale`. If there is no further
activity, it closes the issue after another 30 days. New activity removes the
stale label. A maintainer can apply `lifecycle/frozen` to exempt an issue that
must remain open, such as accepted, assigned, or deferred work that goes long
periods without comments; assignment alone does not exempt an issue from
automatic closure. The workflow creates both labels if needed.

Pull requests are not closed automatically.
