# Runnable journeys

The maintained scripts use the public lifecycle or demonstrate one focused
supporting contract. They never turn local diagnostics into release authority.

## Definition → Run

```sh
# Plan only: validates and creates no attempt storage.
nkdctl run plan --definition definitions/nke-v1/definition.yaml --environment definitions/nke-v1/environment-local.yaml

# Local execution; Docker, Go and acquisition/build network access are required.
# Despite its historical name, the M1 script executes CLI + CWE + Helm.
NKE_LOCAL_CLONE=/path/to/nke scripts/demo-m1-local.sh
scripts/demo-m1-local.sh --publish
```

`demo-m1-local.sh` also checks that the committed GitHub-hosted bundle
(`.github/nkd-run/`) matches the current Definition/Environment
(`nkdctl run generate --check`) before executing. Local attempts are immutable and
`unqualified-local-execution`; plans remain `static-unqualified`. See the
[M1 walkthrough and acceptance limits](../docs/demo-m1.md).

The demo uses the loopback registry on port 5000. It generates its own valid
immutable attempt names. The OS-image wrapper remains unimplemented; the
worker-package contract fixtures are not an OS-image demo.

## Release consumption

```sh
scripts/demo-release-consumer.sh --help
```

The consumer takes one digest-pinned reference and one explicit Verification
Policy with `artifact-publication` scope. This does not establish M3 qualification or M4 Release-manifest
acceptance. Run it on Linux: it requires a real statically linked, digest-pinned Linux
cosign executable and a signed GitHub publication; local dry-run output is
deliberately insufficient.
Signer-policy rejection is covered by the owning consumer contract tests.

## Focused supporting contracts

- `demo-catalog-change.sh` exercises previewed and machine-managed source pin
  changes against an isolated catalog copy.
- `demo-tamper-detection.sh` demonstrates canonical identity changes, corrupt
  transport rejection, and path-containment failures.

Package representation details remain covered
by contract tests rather than parallel public workflows. Workspaces live below
`.nkd/`; scripts either create immutable attempts or require their own workspace
marker before resetting derived output.
