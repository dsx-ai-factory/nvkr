# Demo language and execution audit

Audited the maintained scripts, their help text, Definition READMEs, M1 walkthrough and milestone status
against the working tree after the NVIDIA/module migration and catalog tool/action changes. Executions used
the local macOS workstation, Docker's Linux runtime, and the local NKE clone. This is uncommitted working-tree
evidence, not a new GitHub Run or organizational milestone acceptance.

## Corrections

| Previous wording | Actual scope and correction |
|---|---|
| M1 script builds only the CLI through a hermetic journey | Its current Definition requires CLI, CWE and Helm. CLI compilation is offline; CWE uses a network-enabled wrapped build. Only CLI pull-back, SBOM subject and version checks are demonstrated after publication. |
| Local execution is `static-unqualified` | The plan has that tier. Executed Run records use `unqualified-local-execution`. Neither supplies signature authority. |
| “Dry-run-signed” | No signature exists. Messages now say dry-run signing records or no signatures. |
| Real hosted signing and consumption remain entirely unproven | Earlier personal-namespace runs recorded both. They do not establish the current NVIDIA inputs, identity, or governance requirements. |
| Cosign is pinned in the Environment | The catalog locks executable bytes; the Definition selects cosign; the Environment selects signing mode and identity policy. |
| “Release verified” without its scope | The consumer demonstrates `pipeline-attested/artifact-publication`. It does not establish M3 qualification, M4 manifest/revocation acceptance, or deployment readiness. |
| Other demos include GPU cluster planning | The maintained accelerator journey renders and packages AICR output; worker and cluster compiler fixtures are not OS-image or deployment demos. |

Milestone requirements were not reduced to match the implementation. Default-branch push execution,
same-repository PR builds, and the per-job helper digest negative demonstration remain gaps relative to the
M1 specification. Named reviews, decision ratification and current-namespace operational evidence remain
separate requirements. Selecting cosign mode alone proves none of them.

## Executed journeys

| Journey | Result |
|---|---|
| `demo-m1-local.sh --attempt audit-language-20260910` with the local NKE clone | Passed: all three outputs built and published; CLI pulled by digest, matched its SBOM subject and executed with the expected version. Run execution took 262 seconds; pull took 2 seconds. |
| `demo-ai-factory-local.sh --plan` | Passed; one expected output, no execution attempt. |
| `demo-ai-factory-local.sh --publish --attempt audit-language-20260910` | Passed: exact AICR acquisition, offline render, Helm packaging and loopback publication. |
| `demo-ai-factory-github.sh` | Passed generation and byte-exact regeneration; no hosted dispatch. |
| `demo-catalog-change.sh` | Passed no-write preview, pin update, changed Recipe identity and malformed-pin rejection. |
| `demo-tamper-detection.sh` | Passed diagnostic mismatch, authority-boundary, overwrite, corruption and escaping-symlink refusals. |
| `demo-release-consumer.sh` | Help and unsafe-input contracts checked; real signed consumption was not repeated during this audit. Earlier independent consumption retains its recorded scope. |

The local executions preceded the final message-only edits to the scripts. No execution commands, build inputs
or assertions changed. Revised help and shell syntax were checked separately.

## Exact local records

The [NKE Run](2026-09-10-demo-language-nke-run.json) is complete with exactly `cwe`, `nke-helm` and `nkectl`
expected and observed. Its lane is `unqualified-local-execution` and its plan is
`sha256:3a4c0d47a0deea69f026b7877761c7615399fdc1bf2b42f2c5725969566444b6`.

The CLI publication was
`127.0.0.1:5000/nke/nkectl@sha256:9b2bb331943646e0586b08ee33a6e47abc237828bba014e6e3046c2a31f0b493`.
The recovered binary reported `nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)`.
Signing and verification records were explicitly dry-run; the script checked their publication digest and
the signing record's exact predicate hash.

The [AICR Run](2026-09-10-demo-language-aicr-run.json) is complete with `gpu-runtime` expected and observed,
also in `unqualified-local-execution`. Its publication was
`127.0.0.1:5001/nke/gpu-runtime@sha256:8ff50d3ecfea403a778a6d6ab4484c8e5ae290e25d46f00ef048cd572f00ab36`.

Loopback publications are disposable workstation artifacts, not durable consumer endpoints. The local registry
service uses a mutable `registry:3` tag; build, tool and generated action pins have their separate catalog
contracts. These runs do not exercise an OS-image build, deployed cluster, new signature, or NVIDIA-hosted Run.

## Completion checks

The final `make check` passed (formatting, vet, lint, ShellCheck and race-enabled tests; 71.5% statement
coverage). Focused demo help, unsafe-input and workspace tests also passed uncached. Changed-document local
links and `git diff --check` passed. No GitHub dispatch or independent signed-consumer rerun was performed.
