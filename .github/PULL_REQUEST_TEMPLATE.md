## Summary

<!-- What changed? Keep it short (1-2 sentences). -->
<!-- Note: this repository squash-merges, so the pull request TITLE becomes
     the single commit subject on main. Write it as a Conventional Commit
     for a reader; see CONTRIBUTING.md. -->

## Motivation / Context

<!-- Why is this change needed? Link issues/discussions. -->

Closes: <!-- #123 or N/A -->
Related: <!-- #123 or N/A -->

## Type of Change

<!-- Check all that apply -->

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to change)
- [ ] Documentation update
- [ ] Refactoring (no functional changes)
- [ ] Build/CI/tooling

## Component(s) Affected

- [ ] Catalog data — pins (`pins.lock.yaml`)
- [ ] Catalog data — registry: sources, constraints, build commands (`registry.yaml`)
- [ ] Definitions — output selection / catalog substitutions
- [ ] CLI (`cmd/nkdctl`)
- [ ] Generator / pipeline emission
- [ ] Signing, provenance, or evidence
- [ ] ADRs (`docs/adr/`)
- [ ] CI / workflows (`.github/`)
- [ ] Docs / examples
- [ ] Other: ____________

## Implementation Notes

<!-- Key decisions, trade-offs, and any non-obvious behavior changes. Delete if not applicable. -->

## Testing

```bash
# Commands run (the canonical local gate)
make check
```

<!-- Summarize test results or paste relevant output -->

## Risk Assessment

<!-- Select one and explain -->

- [ ] **Low** — Isolated change, well-tested, easy to revert
- [ ] **Medium** — Touches multiple components or has broader impact
- [ ] **High** — Breaking change, affects critical paths, or complex rollout

**Rollout notes:** <!-- Migration steps, feature flags, backwards compatibility, or N/A -->

## Checklist

- [ ] `make check` passes locally
- [ ] I did not skip/disable tests to make CI green
- [ ] I added/updated tests for new functionality
- [ ] I updated docs if user-facing behavior changed
- [ ] Changes follow existing patterns in the codebase
- [ ] Every commit is cryptographically signed and carries a DCO sign-off (`git commit -S -s`) — [GPG signing info](https://docs.github.com/en/authentication/managing-commit-signature-verification)
