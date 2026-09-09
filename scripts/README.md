# Runnable journeys

The maintained scripts use the public lifecycle or demonstrate one focused
supporting contract. They never turn local diagnostics into release authority.

## Definition → Run

```sh
# Plan only: validates and creates no attempt storage.
nkdctl run plan --definition definitions/nke-v1/definition.yaml --environment definitions/nke-v1/environment-local.yaml
scripts/demo-ai-factory-local.sh --plan

# Full local journeys; Docker and Go are required.
NKE_LOCAL_CLONE=/path/to/nke scripts/demo-m1-local.sh
scripts/demo-ai-factory-local.sh
scripts/demo-ai-factory-local.sh --publish
```

`demo-ai-factory-local.sh` runs the pinned AICR render, terminal observation,
Helm OCI package, and lifecycle envelope. It does not build the worker payload;
that belongs to the NKE OS-image build. Local attempts are immutable and
`static-unqualified`.

M1 uses the loopback registry on port 5000; AICR publication uses port 5001.
Both scripts generate their own valid immutable attempt names. The OS-image
wrapper remains unimplemented; the worker-package contract fixtures are not an
OS-image demo.

## Hosted Run generation

```sh
scripts/demo-ai-factory-github.sh
```

The script validates the Definition, shows its Run plan, generates provider files,
and performs a byte-exact check without contacting a provider. It
generates publication to a placeholder GHCR destination; hosted acceptance still
needs a real destination and signing configuration.

## Release consumption

```sh
scripts/demo-release-consumer.sh --help
```

The consumer takes one digest-pinned reference and one explicit Verification
Policy. Run it on Linux: it requires a real statically linked, digest-pinned Linux
cosign executable and a signed GitHub publication; local dry-run output is
deliberately insufficient.
Signer-policy rejection is covered by the owning consumer contract tests.

## Focused supporting contracts

- `demo-catalog-change.sh` exercises previewed and machine-managed source pin
  changes against an isolated catalog copy.
- `demo-tamper-detection.sh` demonstrates canonical identity changes, corrupt
  transport rejection, and path-containment failures.

The AICR Definition is the maintained accelerator example, so there is no separate AICR
or structural cluster-object demo. Package representation details remain covered
by contract tests rather than parallel public workflows. Workspaces live below
`.nkd/`; scripts either create immutable attempts or require their own workspace
marker before resetting derived output.
