# FDE-only branch build

This Definition selects one native AMD64 QCOW2 and its complete eleven-component material closure.
The maintained [M2 pipeline](../nvke/README.md) includes both native FDE outputs with build-provenance
publication; this separate selection retains the smaller build-only diagnostic route.
It reuses the M2 OS catalog and native toolchain pins, selecting the separate `nvke-node-fde-amd64`
producer. Other M2 outputs and the plain disk are absent. Material producers still run because their
bytes are required by the FDE image; this does not introduce cross-Run cache reuse.

The Environment selects Docker/KVM on `arc-amd64-kvm` and admits manual dispatch on
`codex/fde-image-pipeline` as well as the default branch. GitHub's `nvkr-arc` Environment must separately
admit that exact branch. The source-fetch stages use the existing `NKE_SOURCE_TOKEN`; the producer job
receives source bytes without the token. Native toolchain preparation uses the workflow token with
read-only package access and removes its temporary registry configuration before execution.

Publication and signing are omitted. The generated pipeline retains raw QCOW2 bytes, their producer
record and a `static-unqualified` Run through Actions artifacts. It emits no registry publication,
signing or full-disk inspection. A successful build is not TPM enrollment, secure deployment,
production acceptance or byte-reproducibility evidence.

Build-only generation admits one disk output. Finalization uses a fresh job on the same selected ARC
pool, with a matching helper, read-only repository access and a two-hour bound for transfer and observation.
It restores the complete source/material/disk closure. The comparable AMD64 carriers from
[M2 Run 37533806963](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963) totaled
15,362,463,697 bytes (14.31 GiB) compressed, already beyond the generic hosted runner's
[documented 14 GB allocation](https://docs.github.com/en/actions/reference/runners/github-hosted-runners).
That measurement motivates ARC finalization; it does not establish the FDE image's size or sufficient
scratch capacity for a new Run.

## Bootstrap value and other keys

The producer explicitly declares `luks_passphrase: changeit`. This is a documented public bootstrap
value included in Build identity, so it needs no GitHub secret. Runtime admission rejects omitted,
duplicate or different bootstrap values. Do not replace it with a real secret in public Packer variables.
The [pinned-source audit](fde-audit.md) lists the copies in the image and installer, temporary-key cleanup,
SSH client/host key distinctions and firstboot limits. A GitHub Environment secret alone would not
protect a value that the pinned producer writes into the disk and potentially its logs.

The image is for build iteration. Cloning its LUKS payload preserves the volume key; TPM enrollment
does not rotate that key. The public golden image therefore supplies no confidentiality guarantee for
later deployed clones. `swtpm` is unnecessary for baking; testing enrollment requires a separate boot test.

## Inspect the standalone bundle

From the repository root with the Go patch in `.go-version`:

```sh
fde_bundle_root=$(mktemp -d)
GOTOOLCHAIN=local go run ./cmd/nvkr run generate \
  --definition inputs/definitions/diagnostics/fde-amd64.yaml \
  --environment inputs/environments/github-diagnostics.yaml \
  --engine github --root "$fde_bundle_root"
GOTOOLCHAIN=local go run ./cmd/nvkr run generate \
  --definition inputs/definitions/diagnostics/fde-amd64.yaml \
  --environment inputs/environments/github-diagnostics.yaml \
  --engine github --root "$fde_bundle_root" --check
```

This writes a scratch workflow, Plan and manifest for the standalone selection. It does not authorize
registry publication. The maintained repository bundle and tests select the combined M2 pipeline;
`make workflows-check` verifies that committed selection. Do not hand-edit rendered files or replace
the maintained bundle with this alternate selection in a PR to main.

The diagnostic branch contains its standalone bundle. Dispatch requires its
exact branch to be admitted by the selected GitHub Environment:

```sh
gh workflow run nvkr-run.yml --repo dsx-ai-factory/nvkr --ref codex/fde-image-pipeline
```

On October 13, 2026, [Run 38443178035](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035)
completed this generated feature-branch workflow at commit `68c219845398b8ef5890e5c76a13d4901a22630d`.
It retained an [image artifact containing the 6.28 GiB QCOW2](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11840209683)
and a [complete, static-unqualified Run record](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11841676055).
The [reconciliation record](../../../docs/adr/reconciliation.md#native-arc-disk-route) binds the exact raw
QCOW2 digest and size, Plan and Build digests, producer result and terminal observation. The native build
step took 29m 48s; uploads and finalization added time. This validates the pinned FDE-target bake and
artifact transport, with the security and inspection limits described above.
