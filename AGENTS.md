# AGENTS.md

This file provides guidance to coding agents (Claude Code, Codex, and others) working in this repository.
`CLAUDE.md` is a symlink to this file. Edit `AGENTS.md` only.

## Local Overlay

If present, also read `AGENTS.local.md` at the repo root. It is gitignored so personal overlays stay local;
check the exact path directly (`Read` or `cat`), not with ignore-respecting discovery tools such as `rg`, `fd`,
or `git ls-files`. Follow it when it does not conflict with higher-priority instructions or this shared file.

## Role & Expertise

Act as a principal supply-chain and release-engineering architect with deep Go expertise. Optimize for
fail-closed correctness, reproducibility, and operational simplicity. All code must be production-grade, and
every claim about what the system does must be backed by a test, a recorded run, or an ADR that says it is not
yet implemented.

## Project Overview

NKD (`nkdctl`) turns a declarative component catalog into immutable, verifiable build and release identities,
and generates the CI pipelines that build, sign, and publish those artifacts. The v1 customer is NKE: NKD wraps
NKE's CLI, image and chart builds and publishes signed OCI artifacts with attested provenance. OS-image
integration remains capability-gated.

**Public lifecycle:** Definition → Run → Release. The six public objects are Definition, Environment,
Run, Artifact, Release, and Verification Policy (ADR-17). Definitions declare outputs; downstream consumers
own deployment topology and node placement. Plans, inventories, vectors, clearance, and publication roots are generated
or authenticated internal records, inspectable without becoming independently authored lifecycle inputs.

**Internal workflow:** Resolve output-selected pinned producers → freeze required outputs → compile → execute and observe →
check completeness and evidence → publish → authenticate the selected closure under consumer policy.

Two evidence lanes share this workflow. `qualified` requires exact-byte reproduction and authenticated
runtime/evidence/release gates. `pipeline-attested` is the v1 hosted-workflow production claim (ADR-04/09/16/18).
Dry-run stays `static-unqualified`. Never infer qualification or operational acceptance from a plan or test.

**Tech Stack:** Go (version in `.go-version`, `GOTOOLCHAIN=local`), go-containerregistry for OCI, cosign
(keyless, pinned by digest) for signing, golangci-lint v2, shellcheck. Linux is required for authority-bearing
paths; macOS builds and runs tests and diagnostics.

## Authority order

1. Current ADR-01–22 contain the engineering contracts directly, pending named-decider ratification.
   Each section states its lane and any deferred capability; proposed design is not organizational acceptance.
2. `docs/adr/reconciliation.md` owns current implementation, evidence and disabled-path status.
3. Code and tests establish implementation; unit tests do not prove external services or production workflows.

`docs/sadd.md` explains architecture, `CONTEXT.md` defines vocabulary, and `docs/milestones.md` plus
`docs/epics-v1-spec.md` govern product acceptance. `docs/README.md` maps active documentation. Dated evidence
retains its original scope; superseded requirements and plans are available only in Git history.

## Working contract

- Read the relevant ADRs and their reconciliation rows before changing an authority-bearing path.
- Put each invariant in one deep module under `pkg/nkd`; keep `cmd/nkdctl` focused on flags, presentation, and
  error mapping. Existing command-layer orchestration is migration debt, not a model for new authority.
- Represent authority with package-issued opaque receipts bound to exact bytes, subjects, policies, and
  identities. Treat caller-provided structs, booleans, paths, and digests as descriptions until the owning module
  authenticates them.
- Admit capabilities before artifact, filesystem, network, subprocess, or output work. Unsupported lanes return a
  coded error and no partial output.
- Thread `context.Context` through blocking work, bound external I/O, and own every goroutine or child-process
  lifecycle through stop and join.
- Validate the complete graph at every executable boundary. Preserve the separation between logical build
  identity, transport identity, runtime observation, terminal artifact verification, and release authorization.
  Bind completeness and evidence to each exact named output; equal Build Digests do not authorize sibling reuse.
- Use the canonical codec and an existing typed digest family for structured identity. Raw SHA-256 is only exact
  blob integrity, never a substitute for a typed identity or authenticated receipt.
- Treat durable authority as a cross-process state machine: kernel-released locking, byte-CAS/no-replace
  commits, deterministic recovery, and directory fsync where required by the governing ADR.

## Non-Negotiable Rules

1. **Read before writing.** Never modify code or a document you have not read.
2. **The gate must pass.** Run the completion gate below before claiming any change is done. Do not treat
   pre-existing failures as acceptable; fix them or report them explicitly.
3. **Fail closed.** Unknown inputs, unsupported platforms, missing capabilities, and ambiguous matches are
   errors, never warnings or silent defaults.
4. **Never overclaim.** Distinguish implemented, contract-tested, and externally exercised. Mocks and unit tests
   do not prove registries, CI, signing services, or runners.
5. **Generated CI targets generic hosted runners only.** Never emit self-hosted runner labels. Maintained
   execution targets are local and GitHub; publication and signing are GitHub-only in v1.
6. **Do not commit, push, tag, or open PRs unless explicitly asked.** Leave the working tree for the user to
   review.
7. **Prefer file tools and `git mv` over destructive shell commands.** Do not use `rm`-style commands when a
   file tool or Git operation does the job.
8. **3-strike rule.** After three failed fix attempts, stop, reassess, and explain the blocker.

## Commands

```sh
make check          # full local gate: fmt-check → vet → lint → shellcheck → test (run before pushing)
make test           # unit tests with -race and coverage
make lint           # golangci-lint v2 (.golangci.yml)
make build          # build bin/nkdctl
make nkd-exec-helper
make tidy           # go mod tidy + gofmt

# single package / single test
go test ./pkg/nkd/factory/... -run TestName -count=1

# generated-bundle drift check (byte-exact regeneration from public inputs)
go run ./cmd/nkdctl run generate \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml \
  --engine github --root . --check

# M1 local demo (needs Docker and a local NKE clone; see docs/demo-m1.md)
NKE_LOCAL_CLONE=/path/to/nke scripts/demo-m1-local.sh
```

## Architecture routing

Read `docs/adr/reconciliation.md` for current support before claiming a capability. Read ADR-17 for public
lifecycle/SDK ownership, ADR-18 for hosted execution/isolation, ADR-04/09/16 for evidence lanes, ADR-19 for
artifact membership and downstream ownership, ADR-20 for publication, and ADR-21/22 for generated files and pins. `pkg/nkd/lifecycle`
owns the public workflow; `buildgraph` derives graphs from direct producer selections; `factory` owns
Run compilation/execution. ADR-11/12 own direct selection and target contracts.

## Required Patterns

**Coded errors at every user-visible boundary.** Anything that can reach the CLI, a CI gate, or a downstream
consumer carries a stable `pkg/nkd/errors` code (ADR-03). Internal helpers may use `fmt.Errorf` with `%w`, but
the error must be wrapped with a code before it crosses a package boundary that maps to an exit code.

```go
import "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"

return errors.New(errors.CodeInvalidCatalog, "component %q does not support platform %q", name, platform)
return errors.Wrap(errors.CodeFetchFailed, err, "checkout %s", pin.Commit)
```

**Context first.** Every blocking or I/O function takes `ctx context.Context` as its first parameter. Bound
external calls with a timeout. `context.Background()` is acceptable only in `main`, tests, and deferred cleanup.

**Capability admission before work.** Check engine, platform, and tool capabilities up front and return
`CodeEngineCapability` (or the matching code) with no partial output. Do not start a build, write a bundle, or
push an artifact and then discover the lane is unsupported.

**Typed identity, canonical bytes.** Hash structured data only through the canonical codec and a named digest
family. Never `yaml.Marshal` or `json.Marshal` a map for anything that feeds a digest, signature, or manifest.

**Deterministic output.** Sort every emitted collection. Generated bundles must regenerate byte-for-byte
(`run generate --check` with the same Definition and Environment). Timestamps and randomness are injectable
and excluded from logical build identity; attempt metadata may affect its own Run or provenance identity.

**Table-driven tests** for any function with more than one case. Contract tests assert the coded error, not just
`err != nil`.

## Anti-Patterns (Do Not Do)

| Anti-Pattern | Correct Approach |
|---|---|
| Put orchestration or authority in `cmd/nkdctl` or `client/v1` | Move it into the owning `pkg/nkd` module; the command layer maps flags and errors |
| Accept a caller-provided digest, path, or boolean as proof | Authenticate in the owning module and issue an opaque receipt bound to the bytes |
| Raw `sha256.Sum256` as a structured identity | Use the canonical codec and an existing typed digest family |
| Log a warning and continue on a validation, pin, or signature problem | Return a coded error; fail closed |
| Emit a self-hosted runner label or a `git tag` trigger in a generated workflow | Hosted runners only; sign on default-branch push or manual dispatch |
| Hand-edit `.github/workflows/nkd-run.yml` or `pins.lock.yaml` | Regenerate with `run generate` from the Definition and Environment, or `catalog pin`; edit authored inputs, not generated output |
| Claim a feature works because a unit test with a fake passes | Record external evidence under `docs/evidence/` and keep the reconciliation row honest |
| Write repo paths, commands, or code identifiers into a product spec or epic | Specs use product nouns only; implementation detail belongs in ADRs and code |
| Append an amendment chain for an existing decision | Update its owning ADR section and reconciliation directly; label lane and deferred behavior |
| Modify the NKE checkout or catalog to make a local run work | Use the Git `insteadOf` override in the demo script; the catalog stays canonical |

## Git Configuration

- The default branch is `main`.
- Before committing, follow the Conventional Commits rules in [CONTRIBUTING.md](CONTRIBUTING.md#commit-messages).
  Apply the same format to PR titles used for squash merges.
- Commit only when asked. Sign every commit with `-S` (`commit.gpgsign` is on) and `-s` (DCO sign-off, see
  `CONTRIBUTING.md`), authored as the human, not the agent.
- Do not add `Co-Authored-By` lines or agent attribution in commits or PRs (organization policy, aligned with
  the AICR repository).
- Never rewrite history on a shared branch; use `--force-with-lease` only on your own branch and only when a
  rebase or signature fix requires it.

## Documentation conventions

- Deliverables (audits, plans, specs, demo scripts, evidence) go in `docs/` as `docs/<topic>.md`; summarize in
  chat, do not paste the document.
- ADRs follow the header template in `docs/adr/0001-*.md`. Update the current owning decision directly; create
  a new ADR only for a distinct decision. Update reconciliation and preserve lane-specific authority. Avoid
  amendment/addendum histories while the design is pre-ratification.
- Product specs and epics (`docs/epics-v1-spec.md`) describe production behavior in product terms. No repo
  paths, `nkdctl` commands, code identifiers, or POC tactics.
- Use auto-generated heading anchors; no tables of contents or explicit anchor markup, except where a document
  template already supplies `{#slug}` anchors (the SADD). Promote a bold label to a
  heading only when it names a topic with substantial content beneath it.
- Keep `docs/sadd.md` and `CONTEXT.md` (domain vocabulary) consistent with any ADR change.

## Key files

Use `docs/README.md` for the documentation map and `scripts/README.md` for runnable demos. `definitions/nke-v1/`
contains current NKE product inputs. `.github/workflows/nkd-run.yml` and `.github/nkd-run/manifest.json`
are generated. `toolchains/` owns toolchain sources and helper pins. `CONTRIBUTING.md` owns contribution rules.

## Completion gate

Run checks proportionate to the change, with the full release-blocking gate before claiming repository-wide
completion:

```sh
gofmt -l cmd client internal pkg
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
golangci-lint run
git diff --check
```

`make check` is the same gate plus shellcheck. Tests that need local listeners may require execution outside a
restricted sandbox. The `-race` gate has historically differed between macOS and Linux; when a change touches
`pkg/nkd/sign` or `pkg/nkd/pipeline`, run it on Linux too. Record external operational evidence separately from
the contract suite.

## Troubleshooting

| Issue | Check |
|---|---|
| Authority path returns `CodeEngineCapability` or a platform error on macOS | Expected; sealed descriptor execution is Linux-only. Run on Linux or use the dry-run/diagnostic path |
| `run generate --check` reports drift | Regenerate with the same Definition, Environment, engine, and output root; never patch the rendered YAML |
| `catalog pin` rejects a tag on a monorepo source | Set `tagPrefix` on the source and pass flags before the positional source name |
| `golangci-lint` not found | Install v2 from <https://golangci-lint.run/welcome/install/>; `make lint` fails closed without it |
| Local demo cannot reach the registry | Docker must be running; the script manages the `nkd-m1-registry` container and `--stop-registry` tears it down |
| Shell glob errors under `zsh` | Quote patterns or run from the repo root; `zsh` fails on unmatched globs |

## Design Principles

- **Fail closed** is the default posture; permissiveness is a documented exception.
- **Identity is derived from bytes**, never from names, tags, or paths.
- **Authority lives in one deep module**; adapters and commands delegate.
- **Evidence is typed and subject-bound**; a claim without a recomputable digest or a recorded run is not
  evidence.
- **Local equals CI**; the same gate and the same generated bundle run in both places.
- **Boring first**; proven primitives (OCI, in-toto, SLSA, cosign, Git) over bespoke mechanisms.

## Decision Framework

When choosing between approaches, prioritize in this order:

1. **Correctness under failure.** Does it fail closed with a coded error?
2. **Testability.** Can it be contract-tested without external services?
3. **Consistency.** Does it match the owning module's existing pattern and the governing ADR?
4. **Readability.** Can another engineer understand it quickly?
5. **Simplicity.** Is it the simplest solution that meets the ADR?
6. **Reversibility.** Can it be changed later without breaking a frozen contract (exit codes, digest families,
   generated-bundle identity)?
