# Local ARM64 container bootstrap, October 7, 2026

## Result and scope

The component Run `laptop-container-components-arm64-20261007-01` completed
with exit zero, 22 required outputs, 22 observed outputs and no failure. A separate
provider-only bootstrap Run completed with 2/2 outputs. Both used the existing
Apple Silicon laptop's rootless Podman VM and a local NKE Git transport override.

These are unqualified artifact builds, not a clean-machine installation, a
remote-only source-access test, a full source-built distribution, an OS bake,
registry publication, a signed Release or a deployed NKE site/tenant.

The implementation base was `c9041402a56b5a444ecf091f990e574d23e41d74` with
uncommitted bootstrap changes. The bootstrap runner reported `dev`/`unknown`,
not a verified committed-head version. Exact exercised file hashes below identify
the bytes; later documentation and pre-pull admission tests are separate changes.

## Executed commands

From the managed NKE Recipes checkout:

```sh
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh laptop-container-bootstrap-arm64-20261007-02 2 providers
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh laptop-container-components-arm64-20261007-01 2 components
```

The launcher entered the existing Linux ARM64 Podman VM as `core`. A digest-pinned
utility container downloaded the locked Go 1.27.1 ARM64 archive, checked its exact
SHA-256, then compiled the static Linux runner. No host Go or Make was invoked by
this launcher. Producers subsequently used their own catalog-pinned tooling.
Private Git objects came from the local NKE clone; CAPNICo fetched its public source.

The first trial, `laptop-container-bootstrap-arm64-20261007-01`, failed because
the utility image's Go compiler was 1.26.8 while the repository requires 1.27.1.
No NKD Run was created. The corrected launcher downloads and verifies the locked
compiler archive instead of treating the utility image's bundled compiler as the
repository compiler. The failed bootstrap remains retained.

## Terminal record

The completed component lifecycle record summarizes as:

```json
{
  "attempt": "laptop-container-components-arm64-20261007-01",
  "status": "complete",
  "lane": "unqualified-local-execution",
  "expectedCount": 22,
  "observedCount": 22,
  "failure": null
}
```

Counts summarize the record's arrays; they are not additional stored fields.
The Run selected 15 container images, five Helm charts and two executable bundles.
Only the CLI, controller/chart and two providers were source builds; twelve
dependency images, four external charts and operator tools were acquired.

| Identity | Value |
| --- | --- |
| Definition digest | `sha256:94cf46c5ef93fac57779d7d2dfda9baa9d2e7d237f60ebb92485c9e1e11cc612` |
| Environment digest | `sha256:646eaf6e6f7a0c5037a87faa4b23345669dfd68d976e1d6162cfaf9a29e66e4c` |
| Compiled Plan digest | `sha256:62e427bd201a9e00a025b055bc28c2e99acde6f0665b701811b1b85ca4e7f863` |
| Controller native OCI manifest | `sha256:2f84667c62efc9f3a0f94c215a8ad39172c4ddbf04187fdb883baf87ea8b3f53` |
| Packaged bound controller-chart manifest | `sha256:e1fe437ac7996ce8e602f855a97004c6887d00033a55d5ac59a93bc24333191f` |
| CAPNICo native OCI manifest | `sha256:077ff2290eb01ebc109c922b96d1fdc3ab8483f4758e775e74d17f0156c92fca` |
| Provisioned-provider native OCI manifest | `sha256:45b31863a29c78024d3a3ad8c259157147c76c67480f0cfad7fe8b89d014cbbf` |

CLI presentation labels the Plan digest as `run`; it is not an attempt-unique Run
identifier. Blob SHA-256, OCI manifest digest, Build Digest and Plan identity are
different subjects and do not grant qualification or release authority.

## Read-back and smoke checks

`lifecycle.json` independently reported the completed status and counts above.
Both the controller and chart OCI indexes matched their observed digests.
Reading the chart's content layer found `image.tag: ""` and `image.digest` equal
to the controller manifest above. The repository remains the source default;
local packaging did not publish that new image at the default registry.

Executing the produced CLI inside the VM returned:

```text
nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)
```

This proves CLI version startup and chart binding, not reconciliation or tenant
provisioning. No cluster or site credentials were supplied to this trial.

## Exercised file hashes

| File | Exact SHA-256 |
| --- | --- |
| `scripts/nke-full-arm64-local.sh` at execution | `60227f49a4515ce13db80f9bb032cc9f7a664bf9f0ab4597e5f7d23466f5b19e` |
| `definitions/nke-laptop-arm64/definition-components.json` | `9d36b5f6863ecc9ff19817d0148e9b32894f4580b0e44a3ccaf159139cb8afbc` |
| `definitions/nke-laptop-arm64/environment-local.json` | `b360cd39867a90751fb7b87b344533781577c9ee50a950945a5086eb72a23d4d` |
| `toolchains/nkd-packer/toolchain.lock.json` | `0d10becf51e776c33c05be359b29c76d2c48797aaa24acbb10cb778c9627e40f` |
| Bootstrap runner | `85af975776f598085bbe8f322ccd607861e88efbdc1841dfd6f0eb7bbc030c04` |
| Downloaded Go archive | `3450b45a3f9ee8568792736a5c5e70a1f2e9b36c35a8f74958c03e51d7d92bec` |
| Retained `.runner/plan.json` bytes | `a311613264904baf263f9cb121203b392333405c2cbc10757e51ce2fb63022e5` |

Attempts and payloads remain under `.nkd/` on the execution machine, not in Git.
No byte-reproduction claim is made for source-built container outputs.

## OS and deployment limits

A later real `--check os` exited one before downloads, measuring 8/8 CPUs,
14,857/10,240 MiB available memory and 33,846/65,536 MiB workspace. Measurements
are snapshots, not reservations. No admission requirement was lowered, no cache
was pruned and no VM was reset. The fresh Packer bootstrap and native Docker
execution remain contract-tested rather than externally exercised by this Run.

See [the local guide](../local-development.md), [the full-source gap map](../full-nke-source-build.md)
and [the demo handoff](../nkd-to-nke-demo.md) for the remaining acceptance work.
