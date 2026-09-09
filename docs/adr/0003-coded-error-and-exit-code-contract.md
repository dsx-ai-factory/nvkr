# ADR-03 — Coded-error & exit-code contract

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-03 in the consolidated NKD ADR set.
- **Relates to:** ADR-04 (trust-failure codes), ADR-17 (CLI/SDK), and every fail-closed path across the ADRs.

## Context

NKD fails **closed** with machine-readable errors and **distinct exit codes** — an SRD invariant, because
verification runs as the unconditional final stage and a promotion gate must stop on a coded, auditable failure.
A downstream gate, whether a CI job or a consumer such as NKE, branches on the exit code. The code set is
therefore a **public contract**, not an internal detail.

## Decision

### 1. A frozen, banded public contract

Exit codes are a **published contract with specific integers**. They are **banded** with reserved gaps so new
codes slot in without renumbering, and they **never change meaning**. Shell command/permission/signal statuses
`126–165` are avoided; status `1` is deliberately retained as the generic internal-error fallback.

| Band | Purpose | Examples |
| :---- | :---- | :---- |
| `0–3` | Semantic (non-error) | success `0`; internal `1`; usage `2`; "differences present" (diff) `3` |
| `10–19` | Resolve / input / preflight / crypto | `no-match`, `ambiguous`, `reserved-axis`, `unsupported`, `invalid-catalog`, `unpinned-source`, `tag-toolchain`, **`builder-not-provided`**, `preflight-failed` (including builder-version mismatch), `signature-invalid` |
| `20–29` | Build / verify / trust-policy / disk | `fetch-failed`, `build-failed`, `verify-failed` (digest divergence), `untrusted-signer`, `trust-policy-missing`, `untrusted-profile`, `encrypted-artifact`, `envelope-decode`, `engine-capability` |
| `30–31` | Retryable / transient | `unavailable`, `timeout` (`32–39` reserved) |
| `40–53` | Release / lifecycle | `vector-unpinned`, `release-incomplete`, `gate-unmet`, `unverified-ref`, `compat-mismatch`, `provenance-missing`, `inventory-unpinned`, `immutable-violation`, `withdrawn`, `unpinned-input`, `mirror-mismatch`, `pipeline-drift`, `status-checkpoint`, `write-conflict` |

Builder-preflight failures live in the resolve/preflight band. A profile that does not provide a required
builder maps to `builder-not-provided`; a builder version, executable-identity, sandbox, or privilege mismatch
maps to `preflight-failed`. "Builder-version-mismatch" is a failure scenario, not a third public code.

### 2. Retryable vs input classification

A **retryable band** (`unavailable`, `timeout`) plus a `Retryable()` predicate lets a gate distinguish a
transient failure from an input error. Network 5xx, dial, and EOF classify as `unavailable`; a deadline or
cancellation classifies as `timeout`. A gate retries the retryable class and fails closed on the rest.

### 3. Trust-failure granularity and the typed-error discipline

- Each **trust-failure class** gets a distinct code — `signature-invalid` vs `untrusted-signer` vs
  `trust-policy-missing` vs `digest-diverged` — so a gate branches on the cause rather than seeing one opaque
  "verify failed".
- **Every** error crossing an I/O, subprocess, serialization, or verification boundary is a **typed** error
  mapped to a distinct code, never a bare error that degrades to a catch-all and silently collapses to the
  internal `1`.
- The full code → exit-status map is **snapshot-tested**: every code appears exactly once, all statuses are
  pairwise-distinct, and a reorder or collision fails the build.

## Consequences

**Positive**
- Downstream gates branch precisely and auditably on cause.
- Reserved bands let the contract grow without renumbering.
- The typed-error discipline prevents silent loss of diagnosability.

**Negative / costs**
- The integers are frozen once published; renumbering is a breaking change, so the initial allocation must be
  laid out carefully.
- Every new fail-closed path must be assigned a code in the right band.

**Neutral**
- Exact integers within a band are adjustable *before* first publication. The load-bearing guarantees are
  distinctness, banding, and stability.

## Alternatives considered

- **Internal-only codes (zero/non-zero to callers).** Rejected — destroys the downstream diagnosability the
  SRD's coded-failure requirement depends on.
- **A few generic codes with a message field.** Rejected — a gate cannot branch on a free-text message reliably.
- **Band-by-magnitude (retryable = exit ≥ N).** Rejected — collides with POSIX 128+signal and is brittle versus
  an explicit `Retryable()` predicate.

## Follow-ups

- **ADR-04:** defines the trust-failure classes and their ordering (the signature/trust gate runs before the
  digest comparison).
- **ADR-17:** the CLI maps a typed error to its process exit code and surfaces the retryable predicate.
- **ADR-06 / ADR-18 / ADR-21:** the distinct `status-checkpoint` (52), `engine-capability` (29), and optimistic
  `write-conflict` (53) codes are allocated. Existing numeric meanings remain frozen; these failures do not
  overload `withdrawn`, `unsupported`, or `pipeline-drift`.

## Acceptance evidence

Current evidence in `pkg/nkd/errors/errors_test.go` proves that declared codes map once, statuses are distinct,
and bands are respected. The currently named codes are allocated. Acceptance remains partial until CLI
integration tests prove cancellation/deadline, network unavailability, stale-status, capability failure, and
write-conflict causes reach their documented process status without collapsing to internal error 1.
