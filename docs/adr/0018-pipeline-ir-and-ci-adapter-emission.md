# ADR-18 — Pipeline IR & CI-adapter emission (hermetic execution)

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-18 in the consolidated NKD ADR set (added to close the recipe→realized-pipeline gap).
- **Relates to:** ADR-02 (IR), ADR-10 (builder types), ADR-07 (builder profile / runner-digest pin), ADR-04
  (verify), ADR-19 (cluster orchestration).
- **Satisfies:** FR-05 (CI portability), FR-06 (hermetic build), SEC-06 (terminal verify), SEC-11 (build
  isolation).

- **Scope:** Sections 1–7 preserve qualified graph/transport authority with helper-owned isolation. Section 8 defines the separate v1 emitter and event policy.

## Context

The IR must become a **concrete, realized pipeline** on whichever CI engine an operator runs, and every engine
must build **byte-identically** and **hermetically**. The open question is what executes each build node, and how
that stays identical across engines without a fragile parallel implementation of rendered strings that must be
kept in sync.

## Decision

### 1. One IR, many adapters — topology/transport only

Each CI adapter (GitHub, GitLab, Tekton, Argo) and the local runner **lower the same IR**. Adapters map **job/step
topology and artifact transport only**, never build semantics. Adding an engine is one thin adapter; adding a
component is one catalog entry and no code.

#### Required engine capability and transport contract

An adapter declares an `EngineCapabilities` contract before lowering. At minimum the engine or its configuration
must provide:

1. a digest-pinned toolchain container launched by the admitted helper/runtime per execution node;
2. an enforceable network-deny mode for build nodes — not merely a convention that the command will avoid the
   network — with fetch and build split into different jobs or pods when isolation cannot change within a job;
3. read-only logical inputs, a private output directory, a fixed environment allowlist, and no ambient secret or
   service-account credentials in build/verify nodes;
4. content-verified artifact/workspace transport between jobs and pods;
5. an always-run finalizer capable of receiving all successful node reports and detecting missing or failed
   nodes; and
6. for a from-source graph, a typed provider execution-attestation adapter that measures the runner, the
   provider-selected container/toolchain identity, sandbox, and any realized closure after materialization, and
   produces ADR-07's stage-two receipt before artifact work.


Lowering also produces a canonical `TransportPlan`. The plan binds each logical item to its producer, exhaustive
consumer set, allow-listed workspace paths, media type, and authentication method: verified runner binary,
canonical node bundle and bundle manifest, fetched input blobs and bindings, component outputs, per-node result
receipts, ValidationVector, and terminal verification result. An implicit shared path is not a transport
contract.

The plan **does not claim the future content digest** of a runner, source closure, component output, or result
that does not exist at emission time; such a digest is unknowable then. The runtime producer emits a content
receipt after creating the item, transport carries the item plus receipt, and every consumer re-hashes the
received bytes before use. Exact digests of already-existing static bundle members stay in the execution-bundle
identity and are never fabricated as future `TransportPlan` values.

Emission validates both the adapter's declared capabilities and the requested policy. If the target engine cannot
express offline isolation, immutable container selection, required transport, or unconditional finalization, the
emitter fails with the coded `engine-capability` error. A from-source emitter also fails with that code when no
provider execution-attestation adapter is configured; runner, toolchain, and sandbox declarations are not
observations. The emitter MUST NOT emit a best-effort pipeline or silently weaken policy. Capabilities supplied by
external infrastructure — a Kubernetes NetworkPolicy, for example — are named as immutable configuration inputs
and checked by an emitted preflight rather than assumed.

The admitted emitter interface is context-first and returns one finite byte value. Publication to a terminal,
file, repository, or provider is a separate owning boundary. Writer-callback emission is rejected before graph
inspection or output, so a blocking caller-controlled writer cannot defeat cancellation.

### 2. A shared, from-source runner owns execution semantics

Build execution is a single **static, from-source runner** (`nkd-exec`, a mode/subset of `nkdctl`) that executes
one IR node (`fetch → build → collect`). Every job uses the same execution code as the local runner, avoiding duplicated build semantics. Reproduction still requires independently checked bytes and execution evidence.
The IR node is passed as a **pinned data file** for auditability: a job runs `nkd-exec exec-node <node.json>`.

### 3. Helper-owned isolation on generic hosted runners

Every job uses a generic hosted runner. The verified `nkd-exec` helper launches each pinned toolchain container
through the same `exec-node --launch` / `Local.Run` path used locally. Adapters render job topology, dependencies
and declared transport; they do not reproduce isolation as provider-specific build syntax. Fetch/pull nodes
materialize pinned inputs first; hermetic build and verification nodes enforce network denial, read-only inputs,
private output and a fixed credential-free environment. Shared code prevents semantic drift; it does not alone
prove byte identity across hosts.

Environment selects the runtime and its pins. Rootless podman is the intended default, with docker an explicit
operator-selected alternative. Admit the configured runtime before work; do not silently choose another sandbox
or weaker isolation. Rootless operation still depends on supported user namespaces, UID mapping, storage,
cgroups and the provider's nesting policy. A mounted privileged Docker socket remains an explicit trust cost.
Missing prerequisites fail capability admission. No bespoke/self-hosted runner labels are emitted.

Helper delivery can use pinned source bootstrap or a pinned helper image. Delivery identity is not qualified
execution authority: a qualified claim still needs exact helper bytes and the ADR-07 runtime observation.
Prebuilt helper-image qualification stays disabled until that admission contract exists. Runtime/helper changes
belong to Environment/profile configuration; component toolchains remain Recipe inputs.

### 4. Runner provenance and conditional self-hosting bootstrap

- The exact runner bytes that execute from-source nodes are always measured and verified against the
  builder-profile runner pin.
- A self-hosting execution builds the runner **from pinned source** — its own recipe, because the tool is not a
  trusted pre-built binary — under a pinned bootstrap toolchain, producing a digest-pinned runner. Its stage-two
  observation records `bootstrapped`, measures that actual bootstrap toolchain, and verifies both identities.
- An execution that uses a preexisting runner records `preexisting`, hashes those exact bytes, and MUST NOT claim
  or require a bootstrap toolchain that did not participate. This prevents a profile declaration from becoming
  fabricated evidence.
- The runner is **output-affecting** (it executes the build), so its digest is a **fail-closed builder-identity attribute in the builder
  profile** (ADR-07), not part of `recipeDigest`: a runner bump is an environment change, not a definition
  change.

The profile-free prebuilt artifact lane still executes a runner to acquire bytes, enforce the transport
allow-list, and perform terminal verification. That runner therefore needs authority even though the artifact
does not need a build-environment profile. Before any provider bundle reads a catalog or graph, the generator
authenticates the exact canonical runner profile under `RoleBuilderProfile` with explicit signer, issuer,
trusted-root, and verifier pins, then derives `RunnerDigest` only from that opaque receipt. The emitted digest
authenticates the interpreter. It does **not** attach the profile to a prebuilt artifact, create a stage-two
build observation, or change the artifact's profile-free identity.

### 5. Hermetic execution

Every qualified build job runs under an **enforced network-deny capability** in its helper-launched pinned container, and
**least-privileged** (ADR-10's rootless-userns for `nix`). Anything needing the network — source fetch,
dependency vendoring, the `nix` locked-closure realize — runs in a **separate, explicit fetch job or pod before**
the offline build and crosses the `TransportPlan` with a runtime producer receipt plus consumer re-hash. Putting
fetch and compile in one network-enabled job and asking the compile command not to connect is nonconforming.
Isolation and the fixed environment/mount contract are identical across all engines and the local runner.

### 6. Lane lowering

- `oci-hermetic` and proposed `nix` build nodes use helper-launched pinned toolchains; unsupported execution adapters fail admission.
- `buildkit-rootless` is the networked wrapped OCI-export mode for generated lifecycle Runs (ADR-10).
  The shared Docker launcher owns its fixed non-root isolation settings and child-process cleanup. Factory
  build projections must match the embedded recipe IR's network, container and platform exactly. The qualified
  pipeline emitter rejects this mode; it cannot silently render it as a network-denied build.
- `prebuilt` nodes lower to acquire-and-digest-verify.
- `exclusion` emits no node.

Generated Docker utility containers run with the host runner's UID/GID and a temporary home, matching the
local runner's ownership contract. Private package layouts must remain reopenable by later host transport
steps. Rootless Podman's existing user mapping is retained; the rootless BuildKit build container keeps its
separate fixed UID and private output-promotion contract.

Each fetched source transports its exact receipt file, using the pipeline module's destination-to-receipt
path mapping. A receipt carrier must not include the shared receipt directory: independent fetch jobs can
otherwise claim the same immutable destination during finalization. The path is a transport description;
receipt authentication and no-replace restoration remain mandatory.

### 7. Terminal verify + granularity

Every emitted pipeline is **job-per-component** and ends in an **unconditional terminal verify** (GitHub
`if: always()`, GitLab `when: always`, Tekton `finally:`, Argo finished-state `depends`), so verify runs even if a
build fails, on every engine (SEC-06). An unchanged recipe identity can identify a prior-verification reuse
candidate, but the candidate diff is not evidence authority: actual skipping also requires an authenticated
prior clearance whose exact member, policy, status, and time bindings remain current.

The terminal signature verifier is itself an authenticated execution input. An emitted qualified pipeline must
name a committed workspace-relative `cosign` executable and carry its exact lowercase `sha256` byte digest. The
stock verifier privately snapshots and executes only bytes matching that pin. An adapter that omits either value,
or cannot make the executable available to the terminal job, fails emission before writing provider syntax.


An unintegrated `exec-node` process cannot turn its own declarations about a container into a provider-authenticated runtime observation. Its result is therefore explicitly **unqualified**. Only a provider attestation adapter may
attach a stage-two receipt and convert exact-node results into qualified terminal evidence; the finalizer rejects
missing, zero, changed-selection, or forged bindings.

### 8. Pipeline-attested generation and credential admission

Two emitters share the node-execution primitive. The single-recipe authoritative emitter retains §1's qualified
provider-attestation gate and stays frozen where adapters are incomplete. The multi-recipe factory emitter may
render from-source production for GitHub at the pipeline-attested tier without a qualified provider receipt.
Self-reported isolation is recorded, never promoted to qualification. Wrapped production uses ADR-10's deferred
fused-job contract. Invoking an upstream reusable workflow needs a separate execution and evidence contract;
v1 invokes pinned source entrypoints instead.

GitLab is build/package-only. GitLab, Tekton and Argo reject signing plans with `CodeEngineCapability` before
output; omitting unsupported signing jobs silently is forbidden. Tekton/Argo factory adapters remain unavailable.

| Event | Admitted work and credentials |
|---|---|
| Fork pull request | Regeneration/schema checks; no credentials |
| In-repository pull request | Build/package; read-only mirror credential on fetch steps only; no OIDC, push or environment access |
| Default-branch push or manual dispatch on that ref | Publish/sign/attest/verify inside a protected environment; phase-scoped registry and OIDC credentials |
| Git tag | No signing trigger |

The current generated Run workflow admits a narrower subset: all pull requests perform only bootstrap and
regeneration checks, and execution requires manual dispatch on the default branch. Default-branch push
publication and in-repository PR builds remain deferred. Live publication/signing/verification jobs name the
`nkd-release` GitHub deployment environment; operators must configure its deployment policy to admit only
the default branch. Only the signing job receives OIDC minting permission. The verifier needs registry read
access and obtains no identity token. Job timeouts are
bounded, and workflow concurrency does not cancel an in-progress publication. Terminal observation and Run
finalization still run after failures within an admitted execution; they do not run for static-only PR checks.

Identity is one committed workflow path and default ref; verification rejects PR refs. Protect workflow,
generator, helper and signing code with branch protection and code ownership. Hosted source access uses a mirror
with vendored inputs. The renderer must reject guards it cannot express, including OIDC outside the signing job
family or credentials in build phases. These are testable contracts; current rendering is not proof that a
provider or protected environment has exercised them.

The v1 scheduler may group consecutive offline observation, SBOM and packaging nodes with identical runner,
container and platform into one bounded job. Every logical node retains its exact execution command and
container isolation; fetch, build, publication and signing remain separate jobs. Internal dependencies use the
same workspace bytes and consumer checks. Each logical output retains its carrier for finalization. A failed
shared job makes its outputs unsuccessful for finalization, even if an earlier step uploaded a carrier.
This provider scheduling choice does not change the logical graph or Recipe identity.

GitHub job and step labels describe the operation and selected component or output. Grouped jobs disclose Helm
image binding when present; stable execution step IDs and exact commands retain the graph binding independently
of display names. Each execution step logs its start, node ID, successful outputs, elapsed seconds and exit
status. Failure preserves the command's exit code and cannot print a successful completion. Packaging reports
the actual bound image digest, and publication reports its persisted immutable reference. Logs are diagnostic,
not authority receipts, and generated progress messages never dump credentials or environment variables.

The compiler creates one release package and one publish/sign/verify route. GitHub handoff artifacts remain
internal job transfers, not additional deliverables. Product names do not cause duplicate copies of the full
closure to be uploaded or published.

The finalizer batches downloads with an exact set of planned carrier names. It still restores every carrier
and records its producer and restore outcome; a successful batch download alone proves no completeness.

Finalizers are scheduled unconditionally and fail on failed/missing producers, failed restore, omitted required
artifacts, malformed or substituted observations/SBOMs, and wrong production predicates. The platform may prevent
the finalizer itself from starting; inspect provider status when no Run record exists (ADR-17).

## Consequences

**Positive**
- Shared execution code removes parallel build implementations; independent observations and comparison still
  establish byte identity.
- Helper-owned isolation shares the local execution path across hosted providers.
- When self-hosting is selected, the tool itself is built from source and pinned and verified, so "trust nothing
  pre-built" extends to `nkd-exec` without pretending every execution performed a bootstrap.
- Adapters are genuinely thin (topology and transport); a new engine is a small adapter.

**Negative / costs**
- A self-hosting bootstrap adds startup time and a bootstrap-toolchain dependency.
- The runner must be a static binary that runs in any toolchain image and needs no network during a build.
- Passing the runner, the node file, and inputs/outputs between jobs is the adapter's transport responsibility.
- Some nominal CI configurations cannot meet the capability contract without operator-provided network policy or
  artifact storage; emission correctly fails for those configurations.

**Neutral**
- The runner is a mode/subset of `nkdctl`; `nkdctl` orchestrator mode (local dev) and `nkd-exec exec-node` (CI)
  share the same execution code.

## Alternatives considered

- **Engine-owned isolation per provider.** Rejected as the shared execution model because provider-specific network control duplicates the local launcher and can require bespoke executors.
- **Emitter renders the build command per node.** Rejected — byte-identity would be *asserted*, since rendered
  strings must match the local runner; that is the fragile parallel implementation this ADR avoids.
- **A fat runner image bundling every toolchain.** Rejected — loses per-component toolchain pinning and bloats the
  image; keep toolchains pinned per component and launched by the helper.

## De-risking spikes

1. **Helper isolation** — exercise the same local and hosted launch path with real pinned containers, network-egress probes, read-only inputs, UID ownership, cancellation and nested-runtime capability failures.
2. **Runner provenance** — prove both allowed origins: a self-hosted runner builds reproducibly and binds its
   actual bootstrap toolchain, while a preexisting runner binds its exact bytes and no bootstrap identity.
3. **Transport/failure matrix** — for every engine, prove the runner, inputs, outputs, node receipts, and the
   ValidationVector reach their consumers, and that an intentionally omitted or corrupted item fails closed.

## Follow-ups

- **ADR-03:** owns the stable coded `engine-capability` failure used by this contract.
- **ADR-07:** the runner digest is a fail-closed builder-identity attribute in the builder profile.
- **ADR-19:** how a full release or cluster fans these per-category pipelines out and aggregates them.

## Acceptance evidence

Current module evidence in `pkg/nkd/pipeline/emitters_test.go`, `pkg/nkd/pipeline/execnode_test.go`, and
`pkg/nkd/pipeline/results_test.go` proves engine-neutral lowering, node serialization, exact bundle/recipe
binding, explicit unqualified exec-node results, terminal receipt enforcement, and that all four default adapters
reject a from-source graph before emitting bytes when provider execution attestation is absent. Prebuilt-only
GitLab emission tests retain unconditional-verify, exact pinned-verifier arguments, transport-shape coverage,
and fail-before-catalog authentication of the exact signed runner profile, including authored-path substitution;
incomplete GitHub, Tekton, and Argo transport adapters reject emission. These tests are **not** evidence that a
provider accepts or can execute the emitted configuration.

Acceptance requires, for GitHub, GitLab, Tekton, and Argo independently:

1. schema/provider validation of the emitted document and a fixture run that transports the runner, fetched
   source, outputs, node receipts, and the ValidationVector through separate jobs or pods;
2. an egress probe proving the build node cannot connect while the fetch node can, plus assertions for read-only
   source, private output, sanitized environment, and absent ambient credentials;
3. corruption/omission tests at every transport edge and a failed-build test proving the terminal verifier still
   runs and reports every missing node;
4. negative capability fixtures proving emission itself fails when offline isolation, finalization, or actual
   stage-two observation cannot be supplied; and
5. an adversarial provider-receipt fixture proving a zero receipt, changed profile/node set, missing observation,
   or result from outside the selected build set cannot reach a qualified terminal report.
