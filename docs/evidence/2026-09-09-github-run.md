# First GitHub hosted Run

Executed on 2026-09-09 at commit `3c55dcfd6aed7d5d23b2c76897a5bcf179ee9af7` after fast-forwarding
GitHub `main` and `github-baseline` to the signed cleanup commit.

- [Generated Run 34416283747](https://github.com/njhensley/nke-recipes/actions/runs/34416283747): **failed**.
- Trigger: manual dispatch on `main`, using the maintained NKE Definition and GitHub Environment.
- Plan: `sha256:04a2fd55836d9bef3d3b34b52d5df6b9fb65a8ff6a6a265089203cc8a500d504`.
- Evidence lane: `static-unqualified`; signing mode: `dry-run`.

## What ran

The GitHub runner checked out the repository, installed Go, built the static helper, passed byte-exact
regeneration, uploaded the helper, and restored it in the source-fetch job. That job attempted the pinned
NKE commit `ed9fb09b04fd40bf331d25df2222206ffaca593e` and failed with `NKD-E-FETCH-FAILED` (Git exit 128).

GitHub API inspection confirmed `NVIDIA/nke` has `internal` visibility. The destination repository had no
Actions secrets configured, and the generated source-fetch job supplies no cross-repository credential.
Authenticated source access is the current blocker. This run does not establish whether subsequent build,
packaging, GHCR publication or signing steps work: those jobs were skipped.

Terminal observation and lifecycle finalization ran after the failure. Finalization uploaded the
[failed Run record](https://github.com/njhensley/nke-recipes/actions/runs/34416283747/artifacts/10129234024).
The downloaded record has attempt `34416283747-1`, status `failed`, lane `static-unqualified`, retains the
expected `nkectl` requirement, and reports `NKD-E-PROVENANCE-MISSING` because the required carrier was not
restored from a successful provider job. No successful artifact or Release is claimed.

The action runtime emitted Node 20 deprecation warnings while forcing those pinned actions onto Node 24.
Checkout, setup, upload and download nevertheless completed successfully in this run.

## Repository CI failure and correction

The [initial repository CI run](https://github.com/njhensley/nke-recipes/actions/runs/34416271028) passed
formatting, vet, lint and ShellCheck, then failed the Linux sandbox test under race/coverage instrumentation.
The policy probes passed, but Go's coverage shutdown tried to create files outside the Landlock write root.
Earlier local Linux checks used race detection without coverage and did not expose this interaction.

The subprocess test now directs coverage and temporary files into its existing allowed directory and preserves
the coverage argument across sandbox exec. Production sandbox rules are unchanged. The focused Linux test
passed with race detection and coverage enabled; no assertion or sandbox enforcement was disabled.

## Next executable step

Configure approved, narrowly scoped read access to the internal NKE source, then repeat the dispatch. Do not
copy a developer's local credentials into CI or change the canonical source/pin to bypass that requirement.
Real signing and signed Release-consumer acceptance remain unexercised.
