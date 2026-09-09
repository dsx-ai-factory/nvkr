# Building Milestone 1 on the existing code

- **Date:** 2026-09-04 (Friday). Sep 7 is a US holiday. M1 target: Tue Sep 16. Three engineers.
- **Question:** given the POC in this repository, how should M1 (`docs/epics-v1-spec.md`, Epic M1) be built?
- **Method:** a recommendation drawn from the audit (`docs/milestone-audit-and-plan.md`), then stress-tested by three
  advocates arguing for a new repository, a parallel v1 package tree, and a hand-written-workflow-first path, each
  reading the code. Their surviving objections are folded in below as the "hardening moves".

## Decision

Build M1 as a production lane **through** the existing repository: reuse the tested deep modules, add the missing
capabilities as new deep modules, and make the freeze of everything else a build boundary rather than a note. Do
not start a new repository, do not build a parallel v1 tree, and do not ship a hand-written workflow as M1. Use a
hand-written spike workflow on Sep 8 as the oracle and the fallback, not the deliverable.

**Why not a new repository.** The trust primitives M1 must build on (`pkg/nkd/internal/heldfile`,
`internal/ownedcmd`) are `internal/` and cannot be imported across a module boundary; a copy is a fork of the
worst code to fork. The generator, transport, exec-node, OCI layout, and registry push have run 23 jobs green end
to end (GitLab, Aug 26); a fresh thin generator has no run history and costs 5-8 pd before parity, which moves
the first signed run to about Sep 21-23. The two things a new repo would fix for free (module path, repository
name) are fixed below as first commits instead.

**Why not a parallel v1 tree.** It makes three GitHub render stacks where two are already debt, forfeits or
duplicates the laptop-proven `factory run` path, and adds 6-9 pd to the critical path. Every improvement it
promises (typed renderer, enforced freeze, one definition digest, no `examples/` leak) is achievable in place.

**Why not hand-written first.** The exit criterion is an NKD-generated workflow, and five of the spec's six M1
criteria plus the whole definition of done cannot be met by hand-written YAML. The generator's validator
hard-codes a job model that a hand-written workflow would not share, so "teach the generator to emit it later" is
a rewrite. What hand-writing does buy, retiring the external unknowns before any adapter is committed, is captured
by making the Sep 8 spike a real signed run.

## What to reuse, extend, build, freeze

**Reuse as-is.** Canonical codec and digest families; catalog, lockfile, pin and bump; factory compiler and
validator core; exec-node and the local container launch with its network-disabled build and source-closure
verification; transport carriers; OCI layout writer and `delivery.Push`; held-file and owned-subprocess
primitives; action lock and `actions bump`; coded errors; `factory run` for laptop debugging.

**Extend.** `pipeline.ArtifactResult` becomes the run record (add media type and source commit and tree hash from
the source receipt); the GitHub renderer and validator become a typed job and step model; the `DeliveryProfile`
gains a signing policy (identity regexp, issuer, cosign pin); `Plan` gains an enumerated evidence tier and a
definition digest; `factory.Tool` gains a raw single-file download for cosign.

**Build new, as deep modules under `pkg/nkd`, never in `cmd`.** A registry-facing pinned-cosign adapter in
`pkg/nkd/sign` (sign, attest, verify, verify-attestation, receipts bound to subject, identity, issuer, log index;
credentials via a private `DOCKER_CONFIG`, never argv); a production SLSA v1 predicate in `pkg/nkd/provenance`
with its own validator; `sign` and `verify-publication` node kinds in `factory`; a thin helper main that links only
the M1 lane. Roughly 11-13 pd.

**Freeze, and make the freeze a build boundary.** From-source Kubernetes lane and custom toolchains; the second
render stack (`pipeline.GitHub`, `GitLab`, `Tekton`, `Argo`); air gap; assemble beyond the disk manifest; aicr
beyond the render node; status; variant; the ADR-16 clearance chain; APT and helm-oci packaging. Deletion is
M4 work; in M1 the freeze is enforced by the helper's dependency closure, not by prose.

## Hardening moves the stress test forced

These change the plan's M1 tasks. Each is small; together they are what makes "production lane through the POC"
true rather than aspirational.

1. **Name the repository once, at onboarding, and never rename it.** The keyless signing identity is
   `<org>/<repo>/.github/workflows/<file>@refs/heads/main`. A rename after the first signed run invalidates every
   consumer pin and transcript. File the org request today with the final name; the throwaway repo carries only
   throwaway identities. Rename the Go module from `github.com/nhensley/nkdctl` to the org path as the first
   commit on the new remote, before the first generated run.
2. **One definition digest, minted as a canonical kind.** Define it in the decision record as the canonical
   digest over the factory definition, the delivery profile (now carrying signing policy), the lockfile, the
   action lock, and the committed helper digest. Read the action lock from the repository instead of embedding it
   in the binary, so pin tooling rewrites committed bytes. Remove the generator version from the plan digest now,
   in W2, not W3; a stamped build must regenerate identical bytes.
3. **A thin helper.** A `cmd/nkd-exec` main that exposes only exec-node, transport pack and unpack, execute-node,
   the regeneration check, and the new sign and verify-publication verbs, replacing `go build ./cmd/nkdctl` in
   the bootstrap. Move the bundle-member readers and atomic writer out of `cmd/nkdctl/commands.go` into a shared
   package so the helper's input authority is not command-layer code. Pin the helper by a committed digest
   computed at generation from a reproducible build (`CGO_ENABLED=0 -trimpath`, exact Go version) and checked by
   the existing runner-guard script in every job. If the spike shows the build is not byte-reproducible between
   the generating machine and `setup-go` on the runner, record that deviation in the decision record; do not call
   a same-run checksum a pin.
4. **Typed renderer and validator, in place.** Replace the string-concatenating GitHub renderer and the
   `strings.Contains` validator with a typed job and step model rendered to YAML and validated as a model, so
   "prepare-helper precedes any helper invocation", "`id-token: write` only on sign-family jobs", "`environment`
   only on publishing kinds", and the pull-request guards are structural properties. About 2 pd, replacing about
   3 pd of substring patches (the renderer fix, the trigger model, and the validator half of the node-kind work).
   Keep the mutation-table test shape, drive it from the typed model.
5. **Subtract as well as add.** For the `pipeline-attested` tier the validator rejects: free-form
   `runtime.setup` shell, `runtime.services` and `runtime.variables`, `builderImage`, `catalog.base: embedded`,
   plain HTTP off loopback, `apt` and `object` targets, and APT or helm-oci packages. The delivery profile that
   carries signing policy must not also be able to inject shell into the sign job.
6. **Enforce the freeze in the build.** Move `delivery/qualified.go` into its own package so `delivery` stops
   importing `release` and `trust`; split `factory/execute.go` so publish, sign, verify, run-record, and
   tool-acquire executors import neither `assemble` nor `aicr`; add a dependency-closure test (or depguard rule)
   asserting the helper main's closure excludes airgap, release, status, sbom, vuln, variant, assemble, aicr, and
   the frozen emitters. Replace all nine evidence-tier literal sites with one enum (zero value = legacy tier so
   committed bundles stay byte-identical) and add a test that fails if the literal reappears. Credentials enter
   executors as an admitted capability built from the rendered environment, never `os.Getenv` inside `factory`;
   `delivery.Push` fails closed rather than falling back to the ambient keychain when a target declares a
   credential reference.
7. **GitLab fails closed.** The GitLab renderer and validator return the engine-capability error for any plan
   whose tier is not the legacy one, following the Tekton and Argo pattern. No "render sign as skipped". The
   committed GitLab bundle stays at exactly its current bytes.
8. **Merge the two spikes into one hand-written workflow and gate the adapter on it.** On Mon Sep 8, one throwaway
   workflow builds the stand-in (or the NKE CLI only if Sigstore sign-off has arrived) in the pinned golang image
   with the network disabled under rootless podman, pushes an NKD root to ghcr, signs, attests with a hand-authored
   predicate in the production shape, and verifies online and offline from the runner and a laptop; it also
   records cosign's linkage, the JSON output shapes, ghcr's acceptance of the root as a subject, and podman file
   ownership. The sign adapter starts only after that run URL exists. The spike is the semantic oracle for the
   renderer tests (same argv, permissions, environment, guards) and stays alive as the labelled fallback signed run.
9. **Authority order first.** Put the decision record and `docs/milestones.md` above the POC ADR set in
   `CLAUDE.md` for the production lane on Sep 8, so an agent following the repository's own rules does not
   restore the fail-closed gates the plan relaxes.
10. **Production definition lives under a production path.** Not `examples/`. The path is baked into the
    regeneration command and into provenance parameters, so it is permanent once signed.

## Sequence

W1 is over: none of the planned Sep 3-4 preconditions exist yet (no GitHub remote, no production definition,
renderer defect unfixed). The calendar below is honest about that.

| When | Eng-1 (critical path) | Eng-2 | Eng-3 |
|---|---|---|---|
| Fri Sep 4 | File the org request with the final repo name; create the throwaway repo, Actions, ghcr namespace, `release` environment; module-path rename as the first commit | Request NKE CLI read access; draft the spike workflow | Commit `ci.yml`, fix lint for Go 1.27, fix the nil-context test; add a `test` job to `.gitlab-ci.yml`; record the GitLab green run |
| Mon Sep 8 | Run the merged spike; record the probe table; go/no-go for the adapter | Renderer fix and production definition in the morning; typed renderer and validator through the week | Decision-record memo (tier, digest composition, identity rule, credential model); `CLAUDE.md` authority order; static fake-cosign helper so the sign tests pass under `-race` on Linux |
| Tue-Wed Sep 9-10 | Sign adapter (after the spike), production predicate | First generated stand-in run on GitHub, unsigned; debug podman ownership and transport; run record extension | Thin helper main; helper digest pin; generator version out of the plan digest; action lock read from the repo |
| Thu Sep 10 | Checkpoint with the sponsor: spike green? adapter signing on ghcr? NKE clone access? Sigstore sign-off? Decide the M2 shape the same day | | |
| Thu-Fri Sep 11-12 | `sign` and `verify-publication` node kinds; tier enum across all nine sites; subject binding to the run record | NKE mirror with vendored inputs and the fetch credential on fetch steps only; NKE CLI catalog component; swap the stand-in | Freeze boundary: delivery split, executor split, dependency-closure test; validator subtraction rules; GitLab fail-closed |
| Mon Sep 15 | Integrate; dry run on the throwaway repo | Fix what the dry run finds | Review R1 of the adapter, predicate, node kinds, trigger model, fetch credential; findings fixed same day |
| Tue Sep 16 | **Signed generated run**; laptop verification; evidence log entry | | Consumer instructions; decision-record draft |
| Wed-Fri Sep 17-18 | Buffer for a red first run; second run if needed | | |

## What Sep 16 realistically shows

Under the plan's own defaults, the Sep 16 run is very likely a signed generated run of the **stand-in**. The
"builds the NKE CLI" clause is hostage to two external dates: clone access to the NKE CLI repository by Sep 8
(else no mirror) and product-security sign-off on public Sigstore by Sep 11 (else no NKE bytes are signed on a
public transparency log). Say this plainly in the evidence log and to the sponsor; a stand-in run is mechanics
evidence and never closes the NKE criterion. Everything else in M1 (generator, pinning, run record, signing,
attestation, verification, trigger model, Linux gate, review, decision record) does not depend on NKE and
should be green on Sep 16.

## Effort

About 24 pd of M1 work against 3 engineers × 7 working days (Sep 4, 8-12, 15-16) = 21 pd, before the two-day
buffer. It fits only because the typed renderer replaces substring work at lower cost and the run record is an
extension of `ArtifactResult` rather than a new type. Nothing in the hardening moves is optional: items 1-3 are
cheaper now than after the first signed run, items 4-7 are what make the lane production rather than POC, and
items 8-10 are what keep Sep 16 honest.
