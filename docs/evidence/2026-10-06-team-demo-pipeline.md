# Team demo: recorded M2 pipeline and release

The user-selected [GitHub Actions run 37533806963](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963)
includes both AMD64 and ARM64 NKE OS images. This record was captured for the team introduction on
2026-10-06. It reports the exact candidate's workflow results and downloaded metadata, rather than
extending the current checkout's implementation claims.

The final snapshot records **all 289 jobs succeeded**, including **Finalize Run and check completeness**,
which completed at 2026-10-06 23:03:17 UTC (4:03:17 PM Pacific). The workflow concluded `success`;
its last-update timestamp is 23:03:19 UTC. Release assembly, publication, signing and
signature/attestation verification succeeded, including both disk outputs.

## Exact candidate

| Item | Recorded value |
|---|---|
| Source commit | `625980e30b1a9f5ef27a9b46534de65eefff385a` |
| Branch / event | `codex/m2-arc-validation` / manual `workflow_dispatch` |
| Started | 2026-10-06 21:25:39 UTC (2:25:39 PM Pacific) |
| Final workflow result | `completed` / `success`; all 289 jobs succeeded |
| Workflow display name | `nkd-run-nke-deploy` |
| Actual Definition | `nke-m2`, `definitions/nke-m2/definition.yaml` |
| Actual Environment | `definitions/nke-m2/environment-github.yaml` |
| Plan lane | `pipeline-attested` |
| Plan | 348 operation nodes, 488 artifact records, 34 named outputs |
| Publication metadata | 34 outputs and 60 evidence objects |

The workflow's display name is historical. Its generated command selects the M2 Definition, rather than
the six-output deploy Definition. The [exact Plan](2026-10-06-team-demo-pipeline/plan.json) and
[Definition](2026-10-06-team-demo-pipeline/definition.yaml) were read from the immutable commit.
The Plan describes intended work; the [job snapshot](2026-10-06-team-demo-pipeline/run-summary.json)
records its observed workflow progress separately.

The downloaded [final Run record](2026-10-06-team-demo-pipeline/run.json), from the `nkd-lifecycle-run`
artifact, records attempt `37533806963-1`, lane `pipeline-attested`, and status `complete`. It contains
34 expected and 34 observed outputs with matching names. Its Definition, Environment, Plan and content
subject digests match the release control document. This is an observed successful workflow finalization;
the demo preparation did not independently replay its consumer policy or complete registry closure.

The 34 outputs comprise 25 OCI container images, five Helm charts, two executable bundles (`nkectl` and
`operator-tools`), and two QCOW2 disk images. The five charts are `controller-chart`, `postgresql`, `spire`,
`spire-crds`, and `temporal`. Exact output names, component subjects, and sibling publication references
are retained in the [release control document](2026-10-06-team-demo-pipeline/release-control.json).

## Immutable release and OS outputs

The publication artifact records this exact release root:

```text
ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases@sha256:292524ba607d5a45326ae4801e81867f5358921824df3e12c59d274360baaf0e
```

The SHA-256 of the downloaded [OCI index bytes](2026-10-06-team-demo-pipeline/release-index.json)
matches that reference. All 191 content-addressed blobs included in the downloaded metadata carrier
matched their filenames. This checked only the small metadata carrier, not the complete published payload
closure or registry availability.

| Disk output | Exact QCOW2 digest | Bytes | Sibling publication digest |
|---|---|---:|---|
| `nke-node-image-amd64` | `sha256:b955979a5b84b1ea63408fe71e04484bb87c24b21f949610f277e9b1bc279d4c` | 3,578,528,256 | `sha256:7f576625391f990d2c2a77cfbabe764911569bd3609db91f2ff9482f28722bd1` |
| `nke-node-image-arm64` | `sha256:a62ed828ab7dbd62e4e7e8beb6270b131bf2683bd1a72140d66a968883fc3bfa` | 3,298,230,784 | `sha256:8243ab95966e0c6acb0b7a007d480376bb1eafeb6798d0d121783463d59d0739` |

The sibling repositories are respectively `ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases-nke-node-image-amd64`
and `ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases-nke-node-image-arm64`.
The control document describes both disks as `qcow2`, unencrypted, successfully produced, with
`evidenceMode: build-provenance-v1`. Do not present these records as full native disk inventory,
qualification, exact-byte reproduction, boot compatibility, or downstream deployment acceptance.
The disk material subjects and build-output evidence are still useful navigation examples.

## What signing evidence establishes here

The downloaded [release verification record](2026-10-06-team-demo-pipeline/release-verification.json),
[AMD64 verification record](2026-10-06-team-demo-pipeline/amd64-verification.json), and
[ARM64 verification record](2026-10-06-team-demo-pipeline/arm64-verification.json) report
`signatureVerified: true`, `attestationVerified: true`, and `dryRun: false` for their exact references.
They record cosign `v3.1.3`, SLSA provenance v1, GitHub Actions' OIDC issuer
`https://token.actions.githubusercontent.com`, and observed certificate identity:

```text
https://github.com/dsx-ai-factory/nke-recipes/.github/workflows/nkd-run.yml@refs/heads/codex/m2-arc-validation
```

These are records produced by the workflow and corroborated by successful verification jobs. This demo
preparation did not independently run cosign, authenticate a consumer policy, or fetch artifact payloads.
The PR #108 metadata browsing replay below is a separate inspection operation. The control document's literal
`qualification: diagnostic` and the Plan's `pipeline-attested` lane are retained without conflating them.
Neither is evidence of the `qualified` lane.

## PR #108 metadata browsing replay

The team demo now uses [PR #108](https://github.com/dsx-ai-factory/nke-recipes/pull/108) at exact head
`00c71385580e798eb3d7f74b88fc694225cfe175`. The rebuilt CLI reports `00c71385 (00c71385)`;
its executable SHA-256 is `5f8bc8f2e41eb527185230b0e173946b459d265fc2929522961a3e318d1f487a`.
This changes browsing to inspect bounded metadata and evidence without opening artifact payload layers.
The earlier [PR validation record](2026-10-06-release-browse-metadata.md) remains separately scoped;
the commands here are fresh checks at the recorded PR head on October 6 Pacific (October 7 UTC).

The original `nkd-metadata-release` carrier was reused and safely materialized as 193 regular files totaling
2,213,704 bytes. All 191 blob names were rechecked against their SHA-256 values. The 34 output manifests
declare 111 unique artifact payload-layer digests; none of those payload files is present in the layout.
The PR-head CLI successfully read this metadata-only layout and produced the
[complete native browse JSON](2026-10-06-team-demo-pipeline/pr108-browse.json): 34 outputs, 60 evidence
members, 96,092 bytes, SHA-256 `ab2e7d4fd77f76b581cac8838b6d4d19bc01b8529c0df7418ab19014d6ef17b2`.
Its label is preserved verbatim:

```text
metadata inspection; payloads, signatures and policy not verified
```

The same binary then read the exact root from GHCR through the existing registry credential keychain.
The live command returned exit 0 in 184.875 seconds, with 96,092 standard-output bytes and empty standard
error. Its JSON was byte-identical to the local metadata-layout result. The private acquisition directory
was removed and its caller-owned workspace was empty afterward.

```sh
bin/nkdctl release browse --reference "$RELEASE_REF" --format json \
  --timeout 10m --workspace "$WORKSPACE"
bin/nkdctl release browse --reference "$RELEASE_REF" --layout "$LAYOUT" --format json
```

Full text, JSON and Mermaid views passed. Both `nke-node-image-amd64` and `nke-node-image-arm64`
passed in all three formats; each filtered JSON retained `totalOutputs: 34`, displayed one output,
and retained its 11 evidence memberships. The unknown selector `not-a-release-output` returned
`NKD-E-USAGE`, exit 2, and no standard output. The [full Mermaid export](2026-10-06-team-demo-pipeline/pr108-browse-full.mmd)
and [focused ARM64 JSON](2026-10-06-team-demo-pipeline/pr108-browse-nke-node-image-arm64.json)
are retained alongside the other views.

The self-guided `docs/demo-team-release.sh` also passed against this metadata-only layout with
`--output nke-node-image-arm64 --mermaid --non-interactive`; its complete JSON was byte-identical to the
native and live results. The [replay summary](2026-10-06-team-demo-pipeline/pr108-replay.json) records
the executable identity, view checks, cleanup result and script identity. This replay did not install
an additional HTTP payload-request guard; the earlier PR validation record separately records that
guarded external check.

The offline explorer embeds that actual native browse result. Its platforms are config-declared;
disk digest/size fields describe the published metadata. Successful browsing grants neither payload
verification nor signature/policy authority. The original raw control document is retained as the initial
downloaded source rather than presented as a native browse result.

## Historical compatibility observation

During the initial preparation on 2026-10-06, checkout
`61ec66fa4ffaf75fee291450f7572d244f2e624f` predated candidate metadata fields. A read-only helper passed
the downloaded control bytes to that checkout's `canonical.UnmarshalExact` with
`delivery.PublicationRoot`; it failed closed with `NKD-E-VERIFY-FAILED`, `unknown field "publication"`.
That result describes the older checkout, not PR #108. The earlier full-payload behavior also does not
describe PR #108: current browsing validates all metadata and evidence memberships before filtering,
while leaving container layers, executable archives and disk payloads unread. Registry credentials and
bounded network time are still needed for live metadata acquisition. Use the separately documented
`release verify` path for authenticated consumption.

The [source manifest](2026-10-06-team-demo-pipeline/sources.json) records exact copied-byte SHA-256 values,
GitHub artifact IDs/URLs, original tar member names, and immutable source links. Carrier files were
read directly without arbitrary archive extraction. No pipeline was dispatched, registry object written,
or complete disk payload downloaded during this investigation.
