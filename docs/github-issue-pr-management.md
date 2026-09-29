# GitHub issue, discussion, and pull request management

This is the NKE Recipes repository procedure under the NVIDIA-internal
[DGXC OSS GitHub issue and PR policy](https://nvidia.atlassian.net/wiki/spaces/DGXCLD/pages/3062795945/DGXC+OSS+Policy+For+Github+Issue+and+PR+Mgmt).
The central policy owns portfolio-wide priority definitions, internal service
targets, access rules, and dashboard reporting. This document explains how
this repository routes and reviews work; it does not replace that policy or
turn [best-effort community support](../SUPPORT.md) into a public fix promise.

## Intake and routing

| Request | Route |
|---|---|
| Reproducible defect | [Bug form](../.github/ISSUE_TEMPLATE/bug_report.yml) |
| Concrete enhancement | [Feature form](../.github/ISSUE_TEMPLATE/feature_request.yml) |
| Documentation correction or request | [Documentation form](../.github/ISSUE_TEMPLATE/documentation_request.yml) |
| Usage question | [Discussions Q&A](https://github.com/dsx-ai-factory/nke-recipes/discussions/categories/q-a) |
| Open-ended design idea | [Discussions Ideas](https://github.com/dsx-ai-factory/nke-recipes/discussions/categories/ideas) |
| Suspected vulnerability | [Private NVIDIA PSIRT reporting](../SECURITY.md), never a public issue or PR |

The issue chooser exposes the three work-item forms and the security reporting
link. Questions and ideas go to Discussions through [SUPPORT.md](../SUPPORT.md).
A discussion that becomes an actionable defect or accepted feature can be
linked from a new issue. Keep the
public issue updated when internal work is tracked elsewhere; do not expose
embargoed security details in either location.

## Weekly triage

The [maintainers](../MAINTAINERS.md) review new and open issues and discussions
at least weekly. They route bugs to an Engineering PIC and features to a Product
PIC under the central policy, route documentation issues to an appropriate
code owner, and record a missing PIC as an assignment gap.
At each review:

1. Check for duplicates, the right public/private channel, a usable
   reproduction or problem statement, and the affected component. Redirect
   security reports privately and avoid repeating sensitive details publicly.
2. Assign the responsible PIC and use the central P0/P1/P2 definitions and
   internal targets where applicable. Collaborators with Triage or higher
   access can apply labels; the issue forms apply type labels, not priority.
   This repository currently has no P0/P1/P2 labels or documented Project
   field.
   Record the policy priority in an authorized internal tracking item if one
   exists; otherwise flag that the assignment cannot yet be recorded and
   resolve the missing mechanism with the DGXC policy owner. A reporter's
   impact selection is evidence for triage, **not** an assigned priority.
3. Record the next action and link any related PR, Discussion, or internal
   tracking item. Revisit blocked, unprioritized, and approaching-target items
   during the next review. Close duplicates or invalid requests with a reason.

Maintainers also review the latest [vulnerability scan](cve-management.md) at
this cadence. The daily scan does not currently notify the team directly on
failure; scan-failure detection relies on this weekly review or a failed merge
gate. A PSIRT-forwarded report is triaged promptly when received.

## Pull requests and reporting

Contributors use the [PR template](../.github/PULL_REQUEST_TEMPLATE.md), link
the relevant issue or discussion, and follow [CONTRIBUTING.md](../CONTRIBUTING.md)
for the local gate, DCO sign-off, and cryptographic commit signatures. Code
owners review their paths; maintainers handle review, requests for changes, and
merge decisions under [GOVERNANCE.md](../GOVERNANCE.md). Draft PRs are not ready
for maintainer review.

The central DGXC dashboard tracks first response, time to close, and external
PRs closed without merge for repositories covered by it. Its 96-hour
first-response breach metric applies once response-event collection is enabled;
this document does not claim that collection or this repository's dashboard
enrollment has been verified. Maintainers should check and resolve those
operational prerequisites with the DGXC OSS working group.

## Inactive issues

The [stale-issues workflow](../.github/workflows/stale-issues.yaml) marks an
issue with no activity for 90 days as `lifecycle/stale`. If there is no further
activity, it closes the issue after another 30 days. New activity removes the
stale label. A maintainer can apply `lifecycle/frozen` to exempt an issue that
must remain open. The workflow creates both policy labels if needed.
Maintainers should apply `lifecycle/frozen` to accepted, assigned, or deferred
work that must remain open despite long periods without comments; assignment
alone does not exempt an issue from automatic closure.

The source policy section is headed “Stale Pull Request Policy,” but its
operative sentences explicitly describe **issues**. This repository therefore
automates issues only. Pull requests are not auto-closed pending clarification
from the policy owner. A committed workflow specifies intended behavior; its
first scheduled run and resulting issue state still need operational read-back.

Before making this repository public, the maintainers must review this document
and [the CVE procedure](cve-management.md) for internal links, PIC and priority
terminology, dashboard references, and restricted-tracker instructions.
