# Registry release navigation replay

On 2026-10-03 UTC (2026-10-02 Pacific), the built `nkdctl release browse` command read a real Docker registry
containing the two locally built NKE CLI bundles and their exact dependency SBOMs. Local-layout and registry
JSON views were byte-identical. Both observed platforms and both evidence subjects matched the retained
executable bytes. Output focus and Mermaid export also completed successfully.

This was an unsigned diagnostic publication on loopback, not GHCR, hosted signing, a full deploy release,
or consumer-policy acceptance. The replay used raw preparation SBOMs. It did not invoke cosign or claim that
the fixture's recorded Definition/Environment/Build identities independently authenticate a Run.

## Exact inputs and scope

The implementation was the uncommitted `codex/nkd-release-navigation` tree at base
`d1a4ec9403a936b154b16f4c53e3d3f6daa4c739`. [Source and executable identities](2026-10-03-release-navigation/source.json)
record every changed production-code blob involved in the replay, the compiled CLI and the replay helper.

Payloads came from local attempt `nkd-cli-dual-20261003-02` in the `codex/nkd-multiarch-cli` worktree. Both
selected source commit `ed9fb09b04fd40bf331d25df2222206ffaca593e`. The source attempt ran ARM64 natively on
Docker Desktop and AMD64 under explicit emulation. Each SBOM recorded 27 dependency components.

| Output | Executable bundle manifest | Exact executable subject |
|---|---|---|
| `nkectl-linux-amd64` | `sha256:918ee3e3a7f3682277c9cf2487c0723a011ab7751428eb80abd1338ce0989a53` | `sha256:eeb21f04fb79f9276f571c51f8e8155c5f3e5bdd698493e4262b0c8eef75a28d` |
| `nkectl-linux-arm64` | `sha256:123e7fefdadd61ed59599c1f0b7cbef5d1db9c96cbbbbe42af089e14ce31cc84` | `sha256:24aa1e1e667776097fae38b3d0d31c072b4ce5b0ddee45fbc21d94aadfd0763f` |

The [replay helper](2026-10-03-release-navigation/replay.go.txt) inspected both binary layouts, checked their
platform/member declarations and compared each SBOM subject to its packaged executable. It bound each
inventory to the `build-output` stage, built a diagnostic metadata/evidence root, opened its complete held
closure, then pushed to a loopback registry. The helper copied two recorded input identities as descriptive
fixture metadata; it did not parse or authenticate the CLI branch's newer Plan schema.

Registry image: `registry@sha256:1be55279f18a2fe1a74edf2664cac61c1bea305b7b4642dab412e7affdcb3e33`.
It listened only on `127.0.0.1:65010`. The replay created and then stopped this temporary container; the
registry endpoint is no longer available. Its exact publication root was
`sha256:416f7fe2f29d6a105dcc0d83dc6a0314639a1b596fd971fb8895e8e26c026c1f`, and its content subject was
`sha256:6aa72cf394fd6e74147dcb8993426eac29d049e852cd48c9798d7a9c0e2e8755`.

## Recorded results

- [Registry JSON](2026-10-03-release-navigation/remote.json) and [local JSON](2026-10-03-release-navigation/local.json)
  compare byte-for-byte. They retain exact output and evidence references, subjects and input identities.
- [Focused ARM64 text](2026-10-03-release-navigation/arm64.txt) displays one of two outputs and its precise evidence.
- [Mermaid graph](2026-10-03-release-navigation/graph.mmd) contains both outputs, their component subjects and
  each evidence-to-output/subject relationship. This record checks emitted diagram text; it does not record a
  graphical Mermaid renderer run.

The commands used the exact recorded root reference, with `--anonymous --plain-http` for registry reads,
`--layout .nkd/navigation-replay/layout` for the local comparison, `--output nkectl-linux-arm64` for focus, and
`--format json` or `--format mermaid` for export. The inspection label in every result explicitly leaves
signature and policy verification unperformed.

## Contract validation

The navigation branch passed `make check`: formatting, vet, full lint (zero issues), ShellCheck, full native
race/coverage tests (71.3% statements), and vulnerability scan (none found). The uncached repository suite
also passed. Additional focused race tests passed for returned-copy isolation, source replacement, concurrent
close/read, missing platform metadata and contradictory executable platform rejection. Admission tests reject
malformed references and options before workspace/network access; a timed-out registry read cleans its stage.

These checks and the loopback replay do not extend policy verification to macOS, establish native hosted
AMD64 execution, or prove signed production-registry interoperability. Those remain separate evidence lanes.
