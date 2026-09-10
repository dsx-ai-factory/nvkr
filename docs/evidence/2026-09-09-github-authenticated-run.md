# Authenticated GitHub hosted Run

Executed 2026-09-09 America/Los_Angeles (finishing 2026-09-10 UTC), at commit
`c15e534bb9ea8d6955e4cd61cb57619241b67555`.

- [Generated Run 34419180257](https://github.com/njhensley/nke-recipes/actions/runs/34419180257): **success**.
- [Repository CI 34419165076](https://github.com/njhensley/nke-recipes/actions/runs/34419165076): **success**.
- [Run record artifact](https://github.com/njhensley/nke-recipes/actions/runs/34419180257/artifacts/10130368187): downloaded and inspected.

## What actually ran

The default-branch manual dispatch built the helper and passed byte-exact regeneration. The fetch execution
step used repository secret `NKE_SOURCE_TOKEN` to read the private `njhensley/nke` fork through an
Environment source-access mapping. It verified canonical NKE commit
`ed9fb09b04fd40bf331d25df2222206ffaca593e` and tree `309887e8cdb8ee24a39785b864390eda3afce5ec`.
The canonical source and Recipe graph were unchanged by the mirror/authentication configuration.

All eleven jobs passed: bootstrap, source fetch, CLI build, terminal observation, SBOM generation, executable
packaging, release-envelope packaging, GHCR publication, dry-run signing, publication verification, and
lifecycle finalization. This exercises actual hosted execution and cross-job artifact transport, not just
generation or mocked contracts.

The publication-reference artifact contains:

```text
ghcr.io/njhensley/nke-recipes/nkectl@sha256:ac82b6eb2aec07d3febce776b9d4f0a769387f7d17db1f45585e389a7eec8455
```

The downloaded Run record has:

| Field | Value |
|---|---|
| Attempt | `34419180257-1` |
| Status | `complete` |
| Lane | `static-unqualified` |
| Plan digest | `sha256:eb5425d44ec91650c3ddcce825f2274e567b6e64200b63ef22171bc59b018102` |
| Recipe digest | `sha256:439746ba5095230598792c43bcc7523946a1d2e31e4cf3c72525f72bd10a8861` |
| Observed executable output digest | `sha256:99cfd9c354a2c8719053dd1b69ce9917ed4b05d21fbe0478122d2b05d94ee969` |
| Content subject | `sha256:3f820f8adb8104f32906fe64af6aa645cecfa13c5ce904b462fca6ffad355d42` |

## Limits

The publication-verification job checked the served registry digest, but signing remains deliberately
`dry-run`. Its downloaded record explicitly has `dryRun: true`, `signatureVerified: false`, and
`attestationVerified: false`. This is a successful hosted build/publication demonstration, not a signed
Release or `pipeline-attested`/qualified acceptance. Live OIDC signing and positive signed consumption remain
unexercised. The OS-image wrapper remains deferred.

Pre-push checks passed: complete local `make check`, uncached factory/lifecycle tests, Linux source-access
race tests, Actionlint, YAML linting, regeneration and diff checks. Tests exercise repository-scoped Git
credential lookup, empty-token refusal, tampered workflow rejection, unmatched source mappings, unsupported
engine admission, and preservation of the Recipe-affecting compiler inputs.
