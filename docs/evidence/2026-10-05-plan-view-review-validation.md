# Plan-view review validation

Date: 2026-10-05 UTC. Base: `42e81ffd4a2496a5dada671c6576492aca9db869`.
Scope: read-only Plan views and contract tests on macOS ARM64 with Go `1.27.1` and
`GOTOOLCHAIN=local`. The source table identifies the tested candidate independently of the eventual commit.
Git blob identities describe exact source bytes; they are not NKD Build Digests.

The [October 3 record](2026-10-03-plan-views.md) retains its original source and test scope. It does not
claim to cover subsequent review commits. The earlier `b6d31b8` candidate passed its full local gates and
[hosted Merge Gate](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37342253222); this record covers
the further review changes below rather than replacing historical source identities.

## Tested changes

- Producer text retains every serialized instruction field. Node identity/kind and dependencies remain in
  their headers; remaining fields are sorted without reordering patches, arguments or other ordered values.
  Complete compiled OCI, Nix, acquired-artifact, VM-command and VM-Packer/material Plans exercise the fields
  absent from the former renderer. Tests round-trip displayed values, escape terminal controls, retain exact
  canonical Plan bytes and prove that nested caller mutations cannot change a retained snapshot.
- Comparison options reject JSON with `--details` before input I/O. The package owns render-mode selection
  and canonical Plan equality; the command maps flags, writes the result and maps the existing diff exit code.
- Schema-scoped category rules classify graph runner changes and whole catalog fields consistently and keep
  arbitrary map keys separate from schema fields. Tests resolve rule paths against the typed Plan schema.
  Complete-Plan tests reproduce the runner mismatch and reject copied factory-node requirements/bindings;
  whole `resolvedComponents` addition/removal coverage exercises the lower-level comparison, without
  claiming that removing a selected component from an otherwise unchanged Plan is valid.
- Long diff values retain their byte budget at a UTF-8 boundary. JSON retains complete values. Shared key-union
  and text-header helpers preserve deterministic comparison output and agreement between the two text views.
- DOT labels use DOT-specific escaping, preserving literal substitution sequences and readable Unicode while
  showing nonprinting characters as escaped text. Tests include a Unicode publication-host label admitted
  through the public compile/inspect boundary. No registry is contacted by this fixture.
- Snapshot copying no longer re-encodes internally generated canonical bytes merely to decode an owned copy.
  Factory file/canonical admission and complete Plan validation remain intact. No trusted caller flag, alternate
  digest implementation or unmeasured performance claim is introduced.

Independent review found no actionable findings across the final diff, inspection, graph and CLI changes.
Hosted CodeQL subsequently identified a possible integer overflow in the optional map capacity hint added
in `42e81ff`. The hint is removed without changing union behavior; the current source and repeated full
gates below include that independently reviewed correction.

## Tested source

| Source | Git blob |
|---|---|
| `pkg/nkd/planview/inspect.go` | `ac261984944b55a5673dbd8828caf147209c9fe9` |
| `pkg/nkd/planview/graph.go` | `ef84922325f0f77e1a9b339b1c211d625f20d055` |
| `pkg/nkd/planview/diff.go` | `55b7d75c63ab074f9bfbd1482323c0d94b58e4b4` |
| `pkg/nkd/planview/comparison_options.go` | `0e76a7cd8ffe7c84d7a80df1429bdbfb9611faa5` |
| `cmd/nkdctl/plan_views.go` | `1263e9356c60b6897c8b56259cd535616534eba3` |
| `cmd/nkdctl/lifecycle.go` | `5ce65f77ab320b6c5e8f91f990a2a26c38b9628c` |
| `pkg/nkd/planview/inspect_fields_test.go` | `71ef0e72ede8f6eee41f7a34d87532596e613fe4` |
| `pkg/nkd/planview/graph_dot_test.go` | `d7662d199aae1bf00c94116951ca4ecb42a84793` |
| `pkg/nkd/planview/diff_review_test.go` | `729b671da1645f7ecc3aa46ac486ae07f225e9fc` |
| `pkg/nkd/planview/comparison_options_test.go` | `1539a53c70765cf53ec82e1f47ac309ea4ff833e` |
| `cmd/nkdctl/plan_views_test.go` | `ae29cbcf3d3a5147ef66a068a604c8e4a2f4eb9e` |

## Completion gates

All commands below passed on the source above. `make check` covers formatting, vet, lint, ShellCheck,
race-enabled coverage and the vulnerability scan. The separate normal and race suites used `-count=1`.
Source bytes were unchanged throughout the successful gate sequence. Lint reported no issues and the
vulnerability scanner reported no vulnerabilities.

| Command | Exit | Log SHA-256 |
|---|---|---|
| `make check` | 0 | `b2dde0dd3ebb771bbe14ef0967ff6fa16eb48b3e293884e6f6dd3245cde5a363` |
| `go test ./... -count=1` | 0 | `14facde0cb323d1a56fd178fa5a85510558d87dbb3d299d492836bab3f14a109` |
| `go test -race ./... -count=1` | 0 | `c54b602bcac01e6601c6f69a178aaa223be7a93a70319dc8266c2ca9e5ffd15b` |
| `make build` | 0 | `7089aa3cca76af0e53d3fc198c4568f2eb56d0e9ae76853b872135d6dba3b353` |
| `make workflows-check` | 0 | `5626c4456f2d99a4f503cd4d7c93061bbac18b9fde1b4dfc66473250221c2cf9` |
| `git diff --check` | 0 | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |

The generated Plan, manifest and workflow remained byte-for-byte equal to the base:

| Generated file | SHA-256 |
|---|---|
| `.github/nkd-run/plan.json` | `b58a29d86da34bab189c0bd7810a16b604475ac64766962aebb331b078fbe8ac` |
| `.github/nkd-run/manifest.json` | `c820bd9cadcd28b8d99bb7295623311ee572625be9612b01bbb9b6968029e491` |
| `.github/workflows/nkd-run.yml` | `4b34fdca17c1f100294d5b084b7c25e014f459d4958c14d38860ddab5060a267` |

## Evidence limits

Graphviz `dot` is not installed in the local validation environment. Its optional bounded SVG-rendering
regression skipped; DOT encoding and the admitted-Plan label boundary are contract-tested, without a
claim that an external renderer or particular diagram layout was exercised. The encoding follows
[DOT string syntax](https://graphviz.org/doc/info/lang.html) and
[escString semantics](https://graphviz.org/docs/attr-types/escString/).

The VM fixtures compile and inspect Plans; they do not build or boot OS images. No producer execution,
registry transfer, hosted artifact workflow, signing operation, release acceptance or qualification is
established by this record. Existing file/canonical/full-Plan admission and generated-byte contracts remain
in force. Neither signing nor pipeline execution implementation changed.
