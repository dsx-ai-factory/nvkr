# ADR-15 — Air-gap & mirror model

- **Decision status:** Deferred outside the current product scope; no implementation retained.
- **Implementation maturity:** Removed. The former mirror package and CLI were disconnected from the maintained lifecycle.
- **Acceptance-test maturity:** No current air-gap acceptance claim.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-15 in the consolidated NVKR ADR set.
- **Relates to:** ADR-04 (offline signature verification), ADR-06 (status), ADR-20 (publication).

## Context

The maintained product runs locally or through GitHub with networked acquisition and publication. It does not
provide an air-gapped mirror lifecycle. Keeping a separate closure planner, activation state machine, and disabled
commands imposed maintenance without an operational use case.

## Decision

Remove the mirror implementation, input enumeration command, and their tests. Backward compatibility is not a
requirement. This does not remove offline signature-verification primitives used by supported trust APIs.

If air-gap execution becomes a product requirement, design it against the current Definition, Run, Release and
Verification Policy model. It must preserve exact logical identities, authenticate the complete required closure
and trusted status, and demonstrate zero-egress execution before being described as supported. The former
implementation is available in Git history, not as a second dormant workflow in the source tree.

## Consequences

There is no `inputs` or `mirror` command and no `pkg/nvkr/airgap` package. ADR-15 remains as the scope decision so
historical references do not imply a current capability. No package is retained solely to preserve its old API.
