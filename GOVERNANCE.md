# NVKR Project Governance

This document describes how the NVKR (NVKR) project is
governed: the roles people hold, how decisions are made, and how maintainers
join and leave. It is intentionally lightweight and will grow with the project.
For the current roster and maintainer responsibilities, see
[MAINTAINERS.md](MAINTAINERS.md).

## Roles

NVKR uses three roles. Code-owner review routing is declared in
[`.github/CODEOWNERS`](.github/CODEOWNERS), which currently resolves every path to the
org-wide `@dsx-ai-factory/nvke-write` team pending a repository-scoped one.

### Contributors

Anyone who opens an issue, pull request, or discussion. Contributors follow the
[Code of Conduct](CODE_OF_CONDUCT.md) and sign off their commits under the DCO
(see [CONTRIBUTING.md](CONTRIBUTING.md)). No special access is required to
contribute.

### Code Owners

Trusted contributors (the `@dsx-ai-factory/nvke-write` team) who review and approve pull
requests for the paths they own. Code ownership is declared per path in
[`.github/CODEOWNERS`](.github/CODEOWNERS), which GitHub uses to request reviews
automatically. Code owners keep their areas healthy and mentor contributors.

### Maintainers

Maintainers have merge rights, make and
ratify project decisions, manage releases, and own this governance process —
including adding and removing maintainers. Maintainers exercise this authority
through merge rights and the governance vote below, not through a distinct
[`.github/CODEOWNERS`](.github/CODEOWNERS) entry: code-owner review currently routes
to the org-wide `@dsx-ai-factory/nvke-write` team, which is broader than the maintainer
roster (see [MAINTAINERS.md](MAINTAINERS.md#current-maintainers)).

## Areas of Ownership

Path-level ownership is authoritative in
[`.github/CODEOWNERS`](.github/CODEOWNERS). At a high level the project is
organized into:

- **Definitions, catalog, and evidence** — `definitions/**` (Definition, Environment,
  `registry.yaml`, and the `pins.lock.yaml` trust roots under ADR-22).
- **Core engine** — producer resolution, build graph compilation, bundling, collection, and validation
  (`pkg/nvkr/**`).
- **CLI** — `cmd/nvkr`.
- **Toolchains** — pinned build inputs (`toolchains/**`).
- **Supply chain and CI** — release, attestation, and workflow tooling
  (`.github/**`, `Makefile`).
- **Documentation and design record** — `docs/**`, the ADRs under `docs/adr/`,
  and the root project docs.

Maintainers hold cross-cutting responsibility across all areas.

## Decision-Making

NVKR decides by **lazy consensus**: a proposal (pull request, issue, or
discussion) is accepted if no maintainer raises a blocking objection within a
reasonable review window — at least five business days for non-trivial changes.
Most day-to-day changes are merged through normal code-owner review under
[`.github/CODEOWNERS`](.github/CODEOWNERS) and the process in
[CONTRIBUTING.md](CONTRIBUTING.md).

When consensus is not reached:

- **Majority vote.** Any maintainer may call a vote. A proposal passes on a
  simple majority of non-emeritus maintainers; quorum is a simple majority of
  that same group.
- **Blocking objection (veto).** A maintainer may block a change by stating a
  concrete technical rationale and an actionable path to resolution. A blocked
  change proceeds only if a two-thirds supermajority of maintainers votes to
  override.
- **Supermajority decisions.** Adding or removing a maintainer, and changes to
  this document, require a two-thirds supermajority of non-emeritus maintainers.

## Tie-Breaking

If a vote is tied, the **lead maintainer** has the casting vote. The lead
maintainer is a maintainer designated by the maintainer team and recorded in
[MAINTAINERS.md](MAINTAINERS.md); the role exists to break deadlocks and
carries no additional day-to-day authority. If the lead maintainer is the
subject of, or is recused from, a decision, the remaining maintainers designate
an acting lead for that decision.

## Adding and Removing Maintainers

### Adding

Maintainers are added on merit. The selection criteria and nomination steps are
listed in [MAINTAINERS.md](MAINTAINERS.md#becoming-a-maintainer). A nominee is
added when a two-thirds supermajority of current maintainers approves.

### Removing and stepping down

A maintainer may step down at any time by opening a pull request that moves them
to emeritus. A maintainer may also be removed by a two-thirds supermajority vote
— for sustained inactivity (see [Emeritus](#emeritus)), or for conduct that
violates the [Code of Conduct](CODE_OF_CONDUCT.md). Removal for cause follows the
Code of Conduct enforcement process.

## Emeritus

A maintainer is considered **inactive** after six months with no substantive
contribution, review, or governance participation. Inactive maintainers are
moved to the Emeritus list in [MAINTAINERS.md](MAINTAINERS.md) — voluntarily, or
by a maintainer vote — which removes merge rights and excludes them from quorum
and vote counts. Emeritus maintainers are welcomed back through the normal
onboarding process when they return to active participation.

## Changing This Document

Amendments follow the supermajority rule above: a pull request plus approval from
a two-thirds supermajority of non-emeritus maintainers.

## Maintainer Roster

The current maintainers, sorted by GitHub
handle. [MAINTAINERS.md](MAINTAINERS.md) is the authoritative roster; this table
is a convenience snapshot.

| GitHub | Name | Headshot |
|---|---|---|
| [@bmcadams1](https://github.com/bmcadams1) | Byron McAdams | <img src="https://avatars.githubusercontent.com/u/243672877?s=48&v=4" width="48" height="48" alt="bmcadams1"/> |
| [@njhensley](https://github.com/njhensley) | Nathan Hensley — **lead maintainer** | <img src="https://avatars.githubusercontent.com/u/229213852?s=48&v=4" width="48" height="48" alt="njhensley"/> |
| [@xdu31](https://github.com/xdu31) | Jayson Du | <img src="https://avatars.githubusercontent.com/u/66693539?s=48&v=4" width="48" height="48" alt="xdu31"/> |

> **Note on a three-person roster.** A two-thirds supermajority of three is two,
> so adding or removing a maintainer and amending this document require two of
> three — a single maintainer cannot block either. A veto still requires a
> two-thirds override, which is likewise two of three. Quorum for a vote is a
> simple majority, also two. Revisit these thresholds if the roster shrinks
> below three, where every one of them collapses to unanimity.

Service accounts are not maintainers. `svcsectool` is authorized to approve fork
CI runs through the copy-pr-bot fork-CI mirror but holds no governance role: it
cannot participate in lazy consensus, vote, or veto. (Fork-CI approval is not yet
configured via a committed `.github/copy-pr-bot.yaml`.)
