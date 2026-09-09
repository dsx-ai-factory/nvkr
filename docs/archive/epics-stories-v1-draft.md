# NKD v1 — Epics and stories (DRAFT for review)

> **Draft, 2026-09-03 (revision 2).** Derived from `docs/milestones.md` and the task plan in
> `docs/milestone-audit-and-plan.md` (§6, as amended by §7: the NKE OS image build contains Kubernetes
> including control-plane images, CNI, and node-agent, so matrix rows 3, 7, 8 ride inside row 6 and the OS
> image is the central M2 deliverable). One epic per milestone. Cross-cutting work (hygiene,
> Linux gate, ADR-26, reviews) is filed under the milestone it first gates; move it to a fifth
> "Foundation" epic if the board prefers. `Plan:` points at the task in the audit document that
> carries the full engineering detail and gap traceability.

**Conventions** (match `docs/epics-jira.md`): Component `release_eng`, Label `nke_nkd` on everything.
Each story = one metadata line · a short description · bullets, which are the acceptance criteria.
`Verified downstream: X` means the observable proof lands in story X; the story closes on its own bullets.

- **Priority.** `P0 - Must have` = on an exit-criterion critical path. `P1 - Should have` = pre-authorized
  to slip or conditional on a W1 decision.
- **`shrink n`** = position in the slip order (end of file); `(part)` = only the named part slips.
- **Size** is person-days (pd). **Weeks:** W1 Sep 1-7 (two working days left), W2 Sep 8-14,
  W3 Sep 15-21, W4 Sep 22-Oct 1. **DEC-nn** = the W1 decisions in the audit document §5.
- **Never cut** marks the review budget and the ADR-26/evidence work; the plan protects both.

**Glossary** (full table: audit Appendix B). *Row* = a row of the 17-row artifact matrix in `milestones.md`.
*FS-wrap* = from-source build of a pinned NKE component; *pull+bundle* = prebuilt upstream bytes re-delivered
in OCI. *Stand-in* = the public `crictl` built in place of the NKE CLI until NKE source is reachable; never
counts as M1. *Publication root* = NKD's OCI index, the unit that is pushed, signed, and verified. *Node kind* =
a job type the generator can emit. *Evidence tier* = the label stating how output was qualified
(`static-unqualified` today; `pipeline-attested` is the new v1 tier). *Receipt* = a package-issued proof bound
to exact digests. *Held* = a binary opened once and run by file descriptor so it cannot be swapped.
*Negatives* = failure-case (tamper) evidence. *Authority code* = code that mints receipts or makes trust
decisions. *CWE/SWE* = NKE's cluster and site workload-engine controllers.

**Totals:** 4 epics · 58 stories · 82.5 pd (plan §7.5: 82.25 + 0.25 for the M2 run) against 60 pd for three
engineers. Per epic: M1 23 stories / 30.5 pd · M2 16 / 26.5 · M3 8 / 13.0 · M4 11 / 12.5.

---

## Epic M1 — Vertical slice: one signed NKE CLI from a generated GitHub Actions run

| Field | Value |
|---|---|
| **Issue type** | Epic |
| **Epic name** | NKD M1 Vertical Slice |
| **Summary** | One green NKD-generated GitHub Actions run builds the NKE CLI, emits SLSA provenance, keyless-signs, pushes artifact + provenance + signature to OCI; `cosign verify` passes |
| **Priority** | Highest |
| **Depends on** | — |
| **Blocks** | M2, M3, M4 |
| **Target** | Tue Sep 16 (W3). The W1 target in `milestones.md` is not achievable: today the workflow cannot run, nothing signs, and there is no GitHub remote. |
| **Stories** | 23 · 30.5 pd |
| **Decisions** | DEC-01 GitHub home/registry, DEC-02 NKE mirror + credential, DEC-03 "wrapped build" reading, DEC-04 Sigstore + cosign pin, DEC-05 ADR-26 tier, DEC-06 trigger/credential model, DEC-11 catalog modelling, DEC-12 hygiene, DEC-13 staffing/quota, DEC-14 external trust ownership |

**Description.** Stand up the minimum pipeline that satisfies every M1 clause: a new minimal `examples/nke-v1`
FactoryDefinition (public `crictl` stand-in until NKE source is reachable), a registry-facing pinned-cosign
adapter, a production SLSA v1 predicate, `sign` and `verify-publication` node kinds under a new
`pipeline-attested` evidence tier, and a trigger model that publishes and signs only from `main` or dispatch.
Foundation work that blocks the first green run (renderer fix, Linux gate, GitHub provisioning) lives here.

**Definition of done**
- One run URL of the generated `nke-v1` workflow on `ubuntu-latest` with all nine jobs green, recorded in `docs/evidence/` and `reconciliation.md`.
- The build job produced the NKE CLI from a pinned mirror commit (tree hash verified). A stand-in run never closes this bullet.
- `cosign verify` and `cosign verify-attestation --type slsaprovenance1` pass from a laptop with only cosign; the predicate's `builder.id` is the workflow identity.
- The committed workflow regenerates byte-exactly (`--check` green in `ci.yml`), `planDigest` is independent of build stamping, and the helper is digest-checked.
- `ci.yml` is green on Linux including `go test -race`, lint, and `factory-check`.

**Out of scope.** Reusable-workflow (`uses: org/repo/...@sha`) invocation; reproduction-qualified receipts; the
`examples/ai-factory` GitHub bundle (removed from `main`; ai-factory stays GitLab build-only).

### Stories

#### M1-S01 — Generated GitHub jobs can execute the helper: chmod precedes every `nkd-exec` call
`Story · P0 · 0.5 pd · W1 · Depends on: — · Plan: E-1`
The renderer emits `nkd-exec transport-unpack` before `chmod +x nkd-exec`; upload-artifact v4 drops execute bits, so every node job fails at its first Restore step. Emit a `Prepare helper` step right after the helper download; make the validator reject any job that invokes `./nkd-exec` before it.
- Renderer test fails on the old order and passes on the new one.
- `factory generate --check` green after regeneration.
- Verified downstream: M1-S15 (first Restore step passes on GitHub).

#### M1-S02 — `ci.yml` committed; lint works on Go 1.27; the nil-context Linux test passes
`Story · P0 · 0.5 pd · W1 · Depends on: DEC-12 · Plan: E-2a`
Commit `.github/workflows/ci.yml`. Bump golangci-lint to a go1.27 build (or lower `go.mod` to 1.26). Fix `TestStageTwoObservationRunsAfterTrustedFetchMaterialization` in `pkg/nkd/pipeline`, which calls `Local.Run` without a context.
- `golangci-lint run` succeeds locally and in `ci.yml`.
- The pipeline Linux test passes in a `golang:1.27` container.

#### M1-S03 — Linux completion gate green: `-race`, lint, and `factory-check` pass in CI for the first time
`Story · P0 · 1.5 pd · W2 · Depends on: M1-S02, M1-S05, DEC-06 · Plan: E-2b`
The documented gate has never been green on Linux. Build a static fake-cosign helper in `TestMain` so the `pkg/nkd/sign` Linux tests pass under `-race`; add the missing negative test that a dynamic ELF is rejected; add `make factory-check` (byte-exact `--check` of every committed bundle) to `ci.yml` plus a Go test calling `factory.CheckGeneratedBundle`; add a `test` job to `.gitlab-ci.yml` so the current remote runs the suite; set branch protection on `main` and CODEOWNERS per DEC-06.
- One green `ci.yml` run on `ubuntu-latest` with `go test -race ./...`, lint, and factory-check; one deliberately stale bundle fails at factory-check.
- Branch protection (required checks, force-push denied) and CODEOWNERS in place; recorded in `docs/evidence/`.

#### M1-S04 — Repo hygiene: planning wave committed, GitLab green run recorded, stale docs fixed
`Story · P0 · 0.5 pd · W1 · Depends on: — · Plan: E-3`
Commit `milestones.md`, ADR-24/25, SECURITY/CoC/NOTICE/.editorconfig; gitignore the binary, decks, `.DS_Store`, Office lock; remove the stray `.lease` and tracked docx/html exports; `.go-version` = `1.27.0`; scope Dependabot to `ci.yml`. Add an "Operational evidence" section to `reconciliation.md` recording GitLab pipeline 64812267 (sha f0c6890, 23 jobs green, per-job durations) and that the bundle on `main` has not executed. Fix the self-hosted-runner sentences in `examples/ai-factory/README.md` and `docs/ai-factory-operator.md`; re-date M1 in `milestones.md`.
- `git status` clean of planning files; `git diff --check` clean.
- `reconciliation.md` contains the pipeline id and per-job durations; `.go-version` is `1.27.0`.

#### M1-S05 — GitHub home provisioned: repo, Actions, ghcr namespace, `release` environment
`Story · P0 · 0.5 pd · W1 · Depends on: DEC-01 · Plan: A-0a`
File the github.com/NVIDIA request and, the same day, create a throwaway private repo under a team org; enable Actions; create the ghcr namespace and set package visibility; create environment `release` with no required reviewers; disable fork PR workflows; record the OIDC issuer and expected `job_workflow_ref`.
- Repo URL with Actions enabled; NVIDIA-org ticket id recorded.
- Environment and package settings, issuer, and identity string exported to `docs/evidence/`.

#### M1-S06 — Spike: keyless sign/verify against ghcr and runner probes, with go/no-go
`Story · P0 · 1.5 pd · W1 · Depends on: M1-S05, DEC-04 · Plan: A-0b`
A throwaway workflow with `id-token: write` + `packages: write`: pin and `readelf` the cosign binary; push a real NKD publication root to ghcr; `cosign sign` and `attest --type slsaprovenance1`; `verify`/`verify-attestation` online and `--offline`; probe the referrers API; rootless podman bind-mount ownership with and without `--userns=keep-id`; `/dev/kvm` after the udev rule; wall time and disk.
- Go/no-go: `cosign verify` passes against ghcr with the identity pin.
- Probe table in `docs/evidence/` with a named fallback per probe (referrers 404 → tag fallback; artifactType rejected → change registry; ownership wrong → `--userns=keep-id`; dynamic cosign → tier-specific constructor; private package → login step in the evidence procedure).

#### M1-S07 — Minimal `examples/nke-v1` definition with a `crictl` stand-in owns the committed GitHub workflow
`Story · P0 · 1 pd · W1 · Depends on: M1-S01, DEC-01, DEC-03, DEC-11 · Plan: A-6`
Standalone `SourceBuildRegistry` with stand-in source `cri-tools`/component `crictl` in a public `golang@sha256` toolchain; one RecipeTarget (`category: operations`, amd64); one `binary-bundle`; one OCI route to `ghcr.io/<org>/nke/nke-cli` with `credentialRef: github:GITHUB_TOKEN` (placeholder org until M1-S05, then re-pin); FactoryCatalog entry `nke-v1@0.1.0`. Remove the ai-factory GitHub bundle from `main`, regenerate `.github/workflows/nkd-factory.yml` from nke-v1, commit.
- `factory generate --check` green; `factory run` completes the stand-in graph locally.
- The plan records `factoryReference` (generated via the FactoryCatalog, not `--definition`).

#### M1-S08 — Spike: NKE CLI builds hermetically in a public golang image
`Story · P0 · 1 pd · W1 · Depends on: DEC-02, DEC-03 · Plan: B-0`
Request read access to the NKE CLI repo. Clone the last green tag; record language, build system, Go version, private modules, cgo, artifact path. Vendor with network, then `go build` with `--network none` in a digest-pinned public golang image; time it.
- Go/no-go: hermetic build ≤ 15 min in a public image with vendored deps.
- Transcript (image digest, commands, artifact sha256, wall time) in the decision memo, with the fallback chosen if no-go (custom toolchain, last-known-good commit, or stand-in path with the NKE clause recorded external-blocked).

#### M1-S09 — W1 decision memo, external requests filed, 17-row status tracker
`Story · P0 · 0.5 pd · W1 · Depends on: — · Plan: F-1a · Never cut`
Circulate DEC-01..14 with owners, defaults, and deadlines (Sep 8/10/11/12/14). File the product-security Sigstore sign-off request; record ticket ids for all three external requests (NVIDIA org: M1-S05; NKE read access: M1-S08; Sigstore: this story). Create `docs/evidence/status.md` (17 matrix rows × status, evidence URL, owner, blocking external dependency; plus the four exit criteria) for the Friday sponsor review; name the Sep 10 go/no-go and the Sep 16 stop-loss.
- Memo and tracker committed; three ticket ids recorded in the memo.

#### M1-S10 — Renderer/validator: PR runs build only; publish and sign run only on `main` push, `v*` tag, or dispatch
`Story · P0 · 1.5 pd · W2 · Depends on: M1-S01, DEC-06 · Plan: A-1`
Render `on: {pull_request: {paths}, push: {branches: [main], tags: ['v*'], paths}, workflow_dispatch: {inputs: {releaseTag}}}`. Publishing and signing kinds get `if: github.event_name != 'pull_request'` and `environment: release`; `credentialRef: github:GITHUB_TOKEN` maps to `secrets.GITHUB_TOKEN`. Validator requires the guard on those kinds and forbids `id-token` elsewhere.
- Renderer/validator tests for trigger shape, guards, and path filters.
- A PR run skips publish/sign jobs; a push or dispatch run executes them.

#### M1-S11 — cosign acquired in-pipeline as a digest-pinned factory tool
`Story · P0 · 0.5 pd · W2 · Depends on: M1-S06, DEC-04 · Plan: A-2`
Extend `factory.Tool` so an entry with empty `executablePath` is a raw executable download verified against `executableDigest` and written 0555. Pin the exact cosign 2.x release from the spike in `examples/nke-v1/factory.yaml`. Keep the four-action allowlist (no installer action).
- `execute_test` acquires a raw tool from httptest with digest match and mismatch cases.
- Verified downstream: M1-S20 (acquire-cosign job green; sign job executes it).

#### M1-S12 — Registry-facing pinned cosign adapter with receipts (`pkg/nkd/sign/oci.go`)
`Story · P0 · 3.5 pd · W2 · Depends on: M1-S06, DEC-04, DEC-14 · Relates to: M1-S03 · Plan: A-3`
`NewPinnedOCISigner` reusing the held-executable snapshot, static-ELF check (or a tier-specific constructor if the spike showed the official binary is dynamic), environment scrub, owned subprocess lifecycle, context. Subjects only as `repo@sha256:`. Registry auth via a private 0600 `DOCKER_CONFIG`, never argv. `Sign`, `Attest` (predicate via anonymous descriptor), `Verify`, `VerifyAttestation`, each parsed into an opaque receipt bound to subject, identity, issuer, Rekor index. Bounded retry on Fulcio/Rekor transport errors; coded errors; Darwin fails closed before I/O.
- Argv/env/descriptor tests for all four subcommands and error mapping; negatives for tag refs, dynamic ELF, wrong identity, missing `DOCKER_CONFIG`.
- Linux held test green under `-race` once the M1-S03 static helper lands (last acceptance item, not a start condition).

#### M1-S13 — SLSA v1 production predicate (`pkg/nkd/provenance/production.go`)
`Story · P0 · 1.5 pd · W2 · Depends on: — · Plan: A-4`
buildType `https://nkd.dev/buildtypes/github-factory/v1` for built rows and `BuildTypePrebuilt` for mirrored rows; `builder.id` = workflow identity; `invocationId`, `startedOn`, `finishedOn`; externalParameters = plan/factory/catalog/delivery digests + event + inputs; internalParameters = nkd-exec digest (filled once M1-S14 lands), cosign digest, generator version; resolvedDependencies = repo@sha, source commits + tree hashes, graph digests, image digests; byproducts = child manifest digests. Separate `ValidateProduction`; the reproduction validator is untouched.
- Golden canonical predicate; `ValidateProduction` rejects missing `builder.id`, `invocationId`, or times.
- A test asserts the reproduction validator still rejects the production shape.

#### M1-S14 — Helper pinned: generator version out of `planDigest`; nkd-exec digest guard in every job
`Story · P0 · 1 pd · W2 · Depends on: M1-S01, DEC-12 · Plan: E-4`
`Plan.Digest` hashes a copy with `GeneratorVersion` cleared (recorded as transport metadata). The bootstrap emits `sha256sum nkd-exec` and uploads it; every node job checks it before `chmod`. The digest feeds provenance `internalParameters.nkdExecDigest`. ADR-26 states this guards transport corruption, not a malicious same-repo PR.
- Stamped and unstamped builds produce identical `planDigest` (test); renderer test asserts the guard step.
- A tampered helper artifact fails at the guard.

#### M1-S15 — First NKD-generated run on GitHub: stand-in, unsigned publish (Mon Sep 8)
`Story · P0 · 1.5 pd · W2 · Depends on: M1-S01, M1-S06, M1-S07 · Plan: A-7`
Dispatch the nke-v1 stand-in workflow. Expected green: bootstrap, fetch, build, terminal-observe, package, publish (unsigned diagnostic root with `GITHUB_TOKEN`). Debug budget covers rootless podman ownership (`--userns=keep-id` behind the runtime selector if needed), short-name resolution, egress. Record wall time and sizes.
- Run URL with all six jobs green and a pushed root digest, logged in `docs/evidence/` as **mechanics evidence, not M1 completion**.

#### M1-S16 — `sign` and `verify-publication` node kinds; `pipeline-attested` tier; identity/issuer pinned in the DeliveryProfile
`Story · P0 · 3.5 pd · W2 · Depends on: M1-S05, M1-S10, M1-S12, M1-S13, DEC-05 · Plan: A-5`
Per OCI route emit `sign/<route>` then `verify-publication/<route>`. `DeliveryProfile.signing` gains `identity` (regexp, e.g. `^https://github.com/<org>/<repo>/\.github/workflows/nkd-factory\.yml@refs/(heads/main|tags/v.*)$`) and `issuer` (`https://token.actions.githubusercontent.com`) from the values recorded in M1-S05; `verify-publication` passes both to the adapter. `executeSign` re-opens the downloaded layout, recomputes the index digest, and refuses to sign if it differs from the plan's artifact digest or the published ref. Replace the three string-equality checks on the qualification label with `factory.EvidenceTier {static-unqualified, pipeline-attested}`; header `# qualification: pipeline-attested (keyless cosign + SLSA v1 production provenance; not reproduction-qualified)`; publish gains an admission check and tier/source-lane annotations. Sign-family jobs get `contents: read, packages: write, id-token: write`; verify is never `if: always()`. GitLab renders sign-family as skipped.
- Compile test asserts node/edge shape per route; execute test with a fake cosign asserts predicate contents, argv, and refusal on digest mismatch.
- Renderer test asserts identity and issuer flags on every verify-publication node and the permissions/guards; validator rejects a route with a sign node and no identity/issuer, `id-token` outside sign-family jobs, and an unknown tier.

#### M1-S17 — NKE CLI source mirrored to a private GitHub repo; fetch credential scoped to private-source fetch jobs
`Story · P0 · 2 pd · W2 · Depends on: M1-S05, M1-S08, DEC-02 · Plan: B-1`
Push the pinned tag (with `vendor/`) to `github.com/<org>/nke-cli-mirror` (private). Catalog source gains `private: true`; the fetch stage honours `NKD_GIT_CREDENTIAL` via an askpass helper for private sources only; the renderer emits the secret only on fetch jobs of private sources, never on `--network none` build jobs; validator enforces; fork PRs fail closed with a coded error.
- Renderer/validator tests that the credential appears only on private-source fetch jobs; fetch test with a fake askpass.
- Secret owner and rotation recorded (DEC-02).

#### M1-S18 — NKE CLI is a pinned wrapped catalog component; stand-in swapped out
`Story · P0 · 1 pd · W2 · Depends on: M1-S07, M1-S08, M1-S17, DEC-03 · Plan: B-2`
Add source `nke` {repo, constraint, toolchainImage@sha256, vendorCommand, private} and component `nke-cli` {buildCommand, artifactPath, outputType host-package} to the nke-v1 catalog; `nkdctl catalog pin nke --tag <green tag>`; replace `crictl` in the RecipeTarget; regenerate; commit. This satisfies the M1 clause "via a pinned wrapped NKE reusable build" under the DEC-03 reading.
- Catalog load and `--check` green; `nkdctl recipe --data examples/nke-v1/catalog --category operations` resolves.
- Verified downstream: M1-S20 (build job produces `bin/nke` from the pinned commit).

#### M1-S19 — Named review R1: M1 authority code
`Story · P0 · 1.5 pd · W3 · Depends on: M1-S10, M1-S12, M1-S13, M1-S14, M1-S16, M1-S17 · Plan: E-7a · Never cut`
Independent review of M1-S01/S14 (renderer, helper guard), S10, S12, S13, S16, S17 against the `CLAUDE.md` working contract: capability admission before I/O, context threading, receipts bound to exact digests, coded errors, no caller booleans as authority, no secret on argv or on `--network none` jobs.
- Review record (PR approvals with checklist) linked from `reconciliation.md`; zero open critical findings before M1-S20 is declared evidence.

#### M1-S20 — M1 signed run and evidence log (target Tue Sep 16)
`Story · P0 · 2.5 pd · W3 · Depends on: M1-S14, M1-S15, M1-S16, M1-S18, M1-S19 · Plan: A-8, F-2 (M1 entry) · Never cut (evidence log)`
Push to `main`: bootstrap + helper digest; acquire cosign; fetch from the private mirror; hermetic build; observe; package; publish; sign; verify-publication. From a laptop with only cosign: `cosign verify` and `verify-attestation --type slsaprovenance1`. Write `docs/evidence/<date>-m1.md` (run URL, commit, planDigest, durations, refs@digest, cosign version/digest, laptop transcripts, Rekor index, probe table) and link it from `reconciliation.md`.
- Run URL with all nine jobs green where the build job produced `bin/nke` from the pinned mirror commit; the fetch receipt records the pinned commit and a tree hash equal to the lock entry. **This is the M1 exit evidence.**
- Laptop transcripts pass; the decoded predicate's `builder.id` equals the `job_workflow_ref` recorded in M1-S05 and matches the identity regexp used on the laptop.
- If the mirror is unavailable: the same run on the stand-in, logged as mechanics evidence with the NKE clause external-blocked; the epic stays open on DoD bullet 2.

#### M1-S21 — ADR-26 draft: the v1 `pipeline-attested` lane
`Story · P0 · 1.5 pd · W3 · Depends on: M1-S06, M1-S08, DEC-05, DEC-06 · Plan: F-1b · Never cut`
Write `docs/adr/0026-v1-pipeline-attested-lane.md` (Proposed, named deciders). Purpose: name the v1 evidence tier so the repository's own doctrine stops forbidding what M1 ships (today ADR-18 rejects from-source emission without a provider attestation adapter). Contents: (a) one-clause amendments to ADR-04, -09, -16, -18 §1.6/§7, -20 §4, -21 §3, -23, -25 (detail in Plan F-1b); (b) the rules the pipeline relies on: SLSA Build L2 self-attested, cosign version and tag-based storage, trigger/identity rule, private-mirror credential model, waiver policy, exclusions, mirror-vs-build labelling, revocation, org migration, freeze list, glossary; (c) `milestones.md` and ADR-26 added to the `CLAUDE.md` authority order; support-boundary table updated.
- ADR-26 committed with named deciders; `CLAUDE.md` rows describe the pipeline-attested GitHub lane; reconciliation addendum present.

#### M1-S22 — Generated jobs carry `timeout-minutes`, `retention-days`, `concurrency`; run cost measured against quota
`Story · P0 · 1 pd · W3 · Depends on: M1-S01, M1-S15, DEC-13 · Plan: E-5`
`DeliveryProfile.runtime` gains `timeoutMinutes` (60), `retentionDays` (7), `concurrencyGroup`; renderer emits them; validator requires them. After the first real runs, record wall time, sizes, disk headroom, and minutes per run, and compare to the quotas fixed in DEC-13.
- Renderer/validator tests for the three keys.
- Budget table with measured minutes and the quota comparison in `docs/evidence/` after M1-S20.

#### M1-S23 — Opt-in real-cosign integration job in `ci.yml`
`Story · P1 · 0.5 pd · W4 · Depends on: M1-S03, M1-S12 · Plan: D-7 · shrink 5 (part)`
Env-gated job on `ubuntu-latest` with `id-token: write` that runs the pinned real cosign through the adapter and the blob path against a scratch ref, asserting exit codes and the argv assumptions.
- Job green with a real cosign; supported cosign version range recorded in `docs/evidence/`.

---

## Epic M2 — Breadth: the in-scope artifact set, each delivered and signed in OCI

| Field | Value |
|---|---|
| **Issue type** | Epic |
| **Epic name** | NKD M2 Breadth |
| **Summary** | The generated pipeline produces the in-scope set (from-source + pull+bundle), each signed in OCI with provenance; the four recipe-class variants each yield one signed OCI bundle |
| **Priority** | High |
| **Depends on** | M1 |
| **Blocks** | M3 (rows to qualify), M4 (rows to tag) |
| **Target** | Signed OS image and M2 core by Fri Sep 25 (W4). CNCF charts, from-source CWE/NKE images, and the site-controller SKU slip or take their fallback. |
| **Stories** | 16 · 26.5 pd |
| **Decisions** | DEC-09 OS image = wrapped NKE build (fallback: prebuilt QCOW2 mirror), DEC-15 `wrapped-build` node class, DEC-10 row meanings, DEC-08 chart format, DEC-02 NKE mirrors (CLI and node-images repos) |

**Description.** The NKE OS image build bakes in Kubernetes (node side and control-plane images), CNI, and
node-agent, so the central M2 deliverable is one signed disk-image artifact wrapped from the NKE node-images repo at
a pinned commit through a new `wrapped-build` node class (pinned privileged container, network during the build,
fused build/observe/publish because the output is gigabytes). Around it: the existing per-route publish job learns
to push container images and raw blobs (digest-preserving mirror, no-overwrite, idempotent skip) for CWE/NKE and
third-party images; a standalone prebuilt catalog for the remaining pull+bundle rows; and the class-variant roots
(operator tooling, AICR bundle, tenant SKU, site-controller SKU). Rows 3, 7, 8 have no separate artifact.

**Definition of done**
- The OS image is pushed as a signed, attested disk-image artifact built from the pinned NKE commit; `cosign verify` and `verify-attestation` pass from a laptop; its provenance names the NKE commit and tree hash. (Fallback: the prebuilt QCOW2 mirrored by digest, recorded as pull+bundle.)
- Every other delivered component row has a pinned amd64 digest (`pins.lock.yaml`, or the tool digest in `factory.yaml` for the AICR CLI), a pushed `repo@digest` in `publications/*.txt`, and green `verify-publication`.
- `docker pull` of a mirrored CWE/NKE or third-party image by digest works and its signature verifies; a second run skips already-present mirrors.
- The tenant SKU root and, if pins land, the site-controller root are pushed, signed, and verified, each carrying a `ClusterComposition` that binds the OS image under the `os` category; the AICR bundle root is pushed, signed, and verified.
- Rows that took a fallback or were deferred are recorded in ADR-26, `reconciliation.md`, and the `milestones.md` Source Lane column with reasons and dates.

**Out of scope.** Separate delivery of node-agent, node-side Kubernetes, control-plane images, or CNI (inside the
image); NKD's own from-source Kubernetes lane and custom toolchains; SBOM/vuln scanning; upstream signature
verification of pulled artifacts; archive-member extraction; multi-arch.

### Stories

#### M2-S01 — Spike: upstream reachability from hosted runners, disk headroom, registry layer limits
`Story · P0 · 0.5 pd · W2 · Depends on: M1-S05 · Plan: C-0`
Pull a public image by digest through the existing prebuilt image lane (never tested against a real registry); HEAD dl.k8s.io, get.helm.sh, the AICR chart sources, and any third-party image registries on the DEC-10 list; `df` on `/` and `/mnt`; ghcr per-layer limits for a multi-gigabyte disk image; HEAD the prebuilt QCOW2 URL for Content-Length (fallback path only).
- Probe table rows: reachability, sizes, disk headroom, layer limits, recorded before the Sep 10 checkpoint.

#### M2-S02 — nke-v1 prebuilt catalog and routes for the remaining pull+bundle rows; live `pin-artifacts`
`Story · P0 · 2.75 pd · W3 · Depends on: M1-S07, DEC-10 · Plan: C-1, A-13`
Catalog entries: kubectl (dl.k8s.io, host-package); helm tgz (raw); AICR-referenced chart tgz (raw); the third-party images referenced by AICR and NKE charts that are not baked into the OS image (by digest). Node-side Kubernetes, control-plane images, CNI, and node-agent are inside the OS image and get no entries. Code: allow a prebuilt component to omit `source:` when its ref is not version-templated. The nke-v1 definition and delivery gain one RecipeTarget and one OCI route per mirrored image row under `ghcr.io/<org>/nke/<name>`. Run `pin-artifacts --all --arch amd64` on Linux against real upstreams and commit the lock.
- Catalog load green with all entries; source-less prebuilt tests; `pin-artifacts` run log and committed lock.
- `factory generate --check` green with the new routes; the compiled graph shows one publish → sign → verify-publication chain per mirrored image.
- Compile/execute test: a `binary-bundle` with raw tgz inputs and `tool/aicr` lands executables at `/usr/local/bin/<name>` 0755 and tarballs at `/usr/local/share/nke/<name>`.

#### M2-S03 — Publish job pushes image layouts and raw blobs; no-overwrite and idempotent skip
`Story · P0 · 4.5 pd · W3 · Depends on: M1-S16, DEC-10 · Relates to: M2-S02 · Plan: A-9`
`delivery.PushImageLayout` (preserve the pinned manifest digest; fail closed on mismatch) and `delivery.PushBlobArtifact` (single-layer artifact, canonical config, media type by outputType); `executePublish` dispatches on input kind (root, image layout, raw/tarball/disk blob); `remote.Head` pre-check in every push path (different digest → `CodeImmutableViolation`; same digest → skip and record); tier and source-lane annotations on every manifest; reject `PlainHTTP` off-loopback.
- In-process registry round-trip tests for root, image layout, raw blob, Head no-overwrite, same-digest skip.
- `docker pull ghcr.io/<org>/nke/<cwe-image>@sha256:<pinned>` succeeds and cosign verify passes (needs an entry from M2-S02); a second run skips present mirrors.

#### M2-S04 — Operator tooling bundle and AICR bundle roots
`Story · P0 · 1.25 pd · W4 · Depends on: M2-S02, M2-S03, DEC-08 · Plan: C-2 (bundles)`
`operator-tooling` binary-bundle [kubectl, helm tgz, tool/aicr] (kubeadm and CNI are inside the OS image and drop out); `aicr-bundle` oci-root enclosing the existing `gpu-runtime` AICR tree plus the AICR chart tgz blobs (the DEC-08 default; CNCF charts from M2-S11 only if required); `nke-charts` only if NKE chart sources are mirrored, else Row 14 (NKE helm chart bundles) recorded blocked. One signed OCI route each.
- Both roots compile with one publish → sign → verify-publication chain each.
- Verified downstream: M2-S13 (pushed, signed, verified), M3-S05 (smoke check on the tooling bundle).

#### M2-S05 — Tenant SKU root binding the OS image
`Story · P0 · 0.25 pd · W4 · Depends on: M2-S02, M2-S03, M2-S15 · Plan: C-2 (tenant)`
ClusterSpec `gpu-tenant` (type tenant) whose role binding uses `nodeLevelCategories: [os]` so the composition binds the OS image recipe (not a Kubernetes recipe), plus the AICR bundle and any CWE/NKE images; an `oci-root` nesting bundles + composition and one signed route. Signed SKU bundle = signed root carrying `ClusterComposition` (DEC-05).
- Root compiles with its composition and signed route.
- Verified downstream: M2-S13.

#### M2-S06 — Site-controller SKU root
`Story · P1 · 1 pd · W4 · Depends on: M2-S05, M2-S07, DEC-10 · Plan: C-2 (site-controller) · shrink 6`
ClusterSpec `site-controller` (type operator; the OS image under `os` plus platform-services with capi-core and CWE/SWE image pins from M2-S07) with an `oci-root` and one signed route. Composition-only root or post-v1 if CWE/SWE pins are late.
- Root compiles with its composition and signed route, or the deferral is recorded with a date.
- Verified downstream: M2-S13.

#### M2-S07 — CWE controller and NKE cluster images mirrored by digest as prebuilt image components
`Story · P0 · 0.5 pd · W4 · Depends on: M2-S02, M2-S03, DEC-10 · Plan: B-3 (fallback)`
Rows 4-5 at minimum cost (Time-Constrained Decision 1): prebuilt `image:` components pinned by digest from a hosted-runner-reachable registry, one route each, delivered via M2-S03. Recorded as pull+bundle in ADR-26 and the matrix Source Lane column. Confirm with NKE that these are not already baked into the OS image; if they are, this story closes as not needed.
- Entries and routes compile; `pin-artifacts` resolves the digests.
- Verified downstream: M2-S13.

#### M2-S08 — CWE controller and NKE cluster images built from source (FS-wrap)
`Story · P1 · 2 pd · W4 · Depends on: M1-S18, M2-S07, DEC-02 · Plan: B-3 (FS-wrap) · shrink 3`
For each image that builds like the CLI: mirror at the pinned tag (M1-S17 procedure), add source + component to the nke-v1 catalog (images via M2-S10 only if it lands). Decision to proceed by Sep 18; otherwise M2-S07 stands.
- Fetch/build jobs run from pinned commits; the M2 run pushes and signs the CWE/NKE image refs.

#### M2-S09 — Spike: the NKE OS image build runs on a GitHub-hosted runner (go/no-go for DEC-09 by Sep 10)
`Story · P0 · 1 pd · W2 · Depends on: M1-S05, M1-S08, DEC-02 · Plan: B-5`
Obtain the build's single entrypoint from NKE. Run it on `ubuntu-latest` inside a privileged pinned container with network, from the pinned node-images commit; measure wall time, peak scratch disk (`/` and `/mnt`), output size and format; record whether a component manifest exists inside the image and whether any input is fetched unpinned. Fallbacks: a larger GitHub-hosted runner if disk-bound; the prebuilt QCOW2 mirror (M2-S16) if the build cannot run.
- Go/no-go: the build completes on a hosted runner within the job budget with a pinned commit and no floating inputs.
- Transcript (entrypoint, container digest, wall time, disk, output size, manifest path or its absence) recorded in the DEC-09 memo before Thu Sep 10.

#### M2-S10 — `oci-image` build kind: a built binary wrapped into a deterministic image on a pinned base
`Story · P1 · 2.5 pd · W4 · Depends on: M2-S03, M2-S08 · Plan: A-11 · shrink 1`
Only if M1-S08/M2-S08 show CWE/NKE images are Go binaries on a distroless-style base and capacity exists after Sep 10. PackageSpec kind `oci-image` {base image@sha256 from a pull node's layout, entrypoint, one input}; build via go-containerregistry (pinned base + one epoch-mtime layer), never `podman build`. Default fallback (no code): M2-S07 mirrors.
- Deterministic image digest across two runs from a fixture binary and local base.
- The image runs `--version` in podman on the runner and cosign verify passes.

#### M2-S11 — `helm-charts` package kind emitting CNCF OCI chart artifacts
`Story · P1 · 3 pd · W4 · Depends on: M2-S02, M2-S03, DEC-08 · Plan: A-12 · shrink 2`
Only if consumers require `helm pull oci://` (DEC-08 by Sep 12). Inputs = raw chart tgz; emit per chart a manifest with config `application/vnd.cncf.helm.config.v1+json` and layer `application/vnd.cncf.helm.chart.content.v1.tar+gzip`; push each to `<repo>/charts/<name>:<version>`; sign/verify iterate refs. Fallback (0.5 pd, already in M2-S04): chart tgz blobs in the signed root.
- Round-trip test pushes a chart and `helm pull oci://` extracts it from an in-process registry.
- Chart refs signed and verified in the M2 run.

#### M2-S12 — Named review R2a: publish branches, wrapped-build node class, catalog prebuilt changes
`Story · P0 · 0.75 pd · W4 · Depends on: M2-S02, M2-S03, M2-S14 · Plan: E-7b (M2 half) · Never cut`
Independent review of digest preservation on mirror, no-overwrite semantics, raw blob media types, source-less prebuilt lock rules, route wiring, and the privilege/network admission rules of the `wrapped-build` class.
- Review record linked from `reconciliation.md`.

#### M2-S13 — M2 run and evidence log
`Story · P0 · 0.5 pd · W4 · Depends on: M2-S03, M2-S04, M2-S05, M2-S07, M2-S12, M2-S15 · Plan: C-2 (run), F-2 (M2 entry) · Never cut (evidence log)`
Regenerate nke-v1 with every enabled route, push to `main`, and record the run. Rows from M2-S06, S08, S10, S11, S16 are included if they landed.
- One main-push run URL with every enabled route's publish → sign → verify-publication green, including the OS image. **This is the M2 exit evidence.**
- `docs/evidence/<date>-m2.md` with a 17-row table (ref@digest and `publications/*.txt` entry, "inside the OS image" for rows 3, 7, 8, or deferral reason and date), linked from `reconciliation.md` and `docs/evidence/status.md`.

#### M2-S14 — `wrapped-build` node class: pinned external build with declared privilege and network, fused build/observe/publish
`Story · P0 · 3 pd · W3 · Depends on: M1-S16, M2-S09, DEC-15 · Plan: B-6`
Catalog and IR: component fields `entrypoint`, `network: required`, `privileged: true`, admitted only for `outputType: disk-image` and only when the source is a declared wrapped build. `exec-node --launch` passes `--privileged --device /dev/loop-control` (and `/dev/kvm` when present) for this class. The compiler fuses build → re-hash → publish → sign → verify-publication into one job (no artifact carriers for the multi-gigabyte output). Validator requires the class on the node and forbids privilege or network on any other build node. Manifest annotation `dev.nkd.source-lane: wrapped-build`; ADR-26 states the hermeticity claim is the wrapped build's, not NKD's.
- Admission tests: privilege/network accepted only on a wrapped-build disk-image component; rejected elsewhere.
- Renderer/validator tests for the fused job shape and the annotation; execute test with a fixture entrypoint producing a fixture image.

#### M2-S15 — NKE OS image as an `os`-category component in `examples/nke-v1`; first signed image run
`Story · P0 · 1 pd · W4 · Depends on: M1-S17, M2-S09, M2-S14, DEC-02 · Plan: B-7`
Source `nke-node-images` (mirror URL, pinned commit, tree hash, `private: true`, inputs pinned by the commit) and component `nke-os-image` (`category: os`, `outputType: disk-image`, entrypoint and container from M2-S09); regenerate; push to `main`; from a laptop, `cosign verify` and `verify-attestation` on the image reference.
- Run URL with the fused OS image job green; the provenance names the NKE commit and tree hash; laptop verify transcripts recorded.
- Verified downstream: M2-S13 (M2 evidence), M3-S08 (image manifest check).

#### M2-S16 — Fallback: prebuilt QCOW2 mirrored by digest as a signed `disk-image` artifact
`Story · P1 · 2 pd · W4 · Depends on: M2-S01, M2-S03, DEC-09 · Plan: A-10, B-4 · shrink 4 (fallback)`
Executed only if M2-S09 is no-go on Sep 10. Route option `direct: true` compiles a raw/disk-image pull artifact to one `mirror/<route>` node that streams the pinned URL through sha256 into a single-layer `application/vnd.nkd.disk-image.v1` artifact (no carriers, no 10-minute HTTP timeout); sign/verify hang off it. Prebuilt component `nke-os-image` {url, outputType disk-image, digest = the 2026-06-25 QCOW2 delivery digest recorded in `docs/nkd-srd-doc.md` (`sha256:6d73cd92…`), re-confirmed with URL and size; evidence none with waiver}. Provenance is mirror provenance; the row is recorded as pull+bundle.
- Execute test streams a fixture blob from httptest into an in-process registry and verifies the digest.
- QCOW2 pushed, signed, verified with size and time recorded; if the URL is unreachable or > ~8 GB, row 6 is recorded infeasible by Oct 1 with a post-v1 date.

---

## Epic M3 — Qualification: a fail-closed recipe/evidence gate that emits signed evidence

| Field | Value |
|---|---|
| **Issue type** | Epic |
| **Epic name** | NKD M3 Qualification |
| **Summary** | Every recipe passes an automated gate (schema + pin-consistency + signature/provenance presence + tooling-bundle smoke) that emits signed qualification evidence; a tampered artifact or missing provenance fails closed |
| **Priority** | High |
| **Depends on** | M1 (sign/verify adapter, tier), M2 (routes to qualify) |
| **Blocks** | M4 (release-tag depends on qualify) |
| **Target** | Thu Oct 1 (W4); the module is built in W3 so M3 is not a W4-only bet |
| **Stories** | 8 · 13.0 pd |
| **Decisions** | DEC-05 (`nkd.qualification-evidence/v1` kind, signer role, waiver policy, exclusions), DEC-14 (trusted root ownership), DEC-15 (what the gate can check inside an opaque wrapped-build image) |

**Description.** A new deep module `pkg/nkd/qualify` runs the four named checks over every published subject
and mints a canonical, signed `QualificationEvidence` only on a passing verdict; a `qualify` node kind renders
it as a must-fail job after `verify-publication` (local mode on pull requests); negatives are produced out of
band with no production disable switch. Recipe/evidence-level only; ADR-25's cluster-scoped qualification stays post-v1.

**Definition of done**
- The `qualify` job is green on the main-push run for every recipe target and route in nke-v1, and `cosign verify-attestation --type https://nkd.dev/attestations/qualification-evidence/v1 <ref>` passes from a laptop for each subject.
- A failing check never produces an attestation (test-enforced).
- The scratch-repo tamper script yields three distinct fail-closed codes (mutated blob, deleted attestation, stripped signature); one red GitHub run via the read-only `verifyRef` path is recorded (shrink 7).
- Waivers for `evidence: none` prebuilt subjects are visible inside the signed evidence.
- The OS image's embedded Kubernetes, CNI, and node-agent versions are read from the image and match the versions expected for the pinned NKE commit; a mismatch fails the gate.

**Out of scope.** SBOM generation and vulnerability scanning in the pipeline; cluster conformance; ADR-25 harness protocol and coverage profile; booting the image.

### Stories

#### M3-S01 — `pkg/nkd/qualify`: evidence kind, signer role, four checks, a gate that never attests a failing verdict
`Story · P0 · 4.5 pd · W3 · Depends on: M1-S12, M1-S16, DEC-05 · Plan: D-1`
Evidence {schemaVersion, tier, factoryReference, planDigest, catalogDigest, lockDigest, deliveryProfileDigest, subjects[]{route, ref@digest, childDigests}, checks[]{name, verdict, detailDigest}, waivers[], verdict, checkedAt, checkerIdentity} as canonical kind `nkd.qualification-evidence/v1`; `trust.RoleQualification`. Checks: schema (exact decode of plan/graphs/compositions + `Plan.Validate` + catalog load with lock validation); pin-consistency (published child digests equal plan artifact digests; every pull digest equals its `ArtifactPin`); presence (`Verify` + `VerifyAttestation(slsaprovenance1)` per subject; `CodeProvenanceMissing` when absent; waivers for `evidence: none`); smoke (per-component argv table on an extracted bundle inside the utility container, `--network none`). `Gate` returns a coded error and writes unsigned evidence locally on any failure; passing evidence is attested per subject.
- Golden canonical evidence; table-driven tests: pass; mutated blob → fail; missing attestation → `CodeProvenanceMissing`; wrong identity → fail; smoke non-zero → fail.
- A fake-signer test asserts `Attest` is never called on failure.

#### M3-S02 — `qualify` node kind: must-fail job after `verify-publication`; local mode on pull requests
`Story · P0 · 2 pd · W4 · Depends on: M2-S03, M3-S01 · Plan: D-2`
One `qualify/<definition>` node depending on all verify-publication nodes (remote) or all package nodes (local mode); output `.nkd/factory/qualification/<name>.json`; on push/dispatch attest the evidence to every subject. Thin read-only verb `nkdctl factory qualify --ref <repo@digest>`.
- Compile test asserts one `qualify/<definition>` node depending on every verify-publication node (remote) or every package node (local); renderer test asserts `contents: read, packages: write, id-token: write` and no `if: always()` on qualify or terminal-observe; validator rejects `id-token: write` on any other job.
- Verified downstream: M3-S05 (qualify job green; evidence attestation verifiable from a laptop).

#### M3-S03 — Negatives: Go tamper tests and the scratch-repo tamper script
`Story · P0 · 1.25 pd · W4 · Depends on: M3-S02 · Plan: D-3 (a, b)`
(a) Go negatives with the in-process registry and a fake signer. (b) `scripts/demo-qualification-tamper.sh`: copy a qualified root and its `.sig`/`.att` to a scratch ghcr repo, then mutate a blob, delete the attestation, strip the signature; run `factory qualify --ref` against each. Explicitly rejected: any flag that makes the sign node skip attestation; any key-based mode in the production adapter.
- Script log with three distinct failure codes; contract negatives green.

#### M3-S04 — One red GitHub run via a read-only `verifyRef` dispatch path
`Story · P1 · 0.75 pd · W4 · Depends on: M3-S03 · Plan: D-3 (c) · shrink 7 (part)`
`workflow_dispatch` input `verifyRef` runs only verify-publication + qualify against the given reference; the validator forbids `id-token: write` on that path so it cannot publish, sign, or mint.
- One red run URL where qualify fails on the mutated scratch ref, recorded in the M3 evidence file.

#### M3-S05 — Gate across every delivered route; smoke argv table; waivers; M3 evidence log
`Story · P0 · 1.25 pd · W4 · Depends on: M2-S04, M2-S05, M3-S02, M3-S03 · Plan: D-4, F-2 (M3 entry) · Never cut (evidence log)`
Enable qualify over all delivered routes (the OS image, CWE/NKE and third-party image mirrors, bundles, SKU roots, charts). Smoke argv table per bundle: operator-tooling (`kubectl version --client`, `helm version`, `aicr version`) and nke-cli (`nke version`); the OS image's check is M3-S08. Waivers for `evidence: none` subjects recorded inside the signed evidence.
- One run with every route's qualify check green and one evidence attestation per subject; waivers visible in the decoded evidence. **This is the M3 exit evidence.**
- `docs/evidence/<date>-m3.md` (run URL, per-subject evidence attestation refs, laptop `verify-attestation` transcript, tamper-script log, red-run URL if M3-S04 landed) linked from `reconciliation.md`.

#### M3-S06 — Named review R2b: qualify module and node
`Story · P0 · 0.75 pd · W4 · Depends on: M3-S02 · Plan: E-7b (M3 half) · Never cut`
Independent review of fail-closed semantics, "failing evidence is never attested", and digest binding.
- Review record linked from `reconciliation.md`.

#### M3-S07 — `nkdctl trust init`: pinned trusted root for verify/qualify; `docs/consumer-verify.md`
`Story · P1 · 1.5 pd · W4 · Depends on: M1-S16, M3-S02, DEC-14 · Plan: A-16 · shrink 8`
Pinned `trusted_root.json` + digest referenced from `DeliveryProfile.signing.trustedRoot` (identity and issuer already live there from M1-S16) so verify-publication and qualify run `--trusted-root`. Consumer doc: exact cosign commands (with/without `--offline`, login step if packages are private), read-only consumer identity, org-migration notice.
- Root digest pinned and verified on load; renderer test shows `--trusted-root` on verify and qualify nodes.
- `consumer-verify.md` commands succeed from a clean machine against the M1 reference.

#### M3-S08 — Image manifest check: embedded component versions match the pinned NKE commit
`Story · P0 · 1 pd · W4 · Depends on: M2-S15, M3-S01, DEC-15 · Plan: D-8`
In the qualify gate, mount the published OS image read-only on the runner (loop device for RAW; `qemu-img convert` or `qemu-nbd` through the pinned qemu toolchain for qcow2), read the component manifest NKE writes at a known path (fallback: the package database), and compare the embedded Kubernetes, CNI, and node-agent versions to the versions recorded for the pinned node-images commit. Result lands in the `pin-consistency` and `smoke` checks of `pkg/nkd/qualify` and in the signed evidence.
- Fixture image with a manifest: match passes; a mutated version fails with a coded error and no attestation.
- The M3 run's evidence for the OS image subject lists the embedded versions; the Oct 1 demo shows one mismatch negative.

---

## Epic M4 — Automated releases: pin bump → regenerate → re-qualify → signed, tagged release

| Field | Value |
|---|---|
| **Issue type** | Epic |
| **Epic name** | NKD M4 Automated Releases |
| **Summary** | An automated workflow detects an upstream pin update, regenerates and re-qualifies, and tags a signed OCI release set; a manual dispatch produces the same tagged, signed release |
| **Priority** | High |
| **Depends on** | M1 (trigger model), M2 (publish branches, prebuilt catalog), M3 (qualify node) |
| **Blocks** | — |
| **Target** | Thu Oct 1 (W4); dispatch equivalence is shrink 7 |
| **Stories** | 11 · 12.5 pd |
| **Decisions** | DEC-07 bump token and merge step, DEC-06 tag rule, DEC-05 (`nkd.release-manifest/v1`), DEC-12 freeze inventory |

**Description.** Close the loop with a hand-authored pin-bump workflow that opens a PR with an App/PAT token
(PRs opened with `GITHUB_TOKEN` do not trigger workflows), a `bump --all` that also refreshes prebuilt pins and
re-pins the factory definition, and a `release-tag` node that mints a distinct `nkd.release-manifest/v1`
(explicitly not an ADR-16 ReleaseSet or clearance), pushes it under a deterministic immutable tag, moves the
mutable `main` channel, and signs/attests it. The human merge of the bump PR is the release decision for v1.
Governance closure (ADR-26 final, freeze inventory, docs in sync) lands here.

**Definition of done**
- A scheduled or dispatched pin-bump run opens a PR whose factory run (build + package + local qualify) starts and is green; the PR body lists every pin's old→new tag/commit/tree hash/digest.
- A human merge produces `ghcr.io/<org>/nke/release:<factoryReference>-<planDigest12>-<sha7>` with the release manifest listing every route digest; `cosign verify` and `verify-attestation` (release-manifest type) pass from a laptop; `main` moved; a tag-move attempt is refused.
- A `workflow_dispatch` with `releaseTag` reproduces the same member digests under the requested tag (shrink 7).
- ADR-26 is final; `reconciliation.md`, `CLAUDE.md`, and the `milestones.md` Source Lane column match what shipped; the freeze list is in `CLAUDE.md`.

**Out of scope.** Unattended merge (pre-authorized option needing security approval); upstream signature
verification in the bump loop; scheduled release-audit job; ADR-24 registration adapter.

### Stories

#### M4-S01 — `release-tag` node: `nkd.release-manifest/v1`, deterministic immutable tag, `main` channel, sign + attest
`Story · P0 · 3 pd · W4 · Depends on: M2-S03, DEC-05, DEC-06 · Relates to: M3-S02 · Plan: A-14`
Canonical kind {schemaVersion, tier pipeline-attested, factoryReference, planDigest, catalogDigest, lockDigest, members[]{route, ref@digest, signatureDigest, attestationDigest, qualificationEvidenceDigest}, tag}. Tag = dispatch input, or the pushed `v*` tag, else `<factoryReference>-<planDigest[:12]>-<sha[:7]>`; push the manifest to `<repo>/release`; `delivery.Tag` via `remote.Tag` after `remote.Head` (different digest → `CodeImmutableViolation`; same → no-op); move only the documented mutable `main` tag; sign + attest with type `https://nkd.dev/attestations/release-manifest/v1`. The graph edge release-tag → qualify is wired once M3-S02 lands. Thin `nkdctl release manifest verify` and `publish tag` wrappers.
- Golden manifest bytes; round-trip test tags a digest in an in-process registry, refuses a move, no-ops on same digest.
- Verified downstream: M4-S05 (laptop verify of the tagged ref).

#### M4-S02 — `bump --all` refreshes prebuilt pins in the same run; `factory catalog repin`
`Story · P0 · 1 pd · W4 · Depends on: M2-S02 · Plan: A-15 (chain, repin)`
After `ResolvePin`, re-render prebuilt targets for bumped sources against the in-memory lock and resolve digests before the lock write so the stale-tag check does not block a chained run; `--check` covers both. Add `nkdctl factory catalog repin` (recompute `definitionDigest`).
- Fixture catalog with a templated prebuilt bumps end to end without a stale-lock failure.
- Verified downstream: M4-S05 (the bump PR contains lock + regenerated bundle + re-pinned `definitionDigest` in one commit).

#### M4-S03 — Digest resolvers moved into `pkg/nkd/catalog` on go-containerregistry (no docker CLI)
`Story · P1 · 1 pd · W4 · Depends on: M4-S02 · Plan: A-15 (resolvers) · shrink 5 (part)`
Move `resolveBinaryDigest`/`resolveImageDigests` out of `cmd/nkdctl/commands.go` with httptest tests for the `.sha256` sidecar, hashing fallback, and per-arch manifest selection. The docker-CLI resolvers work on `ubuntu-latest`, so this is hygiene, not a blocker.
- Resolver tests green; `pin-artifacts` no longer shells to docker.

#### M4-S04 — Pin-bump workflow opens a PR with an App/PAT token
`Story · P0 · 2 pd · W4 · Depends on: M1-S10, M1-S14, M4-S02, DEC-07 · Plan: D-5`
`.github/workflows/nkd-pin-bump.yml` (weekly cron + dispatch): SHA-pinned actions; `persist-credentials: false`; unstamped `go build`; `catalog bump --all`; `actions bump` then rebuild if changed; `factory catalog repin`; `factory generate`; `gh pr create` on `nkd/pin-bump-<date>` with the App/PAT token and a body listing each pin's old→new tag/commit/tree hash/digest and source URL; `contents: write` + `pull-requests: write` only on the PR step. Merge = human release decision.
- One scheduled or dispatched run opens a PR whose factory run starts and is green; URL recorded.

#### M4-S05 — M4 cadence evidence: fixture bump → PR → human merge → tagged, signed release
`Story · P0 · 1 pd · W4 · Depends on: M3-S02, M4-S01, M4-S04, M4-S06 · Plan: D-6 (automated path), F-2 (M4 entry) · Never cut (evidence log)`
Create a fixture tag on the NKE mirror (or bump a kubectl patch); trigger the bump workflow; verify the PR run executes build + package + local qualify only; a human merges; verify the push-to-main run executes publish + sign + verify + qualify + release-tag, producing the tagged release manifest and moving `main`; attempt a tag move and confirm refusal. Write `docs/evidence/<date>-m4.md`.
- Run URL producing the tagged, signed release manifest; `cosign verify` + `verify-attestation` (release-manifest type) pass from a laptop; refusal log. **This is the M4 exit evidence.**
- Review R3a findings (M4-S06) on M4-S01 and M4-S04 are closed before this is declared evidence.
- Evidence file (bump PR URL, run URL, manifest ref@digest, laptop transcripts, refusal log) linked from `reconciliation.md`.

#### M4-S06 — Named review R3a: release-tag node, bump chain, bump workflow
`Story · P0 · 1.5 pd · W4 · Depends on: M4-S01, M4-S04 · Plan: E-7c (code half) · Never cut`
Independent review of tag immutability and manifest binding, lock CAS writes, token scope and least privilege on the bump job (no self-approval).
- Review record linked from `reconciliation.md`; findings closed before M4-S05 is declared evidence.

#### M4-S07 — Dispatch equivalence: `workflow_dispatch` with `releaseTag` reproduces the release
`Story · P1 · 0.5 pd · W4 · Depends on: M4-S05 · Plan: D-6 (dispatch) · shrink 7 (part)`
Dispatch on `main` with `releaseTag=<name>`; confirm the same member digests appear under the requested tag.
- Dispatch run URL and `cosign tree` output recorded in the M4 evidence file.

#### M4-S08 — ADR-26 final; `reconciliation.md`, `CLAUDE.md`, and `milestones.md` match what shipped
`Story · P0 · 1 pd · W4 · Depends on: M1-S20, M1-S21, M4-S05 · Plan: F-1c · Never cut`
Record which rows took the Time-Constrained Decision 1 fallback and update the `milestones.md` Source Lane column; list post-v1 rows with reasons and dates; final support-boundary table; ADR-23 GitHub-hosted acceptance; Sigstore sign-off status; the merge-decision rule.
- Every disabled path listed still returns its documented coded error; every enabled path has a run URL; the matrix matches what shipped.

#### M4-S09 — Named review R3b: ADR-26 final text
`Story · P0 · 0.5 pd · W4 · Depends on: M4-S08 · Plan: E-7c (ADR half) · Never cut`
Consistency check of ADR-26 against code and `reconciliation.md`.
- Review record linked from `reconciliation.md`.

#### M4-S10 — Freeze inventory in `CLAUDE.md`; dead code deleted
`Story · P0 · 0.5 pd · W4 · Depends on: M1-S21, DEC-12 · Plan: E-6`
Add a "Frozen for v1" table to `CLAUDE.md` naming packages exactly (airgap, assemble except DiskManifest reuse, aicr beyond render, status, variant, purity, vuln, bake-off/reproduction, the ADR-16 clearance chain, `pipeline.GitHub/GitLab/Tekton/Argo` renderers, APT projection, hidden back-compat verbs, the ai-factory from-source lane) as bug-fixes-only. Delete `sign.Run`/`sign.Available` and the `factory.NodeInventory` constant.
- Build/vet/tests green after deletion; the freeze table matches the plan's frozen list.

#### M4-S11 — Documented Linux dev loop (`make test-linux`)
`Story · P1 · 0.5 pd · W4 · Depends on: M1-S03 · Plan: E-8 · shrink 5 (part)`
`make test-linux` runs the suite in a `golang:1.27` container from a macOS host; `scripts/README.md` and `CLAUDE.md` note that signing and qualification paths are debugged on Linux CI.
- `make test-linux` green on a macOS host with Docker.

---

## Slip order (pre-authorized, taken in this order if M1 is not green by Tue Sep 16)

1. M2-S10 `oci-image` build kind → M2-S07 mirrors stand.
2. M2-S11 CNCF charts → chart tgz blobs in the signed root (M2-S04).
3. M2-S08 FS-wrap of CWE/NKE images → M2-S07 mirrors stand.
4. OS image: if M2-S09 is no-go on Sep 10, M2-S16 (prebuilt QCOW2 mirror) replaces M2-S14/M2-S15 and M3-S08 shrinks to a digest check; if the QCOW2 is also unreachable, row 6 is recorded infeasible by Oct 1.
5. M1-S23 real-cosign CI job, M4-S03 resolver move, M4-S11 dev loop → post-v1.
6. M2-S06 site-controller root → composition-only or post-v1.
7. M3-S04 red GitHub run and M4-S07 dispatch equivalence → Oct 2.
8. M3-S07 trust init → post-v1 (cosign default TUF root; identity pinned in the DeliveryProfile).

**Never cut:** reviews M1-S19, M2-S12, M3-S06, M4-S06, M4-S09 (5 pd) and the ADR-26/evidence work
M1-S09, M1-S21, M4-S08 plus the evidence-log bullets of M1-S20, M2-S13, M3-S05, M4-S05 (4 pd).
