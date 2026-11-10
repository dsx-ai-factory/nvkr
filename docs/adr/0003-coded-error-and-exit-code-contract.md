# ADR-03 — Coded-error & exit-code contract

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation/README.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation/README.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-03 in the consolidated NVKR ADR set.
- **Relates to:** ADR-04 (trust-failure codes), ADR-17 (CLI/SDK), and every fail-closed path across the ADRs.

## Context

NVKR fails **closed** with machine-readable errors and **distinct exit codes** — an SRD invariant, because
verification runs as the unconditional final stage and a promotion gate must stop on a coded, auditable failure.
A downstream gate, whether a CI job or a consumer such as NVKE, branches on the exit code. The code set is
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
| `40–56` | Release / lifecycle | `vector-unpinned`, `release-incomplete`, `gate-unmet`, `unverified-ref`, `compat-mismatch`, `provenance-missing`, `inventory-unpinned`, `immutable-violation`, `withdrawn`, `unpinned-input`, `mirror-mismatch`, `pipeline-drift`, `status-checkpoint`, `write-conflict`, `pin-set-mismatch`, `registry-auth-failed`, `registry-rejected` |

Builder-preflight failures live in the resolve/preflight band. A profile that does not provide a required
builder maps to `builder-not-provided`; a builder version, executable-identity, sandbox, or privilege mismatch
maps to `preflight-failed`. "Builder-version-mismatch" is a failure scenario, not a third public code.

### 2. Retryable vs input classification

A **retryable band** (`unavailable`, `timeout`) plus a `Retryable()` predicate lets a gate distinguish a
transient failure from an input error. Network 5xx, dial, and EOF classify as `unavailable`; a deadline or
cancellation classifies as `timeout`. A gate retries the retryable class and fails closed on the rest.

Publication classifies structured registry responses without inferring a vendor or protocol version.
Registry authentication or authorization refusal uses `registry-auth-failed` (55); other permanent
publication request refusals use `registry-rejected` (56). An unambiguous unsupported operation uses
`engine-capability` (29), and an unambiguous digest or size mismatch uses `verify-failed` (22).
Permanent refusals of Helm chart tag writes or image discovery tag writes are an exception: they use
`invalid-catalog` (16), including structured refusals, unless authentication was refused.
An authentication diagnostic takes precedence over any unsupported diagnostic. HTTP 408, 429 and 5xx
responses remain transient unless authentication was explicitly refused. Ambiguous or unknown permanent
diagnostics do not authorize automatic representation changes. `MANIFEST_INVALID` alone identifies a
rejection, not the specific unsupported field. Diagnostic presentation includes only the status and known
structured codes; untrusted server bodies, messages, details and request URLs are not copied into that
presentation. The original structured cause remains available through error unwrapping.
An absent manifest/blob during post-upload readback remains unavailable rather than proving a permanent
format rejection; publication acceptance is not issued while that readback is incomplete.
An unstructured 404 at the V2 base endpoint uses `invalid-catalog` (16). A successful proxy/HTML base
response followed by an unclassified protocol failure also uses
`invalid-catalog` (16), not a retryable publication error. Correct the endpoint or proxy configuration;
neither response establishes an older protocol. Known structured refusals retain their classifications above.
Explicit transient HTTP responses and readback absence retain their retryable classification.
This is distinct from a missing digest during readback.
The pinned read-only ORAS base check exposes any HTTP 404 only as not-found and does not parse that
response body. It therefore reports endpoint configuration (16) even if the body contains a registry
diagnostic; publication can classify such diagnostics from its own client's structured response.
Diagnostic reports additionally describe the observed API family and request stage, while preserving an
unknown exact OCI specification version and unknown untested features. Typed DNS and certificate failures
receive safe connection diagnostics instead of printing credential-bearing client URLs. Next-action guidance
is derived from the established failure class and request, never from a presumed vendor/version or untrusted
error-message text. The coded cause and retryability remain authoritative; observations do not change them.

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
- **ADR-06 / ADR-18 / ADR-21 / ADR-22:** `status-checkpoint` (52), `engine-capability` (29), optimistic
  `write-conflict` (53), and `pin-set-mismatch` (54) are allocated. The last reports disagreement with a pinned
  umbrella's member declaration or provenance. ADR-20 publication additionally allocates
  `registry-auth-failed` (55) and `registry-rejected` (56). Existing numeric meanings remain frozen.

## Acceptance evidence

Current evidence in `pkg/nvkr/errors/errors_test.go` proves that declared codes map once, statuses are distinct,
and bands are respected. The currently named codes are allocated. Acceptance remains partial until CLI
integration tests prove cancellation/deadline, network unavailability, stale-status, capability failure, and
write-conflict causes reach their documented process status without collapsing to internal error 1.
