## Support by owner

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 26 Definition composition (proposed) | Implemented, contract-tested: U1 composite schema with strict decoding, including optional declared variants (sorted unique components, each naming at least two sorted listed members that each select the component in one of their outputs), the `nvkr.composite-definition` family binding those variants and excluding locators, at most 16 members, bounded no-follow member reads beneath the admitted input root, plain-kind, name and exact `nvkr.definition` digest admission in §2 order with coded errors and no member read after cancellation; U2 pure, order-independent effective-Definition merge with exact disjoint output union, explicit per-output catalog stacks rebased to the composite directory and confined to the input root, identical top-level stacks and tool selectors, preserved Helm bindings and per-output member attribution; U3 composite compilation through the ordinary compiler with each catalog stack loaded once, the cross-member effective-producer check at compilation and saved-Plan validation, which admits divergence only between members a declared variant lists for that component, `nvkr.dev/run-plan/v6` Plans carrying a validated composition record (composite `definitionDigest`, members, declared variants, effective digest, complete attribution) while plain Plans keep v5 bytes, and capture/replay of member files. Identity invariance over the four NVKE families against the family and combined Plans is an opt-in extended contract using the combined Plan's resolved catalogs, not current pin-set access. Composite runtime Plans require the explicit multi-repository `githubRuntimeSources` policy, a captured source-selection/input snapshot and exact replay; runtime Plans without this policy retain their `engine-capability` refusal; U4 `--definition` dispatch by document kind for `definition validate/inspect` (a descriptive composite view, including declared variants) and `run plan/graph/diff/generate`, with composites confined to the working directory as the admitted input root; plain-only loaders and local qualification replay refuse composites with `engine-capability` before catalog work or attempt output; `definition pin [--check]` owned by `pkg/nvkr/definition`, reporting drift as `unverified-ref` without writing and rewriting only stale digests through an admitted byte-CAS commit that loses to a concurrent writer with `write-conflict`; the SDK reaches the same owner; U5 named composite production bundles that regenerate byte-exactly, flat and partitioned, fail generation with `unverified-ref` after an unpinned member edit, and partition by dependency depth and capacity (an opt-in extended contract shows the NVKE composite's worker partitions equal those of its effective Definition compiled plainly); named composite development bundles under `development-<name>` with their own `nvkr-dev-<name>.yml` workflow and `nvkr-dev-project-<name>` workflow name and concurrency group, manifest, Plan and signer identity that coexist with the fixed development layout, while plain Definitions keep refusing `githubDevelopment` with `githubBundle` before catalog work, a composite may also generate the fixed development layout with the fixed workflow, namespace and signer and its composition record in the Plan, and unsigned named development is refused; maintenance admits a composite's rebased catalogs and refuses any committed composite bundle its policy omits. Scoped development cleanup does not yet cover named development namespaces and refuses their runs; U6 `nvkr.dev/run/v5` Run records and `nvkr.dev/publication-root/v5` roots that carry the composition record, validated as complete and consistent with the frozen outputs and the composite `definitionDigest` (unlisted, missing or extra members, tampered effective digests and mismatched majors fail with `verify-failed`), bound into every factory check that compares publication metadata with its Plan, and exported with per-output member attribution in the release description; each `per-output-v1` output-local root carries the complete Plan record with its one output and assembly restores exact Plan membership, while Release issuance, authenticity and inspection refuse a composite root that does not carry every composed output with `release-incomplete`; plain Runs and roots keep v4 bytes, OCI media types are unchanged, and composite `run local` prepares a static-unqualified Run through bounded capture; U7 consumer expectations, including `release verify --definition/--environment/--plan`, which dispatches by kind and requires the authored digest to name the Plan's Definition: a composite exact-input expectation matches only its composite Release, while member, effective-as-plain and composite expectations across forms, and structurally valid but different attribution, fail with `verify-failed`; a verified Release exposes its authenticated composition record and can require an exact member `(name, digest)` set; a member-filtered view is a copied description that grants no capability, and unissued capabilities built from descriptions are refused with `gate-unmet`. U8 hosted qualification carries the complete composition and every output through handoff, artifact checks, native smoke, evidence and release manifest issuance. GPU-site production and development pairs use independent policies; per-run maintenance selection preserves their isolated ownership. Explicit bundle-archive rules require strict artifact validation without a deployment claim. All units are implemented and contract-tested; the hosted publication below records one historical 83-output candidate | Local composite qualification rehearsal and member Release reuse (U9, after the result-reuse decision lands) are deferred and must fail with `engine-capability`; composites rebuild every output. The hosted publication below records one exact candidate; current-candidate execution, hosted qualification and independent consumer verification remain unrecorded. Retiring the combined `definition.yaml` (U10) is gated on one hosted composite Run that completes every member output under a single Release root, followed by independent consumer verification under explicit policy, then on one-to-one per-output equivalence with no output count: each combined output present as a distinct composite output, by name or declared rename (`nico-reference-profiles-fixture` as `controller-profiles`), with an equivalent selection and an equal Build Digest, and each added family-only output listed and attributed to its member in the Release's composition record |

## Hosted GPU-site publication

[Run 42131864402, attempt 2](https://github.com/dsx-ai-factory/nvkr/actions/runs/42131864402/attempts/2)
completed on `main` at `ee79958425989861d93845d8b9fdef0d78ed33db` on November 6, 2026.
The November 14, 2026 acceptance decision records this Run as the hosted family Release proof
for [#342](https://github.com/dsx-ai-factory/nvkr/issues/342). That proof does not require a fresh
Run. It covers the exact 83-output candidate and immutable root below; independent consumer
verification remains the next step under [#352](https://github.com/dsx-ai-factory/nvkr/issues/352).
The Run used the existing partitioned `nvkr-run-nvke-gpu-site.yml` workflow and the six-member
[composite Definition at that commit](https://github.com/dsx-ai-factory/nvkr/blob/ee79958425989861d93845d8b9fdef0d78ed33db/inputs/definitions/nvke/gpu-site.composite.yaml).
The [publication job](https://github.com/dsx-ai-factory/nvkr/actions/runs/42131864402/job/127680628164)
emitted this immutable root:

```text
ghcr.io/dsx-ai-factory/nvkr/m2-candidate/gpu-site/releases@sha256:4a67d9a08fe1775982a76a0994790ecd5d304285ce06bd0d42051787fef9deb3
```

The [signing job](https://github.com/dsx-ai-factory/nvkr/actions/runs/42131864402/job/127681717617)
and [publication-verification job](https://github.com/dsx-ai-factory/nvkr/actions/runs/42131864402/job/127683920746)
report `mode cosign, dryRun false` for that same root. The
[finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/42131864402/job/127688103083)
reports `hosted Run 42131864402-2: complete (83 observed outputs)`.

On November 14, 2026, a read-only registry metadata retrieval found the root's
`nvkr.dev/publication-root/v5` configuration blob at
`sha256:c86b323cbf55aeea0441d85d4d8f1b5f093780be1dbdda4e92ec0efc78c669b4` (263,855 bytes).
Its composition equals the
[producing Plan's composition](https://github.com/dsx-ai-factory/nvkr/blob/ee79958425989861d93845d8b9fdef0d78ed33db/.github/nvkr-run-nvke-gpu-site/plan.json),
and its 83 named publication outputs have this membership:

| Member | Outputs |
|---|---:|
| Core | 21 |
| Services | 34 |
| Telemetry | 17 |
| Ubuntu 26.04 disks | 4 |
| Ubuntu 24.04 disks | 4 |
| GB200 AICR bundles | 3 |

The root binds Definition `sha256:fcf147a347ee9b6559d98a20d9a09d5208e3d33b9167961bd9b281319b6944ba`,
Environment `sha256:3bc43a6761c6a592496a8162859c4a6ded3d189f51421033d11cfa148e2dda94`,
Plan `sha256:8ccb0763bd35b3a0a9ee888513e0ae27bc55d3027b9a0f0701a4c529e4d2da32`, and
aggregate invocation `https://github.com/dsx-ai-factory/nvkr/actions/runs/42131864402/attempts/2`.
Attempt 1 prepared all 83 outputs and signed 75 before it failed. Attempt 2 reran the failed jobs:
it signed the remaining 8, published the root, verified it and finalized the Run.
The Actions artifact listing is now empty; the job logs and registry metadata remain available.

This records hosted `pipeline-attested` publication (the Plan's lane) for the exact historical candidate. The root's
qualification remains `diagnostic`, and disks use `build-provenance-v1`. Metadata retrieval did not
verify signatures or download payloads. Independent consumer verification, published-disk guest boots,
GPU-site installation and qualification remain unproved. NVKE's parser and deployer have not been
exercised with the 83-output composite export in this record, including its services charts.

Compared with the Plan at `72309337216e0be486c1d08e2c6acb2d9766bd2d`, the Definition digest,
top-level catalog digest and complete composition record are identical, including member digests,
83 outputs and eight declared variants. The Environment digest changed from the value above to
`sha256:d526d352c21ea65bc4027acfd814708ac9ff1ae363ded2b0e92aca22f8b345dc`, covering changes to
toolchain access roots, inspector pins and source access. Build Digests match for 58 of 83 outputs;
the 25 changed outputs are all 17 telemetry outputs and all eight disks, four in each disk member.
This record does not establish execution of that later candidate or authorize the combined
Definition's retirement.

On November 14, 2026, the current registry metadata reader completed against this private root using
existing Docker credentials: `release browse --format json --no-cache`, with a three-minute timeout.
It read all 271 metadata objects and returned 83 outputs and 188 evidence records. This checked the
registry metadata closure with the reader at `72309337216e0be486c1d08e2c6acb2d9766bd2d`; it did not
verify payloads, signatures or consumer policy. The literal `release inspect` command was not run:
it requires a complete local OCI layout, which was not downloaded. Registry inspection uses
`release browse`; the containerized consumer script requires read credentials through `--registry-config`.
