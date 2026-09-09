# NKD v1: implementation audit against the milestones, and plan of action

- **Date:** 2026-09-03 (milestone calendar: W1 = Sep 1-7, so this is W1 day 3; W4 ends Thu Oct 1)
- **Scope:** the `nkdctl` repository at commit `c0adc23` plus the uncommitted planning wave, audited against `docs/milestones.md` (M1 vertical slice, M2 breadth, M3 qualification, M4 automated releases).
- **Authority:** informational. Per `CLAUDE.md`, the proposed ADRs and `docs/adr/reconciliation.md` remain the engineering baseline. This document records what was verified, what is missing, the decisions that have to be made this week, and the plan; the decisions become normative only when recorded in the amending ADR-26 this plan calls for.
- **Method:** ten independent subsystem readers with file:line evidence; 159 raw gaps merged to 68; the 38 critical/high gaps each checked by two adversarial verifiers (a code refuter and a requirements refuter); 36 load-bearing capability claims re-verified; a completeness critic; three plan drafts from different angles scored by three judges and synthesized. The headline facts below were additionally verified by hand in this session (build, tests on macOS and in a Linux container, the committed workflow text, external documentation).

---

## 1. Bottom line

**How far along.** Against the milestones as written, the implementation is at the start of M1, not past it.

| Milestone | Exit-criterion clauses met today | Honest state |
|---|---|---|
| M1 vertical slice | 1 of 8 (the generated workflow is committed and pinned, with caveats) | No signing, no provenance, no cosign verify, no OIDC permission, no NKE input, no GitHub remote, and the committed workflow cannot run (renderer defect, see F4). |
| M2 breadth | 0 of 17 artifact-matrix rows delivered | One row (tenant SKU) has a modeled but unsigned path; the AICR bundle is roughly 40% modeled; the AICR CLI and kubectl pull patterns exist. No container-image, Helm-chart, disk-image, or site-controller path. **Scope changed later on 2026-09-03:** the NKE OS image build contains Kubernetes (node side and control-plane images), CNI, and node-agent, so rows 3, 7, and 8 are delivered inside row 6 and the OS image becomes the central M2 deliverable; see §7. |
| M3 qualification | 0 clauses | Strong library contracts exist (lock validation, digest binding, drift gate) but no gate node, no signed evidence object, no artifact-level tamper negative. |
| M4 automated releases | 0 clauses | Detection exists as CLI commands (`catalog bump --check`, `actions bump --check`); no schedule, no post-merge trigger, no release tag, no release-set object the milestone means. |

**Can it become the final solution.** Yes for its core, no as it stands. The deep modules (canonical identity, catalog and lock, factory compiler/validator/renderer, OCI layout writer, held files, owned subprocesses, coded errors) are production-shaped and should be kept. The milestone-critical lane (GitHub generation → OCI push → sign → verify) is the newest and thinnest code in the tree and needs roughly 4-6k LOC of new deep-module work. Four conditions have to hold for the codebase to be the product rather than a POC: an amending ADR that names the v1 evidence tier (today the repository's own doctrine forbids what M1 asks for); an enforced freeze on the ~40% of the code that serves lanes the milestone defers; a Linux CI gate (the documented completion gate has never been green on Linux); and an independent review budget for authority-bearing code written by a single author in 20 days.

**Does the plan fit Oct 1.** Not in full. The synthesized plan totals 77 person-days against 60 available for three engineers over the 20 remaining working days. With the pre-authorized scope reductions below it lands at about 64-67 person-days. M1 (all clauses, hostage to NKE source reachability), M3, M4's automated path, and an M2 core are achievable; the OS image, CNCF Helm charts, from-source nke-agent/CWE/NKE images, and the site-controller SKU are the rows that slip or take their fallback. M1 lands around Tue Sep 16, not W1.

---

## 2. Verified facts

These were checked directly in this session, not inferred from documents.

| # | Fact | Evidence |
|---|---|---|
| F1 | `go build ./...` succeeds; `go test ./... -count=1` passes all 31 packages **on macOS**. | Run in this session. |
| F2 | **In a Linux container** (`golang:1.27`, linux/arm64), `go test -race ./...` fails two packages: `pkg/nkd/sign` (3 tests: `TestProductionVerifierConsumesHeldBytesDuringRenameRestoreAttack`, `TestProductionSignerConsumesHeldBytesAndCapturesAnonymousOutputsDuringRenameRestore`, `TestProductionSignerCancellationFailsClosed`, all "helper never became ready") and `pkg/nkd/pipeline` (1 test: `TestStageTwoObservationRunsAfterTrustedFetchMaterialization`, which calls `Local.Run` without a context and gets `NKD-E-USAGE`). The other 29 packages pass. These are exactly the Linux-only authority tests that macOS skips (10 build-tagged test files, 14 `t.Skip` sites). The sign failures may be partly container-specific and need a run on a real Linux runner, which no CI on either remote performs. | Run in this session. |
| F3 | `golangci-lint` 2.12.2 is built with go1.26 and refuses the `go 1.27.0` module, so `make lint` and `make check` cannot run; the untracked `ci.yml` pins the same version. | Run in this session. |
| F4 | **Renderer defect.** The GitHub factory renderer emits the `./nkd-exec transport-unpack` Restore steps before the `chmod +x ./nkd-exec` that lives inside the Execute step. `actions/upload-artifact` v4 documents that file permissions are not preserved on upload (all files 644), so the downloaded helper is not executable and every input-bearing node job fails at its first Restore step regardless of registry, secrets, or podman. The GitLab renderer emits `chmod` first, which is why only GitLab has ever gone green. `factory generate --check` passing does not detect this: it proves byte-equality with the renderer, not that the renderer is correct. | `.github/workflows/nkd-factory.yml:96-137, 919-953`; `pkg/nkd/factory/github.go:168`; `pkg/nkd/factory/gitlab.go:163`; upload-artifact README "Permission Loss". |
| F5 | GitHub-hosted standard Linux runners (`ubuntu-latest`) expose `/dev/kvm`; a udev rule step is needed to make it accessible to the runner user. The milestone's "hosted runners lack KVM" premise is outdated. The standing constraint that generated workflows target generic hosted runners only (never bespoke self-hosted labels) remains. | GitHub changelog 2024-04-02 "hardware accelerated Android virtualization now available"; community discussions. |
| F6 | The GitLab build-only factory pipeline **did run green end to end** on `dgx-platform-runner-14`: pipeline 64812267, MR !1, sha `f0c6890`, 2026-08-26, all 23 jobs including from-source kubelet (140 s), kubeadm (119 s), kube-proxy (129 s), containerd, runc, crictl, cni-plugins; ~685 MB of job artifacts. This is the strongest execution evidence in the project and it is recorded nowhere in the repository: `reconciliation.md` and ADR-23 still say no end-to-end run is claimed, and the bundles committed on `main` were regenerated after that run and have never executed. | GitLab API (read-only); `git log` d30eff5..f0c6890. |
| F7 | Nothing has ever run on GitHub Actions. The only remote is `gitlab-master.nvidia.com`. `.github/workflows/ci.yml` is untracked. The committed GitHub bundle references a placeholder registry (`ghcr.io/example-ai-factory`, HTTP 404) for the custom toolchains and the publish target. | `git remote -v`; `examples/ai-factory/delivery-github.yaml`. |
| F8 | The committed generated GitHub workflow has no signing, provenance, attestation, cosign, or `id-token` permission anywhere. Its publish job pushes an unsigned `diagnostic` NKD publication-root index with a PAT-style secret and runs on `pull_request`. | `.github/workflows/nkd-factory.yml` (grep: 0 hits for cosign, slsa, id-token, attest). |
| F9 | The repository contains no NKE artifact input of any kind: no NKE CLI, nke-agent, CWE controller, NKE container image, or NKE OS image recipe, source, overlay, or factory target. The docs cite NKE repositories only on `gitlab-master.nvidia.com`, which hosted GitHub runners cannot reach. | grep across pkg/cmd/recipes/examples; `docs/nkd-srd-doc.md`. |
| F10 | All 25 ADRs are `Proposed` with `Deciders: [INPUT NEEDED]`. `docs/milestones.md` is untracked and absent from the `CLAUDE.md` authority order. One author, 36 commits, 2026-08-08 to 2026-08-27, ~55k non-test and ~29k test Go LOC. | `git shortlog -sn --all`; ADR headers. |
| F11 | Roughly 40% of the non-test code primarily serves lanes the milestone defers or never asks for: from-source upstream Kubernetes/etcd/runc/containerd builds, GitLab/Tekton/Argo emitters, builder profiles/variant/bake-off/stage-two identity, air gap, disk assembly, APT projection, and the ADR-16 release-clearance chain. The last eight commits (Aug 26-27) went to the GitLab build-only pipeline, which is Deferred item 1. | Per-package LOC inventory; `git log`. |

---

## 3. Where the implementation stands

### 3.1 Exit criteria, clause by clause

**M1 — Vertical slice**

| Clause | Status today |
|---|---|
| One green NKD-generated GitHub Actions run | Never run on GitHub (F7). The committed workflow fails at its first Restore step (F4). Linux gate never green (F2/F3). Placeholder registry (F7). |
| builds the NKE CLI | No NKE input exists (F9). NKE source is on internal GitLab; there is no fetch-credential path for a private mirror. |
| via a pinned wrapped NKE reusable build | Ambiguous. No reusable-workflow node kind exists (the validator rejects any `uses:` outside four pinned actions; `--emit-callable` makes NKD the callee, not the caller, and is unreachable because `bundle --engine github` fails closed). The catalog source model (repo, commit, treeHash, toolchain digest, build command) can wrap a build script with zero generator change. |
| emits SLSA provenance | `pkg/nkd/provenance` builds a deterministic SLSA v1 statement but it is reproduction-shaped (builder.id = builder-profile digest, no times, no run identity), never imported by the factory, never written to disk, never DSSE-wrapped, never attached to an OCI subject. Its validator rejects a production-shaped predicate. |
| signs (keyless) | `pkg/nkd/sign` shells only `cosign sign-blob`/`verify-blob` over local canonical documents; never `sign`/`attest` against a registry reference. No cosign pin or acquisition exists in any lock, tool list, or workflow. No `id-token: write` is emitted anywhere. Keyless has never been exercised against a real Fulcio/Rekor (all tests substitute the test binary for cosign). |
| pushes artifact + provenance + signature to OCI | Only the raw push exists (`delivery.Push` via go-containerregistry `remote.WriteIndex`). No OCI 1.1 `subject`/referrers support anywhere; the repo's "referrer" is an internal concept (evidence embedded as sibling children inside the root index). The only `OCIRegistry.AttachReferrer` implementation writes to the local filesystem. |
| cosign verify passes | No verify step exists in any generated workflow. The only rendered verifier is `nkdctl verify` of a pre-signed validation vector on the disabled single-recipe GitHub emitter, and it refuses from-source recipes. |
| the generated workflow is itself committed and pinned | True in the repo's own sense: 40-hex action SHAs from `actions.lock.yaml`, `planDigest` header, manifest binding every member, byte-exact `--check`. Caveats: `generatorVersion` is literally `dev (unknown)` and sits inside `planDigest`; `.go-version` floats at `1.27`; the downloaded helper is not digest-checked; `ci.yml` does not run `--check` (only the generated workflow's own bootstrap job does); the committed bundle was generated with `--definition`, bypassing the FactoryCatalog reference. |

**M2 — Breadth**

| Clause | Status today |
|---|---|
| full in-scope set from source (nke-agent, CWE controller, NKE images, OS image) | No NKE components. No package kind produces, encloses, mirrors, or pushes a real OCI container image (`delivery.Push` rejects any index that is not an NKD publication root; the only image constructor is the legacy unqualified `assemble.OCIImage`). No `os` recipe, disk producer, or disk package kind; every rootfs/disk/boot producer returns `CodeEngineCapability`. |
| pull+bundle upstream | The prebuilt lane exists for two shapes only (a single https binary, a digest-pinned OCI image) and is rendered for exactly one component (kubectl via a supplier overlay). No archive extraction for catalog components, no Helm chart output type, no entries for control-plane images, CNI, Helm, spire, third-party images, or charts. `pin-artifacts` is untested and not chained into `bump`. |
| each delivered and signed in OCI with provenance | One unsigned route. Per-route publish exists in the compiler but only for publication roots. |
| 1 OS image | Absent (see above). KVM is available on hosted runners (F5), but the NKE OS build is an internal repo with privilege/loop-device needs; the milestone's own fallback (pull+bundle the prebuilt signed QCOW2) has no disk package kind and unknown size. |
| 1 tenant SKU | `ClusterComposition` + `oci-root` exist for `examples/ai-factory` (unsigned, `diagnostic`, never pushed to a real registry). |
| 1 site-controller SKU | Not modeled. `clusterType` is a closed `{tenant, operator}` domain; a site controller would be `type: operator` + `platform-services`, for which no definition, recipe, or NKE CWE/SWE components exist. |
| 1 AICR bundle | `helm-oci` is a tar of the AICR-rendered tree under a custom NKD media type, not a CNCF Helm chart; none of the 14 charts AICR references is fetched. Unsigned. |

**M3 — Qualification**

| Clause | Status today |
|---|---|
| automated recipe/evidence qualification gate | The only post-build node is `terminal-observe`, which re-hashes local inputs into a `static-unqualified` JSON and is rendered `if: always()`, so it cannot fail anything. `nkdctl verify` cannot serve as the gate (fails closed on from-source recipes, Linux-only, vector-based, never rendered). |
| schema + pin-consistency | Exist as library/CLI contracts (`Plan.Validate`, `canonical.UnmarshalExact`, lock completeness on catalog load, `factory generate --check`) but leave no record and never compare published artifact digests against the plan/lock. |
| signature/provenance presence | Nothing lists referrers or verifies cosign over an OCI reference. |
| cheap smoke check of the tooling bundle | Only `scripts/demo.sh` runs `crictl --version`; no gate. |
| emits signed qualification evidence | `attest.EvidenceBundle` is explicitly UNQUALIFIED and unsigned; ADR-25's `KindQualificationResult` is unimplemented and cluster-scoped (exactly the scope M3 excludes); `release.Authorization` has no CLI and needs the full ADR-16 chain. |
| tampered artifact or missing provenance fails closed | `demo-tamper-detection.sh` mutates hand-written JSON, a sed-edited IR, and tar carriers, never an OCI blob or a signature. Missing-provenance fail-closed is proven only for a locally supplied signed vector. |

**M4 — Automated releases**

| Clause | Status today |
|---|---|
| detects an upstream pin update | `catalog bump [--all] [--check]` resolves newest constraint-matching tags via `git ls-remote` and CAS-writes the lock (CLI only). `actions bump --check` does the same for action SHAs. No schedule; `catalog update-ci` (the designed emitter) is a `CodeEngineCapability` stub. `bump` does not refresh prebuilt artifact pins or toolchain digests, so a templated prebuilt component goes stale and load fails closed. |
| regenerates | `factory generate --check` is the strongest M4 asset (byte-exact, non-mutating, rendered into the bootstrap job). Regeneration has only ever been done by hand; `GeneratorVersion` inside `planDigest` couples it to build stamping; supplier-overlay `requiresCatalogDigest` and FactoryCatalog `definitionDigest` must be hand-re-pinned after every bump. |
| re-qualifies | No gate exists. |
| tags a signed OCI release set | `Push` writes one fixed DeliveryProfile tag with no overwrite check; no digest-to-tag promotion, no release naming, no signature. The repo's ReleaseSet/Authorization/clearance is a much heavier object (per-member `reproduced` receipts, exhaustive conformance from a distinct signer, trusted-time checkpoint, eight signer roles) with no CLI to mint it. |
| manual dispatch produces the same tagged, signed release | `workflow_dispatch` has no inputs and runs the same unsigned graph as a PR. |

### 3.2 Artifact matrix, all 17 rows

| # | Row | Lane | Target | Status today |
|---|---|---|---|---|
| 1 | GitHub Actions pipeline generator | NKD | M1 W1 | Present (`factory generate`, SHA-pinned actions, `--check`) but renders an unrunnable step order (F4), has no sign/verify/qualify/release kinds, a string-equality tier gate, a PR-runs-everything trigger, no timeouts. |
| 2 | NKE CLI | FS-wrap | M1 W1 | Absent (F9). W1 target is infeasible; realistic landing Sep 16. |
| 3 | nke-agent | FS-wrap | M2 W2 | Absent. **§7: delivered inside the OS image (row 6); no separate artifact.** |
| 4 | CWE controller | FS-wrap, OCI image | M2 W2 | Absent; no image-producing kind. §7: assumed a cluster workload image, not baked into the OS image; confirm with NKE. |
| 5 | NKE container images | FS-wrap | M2 W2 | Absent. §7: as row 4. |
| 6 | NKE OS image (+ node-agent) | FS-wrap | M2 W3 | Absent. **§7: now the central M2 deliverable, wrapped from the NKE repo at a pinned commit; the prebuilt QCOW2 mirror is the fallback.** |
| 7 | Kubernetes control plane | Pull+bundle | M2 W1-2 | kubectl pattern exists; kubeadm/kubelet prebuilt entries and control-plane images absent; no image push. **§7: delivered inside the OS image; no mirrors needed. kubectl remains in the operator tooling bundle.** |
| 8 | CNI | Pull+bundle | M2 W1-2 | Ambiguous; `cni-plugins` is modeled as a from-source single bridge plugin, excluded from every bundle. **§7: delivered inside the OS image; no separate artifact.** |
| 9 | Helm | Pull+bundle | M2 W1-2 | From-source in the default catalog; no prebuilt entry; no archive extraction. |
| 10 | AICR CLI | Pull+bundle | M2 W2 | Path exists (a `tools:` entry may enter a `binary-bundle` via `tool/<name>` selectors) but no package uses it. |
| 11 | Spire-agent | Pull+bundle | M2 W2 | Absent; the SRD marks spire WONT-v1, conflicting with the matrix. |
| 12 | Third-party container images | Pull | M2 W2 | Only demo prebuilt images; pulled layouts cannot be pushed; the list is unknown. |
| 13 | AICR helm chart bundle | Bundle | M2 W2 | `helm-oci` = AICR tree tar only; no chart bytes; unsigned. |
| 14 | NKE helm chart bundle(s) | Bundle | M2 W2 | Absent; charts internal. |
| 15 | Operator tooling bundle | Bundle (mixed) | M2 W2 | `binary-bundle` already admits raw + tool inputs; no such package authored. |
| 16 | Tenant cluster SKU | Composition | M2→M3 W3 | Composition + `oci-root` exist unsigned for `examples/ai-factory`. |
| 17 | Site-controller cluster SKU | Composition | M2→M3 W3 | Not modeled. |

### 3.3 What exists and is reusable

Verified to the stated evidence level (contract = package + unit tests; cli = wired to a command; generated-ci = rendered into the committed workflow; end-to-end = observed against a real external system).

- **Generation and transport (generated-ci):** FactoryDefinition → FactoryPlan → artifact graph compiler; GitHub and GitLab renderers over one exec primitive (`ExecNodeCommand`); exact per-edge tar carriers; SHA-pinned actions as data (`actions.lock.yaml` + `actions bump`); fail-closed static validator; byte-exact `--check`; `planDigest` header and manifest; Model-B `exec-node --launch --runtime podman` isolation with `--network=none` on generic runners (the GitLab twin proved this end to end, F6).
- **Catalog and identity (cli/contract):** layered catalog with lock completeness enforced on load; `catalog pin`/`bump`; supplier overlays as whole-producer replacement with digest chaining; prebuilt component schema (`url` XOR `image`, mandatory evidence declaration); canonical codec with frozen digest families; coded error/exit contract.
- **OCI delivery (cli, loopback only):** `binary-bundle` and `oci-root` are real OCI Image Layouts with recursive closure verification; `delivery.Push`/`Pull` via go-containerregistry returning `repo@digest`; digest-only consumer extraction. Demonstrated only against a loopback `registry:3`.
- **Trust plumbing (contract):** pinned, static-ELF, descriptor-held cosign execution with environment scrub (`ACTIONS_ID_TOKEN_REQUEST_*` survives, so ambient GitHub OIDC is compatible); `sign-blob`/`verify-blob` argv contracts including Sigstore v0.3 bundles and trusted roots; role-separated document authentication; SLSA v1 statement/predicate structs; `provenance.BuildTypePrebuilt` for pulled bytes.
- **Cluster model (generated-ci/contract):** `ClusterComposition` per ClusterSpec with recipe-identity re-verification; `type: operator` accepted by the model; the AICR render node runs the pinned `aicr` binary offline.
- **Local execution (end-to-end on a laptop):** `factory run` executed the full arm64 gpu-worker graph natively on 2026-08-26 (24 nodes, seven builds in ~1m46s, published to `localhost:5000`). The laptop path is now inconsistent with the catalog after the toolchain registry re-pin.

Seven of the 36 load-bearing capability claims were downgraded on verification, all in the same direction: `cli` claims that are really `contract` (the qualified verifier, the cosign pin, terminal result re-hash), and `present` claims that are really `partial` (from-source builds rendered but `static-unqualified`; the publish job real but unsigned and never run; a referrer "adapter" that is filesystem-only and disabled).

### 3.4 What is missing (verified critical and high gaps)

All 38 critical/high gaps survived both refutation lenses. The requirement lens lowered severity on six of them (placeholder registry, static-unqualified doctrine, custom-toolchain publication, governance, lint, command-layer debt) on the grounds that they are positioning or hygiene rather than exit-criterion blockers **once M1 is re-scoped to a new minimal NKE-CLI definition** (decision D1 below). The disagreement between lenses reduces to that one decision.

| Milestone | Gap key | Corrected statement (short) |
|---|---|---|
| M1 | `m1-no-keyless-signing-step-in-generated-workflow` | Nothing signs the pushed OCI digest; no sign node kind; `sign` package only does blob signatures on local disk. |
| M1 | `m1-no-id-token-write-permission-emission` | Root `permissions: contents: read`; only `packages: write` on ghcr publish; validator rejects any other shape; OIDC ambient token never available. |
| M1 | `m1-no-provenance-emission` | No SLSA provenance produced, wrapped, signed, persisted, or pushed; `provenance` not imported by `factory` or `pipeline`. |
| M1 | `m1-no-cosign-verify-step` | No job runs `cosign verify`/`verify-attestation` against the pushed reference. |
| M1 | `m1-no-oci-referrer-attach-mechanism` | No manifest carries `subject`; nothing calls the referrers API; the only `AttachReferrer` writes files locally. |
| M1 | `m1-placeholder-toolchain-registry` | `ghcr.io/example-ai-factory` does not exist; four build jobs and publish cannot pull/push. Not on the M1 path under D1. |
| M1 | `m1-no-end-to-end-github-run-evidence` | No generated GitHub workflow has ever executed; all evidence is rendering, unit tests, static validation. |
| M1 | `m1-nke-cli-fs-wrap-lane-absent` | No recipe, source, overlay, or target defines the NKE CLI; no reusable-workflow invocation; NKE repos are on internal GitLab. |
| M1 | `m1-static-unqualified-doctrine-blocks-milestone-evidence` | Every generated result is `unqualified` by design; `Plan.Validate` hard-requires `static-unqualified`; the authoritative emitter rejects from-source graphs until a provider attestation adapter exists. Positioning conflict with M1's accepted evidence class; resolved by ADR-26, not code alone. |
| M1 | `m1-no-pinned-cosign-acquisition-in-pipeline` | No cosign digest, lock entry, or download step anywhere. |
| M1 | `m1-signed-publication-model-diagnostic-root-vs-clearance` | Publish pushes an always-`diagnostic` root unguarded; the repo's only "signed publication" is clearance-in-root, which `cosign verify` cannot consume. |
| M1 | `m1-rootless-podman-on-hosted-runner-unverified` | The podman lane the emitters default to has never executed anywhere (GitLab used docker:dind). Podman is present on ubuntu-latest; rootless ownership of bind-mounted outputs is unverified. |
| M2 | `m2-no-oci-container-image-package-or-mirror-kind` | No package kind produces, encloses, mirrors, or pushes a real container image. |
| M2 | `m2-no-helm-chart-artifact-type-or-chart-bundle` | No Helm chart output type or package kind; `helm-oci` contains no chart bytes. |
| M2 | `m2-no-os-image-lane` | No `os` recipe, disk component, node, or package kind; producers return `CodeEngineCapability`. |
| M2 | `m2-in-scope-artifacts-absent-from-catalog` | No catalog entries for nke-agent, CWE, NKE images, OS image, AICR CLI as artifact, spire, third-party images, charts, tooling bundle. |
| M2 | `m2-no-archive-extraction-for-prebuilt-components` | `pullBinary` handles one regular file; upstream tarballs can only ride as opaque `raw` blobs (acceptable for OCI+sign rows; not for APT). |
| M2 | `m2-aicr-cli-not-a-deliverable` | Partly refuted: `tool/<name>` selectors already work as `binary-bundle` inputs; no example uses it. |
| M2 | `m2-site-controller-sku-and-bundle-kinds-not-modeled` | No definition composes an operator-type cluster; no mixed bundle kind beyond `binary-bundle`. |
| M2 | `m2-toolchain-publication-not-a-pipeline-gate` | Custom toolchains were pushed by throwaway-branch jobs, not `toolchain build`; `toolchain verify` is wired to nothing. Deferred-lane hygiene under D1. |
| M3 | `m3-no-qualification-gate-node-in-generated-pipeline` | `terminal-observe` is `if: always()` and compares against nothing; no node verifies signatures, pins, or runs a smoke check. |
| M3 | `m3-no-signature-provenance-presence-check-or-tamper-demo` | Nothing lists referrers or verifies cosign over OCI refs; the tamper demo never touches an OCI blob or a signature. |
| M3 | `m3-no-signed-qualification-evidence-object` | No canonical kind, signer role, or command mints a signed recipe-level pass/fail record. |
| M3 | `m3-adr25-scope-and-vocabulary-mismatch` | ADR-25 is cluster-scoped and explicitly not a release gate; implementing it would not satisfy M3. |
| M4 | `m4-no-post-merge-trigger-or-release-dispatch-inputs` | Triggers are `pull_request` and input-less `workflow_dispatch`; publish is not gated on event type. |
| M4 | `m4-no-scheduled-pin-bump-regenerate-workflow` | No scheduled workflow; `catalog update-ci` is a stub; regeneration only by hand. |
| M4 | `m4-no-signed-oci-release-set-tagging` | No digest-to-tag promotion, release naming, or signed release object the milestone means. |
| M4 | `m4-bump-does-not-refresh-prebuilt-or-toolchain-pins` | `bump` updates source pins only; templated prebuilt artifacts go stale and load fails closed. |
| M4 | `m4-overlay-and-definition-pins-not-automatable` | Supplier overlays and FactoryCatalog digests must be hand-re-pinned after every bump. |
| Cross | `x-provenance-predicate-reproduction-vs-production-mismatch` | The only predicate is reproduction-oriented and its validator rejects production shape. |
| Cross | `x-no-upstream-signature-or-provenance-verification` | The pull lane is reference-by-digest with trust on first use; no upstream cosign/Helm/SLSA verification. Not required by any milestone clause. |
| Cross | `x-factory-check-not-enforced-in-ci-or-branch-protection` | `ci.yml` never runs `factory generate --check`; the property depends on a required status check on a remote that does not exist. |
| Cross | `x-milestone-release-set-vs-repo-clearance-misalignment` | The milestone's "signed OCI release set" and the repo's ADR-16 clearance are different objects; the lighter one has no implementation. |
| Cross | `x-adr-clauses-conflict-with-time-constrained-decisions` | ADR-07 §1, 08 §1, 09 §1, 16 §2, 18 §1.6/§7, 20 §3-4, 21 §3 as written conflict with the deferred list and accepted provenance class. |
| Cross | `x-keyless-path-unverified-against-real-cosign` | No test, script, or job invokes a real cosign binary; argv contracts are unverified against any version. |
| Cross | `x-governance-deciders-unresolved` | All ADRs `Proposed`, no deciders, milestones untracked. |
| Cross | `x-lint-gate-broken-for-go-1-27` | See F3. |
| Cross | `x-command-layer-authority-debt` | ~1.6-1.9k LOC of durable-state and orchestration authority lives in `cmd/nkdctl`; six hand-rolled no-follow read sequences because `heldfile` is import-restricted. |

Medium and low gaps (30) are listed in Appendix A.

---

## 4. Can this codebase become the final solution?

**Keep.** `pkg/nkd/errors` (frozen exit contract), `canonical`, `internal/heldfile`, `internal/ownedcmd`, the identity core (`catalog`, `recipe`, `ir`, `compose`, `criteria`), `pipeline/actionlock` + `actions.lock.yaml`, `toolchains.lock.yaml` + `nkdctl toolchain`, `delivery` layout/inspect, `factory` compile/model/validate/bundle_io, and the existing per-route publish loop. These are deep modules with receipts, coded errors, cancellation, and adversarial tests; every disabled path probed returned the documented coded error, and the reconciliation record is unusually honest against code.

**Refactor or extend (this is the milestone work).** `pkg/nkd/factory` gains sign, verify-publication, qualify, mirror, release-tag node kinds and an enumerated evidence tier (+2-3k LOC with tests). `pkg/nkd/sign` gains a registry-facing adapter (+0.5k). `pkg/nkd/provenance` gains a production predicate (+0.3k). `pkg/nkd/delivery` gains image-layout push, blob artifacts, tag promotion with no-overwrite (+0.5-0.8k). New `pkg/nkd/qualify` (+1k) and a release-manifest kind in `pkg/nkd/release` (+0.3k). `cmd/nkdctl` authority migration (~1.9k LOC) is real debt but not on the milestone path; move only what the milestone touches (the digest resolvers).

**Freeze (bug fixes only, say so in `CLAUDE.md`).** From-source upstream Kubernetes/etcd/runc/containerd/cni-plugins/crictl/helm/coredns lane (`examples/ai-factory` stays GitLab build-only), custom-toolchain publication, `pipeline.GitHub/GitLab/Tekton/Argo` emitters and `bundle --engine`, air gap, assemble (reuse `DiskManifest` only), APT projection, aicr beyond the render node, status, variant, purity, vuln, bake-off/reproduction/builder-profile paths, the ADR-16 `Authorize`/clearance/`PublishCleared` chain, ADR-24 registration, ADR-25 cluster qualification.

**Delete (dead, no callers).** `sign.Run`/`sign.Available`, the `factory.NodeInventory` constant, tracked docx/html exports, the untracked 13 MB root binary and Office lock file, the stray `.github/nkd-factory/manifest.json.lease`.

**Structural facts a decision-maker needs.**

1. The repository's doctrine forbids the exact thing M1 asks for. ADR-18 §1.6 and `validateEmitterContract` reject from-source emission until a provider execution-attestation adapter exists; the committed GitHub workflow comes from a second, deliberately non-authoritative emitter that labels everything `static-unqualified`. M1 accepts "SLSA-style production provenance (how bytes were produced, not reproduction)", which is precisely the evidence class the repo refuses to call qualified. This is a decision, not a bug; it needs an amending ADR.
2. Two GitHub render stacks coexist (`pipeline.GitHub`, unreachable from the CLI; `factory.RenderGitHub`, live). They share the exec primitive and the action lock but duplicate YAML writers. Only the factory stack matters for the milestones.
3. The milestone's publication model (cosign signature and provenance as OCI referrers on each artifact, consumable by plain `cosign verify`) is inverted relative to ADR-20 §4 (clearance and evidence embedded inside the root index the operator pins). Both can coexist if the v1 tier is named and the clearance model is explicitly post-v1.
4. Investment is skewed toward deferred lanes (F11), but the deferred from-source machinery (fetch → hermetic build → observe → package → publish, exec-node isolation, transport carriers) is exactly what FS-wrap of NKE components reuses. Nothing needs to be thrown away; it needs to be frozen and pointed at the right inputs.
5. Single author, 84k LOC in 20 days, no independent review of authority-bearing code, and a completion gate that has never been green on Linux (F2/F3). The plan carries a named, never-cut review budget and a Linux CI gate for this reason.
6. Consumers and the deliverable format are unconfirmed. The repo's delivery unit is an NKD publication root (custom media types, nested indexes) that only `nkdctl publish pull/extract` understands. The matrix says "OCI image" and "helm chart bundle", implying standard consumables. This is the difference between extending the packaging model and replacing it, and it is a W1 question for the NKE/AICR consumers.

---

## 5. Decisions required this week

These are the adjudications the plan is built on. Each has an owner role and a default that applies if nobody decides by the stated date. The plan's tasks that a decision blocks are listed by id (see §6).

| ID | Decision | Owner | Default if undecided | Blocks |
|---|---|---|---|---|
| DEC-01 | GitHub home, target registry, package visibility: github.com/NVIDIA (OSRB lead time) vs a team-controlled org; `ghcr.io/<org>` as the v1 target; public packages (laptop `cosign verify` with no login) vs private. Identity/issuer/registry are DeliveryProfile data, so migration later is regenerate + re-run. | Eng manager + org admin | File the NVIDIA-org request today **and** create a throwaway private repo under a team org today; ghcr public packages; all W1-W2 identities are throwaway. | A-0a, A-0b, A-6, A-8, B-1, D-5, D-6 |
| DEC-02 | NKE source reachability and fetch credential: may NKE CLI (later nke-agent/CWE) be mirrored to a private GitHub repo with private Go modules vendored; which credential clones it (deploy key, App token, or fine-grained PAT; `GITHUB_TOKEN` cannot read a sibling private repo); who rotates it; what fork PRs do. | NKE eng lead + product security | Mirror at the last green tag with `vendor/` committed; deploy key as a repo secret rendered only on fetch jobs of private sources, never on `--network none` build jobs; fork PRs disabled. No answer by Sep 8: request a source tarball; none by Sep 10: M1 evidence stays on the stand-in and the NKE clause is recorded external-blocked. | B-0, B-1, B-2, A-8 |
| DEC-03 | Reading of "pinned wrapped NKE reusable build": wrap NKE's build as a pinned catalog component (repo@sha, treeHash, toolchainImage@sha256, vendorCommand, buildCommand, artifactPath) executed by the existing chain; a job-level reusable-workflow node kind is out of scope unless NKE insists. A stand-in run never counts as "builds the NKE CLI". | NKD lead + NKE eng lead | Catalog-component reading; zero generator change. | B-2, A-6, A-8 |
| DEC-04 | Sigstore instance and cosign pin: public Fulcio/Rekor with GitHub OIDC vs private Sigstore/KMS; explicit product-security sign-off before the first signed run containing NKE bytes (public Rekor permanently records org/repo/workflow identity and NKE digests). Pin one cosign 2.x release by sha256; storage format = cosign 2.x default tag-based `.sig`/`.att` with embedded Rekor bundle (consumer minimum cosign 2.2), not the new bundle format. Identity policy admits only `@refs/heads/main` and `@refs/tags/v*`, never `refs/pull/*`. | Product security + NKD lead | Public Sigstore for throwaway artifacts only; no sign-off by Sep 11 means the M1 run signs against the throwaway repo and the decision is recorded as pending, never as approval. | A-0b, A-2, A-3, A-5, A-8, D-1 |
| DEC-05 | Ratify ADR-26 with named deciders: the `pipeline-attested` tier (keyless cosign sign/attest + SLSA v1 production predicate; SLSA Build L2, self-attested by the producing workflow; explicitly not reproduction-qualified; the ADR-18 authoritative emitter stays disabled), canonical kinds `nkd.qualification-evidence/v1` (+ signer role) and `nkd.release-manifest/v1` (not an ADR-16 ReleaseSet or clearance), signed SKU bundle = signed publication root carrying `ClusterComposition`, waivers for `evidence: none` prebuilt components recorded inside the evidence, explicit exclusions (SBOM, vuln scan, upstream signature verification), a revocation paragraph, the freeze list, and a milestone→repo glossary. | NKD architect (named decider) + security reviewer | Implement exactly this under ADR-26 `Proposed` with author and reviewer named; record "pending ratification"; add `milestones.md` and ADR-26 to the `CLAUDE.md` authority order. | F-1b, A-5, D-1, A-14 |
| DEC-06 | Trigger, credential, environment, tag rule: `pull_request` = build + package + local qualify only, no secrets; push to `main`, `workflow_dispatch` (ref main), and `v*` tag push = publish + sign + attest + verify + qualify + release-tag; `if: github.event_name != 'pull_request'` on every publishing/signing kind; `id-token: write` only on sign-family jobs; `GITHUB_TOKEN` with `packages: write` (no PAT); one `release` environment for secret scoping **without** required reviewers on the automated path; branch protection on `main` (required checks, force-push denied) and CODEOWNERS on `.github/workflows/**`, `pkg/nkd/factory/**`, `pkg/nkd/sign/**`, `pkg/nkd/qualify/**`, `examples/nke-v1/**`; automated release tag `<factoryReference>-<planDigest[:12]>-<sha[:7]>` plus a mutable `main` channel tag; tags never move. | Release eng lead + repo admin + security | As stated. | A-1, A-5, A-14, D-5, D-6 |
| DEC-07 | M4 tokens and the merge step: PRs opened with `GITHUB_TOKEN` do **not** trigger `pull_request` workflows, so the bump job needs a GitHub App installation token or a bot PAT; and whether the bump PR merges unattended or a human merge is the release decision. | Release eng lead + security | Bot PAT (App for the NVIDIA org) with a named owner and 90-day rotation; **human merge is the release decision for v1**; auto-merge is a pre-authorized option needing security approval. | D-5, D-6 |
| DEC-08 | M2 consumer format: images as standard OCI images mirrored by digest are not in question; Helm charts default to raw chart `.tgz` blobs enclosed in the signed publication root unless NKE/AICR consumers require CNCF OCI charts (`helm pull oci://`), which costs 3 pd on the W4 critical path. | NKE and AICR consumer leads | Raw chart tgz in the signed root; CNCF charts only on an explicit requirement by Sep 12. | A-12, C-2 |
| DEC-09 | **Superseded by §7.2.** OS image row: wrap the NKE node-images build at a pinned commit as the primary path; the prebuilt signed QCOW2 mirror is the fallback if the build cannot run on a hosted runner by the Sep 10 checkpoint. | NKE OS/build team | See §7.2. | B-5, B-6, B-7, A-10, B-4 |
| DEC-10 | **Superseded by §7.2.** Rows 3, 7, 8 are inside the OS image. Remaining pull+bundle meanings: operator tooling = kubectl + helm + AICR CLI; spire-agent in the image or out of scope; third-party images = those referenced by the AICR and NKE charts, list by Sep 12; NKE chart sources by Sep 12. | NKE eng lead | See §7.2. | C-1, C-2, A-9 |
| DEC-11 | Catalog modelling: `examples/nke-v1` is a standalone `SourceBuildRegistry` (no supplier overlays, so no overlay re-pin), NKE CLI uses the existing `operations` category (vocabulary stretch recorded in the glossary), v1 is amd64-only, and the committed ai-factory GitHub bundle is removed from `main` so `.github/workflows/nkd-factory.yml` is owned by nke-v1 (ai-factory stays GitLab-only). Alternative if both must coexist: a small `--workflow-name` on `factory generate`. | NKD architect / repo owner | As stated. | A-6, B-2, C-1, A-15 |
| DEC-12 | Hygiene: golangci-lint fix (bump to a go1.27-built release vs `go.mod` 1.26); `.go-version` = `1.27.0`; do not vendor the nkdctl module tree for the bootstrap in v1 (record the availability risk; record the helper digest in provenance); the KEEP/REFACTOR/FREEZE/DELETE inventory for `CLAUDE.md`. | NKD tech lead | As stated. | E-2a, E-4, E-6 |
| DEC-13 | Staffing and quota: three fully allocated engineers with named lanes (Eng-1 lane A critical path; Eng-2 lanes B + C; Eng-3 lanes D, E, F and reviewer for lane A); Actions minutes, concurrency, and artifact-storage quota; `retention-days: 7`; PR build-only mode. | Eng manager + org admin | Three engineers as above; with two, M2 collapses to the public single-file rows plus the tenant SKU root and M4 to the dispatch path. | all |
| DEC-14 | Operational ownership of external trust dependencies: Rekor/Fulcio availability (bounded retry, fail closed, `--offline` verify with embedded bundles), ghcr package deletion protection and a read-only consumer identity, a post-v1 scheduled release-audit job. | Release eng lead | Release engineering owns; adapter retries 3x within the job timeout; deletion restricted to admins; audit job post-v1. | A-3, A-16 |

Two open questions from the audit that no decision above fully closes: who consumes the M2 deliverables and with which tool (DEC-08 covers charts and images but not whether the NKD publication root format is acceptable at all), and what the NKE CLI actually requires to build (language, build system, private modules, cgo, green tag). Both are answered by W1 spikes (B-0, A-0b) rather than by assumption.

---

## 6. Plan of action

### 6.1 Principles

- **Observe before constructing.** W1 spikes with go/no-go criteria retire the unknowns (registry behaviour, cosign, OIDC, rootless podman, KVM, NKE build shape) so later tasks build on observed behaviour.
- **Decouple mechanics from NKE access.** A public `crictl` stand-in in a new minimal `examples/nke-v1` definition proves every pipeline mechanic on GitHub in W2; only the catalog swap to the NKE CLI remains once access lands. A stand-in run is never logged as M1 completion.
- **Minimum new surface on the existing routes.** Sign, verify-publication, qualify, mirror, and release-tag are node kinds added to the existing compile/execute/render/validate chain; publish-node branches over the routes that already exist carry images and blobs; no second generator.
- **New authority lands as deep modules** under `pkg/nkd` with receipts, coded errors, cancellation, and tests, and an enumerated evidence tier replaces the string-equality gate.
- **Never cut:** the named review budget (5 pd) and the ADR-26/reconciliation/`CLAUDE.md` work (4 pd).
- **Two dates:** Thu Sep 10 go/no-go fixes M2 scope while 14 working days remain; Tue Sep 16 is the M1 stop-loss, after which the pre-authorized shrinks execute immediately.

### 6.2 Workstreams and tasks

Effort is in person-days (pd). "Closes" lists gap keys from §3.4 and Appendix A. Evidence distinguishes contract (tests) from end-to-end (runs).

#### WS-E Foundation, hygiene, Linux gate, review budget (lane E)

| ID | Milestone | Week | pd | Task |
|---|---|---|---|---|
| E-1 | M1 | W1 | 0.5 | **Fix the renderer chmod ordering (F4) and add an ordering test; regenerate.** Emit a dedicated `Prepare helper` step (`chmod +x ./nkd-exec`) right after the helper download and before any Restore step; validator rejects any job whose first `./nkd-exec` invocation precedes it; test fails on the old order. Closes `m1-no-end-to-end-github-run-evidence` (precondition). |
| E-2a | Cross | W1 | 0.5 | **Commit `ci.yml`; fix lint for go 1.27; fix the nil-context Linux test.** Per DEC-12. Closes `x-lint-gate-broken-for-go-1-27`. |
| E-2b | Cross | W2 | 1.5 | **Linux completion gate green.** Build a static fake-cosign helper in `TestMain` so the sign Linux tests pass under `-race`; add the missing negative test that a dynamic ELF is rejected; add `make factory-check` (byte-exact `--check` for every committed bundle) to `ci.yml` and a Go test calling `factory.CheckGeneratedBundle`; add a `test` job to `.gitlab-ci.yml` so the current remote runs fmt/vet/lint/race; after the GitHub repo exists, branch protection and CODEOWNERS per DEC-06. Evidence: one green `ci.yml` run on ubuntu-latest and one deliberately stale bundle failing at factory-check. Release-blocking for every milestone claim. Closes `x-factory-check-not-enforced-in-ci-or-branch-protection`. |
| E-3 | Cross | W1 | 0.5 | **Repo hygiene commit.** Commit the planning wave (`milestones.md`, ADR-24/25, SECURITY/CoC/NOTICE/.editorconfig); `.gitignore` the binary, decks, `.DS_Store`, Office lock; remove the stray `.lease` and tracked docx/html exports; `.go-version` = `1.27.0`; scope Dependabot's github-actions ecosystem to `ci.yml`; add an "Operational evidence" section to `reconciliation.md` recording GitLab pipeline 64812267 (F6) and stating the bundle on `main` has not executed; fix the stale self-hosted-runner sentences in `examples/ai-factory/README.md` and `docs/ai-factory-operator.md`; re-date M1 in `milestones.md`. Closes `x-repo-hygiene-and-stale-docs`, `x-execution-evidence-not-recorded-and-head-bundles-unexecuted`, `x-dependabot-conflicts-with-generated-workflow`. |
| E-4 | M1 | W3 | 1 | **Pin the helper.** Remove `GeneratorVersion` from `Plan.Digest` (record it as transport metadata); bootstrap emits `sha256sum nkd-exec` and every node job checks it before `chmod`; the digest feeds provenance `internalParameters.nkdExecDigest`. State honestly that this guards transport corruption, not a malicious same-repo PR. Closes `m1-generator-version-in-plan-digest`, `m1-helper-not-digest-pinned`. |
| E-5 | Cross | W3 | 1 | **Render `timeout-minutes`, `retention-days`, `concurrency`; budget table.** DeliveryProfile.runtime gains the three keys with defaults (60 / 7 / `nkd-factory-${{ github.ref }}`); after the first real runs record wall time, sizes, disk headroom, and minutes per run against the org plan. Closes `x-workflow-lacks-timeouts-concurrency-and-resource-budget`. |
| E-6 | Cross | W4 | 0.5 | **KEEP/REFACTOR/FREEZE/DELETE inventory in `CLAUDE.md`; delete dead code** (`sign.Run`/`sign.Available`, `factory.NodeInventory`). Closes `x-investment-skewed-toward-milestone-deferred-lanes`. |
| E-7a | Cross | W3 | 1.5 | **Named review R1** of M1 authority code (A-1, A-3, A-4, A-5, B-1, E-1/E-4) against the `CLAUDE.md` working contract. Findings fixed before A-8 is declared M1 evidence. Never cut. |
| E-7b | Cross | W4 | 1.5 | **Named review R2** of publish branches (A-9, A-13), qualify module and node (D-1, D-2), catalog prebuilt changes (C-1). Never cut. |
| E-7c | Cross | W4 | 2 | **Named review R3** of release-tag node and manifest (A-14), bump chain (A-15), pin-bump workflow (D-5), and the ADR-26 final text (F-1c). Never cut. |
| E-8 | Cross | W4 | 0.5 | **Documented Linux dev loop** (`make test-linux` in a `golang:1.27` container). Slippable. Closes `x-darwin-cannot-exercise-authority-paths`. |

#### WS-F Governance, decisions, ADR-26, evidence log (lane F)

| ID | Milestone | Week | pd | Task |
|---|---|---|---|---|
| F-1a | Cross | W1 | 0.5 | **W1 decision memo and the 17-row status tracker.** Circulate DEC-01..14 with owners, defaults, and deadlines (Sep 8/10/11/12/14); create `docs/evidence/status.md` (17 rows × status, evidence URL, owner, hostage-to; plus the four exit criteria), reviewed with the sponsor every Friday; file the NVIDIA-org request, the NKE repo access request, and the product-security Sigstore sign-off request today. Closes `x-governance-deciders-unresolved` (partially). |
| F-1b | Cross | W3 | 1.5 | **ADR-26 draft: v1 pipeline-attested lane.** Amends ADR-09 (second, production predicate), ADR-18 §1.6/§7 (lift the from-source fail-closed rule for this named tier on the factory emitter only), ADR-20 §4 (signed non-clearance publication; cosign-native sign/attest per pushed reference), ADR-16 (`nkd.release-manifest` is not a ReleaseSet or clearance), ADR-21 §3 (bump→regen→PR not gated on reproduction), ADR-04 (tier-specific cosign acquisition), ADR-23 (GitHub-hosted evidence), ADR-25 (post-v1; signer isolation only). Records the trigger/environment/identity rule, cosign version and storage format, private-mirror credential model, waiver policy, exclusions, mirror-vs-build labelling, revocation paragraph, org-migration story, freeze list, glossary. Adds `milestones.md` and ADR-26 to the `CLAUDE.md` authority order and updates the support-boundary table. Closes `x-adr-clauses-conflict-with-time-constrained-decisions`, `m1-static-unqualified-doctrine-blocks-milestone-evidence`, `m1-signed-publication-model-diagnostic-root-vs-clearance`, `m3-adr25-scope-and-vocabulary-mismatch`, `x-milestone-release-set-vs-repo-clearance-misalignment`, `x-terminology-collisions`, `m3-sbom-and-vuln-not-emitted-by-pipeline`, `m2-cluster-definition-and-releaseset-unreachable-from-factory`, `m3-all-prebuilt-examples-declare-evidence-none`, `x-no-upstream-signature-or-provenance-verification`. |
| F-1c | Cross | W4 | 1 | **ADR-26 final; W4 reconciliation/`CLAUDE.md`/`milestones.md` update.** Record which rows took the Time-Constrained Decision 1 fallback and update the matrix Source Lane column; list post-v1 rows with reasons and dates; final support-boundary table; Sigstore sign-off status; merge-decision rule. |
| F-2 | Cross | W3 | 1 | **Operational evidence log** `docs/evidence/<date>-<milestone>.md`: run URL, commit, planDigest, per-job durations, pushed refs@digest, cosign version + digest, laptop verify transcripts, Rekor log index, probe table, negative-run URLs, budget table; each entry labelled contract or end-to-end; stand-in runs labelled as mechanics evidence. Closes `m1-no-end-to-end-github-run-evidence` (recording half). |

#### WS-A Pipeline generation and OCI delivery (lane A, critical path)

| ID | Milestone | Week | pd | Task |
|---|---|---|---|---|
| A-0a | M1 | W1 | 0.5 | **Provision the GitHub home.** File the NVIDIA-org request and create a throwaway private repo under a team org the same day; enable Actions; create the ghcr namespace and set visibility (DEC-01); create environment `release` with no required reviewers; disable fork PR workflows; record the OIDC issuer and the expected `job_workflow_ref` identity. |
| A-0b | M1 | W1 | 1.5 | **Spike: hand-written keyless run and runner probes.** A throwaway workflow with `id-token: write` + `packages: write`: download cosign-linux-amd64 by sha256 and `readelf` it for PT_INTERP/DT_NEEDED; push a real NKD root (`publish push --allow-diagnostic`) to ghcr to observe custom artifactType/nested-index acceptance and package visibility; `cosign sign --yes` and `cosign attest --yes --type slsaprovenance1` with DOCKER_CONFIG auth; `cosign verify` and `verify-attestation` online and with `--offline`; `curl /v2/<repo>/referrers/<digest>`; rootless podman ownership probe with and without `--userns=keep-id`; `/dev/kvm` after the udev rule; wall time and `df`. Go/no-go: `cosign verify` passes against ghcr with the identity pin. Fallbacks named per probe. Closes `x-keyless-path-unverified-against-real-cosign`, `m1-rootless-podman-on-hosted-runner-unverified`, `m1-no-oci-referrer-attach-mechanism` (decision half). |
| A-1 | M1 | W2 | 1.5 | **Trigger, credential, and environment model in renderer and validator (DEC-06).** `on: {pull_request: {paths}, push: {branches: [main], tags: ['v*'], paths}, workflow_dispatch: {inputs: {releaseTag}}}`; publishing/signing kinds get the event guard and `environment: release`; `credentialRef: github:GITHUB_TOKEN`; validator requires the guard on those kinds and forbids `id-token` elsewhere. Closes `m1-pull-request-trigger-runs-publish-with-pat`, `m4-no-post-merge-trigger-or-release-dispatch-inputs`. |
| A-2 | M1 | W2 | 0.5 | **cosign as a digest-pinned factory tool.** Extend `factory.Tool` so an entry with empty `executablePath` is a raw executable download verified against `executableDigest` and written 0555; pin the exact release from A-0b in `examples/nke-v1/factory.yaml`. Keeps the four-action allowlist intact. Closes `m1-no-pinned-cosign-acquisition-in-pipeline`. |
| A-3 | M1 | W2 | 3.5 | **`pkg/nkd/sign/oci.go`: pinned, descriptor-held, registry-facing cosign adapter with receipts.** `NewPinnedOCISigner` reusing the held-executable snapshot, static-ELF check (or the DEC-04 tier-specific constructor), environment scrub, owned subprocess lifecycle and context. Subjects only as `repo@sha256:`. Registry auth via a private 0600 DOCKER_CONFIG populated from `NKD_CREDENTIAL`/`NKD_REGISTRY_USER`, never argv. `Sign`, `Attest` (predicate via anonymous descriptor), `Verify`, `VerifyAttestation` with `--output json` parsed into opaque receipts bound to subject digest, certificate identity, issuer, and Rekor log index. Bounded retry on Fulcio/Rekor transport errors; coded errors; Darwin fails `CodeEngineCapability` before I/O. Tests: argv/env/descriptor contracts, Linux held test under `-race` with the E-2b static helper, negatives. Closes `m1-no-keyless-signing-step-in-generated-workflow`, `m1-no-cosign-verify-step`, `m1-no-oci-referrer-attach-mechanism`, `x-keyless-path-unverified-against-real-cosign`. |
| A-4 | M1 | W2 | 1.5 | **`pkg/nkd/provenance/production.go`: SLSA v1 production predicate.** buildType `https://nkd.dev/buildtypes/github-factory/v1` for built rows, existing `BuildTypePrebuilt` for mirrored rows; builder.id = workflow identity; `runDetails.metadata` with invocationId, startedOn, finishedOn; externalParameters = plan/factory/catalog/delivery digests + event + inputs; internalParameters = nkd-exec digest, cosign digest, generator version; resolvedDependencies = repo@sha, per-source commit + treeHash, graph digests, toolchain/utility image digests; byproducts = child manifest digests. Separate `ValidateProduction`; a test asserts the reproduction validator still rejects this shape. Document SLSA Build L2. Closes `m1-no-provenance-emission`, `x-provenance-predicate-reproduction-vs-production-mismatch`. |
| A-5 | M1 | W2 | 3.5 | **`sign` (sign + attest) and `verify-publication` node kinds; enumerated `EvidenceTier`; subject re-binding before signing.** Per OCI route emit `sign/<route>` then `verify-publication/<route>`. `executeSign` re-opens the downloaded layout, recomputes the index digest, and refuses to sign if it differs from the plan's artifact digest or the published ref (a tampered artifact-store entry cannot redirect a keyless signature). Replace the three string gates with `factory.EvidenceTier {static-unqualified, pipeline-attested}`; header `# qualification: pipeline-attested (keyless cosign + SLSA v1 production provenance; not reproduction-qualified)`; publish gains an explicit admission check and records tier and `dev.nkd.source-lane` annotations. Sign-family jobs get `contents: read, packages: write, id-token: write`; verify must not be `if: always()`; validator enforces. GitLab renders sign-family as skipped. Closes `m1-no-keyless-signing-step-in-generated-workflow`, `m1-no-id-token-write-permission-emission`, `m1-no-provenance-emission`, `m1-no-cosign-verify-step`, `m1-static-unqualified-doctrine-blocks-milestone-evidence`, `m1-signed-publication-model-diagnostic-root-vs-clearance`. |
| A-6 | M1 | W1 | 1 | **Minimal `examples/nke-v1` definition with the crictl stand-in; FactoryCatalog entry; replace the committed ai-factory GitHub bundle.** Standalone `SourceBuildRegistry` (operator-catalog pattern) with stand-in source `cri-tools`/component `crictl` in a public `golang@sha256` toolchain; one RecipeTarget (`category: operations`, amd64), one `binary-bundle`, one OCI route to `ghcr.io/<org>/nke/nke-cli` with `credentialRef: github:GITHUB_TOKEN`, no `toolchainRegistry`; FactoryCatalog entry `nke-v1@0.1.0` so generation records `factoryReference`. Remove the ai-factory GitHub bundle from `main` (DEC-11), regenerate, commit. Closes `m1-placeholder-toolchain-registry`, `m4-committed-generation-bypasses-factory-catalog`, `x-multi-arch-needs-separate-definitions-and-workflows`. |
| A-7 | M1 | W2 | 1.5 | **First NKD-generated run on GitHub (Mon Sep 8): stand-in, unsigned publish with `GITHUB_TOKEN`.** Seven jobs (bootstrap, acquire cosign, fetch, build, observe, package, publish). Debug budget for rootless podman ownership (`--userns=keep-id` behind the runtime selector if needed), short-name resolution, egress. Logged as mechanics evidence, not M1. Closes `m1-rootless-podman-on-hosted-runner-unverified`. |
| A-8 | M1 | W3 | 2 | **M1 signed run (target Tue Sep 16).** Push to `main`: bootstrap + helper digest; acquire cosign; fetch nke-cli from the private mirror at the pinned commit (tree hash verified); hermetic build; observe; package; publish; sign (re-bind subject, sign + attest); verify-publication. Then from a laptop with only cosign: `cosign verify` and `cosign verify-attestation --type slsaprovenance1` against the pushed digest. R1 findings fixed first. If the mirror is not available the run executes on the stand-in and the NKE clause is logged external-blocked. **This is the M1 exit evidence.** |
| A-9 | M2 | W3 | 4.5 | **Publish-node branches over the existing routes.** `delivery.PushImageLayout` (preserve the pinned manifest digest; fail closed on mismatch) and `delivery.PushBlobArtifact` (single-layer artifact with a canonical config; media types by outputType); `executePublish` dispatches on input kind (root, image layout, raw/tarball/disk blob); `remote.Head` pre-check in every push path (different digest → `CodeImmutableViolation`; same digest → skip and record "already present", making mirrors idempotent and cheap); tier and source-lane annotations on every pushed manifest; reject `PlainHTTP` off-loopback; in-process registry round-trip tests for every branch. Closes `m2-no-oci-container-image-package-or-mirror-kind`, `m2-single-route-single-tag-for-all-artifacts`, `m4-tag-overwrite-not-refused-at-registry-seam`, `x-registry-roundtrip-untested-and-coverage-stale`, `x-plain-http-not-restricted-to-loopback`. |
| A-10 | M2 | W4 | 1.5 | **Fused `mirror` route for the OS image (conditional on DEC-09).** `direct: true` on a raw/disk-image pull artifact compiles to one `mirror/<route>` node that streams the pinned URL through sha256 into a ggcr stream layer pushed as a `application/vnd.nkd.disk-image.v1` artifact; no carriers, no 10-minute HTTP timeout; sign/verify hang off it. Closes `m2-no-os-image-lane` (fallback half). |
| A-11 | M2 | W4 | 2.5 | **`oci-image` build kind (conditional; first shrink target).** Wrap a built binary into a deterministic single-layer image on a digest-pinned base via ggcr, never `podman build`. Default fallback: pull NKE's prebuilt images by digest via C-1 + A-9. |
| A-12 | M2 | W4 | 3 | **`helm-charts` package kind emitting CNCF OCI chart artifacts (only if DEC-08 requires).** Default shrink (0.5 pd): enclose chart tgz blobs in the signed root via A-13. Closes `m2-no-helm-chart-artifact-type-or-chart-bundle`. |
| A-13 | M2 | W3 | 0.25 | **Prove mixed raw + tool inputs in `binary-bundle`; installPath by kind; record the CNI decision.** Test that a bundle with raw tgz inputs and `tool/aicr` compiles and lands executables at `/usr/local/bin/<name>` and tarballs at `/usr/local/share/nke/<name>`. Closes `m2-aicr-cli-not-a-deliverable`, `m2-no-archive-extraction-for-prebuilt-components` (accepted as raw), `m2-cni-row-ambiguous-and-single-binary`. |
| A-14 | M4 | W4 | 3 | **`release-tag` node: `nkd.release-manifest/v1`, deterministic immutable tag, `main` channel, sign + attest, `delivery.Tag` no-overwrite.** Manifest {tier, factoryReference, planDigest, catalogDigest, lockDigest, members[]{route, ref@digest, signatureDigest, attestationDigest, qualificationEvidenceDigest}, tag}, explicitly not an ADR-16 ReleaseSet. Tag = dispatch input, or the pushed `v*` tag, else `<factoryReference>-<planDigest[:12]>-<sha[:7]>`; `remote.Tag` after `remote.Head`; move only the documented mutable `main` channel; sign + attest the manifest. Thin `nkdctl release manifest verify` and `publish tag` wrappers. Closes `m4-no-signed-oci-release-set-tagging`, `m4-tag-overwrite-not-refused-at-registry-seam`, `x-milestone-release-set-vs-repo-clearance-misalignment`. |
| A-15 | M4 | W4 | 2 | **Bump chain in one verb.** `bump --all` re-renders prebuilt targets for bumped sources against the in-memory lock and resolves digests before the lock write (`--check` covers both); `factory catalog repin` recomputes `definitionDigest`; move the digest resolvers from `cmd/nkdctl/commands.go` into `pkg/nkd/catalog` on go-containerregistry (no docker CLI) with httptest tests. Closes `m4-bump-does-not-refresh-prebuilt-or-toolchain-pins`, `m4-overlay-and-definition-pins-not-automatable`, `x-pin-resolvers-untested-docker-bound-never-run-live`, `m4-actions-lock-embedded-requires-rebuild`. |
| A-16 | M1 | W4 | 1.5 | **`nkdctl trust init`: pinned trusted root and identity/issuer policy; `docs/consumer-verify.md`.** Late in the slip order. Closes `x-trusted-root-and-identity-policy-not-provisioned`. |

#### WS-B From-source wrap of NKE artifacts (lane B)

| ID | Milestone | Week | pd | Task |
|---|---|---|---|---|
| B-0 | M1 | W1 | 1 | **Spike: NKE CLI access, build facts, hermetic container build.** Request read access today. Clone the last green tag; record language, build system, Go version, private module paths, cgo, artifact path. In a digest-pinned public golang image: `go mod vendor` (network allowed, as the fetch stage) then `go build` with `--network none`; time it. Go/no-go: hermetic build ≤ 15 min in a public image with vendored deps. Fallbacks: cgo/private toolchain → custom toolchain image (+1 pd); no green tag → last known-good commit; not convertible → stand-in path with the clause external-blocked. |
| B-1 | M1 | W2 | 2 | **Mirror NKE CLI source to a private GitHub repo; fetch credential for private sources.** Catalog source gains `private: true`; the fetch stage honours `NKD_GIT_CREDENTIAL` via an askpass helper only for private sources; the renderer emits the secret only on fetch jobs of private sources, never on `--network none` build jobs; validator enforces; fork PRs fail closed. Reviewed in R1. |
| B-2 | M1 | W2 | 1 | **NKE CLI as a pinned wrapped catalog component; `catalog pin`; swap the stand-in out; regenerate.** Source `nke` {repo, constraint, toolchainImage@sha256, vendorCommand, private} and component `nke-cli` {buildCommand, artifactPath, outputType host-package}; `nkdctl catalog pin nke --tag <green tag>`. **This is the D2 "pinned wrapped NKE build".** Closes `m1-nke-cli-fs-wrap-lane-absent`. |
| B-3 | M2 | W4 | 3 | **nke-agent, CWE controller, NKE container images: FS-wrap entries or fallback mirrors.** First M2 item to slip. Fallback per Time-Constrained Decision 1 if build-readiness or mirroring is not in place by Sep 18: prebuilt `image:` components pinned by digest delivered via A-9 (0.5 pd), rows recorded as pull+bundle in ADR-26 and the matrix. Closes `m2-in-scope-artifacts-absent-from-catalog` (NKE half). |
| B-4 | M2 | W4 | 0.5 | **NKE OS image pinned as a prebuilt `disk-image` component routed through the fused mirror.** Conditional on DEC-09. Mirror provenance (how bytes were acquired) stated in ADR-26. |

#### WS-C Pull + bundle upstream and SKU bundles (lane C)

| ID | Milestone | Week | pd | Task |
|---|---|---|---|---|
| C-0 | M2 | W3 | 0.5 | **Spike: upstream reachability from hosted runners, QCOW2 size, disk headroom.** Pull a `registry.k8s.io` image by digest through the existing prebuilt image lane (untested against a real registry); HEAD dl.k8s.io, get.helm.sh, plugins releases, ghcr.io/spiffe, the QCOW2 URL; `df`; ghcr layer limits. Go/no-go for the OS image: ≤ ~8 GB and download+push under 60 min. |
| C-1 | M2 | W3 | 3 | **nke-v1 prebuilt catalog entries for all pull+bundle rows; source-less fixed-ref prebuilt; live `pin-artifacts`.** kubeadm/kubelet/kubectl (dl.k8s.io); control-plane images (registry.k8s.io by digest); cni-plugins and helm tgz (raw); AICR-referenced chart tgz (raw); spire-agent image; the third-party list. Code: allow a prebuilt component to omit `source:` when its ref is not version-templated. Run `pin-artifacts --all --arch amd64` against real upstreams on Linux and commit the lock. Closes `m2-in-scope-artifacts-absent-from-catalog` (upstream half), `m2-prebuilt-requires-unrelated-git-source-pin`, `m2-layered-catalog-requires-both-arch-digests`, `m2-cni-row-ambiguous-and-single-binary`. |
| C-2 | M2 | W4 | 2.5 | **Bundles and class variants.** `operator-tooling` binary-bundle [kubectl, kubeadm, helm tgz, cni-plugins tgz, tool/aicr]; `aicr-bundle` oci-root enclosing the AICR tree plus chart tgz blobs (or CNCF charts if DEC-08 requires); `nke-charts` only if NKE chart sources are mirrored; ClusterSpecs `gpu-tenant` (type tenant) and `site-controller` (type operator, control-plane over the prebuilt k8s set + platform-services with CWE/SWE pins) each with an `oci-root` and one signed route. Site-controller half is composition-only or post-v1 if CWE/SWE pins are late. Closes `m2-site-controller-sku-and-bundle-kinds-not-modeled`. |

#### WS-D Qualification gate, signed evidence, negatives, release cadence (lane D)

| ID | Milestone | Week | pd | Task |
|---|---|---|---|---|
| D-1 | M3 | W3 | 4.5 | **`pkg/nkd/qualify`: `QualificationEvidence` kind, `RoleQualification`, checks, a `Gate` that never attests a failing verdict.** Evidence {schemaVersion, tier, factoryReference, planDigest, catalogDigest, lockDigest, deliveryProfileDigest, subjects[]{route, ref@digest, childDigests}, checks[]{name, verdict, detailDigest}, waivers[], verdict, checkedAt, checkerIdentity} as `nkd.qualification-evidence/v1`. Checks: schema (exact decode of plan/graphs/compositions + `Plan.Validate` + catalog load with lock validation); pin-consistency (every published subject's child digests equal the plan artifact digests; every pull digest equals its `ArtifactPin`); presence (per subject `Verify` + `VerifyAttestation(slsaprovenance1)`; `CodeProvenanceMissing` when absent; waivers recorded for `evidence: none` prebuilt components); smoke (argv table executed on the extracted tooling bundle inside the utility container with `--network none`). Any failing check → verdict fail, evidence written locally for the log, never attested, coded error. Local mode on `pull_request` (no registry). Passing evidence attested to each subject with predicate type `https://nkd.dev/attestations/qualification-evidence/v1`. Closes `m3-no-qualification-gate-node-in-generated-pipeline`, `m3-no-signature-provenance-presence-check-or-tamper-demo`, `m3-no-signed-qualification-evidence-object`, `m3-all-prebuilt-examples-declare-evidence-none`. |
| D-2 | M3 | W4 | 2 | **`qualify` node kind: must-fail gate job after verify-publication; local mode on PRs.** One `qualify/<definition>` node; `terminal-observe` loses `if: always()` for the pipeline-attested tier; release-tag depends on qualify; `nkdctl factory qualify --ref <repo@digest>` thin read-only verb. |
| D-3 | M3 | W4 | 2 | **Negative evidence produced out-of-band (no production disable switch, no key mode).** Go negatives with the in-process registry and a fake signer; `scripts/demo-qualification-tamper.sh` copies a qualified root to a scratch ghcr repo and (1) mutates a blob, (2) deletes the attestation, (3) strips the signature, each yielding a distinct coded error from `factory qualify --ref`; one real red GitHub run via a read-only `verifyRef` dispatch path (validator forbids `id-token: write` on it). Explicitly rejected: any flag that makes the sign node skip attestation. The red GitHub run is the first part to slip. |
| D-4 | M3 | W4 | 1 | **Run the gate across every route delivered in M2; smoke argv table; waivers listed.** |
| D-5 | M4 | W4 | 2 | **Hand-authored pin-bump workflow** `.github/workflows/nkd-pin-bump.yml` (weekly cron + dispatch): SHA-pinned actions; `persist-credentials: false`; unstamped `go build`; `catalog bump --all` (chained pin-artifacts via A-15); `actions bump` then rebuild if changed; `factory catalog repin`; `factory generate`; `gh pr create` on `nkd/pin-bump-<date>` **with an App/PAT token** (GITHUB_TOKEN-created PRs do not trigger workflows) and a body listing each pin's old→new tag/commit/treeHash/digest and source URL. Merge = human release decision (DEC-07). Closes `m4-no-scheduled-pin-bump-regenerate-workflow`, `m4-actions-lock-embedded-requires-rebuild`. |
| D-6 | M4 | W4 | 1.5 | **M4 cadence evidence.** Fixture pin update → PR → PR run (build + package + local qualify) → human merge → push-to-main run (publish + sign + verify + qualify + release-tag) producing `ghcr.io/<org>/nke/release:<factoryReference>-<planDigest12>-<sha7>` and moving `main`, verifiable from a laptop; then `workflow_dispatch` with `releaseTag` reproducing the same member digests; a tag-move attempt refused. **This is the M4 exit evidence.** The dispatch-equivalence half is the last item in the slip order. |
| D-7 | M1 | W4 | 0.5 | **Opt-in real-cosign integration job in `ci.yml`** recording the supported cosign version range. Slippable (A-0b already observed one real run). |

### 6.3 Week-by-week schedule

Calendar: 20 working days remain (Sep 3-4; Sep 8-11; Sep 14-18; Sep 21-25; Sep 28-Oct 1). Sep 7 is Labor Day. Capacity for three engineers: 6 / 15 / 15 / 24 pd per week. Demand: 6 / 16.5 / 20.75 / 34 pd.

**W1 (Sep 3-4).** All: F-1a memo filed; NVIDIA-org, NKE access, and Sigstore sign-off requests filed; A-0a throwaway repo live. Eng-1: A-0b spike and probe table. Eng-2: B-0 NKE CLI hermetic-build spike; A-6 `examples/nke-v1` with the stand-in, regenerated and committed. Eng-3: E-1 renderer fix and test; E-2a `ci.yml` + lint + nil-context test; E-3 hygiene commit.
*Demo Fri Sep 4:* the spike run URL showing `cosign verify` and `verify-attestation` passing against an NKD root pushed to ghcr from a hosted runner, plus the probe table; the renderer test proving chmod precedes unpack and `--check` green on nke-v1; the NKE CLI build transcript (or the documented no-go and fallback); `ci.yml` lint green. M1 is not met this week.

**W2 (Sep 8-14), all hands on M1.** Mon Sep 8, Eng-2: A-7 first NKD-generated run on GitHub (stand-in, unsigned). Eng-1: A-3 adapter; A-5 node kinds and tier. Eng-3: A-1 trigger model; A-4 predicate; A-2 cosign tool; E-2b Linux gate. Eng-2: B-1 mirror + credential; B-2 NKE CLI component and swap. **Thu Sep 10 go/no-go** (all + sponsor): stand-in green? adapter signs on ghcr? mirror + credential in place? Sigstore sign-off? Fix the M2 minimum (public pull rows + operator tooling bundle + tenant SKU + AICR bundle with raw charts; NKE rows as mirrors; OS image conditional on C-0 by Sep 14) while 14 working days remain. Demand exceeds capacity by ~1.5 pd, so the signed M1 run is dated Tue Sep 16.
*Demo Fri Sep 11:* the stand-in pipeline green on GitHub through publish with the run URL; `factory generate` rendering sign → verify-publication jobs with the right permissions, environment, and PR guards; in-process registry and fake-cosign contract tests green; `ci.yml` fully green on Linux including `-race`; the NKE mirror cloned by a fetch job with the deploy key (if DEC-02 landed).

**W3 (Sep 15-21): M1 exit, M2 public rows, qualify module.** Eng-1: A-8 M1 signed run (Tue Sep 16) + laptop verify; E-4 helper pinning; E-5 timeouts/budget; then A-9 publish branches. Eng-2: C-0 probe; C-1 prebuilt catalog + live pin-artifacts; A-13. Eng-3: E-7a review R1 (before A-8 is declared evidence); F-1b ADR-26 draft + W3 reconciliation/`CLAUDE.md` update; F-2 M1 evidence file; then D-1 qualify module (contract complete by Mon Sep 21). **Tue Sep 16 stop-loss:** if M1 is not green, execute the shrinks immediately and re-baseline M2 as the Sep 10 minimum. Demand exceeds capacity by ~6 pd; A-9 may complete early W4.
*Demo Fri Sep 18:* M1 exit evidence in `docs/evidence` and `reconciliation.md`: one green run building the NKE CLI from its pinned mirror commit, pushing bundle + signature + provenance attestation to ghcr, verify-publication green, and a laptop with plain cosign verifying both; from the M2 run in progress, `docker pull` of a mirrored kube-apiserver image by digest with cosign verify passing and a second run skipping already-present mirrors; `pkg/nkd/qualify` contract tests green including all fail-closed negatives; ADR-26 draft PR open with named deciders.

**W4 (Sep 22-Oct 1): M3, M4, M2 stragglers; slip order applies.** Eng-1: A-14 release-tag node; D-6 with Eng-2; A-16 if slack. Eng-2: C-2 bundles and roots + M2 run; A-15 bump chain; D-5 pin-bump workflow; A-10/B-4 only if DEC-09 numbers arrived; B-3/A-11/A-12 only if the Sep 10 checkpoint kept them. Eng-3: D-2 qualify node; D-3 negatives; D-4 gate across routes; E-7b/E-7c reviews; F-1c ADR-26 final and doc updates; E-6 freeze inventory; D-7/E-8 if slack. Demand exceeds capacity by ~10 pd before shrinks and ~3-7 after: the dispatch-equivalence half of D-6, the GitHub red run in D-3, and A-16 are expected to complete Oct 1-2 or slip.
*Demo Thu Oct 1:* M3: the qualify job green on the main-push run with signed evidence verifiable via `cosign verify-attestation --type .../qualification-evidence/v1` from a laptop; the scratch-repo tamper script showing three distinct fail-closed codes (and one red `verifyRef` run if it landed). M4: a pin-bump run opened a PR with an App/PAT token, its PR run built and qualified locally, a human merge produced a signed, attested `ghcr.io/<org>/nke/release:<factoryReference>-<planDigest12>-<sha7>` with `main` moved; a dispatch with `releaseTag` reproduced the same digests (if it landed); tag overwrite refused. M2: the set of signed rows actually delivered, with deferred rows, reasons, and dates in `reconciliation.md` and the matrix Source Lane column updated.

### 6.4 Staffing and feasibility

| Milestone | Person-days |
|---|---|
| M1 | 24.5 |
| M2 | 21.25 |
| M3 | 9.5 |
| M4 | 8.5 |
| Cross-cutting (hygiene, gate, reviews, ADR-26, evidence) | 13.5 |
| **Total** | **77.25** (82.25 after §7) |
| Capacity, 3 engineers × 20 working days | 60 |
| Core after pre-authorized shrinks | 63.5 (66.75 if the OS image is feasible); **about 70 after §7**, see §7.5 |

The full plan does not fit Oct 1 with three engineers. Pre-authorized shrinks, in the order they are taken:

1. A-11 `oci-image` build kind (2.5) → pull NKE prebuilt images by digest via C-1/A-9.
2. A-12 CNCF helm-charts (3.0 → 0.5) → chart tgz blobs enclosed in the signed root.
3. B-3 FS-wrap of nke-agent/CWE/NKE images (3.0 → 0.5) → mirrors of NKE-published images if reachable, else rows recorded external-blocked.
4. A-10 + B-4 OS image (2.0) → infeasible-by-Oct-1 with contract evidence only, unless a reachable QCOW2 ≤ ~8 GB exists by Sep 14.
5. D-7 real-cosign CI job, E-8 dev loop, the ggcr resolver move inside A-15 (2.0 total) → post-v1.
6. Site-controller half of C-2 (1.0) → composition-only or post-v1 if CWE/SWE pins are late.
7. The GitHub red run in D-3 (0.75) and the dispatch-equivalence half of D-6 (0.5) → Oct 2.
8. A-16 trust init (1.5) → post-v1 (cosign default TUF root; identity pinned in the DeliveryProfile).

After shrinks 1-6 the core is ~63.5 pd, still 3.5-7 pd over, so items 7-8 finish Oct 1-2 or slip. What fits by Oct 1 with three engineers: M1 (all clauses, hostage to NKE source reachability and the Sigstore sign-off); M3 (gate, signed evidence, contract and scratch-repo negatives) on the recipes that exist; M4's automated path (App/PAT bump PR, human merge as the release decision, signed deterministic release tag + `main` channel); and the M2 core (public pull+bundle rows, operator tooling bundle, tenant SKU root, AICR bundle with raw charts, mirrored NKE images). Never cut: the 5 pd review budget and the 4 pd ADR-26/reconciliation/`CLAUDE.md` work. A fourth engineer from Sep 15 (+13 pd) carries the core with slack and restores one of A-11 or A-12. With two engineers (40 pd) only M1, the M3 core, and a dispatch-only M4 are achievable.

### 6.5 Risks

| Risk | L / I | Mitigation | Re-plan trigger |
|---|---|---|---|
| NKE source cannot be mirrored to a GitHub-reachable location, has private deps that break `--network none`, or the fetch credential is not provisioned (F9). | high / M1 NKE clause and every M2 FS-wrap row unmet regardless of nkdctl. | B-0 spike by Sep 4; stand-in proves all mechanics so only the swap remains; `vendor/` in the mirror; deploy key on fetch jobs only; fallback = mirror NKE-published artifacts by digest. | No clone access by Sep 8 or no hermetic build/mirror by Sep 10: M1 evidence stays on the stand-in with the clause external-blocked; FS-wrap rows relabelled pull+bundle. |
| GitHub org/repo (OSRB lead time), ghcr permissions, or Actions quota unavailable; throwaway-org identities differ from the final identity. | high / nothing end-to-end demonstrable; re-signing after migration. | Dual track on day 1; identity/issuer/registry are DeliveryProfile data; migration story in ADR-26; GitLab `test` job as the interim Linux gate. | No NVIDIA-org repo by Sep 21: the throwaway org is the v1 evidence home; migration post-v1. |
| Public Sigstore disclosure of NKE digests and workflow identity is disallowed after signing starts, or the sign-off never arrives. | medium / identities and verify pins change; earlier signatures invalid. | Spike on throwaway artifacts only; sign-off required before the first NKE-bearing signed run; issuer/identity/trusted-root parameterized. | Security says no by Sep 11: switch to private Sigstore or a KMS key (+3 pd); M1 +2 days. |
| Rootless podman on ubuntu-latest leaves subuid-owned files or breaks short-name resolution; first-run debugging historically took six GitLab attempts (F6). | medium / M1 slips days in an already over-capacity W2. | A-0b ownership probe first; `--userns=keep-id` confined to the launch argv; the Sep 8 stand-in run absorbs first-run debugging before signing code lands. | More than 1.5 pd lost: switch the nke-v1 runtime to docker (also preinstalled) and record the deviation in ADR-23 evidence. |
| Official cosign binary is dynamically linked, or the pinned release's argv/bundle behaviour differs; Rekor/Fulcio outage turns into a red release pipeline. | medium / sign/verify jobs fail; consumer verify semantics differ. | A-0b verifies the exact release; pin by sha256; tier-specific constructor only if dynamic; bounded retry; DEC-14 names the owner. | Pinned release fails a contract: pin the previous 2.x release and re-run A-0b before A-3 merges. |
| ghcr rejects custom artifactTypes, nested indexes, or multi-GB layers; QCOW2 unreachable or oversized; packages private so laptop verify needs a login. | medium (high for the OS image) / Row 6 infeasible; evidence procedure incomplete. | A-0b pushes a real NKD root; DEC-09 requires URL/digest/size by Sep 14; A-10 streams without carriers; login step written into the evidence procedure. | No reachable QCOW2 or > ~8 GB by Sep 14: Row 6 declared infeasible by Oct 1. |
| Effort exceeds capacity (77 vs 60) and M1 slips past Sep 16, compressing M2-M4. | high / M2 breadth and M4 polish slip past Oct 1. | Shrink list pre-authorized with dates; Sep 10 go/no-go; qualify module in W3 not W4; weekly demos and the 17-row tracker; fourth-engineer option stated. | M1 not green by Sep 16: execute shrinks 1-6 immediately. |
| Bump PRs opened with the wrong token do not trigger workflows; the merge question left implicit; M4 demo stalls. | medium / "regenerates + re-qualifies" evidence missing. | DEC-07 default (bot PAT/App token; human merge); D-5 acceptance requires the PR's factory run to start. | Token not provisioned by Sep 22: D-6 runs with a manually opened PR and records the deviation. |
| Single-author authority code (heldfile, sandbox, sign adapter, node kinds, qualify, release-tag) lands without independent review; a fail-open surfaces during signed runs. | medium / late rework in authority modules; M3 gate trust questioned. | Named reviewers with a never-cut 5 pd budget; R1 before A-8 is declared evidence; argv tests and in-process registry round-trips for every adapter; adversarial negatives in D-3. | Review finds a fail-open: stop feature work on that module and fix before the next signed run. |
| Actions minutes/storage/concurrency: M2 runs mirror upstream images on every main push; Rekor/upstream rate limits. | medium / runs queue or fail; storage overage. | A-1 path filters and PR build-only; E-5 timeouts/retention/concurrency and a computed budget; A-9 `remote.Head` skip makes mirrors idempotent; A-10 streams without carriers. | Any M2 run over 90 minutes or storage over quota: split pull+bundle rows into a scheduled workflow. |
| ADR-26 or deciders stall; `validate.go`'s tier gate and `CLAUDE.md` keep contradicting the shipped pipeline; agents following `CLAUDE.md` keep the gates the plan relaxes. | low / documentation debt; the lane treated as unsupported. | F-1a names deciders on day 1; ADR-26 proceeds as Proposed-with-deciders; reconciliation/`CLAUDE.md` updated at W1, W3, W4 as scheduled tasks. | No deciders by Sep 11: the tech lead ratifies provisionally and records it. |
| Consumer format (DEC-08) or row meanings (spire, CNI, third-party list, NKE chart sources) arrive late or contradict defaults. | medium / rework of C-1/C-2/A-12 or wasted work on struck rows. | Defaults stated with Sep 12 deadlines; standard images and raw chart tgz are the safer defaults; CNCF charts only on explicit demand. | No answer by Sep 12: proceed on defaults; a later CNCF requirement becomes post-v1. |

### 6.6 Explicit scope boundaries

**Out of scope for v1 (stated in ADR-26):** from-source NKE OS image build; a job-level reusable-workflow node kind unless the NKE team insists (needs an ADR-18 amendment); byte-identical guarantees; cluster E2E validation; `ClusterDefinition` and ADR-16 ReleaseSet/Inventory minting from the factory; SBOM generation and vulnerability scanning in the pipeline; upstream signature/provenance verification of pulled artifacts (the pull lane stays digest-pinned trust-on-first-use, with the bump PR making pin provenance visible); non-OCI delivery; multi-arch (v1 amd64 only); NGC/nvcr.io as a target unless probed.

**Post-v1 (gap keys carried, not worked):** archive-member extraction for prebuilt components; command-layer authority migration except the resolvers moved in A-15; render-stack consolidation; drift classification in `factory --check`; supplier-overlay re-pin for ai-factory; `vendorImage` relocation; transport-free snapshot digest; laptop path repair for ai-factory; helper image mode and `nkd-exec` image publication; logical-disk (qemu-img) measurement of the OS image; the scheduled release-audit job; vendoring the nkdctl module tree for the bootstrap; an offline Rekor-bundle verify path beyond `cosign --offline`; a second registry route to NGC.

**Explicitly rejected designs (security):** a production `workflow_dispatch` input or scratch-route flag that makes the sign node skip attestation; a key-based mode in the production registry cosign adapter; registry credentials on cosign argv; `GITHUB_TOKEN` as the private-mirror fetch credential; required reviewers on the automated push-to-main path; a separate `attest` node kind (one sign job signs and attests the same digest); renaming `helm-oci`.

---

## 7. Addendum (2026-09-03, later): the NKE OS image build contains the node and control-plane stack

**Development.** The NKE node-images build produces a single OS image and, inside that build, builds and bakes
in Kubernetes (node side and all control-plane content, including the control-plane container images), CNI, and
node-agent. From NKD's perspective one pinned NKE commit yields the whole node and control-plane substrate. This
section supersedes the parts of §3.2, §5, and §6 it names; everything else stands.

**Still unverified (stated as assumptions below).** The build's entrypoint and environment: a single command NKD
can run, inside what container image, needing what privilege (loop devices, chroot), network during the build,
scratch disk, and wall time. Whether every input is pinned by the repo commit. Whether a machine-readable
component manifest exists inside the image. Whether CWE/SWE and other NKE cluster images, and spire-agent, are
also baked in (assumed not).

### 7.1 Artifact matrix after the development

| # | Row | New disposition |
|---|---|---|
| 3 | nke-agent | Inside the OS image. No separate artifact, story, or catalog entry. |
| 4 | CWE controller | Unchanged: separate cluster-workload image, mirror by digest (fallback) or FS-wrap (stretch). Confirm it is not baked in. |
| 5 | NKE container images | As row 4. |
| 6 | NKE OS image | **Central M2 deliverable.** FS-wrap: one `os`-category component, `outputType: disk-image`, source = NKE node-images repo at a pinned commit, built by a new `wrapped-build` node class, published as one signed disk-image artifact. Fallback: mirror the prebuilt signed QCOW2 by digest (the former A-10/B-4 design). |
| 7 | Kubernetes control plane | Inside the OS image, including the control-plane images. No mirrors. kubectl stays in the operator tooling bundle. |
| 8 | CNI | Inside the OS image. No separate artifact. |
| 9, 10, 15 | Helm, AICR CLI, operator tooling bundle | Unchanged: pull+bundle into one signed `binary-bundle` = kubectl + helm + AICR CLI (kubeadm and the CNI tarball drop out). |
| 11 | spire-agent | Likely inside the image if it is a node daemon; otherwise out of scope (SRD WONT-v1). Confirm; default: no separate artifact. |
| 12 | Third-party images | Only the images referenced by the AICR and NKE charts that are not baked into the OS image; list from NKE by Sep 12. |
| 13, 14 | AICR and NKE chart bundles | Unchanged. |
| 16 | Tenant SKU | Composition binds the OS image (`os` category) instead of a Kubernetes recipe, plus the AICR bundle and any CWE/NKE images; role binding `nodeLevelCategories: [os]`. |
| 17 | Site-controller SKU | As row 16 with `type: operator` and the CWE/SWE images. |

Net: the from-source Kubernetes lane, the custom toolchain images, and the pull+bundle entries for
kubeadm/kubelet/control-plane images/CNI are not needed for v1. The freeze recommendation in §4 strengthens.

### 7.2 Decision changes

- **DEC-09 (rewritten).** OS image = wrapped NKE build as the primary path. Go/no-go at the Thu Sep 10 checkpoint
  on the B-5 spike: the build runs to completion on a GitHub-hosted runner inside NKD's launch path within the
  job budget. If no-go, fall back to mirroring the prebuilt signed QCOW2 by digest (A-10 + B-4). Owner: NKE OS/build team + NKD lead.
- **DEC-10 (rewritten).** Rows 3, 7, 8 are inside the image. Operator tooling bundle = kubectl + helm + AICR CLI.
  spire-agent: in the image or out of scope. Third-party image list and NKE chart sources by Sep 12. Owner: NKE eng lead.
- **DEC-15 (new).** A `wrapped-build` node class for pinned external builds: runs a declared entrypoint from a
  pinned repo commit inside a pinned container with declared `network: required` and `privileged: true`
  (loop devices, chroot); fused with terminal-observe and publish in one job because the output is gigabytes; the
  hermeticity claim is the wrapped build's, not NKD's, and the manifest annotation and ADR-26 say so
  (`dev.nkd.source-lane: wrapped-build`). Prefer a pinned privileged container over running on the runner host so
  the toolchain identity stays digest-pinned. Owner: NKD architect + product security. Default: as stated.
- **DEC-02 (extended).** The NKE node-images repo joins the NKE CLI repo in the private-mirror and fetch-credential
  arrangement, with `vendor/` or equivalent so the build's own inputs are pinned by the commit.
- **DEC-13 (note).** If the standard runner's scratch disk (about 14 GB on `/`, 60-70 GB on `/mnt`) is too small
  for the OS build, a larger GitHub-hosted runner is still a generic hosted runner and within the standing
  constraint; a self-hosted runner is not.

### 7.3 Task changes (§6.2)

| Change | Task | Milestone / week / pd | Detail |
|---|---|---|---|
| New | **B-5** Spike: NKE OS image build on a hosted runner | M2 · W2 · 1 | Obtain the build entrypoint from NKE; run it on `ubuntu-latest` inside a privileged pinned container with network, measure wall time, peak disk, output size and format, and whether a component manifest exists in the image. Go/no-go for DEC-09 at the Sep 10 checkpoint. Fallbacks: larger hosted runner; prebuilt QCOW2 mirror. |
| New | **B-6** `wrapped-build` node class and fused build/observe/publish | M2 · W3 · 3 | Catalog and IR: component fields `entrypoint`, `network: required`, `privileged: true`, admitted only for `outputType: disk-image` (and later image kinds) and only when the source is a declared wrapped build. `exec-node --launch` passes `--privileged --device /dev/loop-control` (and `/dev/kvm` if present) for this class; validator requires the class on the node, forbids it elsewhere, and requires the fused job shape (build → re-hash → publish → sign → verify in one job, no carriers). Renderer emits `dev.nkd.source-lane: wrapped-build`. Tests for admission, renderer, and validator. Reviewed in R2a. |
| New | **B-7** OS image component in `examples/nke-v1` and the first signed image run | M2 · W4 · 1 | Source `nke-node-images` (mirror URL, pinned commit, tree hash, `private: true`) and component `nke-os-image` (`category: os`, `outputType: disk-image`, entrypoint from B-5); tenant composition rebinds `nodeLevelCategories: [os]`; regenerate; run; laptop `cosign verify` on the image reference. |
| New | **D-8** Image manifest check in the qualify gate | M3 · W4 · 1 | Mount the published image read-only on the runner (loop device for RAW; `qemu-img convert` or `qemu-nbd` for qcow2, via the pinned qemu toolchain), read the component manifest NKE writes at a known path (or the package database if none), and compare embedded Kubernetes/CNI/node-agent versions to the versions recorded for the pinned commit. Feeds the `pin-consistency` and `smoke` checks in `pkg/nkd/qualify`. |
| Changed | **A-10 + B-4** prebuilt QCOW2 fused mirror | M2 · W4 · 2 | Now the **fallback only** (executed if DEC-09 is no-go). |
| Changed | **C-1** prebuilt catalog entries | M2 · W3 · 2.5 (was 3) | Drop kubeadm/kubelet and the control-plane image entries and the CNI tarball; keep kubectl, helm tgz, AICR chart tgz, third-party images per DEC-10. |
| Changed | **B-3** NKE FS-wrap / mirror | M2 · W4 · 2.5 (was 3) | nke-agent removed (inside the image); CWE controller and NKE cluster images remain (mirror fallback 0.5, FS-wrap stretch 2). |
| Changed | **C-2** bundles and roots | M2 · W4 · 2.5 | Operator tooling bundle = kubectl + helm + AICR CLI; tenant and site-controller compositions bind the OS image under the `os` category. |
| Changed | **D-4** gate across routes | M3 · W4 · 1 | Smoke table: operator tooling (kubectl, helm, aicr) and nke-cli; the OS image is covered by D-8. |
| Unchanged | A-9 | M2 · W3 · 4.5 | Still needed for CWE/NKE and third-party image mirrors and raw blobs. |
| Unchanged | A-11 | M2 · W4 · 2.5 | `oci-image` build kind, now only for CWE/NKE images; still shrink 1. |

### 7.4 Schedule deltas (§6.3)

- **W2:** Eng-2 adds B-5 (1 pd) after the Sep 8 stand-in run; DEC-02 extends to the node-images mirror. The Thu Sep 10 go/no-go now decides the OS image path (wrapped build vs prebuilt mirror) as its first item.
- **W3:** B-6 (3 pd) lands with Eng-2; C-1 shrinks to 2.5 pd. Eng-2's W3 load (C-0 0.5, C-1 2.5, A-13 0.25, B-6 3) is 6.25 pd against 5; B-6's renderer/validator half moves to Eng-1 after A-8 or into W4.
- **W4:** B-7 (1 pd) precedes C-2 and the M2 run; D-8 (1 pd) precedes D-4.
- **Demos:** Fri Sep 11 adds the B-5 transcript (wall time, disk, output size). Fri Sep 25 shows the signed OS image reference pulled and verified from a laptop. Oct 1's M3 demo includes the image manifest check passing and a version-mismatch negative.

### 7.5 Staffing and feasibility after the development

| Item | pd |
|---|---|
| Previous total (§6.4) | 77.25 |
| New: B-5 (1) + B-6 (3) + B-7 (1) + D-8 (1) | +6 |
| Trimmed: C-1 (−0.5), B-3 (−0.5) | −1 |
| **New total** | **82.25** |
| Core after shrinks 1-6 when the wrapped build works (A-11 2.5, A-12 2.5, B-3 FS-wrap 2, A-10/B-4 fallback 2, D-7/E-8/resolvers 2, site-controller 1) | **about 70** vs 60 available |

The OS image from source costs about 5 pd more than the prebuilt-mirror fallback and it is the milestone's highest-value
row, so it should be protected. What gives: with three engineers, M2's CWE/NKE image work stays at mirrors, CNCF
charts stay raw tgz, and the site-controller root is composition-only; a fourth engineer for W3-W4 (+13 pd) carries
the core with slack. If B-5 is no-go on Sep 10, the plan reverts to the §6 numbers (fallback mirror) and the M2
row 6 disposition is recorded as pull+bundle.

### 7.6 Risk changes (§6.5)

- **New, high:** the NKE OS build cannot run on a hosted runner (privilege, disk, time, or a floating input). Mitigation: B-5 by Sep 10; larger hosted runner; fallback mirror. Trigger: no completed build on a hosted runner by Sep 10.
- **New, medium:** the build is not fully pinned by the commit (fetches a floating version), making the NKD pin and provenance hollow. Mitigation: ask NKE for a pin-by-commit guarantee and a manifest inside the image; D-8 detects drift between expected and embedded versions. Trigger: B-5 shows unpinned fetches.
- **Reduced:** "NKE FS-wrap rows unmet regardless of nkdctl" narrows to CWE/NKE cluster images; node-agent and node-side Kubernetes ride the image.
- **Reduced:** registry/quota risk from mirroring control-plane images on every push disappears.

### 7.7 Questions still open for NKE

1. The build entrypoint and environment: one command, container image, privilege, network, disk, wall time, KVM.
2. Pin-by-commit guarantee for every input the build fetches.
3. A machine-readable component manifest inside the image at a known path.
4. Whether CWE/SWE, other NKE cluster images, and spire-agent are baked in.
5. Output format (RAW, qcow2) and size, for the disk-image artifact and registry layer limits.

---

## Appendix A. Medium and low gaps (carried forward unverified)

| Milestone | Severity | Key | Statement (short) |
|---|---|---|---|
| M1 | medium | `m1-generator-version-in-plan-digest` | `Plan.Digest` includes `GeneratorVersion` (`dev (unknown)` committed); a stamped build fails `--check`. Closed by E-4. |
| M1 | medium | `m1-pull-request-trigger-runs-publish-with-pat` | `pull_request: {}` runs every job including publish with a PAT-style secret. Closed by A-1. |
| M1 | medium | `m1-helper-not-digest-pinned` | Bootstrap reads a floating `.go-version`; helper artifact not digest-checked; `helper-image.lock.json` carries null digests. Closed by E-3/E-4. |
| M2 | medium | `m2-cni-row-ambiguous-and-single-binary` | `cni-plugins` is one from-source bridge plugin; no CNI provider; prebuilt tgz blocked by the archive gap. Closed by C-1/A-13 (raw tgz). |
| M2 | medium | `m2-prebuilt-requires-unrelated-git-source-pin` | Every prebuilt component must reference a git `source:`; fixed-ref components get the wrong version. Closed by C-1. |
| M2 | medium | `m2-single-route-single-tag-for-all-artifacts` | One unary publish node per route; the example pushes the whole root to one fixed tag. Closed by A-9. |
| M2 | medium | `m2-cluster-definition-and-releaseset-unreachable-from-factory` | ClusterDefinition needs a ReleaseSet the factory never produces; a "signed SKU bundle" can at most carry the ClusterComposition. Accepted by DEC-05; closed by F-1b/A-14. |
| M2 | low | `m2-layered-catalog-requires-both-arch-digests` | Layered catalogs demand both arch digests for prebuilt components. Closed by C-1 (standalone amd64 catalog). |
| M3 | medium | `m3-all-prebuilt-examples-declare-evidence-none` | Every prebuilt component declares `evidence: none`, which the release path treats as waiver-required. Closed by D-1 waivers. |
| M3 | low | `m3-sbom-and-vuln-not-emitted-by-pipeline` | Qualified SBOM only on the local `attest` path; no scanner adapter. Out of scope, stated in ADR-26. |
| M4 | medium | `m4-tag-overwrite-not-refused-at-registry-seam` | `delivery.Push` moves a tag silently. Closed by A-9/A-14. |
| M4 | medium | `m4-actions-lock-embedded-requires-rebuild` | `actions.lock.yaml` is embedded; bump requires rebuild. Documented; the bump workflow rebuilds (D-5). |
| M4 | medium | `m4-committed-generation-bypasses-factory-catalog` | Committed bundle generated with `--definition`; no version handle for a release tag. Closed by A-6. |
| M4 | low | `m4-factory-check-lacks-drift-classification` | `--check` reports only "member differs". Post-v1. |
| Cross | medium | `x-pin-resolvers-untested-docker-bound-never-run-live` | Digest resolvers live in `cmd`, shell to docker, have no tests; `bump` never run against real upstreams. Closed by A-15/C-1. |
| Cross | medium | `x-trusted-root-and-identity-policy-not-provisioned` | No trusted root, trust store, or expected identity shipped. Closed by A-16 (late slip). |
| Cross | medium | `x-darwin-cannot-exercise-authority-paths` | All qualified execution and cosign paths are Linux-only; 18 Linux-only tests never run on developer machines. Closed by E-2b/E-8. |
| Cross | medium | `x-workflow-lacks-timeouts-concurrency-and-resource-budget` | No `timeout-minutes`, `concurrency`, or cleanup; unmeasured cost. Closed by E-5. |
| Cross | medium | `x-execution-evidence-not-recorded-and-head-bundles-unexecuted` | The only green run lives in the GitLab API; the bundle on `main` postdates it. Closed by E-3/F-2. |
| Cross | medium | `x-local-developer-path-broken-by-registry-repin` | Laptop path references bare `nkd-toolchain-*@sha256:` with no registry; scripts gate on stale image IDs. Post-v1 (ai-factory). |
| Cross | medium | `x-registry-roundtrip-untested-and-coverage-stale` | No test exercises Push/Pull against a registry; `coverage.out` omits factory and delivery. Closed by A-9. |
| Cross | medium | `x-investment-skewed-toward-milestone-deferred-lanes` | ~13-24k of 55k LOC serve deferred lanes. Closed by E-6 (freeze). |
| Cross | medium | `x-terminology-collisions` | "variant", "recipe-class", "SKU", "site-controller", "qualification", "provenance" collide with repo vocabulary. Closed by the ADR-26 glossary. |
| Cross | medium | `x-two-github-render-stacks` | Two GitHub and two GitLab renderers coexist. Post-v1 consolidation. |
| Cross | low | `x-vendor-image-not-relocated` | `relocateToolchainRegistry` skips fetch-node `vendorImage`. Post-v1 (inert today). |
| Cross | low | `x-snapshot-digest-includes-transport` | Catalog `SnapshotDigest` includes transport locators; mirroring breaks overlay chains. Post-v1. |
| Cross | low | `x-dependabot-conflicts-with-generated-workflow` | Dependabot proposes SHA updates inside the generated workflow. Closed by E-3. |
| Cross | low | `x-plain-http-not-restricted-to-loopback` | `--plain-http` enables HTTP for any host. Closed by A-9. |
| Cross | low | `x-repo-hygiene-and-stale-docs` | Untracked planning wave, binary, decks; stale self-hosted-runner sentences. Closed by E-3. |
| Cross | low | `x-multi-arch-needs-separate-definitions-and-workflows` | A second architecture is a second definition and workflow. Accepted (one variant per class). |

## Appendix B. Glossary (milestone term → repository term)

| Milestone term | Repository term |
|---|---|
| variant (one per recipe class) | one instance / one FactoryDefinition target; **not** `ValidatedVariant` (a non-reproduction divergence) |
| recipe class: OS image | a disk artifact; there is no `os` category recipe today |
| tenant cluster SKU | `ClusterComposition` with `type: tenant` in a signed publication root |
| site-controller cluster SKU | `ClusterComposition` with `type: operator` + `platform-services` |
| AICR bundle | `accelerated-runtime` recipe rendered by the pinned `aicr` tool into the `helm-oci` package (an AICR tree, not a CNCF chart) |
| qualification (M3) | a recipe/evidence-level admission gate producing `nkd.qualification-evidence/v1`; **not** the repo's `qualified` receipt vocabulary and **not** ADR-25's cluster-scoped result |
| SLSA-style production provenance | the new production predicate (builder.id = workflow identity, SLSA Build L2, self-attested); **not** the existing reproduction predicate |
| signed OCI release set | `nkd.release-manifest/v1` + a deterministic immutable tag; **not** an ADR-16 ReleaseSet or clearance |
| release tag | an immutable discovery handle that resolves to a digest; `main` is the only mutable channel tag |
| pinned wrapped NKE reusable build | an NKE build wrapped as a pinned catalog component (repo@sha, treeHash, toolchain@sha256, build command) |
