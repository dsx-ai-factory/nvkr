# Unused renderer and library removal

Assessed 2026-09-18 against the working tree based on `16b3775`, including the completed locality refactor.
Implemented and verified on 2026-09-18. Scope: remove unused pipeline rendering and library code while preserving
additions from the past week. This is a source-usage audit, not a claim of new external execution.

## Usage and age evidence

The census covers production Go packages under `cmd`, `client`, `internal` and `pkg`, maintained Definitions,
generated workflows, scripts and SDK entry points. Production import reachability was followed by a
declaration/caller audit: a package import alone does not establish use when its only caller is an unreachable
helper. Test-only callers and an unused renderer's own helper chain are not retention reasons. Supported SDK
operations and methods required by retained interfaces remain legitimate library use.

The recent-addition exception conservatively protects everything introduced on or after **2026-09-11**.
Introduction is checked in the current branch's Git ancestry, using `git log --follow` and symbol pickaxe;
the date of a later edit, rename or locality extraction does not make old functionality new. Removed renderer
bases and the selected old library surfaces were present at `97140bd` (2026-09-10). The earlier imported baseline
`1793699` also contained the renderer implementations. Uncommitted new locality owners remain in use.
The September 17 bundle-manifest test exercised a changed identity shape of the older renderer; it is removed
with that owner, while independent recent execution and authoring features retain their tests.

The package census and conservative all-platform AST scan were saved locally as
`/tmp/nkd-unused-package-census.txt` and `/tmp/nkd-unused-symbols.txt`. The scan excludes tests/comments and
suggests candidates; manual caller, interface and age checks determine disposition.

## Removed code

| Surface | Evidence for removal | Retained behavior |
|---|---|---|
| Tekton and Argo renderers | Already deleted by the preceding locality change; no current implementation or maintained caller | Explicit unsupported-engine admission |
| Factory GitLab renderer and semantic validator | Only engine-selection plumbing and contract tests remained; no maintained Definition, demo or generated consumer | GitHub generation and strict drift/semantic checks |
| Separate qualified GitHub/GitLab renderers | GitHub had no production constructor; GitLab served only the unused hidden bundle workflow | Shared producer execution, source receipts, sandbox and terminal verification |
| Qualified bundle command and exclusive lowering, transport-plan, manifest and transaction helpers | Every live reference belonged to the retired rendering chain | Maintained Run bundle writer, shared archive transport, bounded regular-input reader and exact-node diagnostics |
| Graph table/JSON/DOT/Mermaid formatter and generic validator wrapper | No production callers; formatter also had no test callers | Inspectable canonical Plan and the used GitHub validator |
| Qualified transport package and `publishAuthorized` helper | The helper was the package's sole production importer but had no production callers; qualified commit admission was unavailable | OCI publication/consumption and explicit qualified-publication refusal |
| Qualified release authorization, receipt assembly, policy evaluation and release-specific trusted time | Their entire caller graph became test-only after tracing the unreachable publisher; internal calls did not establish a usable workflow | Inventory/ReleaseSet/PromotionPolicy document checks, exact authenticated document verification, generic signed status checkpoint verification and durable high-water updates |
| Vulnerability report library and uncalled CLI helpers | The registered command already rejected scanning; remaining callers were the retired authorization graph and tests | Early unsupported-capability refusal |
| Qualified delivery-root constructor | No maintained caller; only its own negative constructor tests | Current publication construction and independent verification |
| Alternate `canonical.TypedDigest` facade | Test-only alternate wrapper around the active canonical codec/digest owner | Frozen families, canonical bytes, retired-family refusal |
| Descriptive provenance observation model | No callers or tests; separate signed observation record remains unimplemented | Actual runtime observations and production provenance |
| Unused AICR matcher/injected bundler, tag/SBOM wrappers and manifest-link helper | No production caller, or a forwarding wrapper used only by tests | Exact profile selection, pinned AICR runner, source-aware tag selection, architecture-aware SBOM construction and evidence attachment |
| Old release selectors/rollback/incremental helpers and unused inspection/convenience methods | No maintained caller; age and interface use checked before deletion | Maintained completeness checks, authenticated status and held-resource primitives |
| Signing test facades and stranded evidence receipt projections | Only tests or the removed clearance implementation consumed them | Pinned signing/verification, exact evidence-byte authentication and tests of the actual retained owners |

`nkd.bundle` and the retired clearance, gate-result, waiver, trusted-time, vulnerability-report and qualified
publication families remain reserved but can no longer mint new identities. No signed family is reassigned.
Run/Plan, Definition/Environment and publication schemas do not change in this removal. Generated GitHub bytes
must remain identical. Retired GitLab/Tekton/Argo generation is rejected before authored-input or output I/O.
The hidden `bundle` command is removed instead of preserving its otherwise unused implementation.
The former GitLab-only `runtime.builderImage` field remains allocated in the Environment schema, but nonempty
values fail capability admission; no retained renderer silently accepts an ignored bootstrap configuration.

## Preserved additions and active owners

| Retained surface | Evidence |
|---|---|
| SBOM attestation discovery and publication evidence references | Added 2026-09-11 in `3fb1d6c`; protected even where an accessor has no current external caller |
| Disk-image authoring | Added 2026-09-15 in `9e94296`; protected. Existing bounded disk measurement remains used |
| Generic execution requirements and review fixes | Added 2026-09-16 in `2af7fa5` / `6a3cd04`; protected |
| Execution-class selection | Added 2026-09-17 in `a8526d5`; protected, including identity and fail-before-work checks |
| Direct Packer producer authoring | Added 2026-09-17 in `c93a902`; protected; execution remains capability-gated |
| Definition owner, shared producer session, held publication and archive ownership | Recent locality additions with maintained workflow callers |
| Public SDK loading, preparation, execution, generation and verification | Supported embedding interface, even when the CLI calls the deeper owner directly |
| GitHub action pins, source receipts, archive transport and input readers | Actual generated Run and execution-helper callers; retained after renderer removal |
| Evidence, trust, signing, status and release document modules | Remaining entry points participate in retained execution, verification or registered diagnostics. Receipt constructors remain only when a retained operation actually consumes them; a closed test-only receipt chain does not qualify |
| Architecture tests | Deliberately test-only repository guards, not an unused domain library |

No external Go requirement is removed. All four direct dependencies remain imported, and every listed indirect
requirement remains in the module graph. Offline `go mod tidy -diff` and the third-party notice check are clean;
dependency test requirements retained by Go are not manually stripped.

## Effect on reading and changing the code

The preceding locality change removed compiler-handoff documents, copied producer execution settings and
consumer-to-compiler conversions. This pass removes whole unused workflows and their exclusive transformations.
It does not reorganize working code into another generic framework. The maintained reading path remains
Definition admission → complete compilation → local execution or GitHub generation → observed output checks →
publication → consumer verification; the [operation map](codebase-locality-implementation.md#ownership-changes)
links directly to those owners.

Required outputs and observations remain distinct because completeness compares independent facts. Canonical
logical identity, transport identity and authenticated evidence also retain their separate purposes. File count
alone is not the success criterion: routine changes should have one owning implementation and fewer values to
copy or reconcile. The remaining factory implementation is substantial; this cleanup does not establish that
every future change will be local.
One remaining mapping freezes Run requirements from the complete Plan and projects them back into a compiler
observation request. That mapping and the large compiler/executor files are visible limits of this pass;
the independent required-versus-observed comparison remains necessary.

Across the preceding locality refactor and this removal pass, production Go source under `cmd`, `client`,
`internal` and `pkg` falls from 215 files / 57,571 lines at `16b3775` to 192 files / 47,920 lines. These counts
include comments, blank lines and platform variants. They describe the combined working tree, not this pass
alone, and do not replace the ownership/copy-site measurements in the locality record.

## Verification

The final source passed:

- Full uncached native unit tests and `make check`: formatting, vet, golangci-lint, ShellCheck and uncached
  race/coverage tests (70.4% statements).
- Linux/arm64 race tests for all affected execution, signing, publication, evidence, document, catalog and
  held-file packages, with network disabled and source/module mounts read-only.
- Byte-exact NKE and AICR generation using the rebuilt CLI, plus Actionlint.
- Offline module tidy, third-party notices, changed-document local links and `git diff --check`.
- Final caller/age and independent integration reviews. Remaining scan candidates are supported SDK
  operations/options, required interfaces, protocol constants and the protected September 11 evidence accessor.

Local gate logs are `/tmp/nkd-unused-unit-final.log`, `/tmp/nkd-unused-check-final.log` and
`/tmp/nkd-unused-linux-final-corrected.log`. No new local artifact build, hosted execution or signing acceptance
is inferred from this removal. The prior locality build retains its own exact source and evidence scope.

The Linux gate exposed an existing sandbox thread-affinity defect: Go could migrate between applying
thread-local Landlock/seccomp restrictions and replacing the process. A forced scheduler-handoff regression
failed five consecutive times before the fix. The sandbox owner now performs policy application and exec on
one dedicated locked OS thread; failure completes the goroutine without unlocking, so the runtime retires the
restricted thread. This is an incidental correctness repair, not an expansion of the removal scope or a change
to the declared kernel policy.
Both thread regressions and the real filesystem/network enforcement probe passed 100 repetitions under Linux
race instrumentation. The full affected Linux suite and final native gate then passed with the fix.

The container's local-Git fixture initially followed the host-only worktree metadata path. Setting
`GIT_CEILING_DIRECTORIES=/src` isolates the read-only mounted checkout from Git discovery; the complete catalog
race suite passes with that harness correction. No catalog production change was needed.
