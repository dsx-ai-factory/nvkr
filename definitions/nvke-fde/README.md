# FDE-only branch build

This Definition selects one native AMD64 QCOW2 and its complete eleven-component material closure.
It reuses the M2 OS catalog and native toolchain pins, selecting the separate `nvke-node-fde-amd64`
producer. Other M2 outputs and the plain disk are absent. Material producers still run because their
bytes are required by the FDE image; this does not introduce cross-Run cache reuse.

The Environment selects Docker/KVM on `arc-amd64-kvm` and admits manual dispatch on
`codex/fde-image-pipeline` as well as the default branch. GitHub's `nkd-arc` Environment must separately
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

## Generate and dispatch

From the repository root with the Go patch in `.go-version`:

```sh
GOTOOLCHAIN=local go run ./cmd/nvkr run generate \
  --definition definitions/nvke-fde/definition.yaml \
  --environment definitions/nvke-fde/environment-github.yaml \
  --engine github --root .
make workflows-check
```

Generation deliberately replaces the branch's managed workflow, Plan and manifest. It does not modify
the M2 Definition or authorize registry publication. The drift gate repeats the selected paths in the
committed Plan, so it checks the FDE bundle on this branch and M2 when that selection is restored.
Do not hand-edit the rendered workflow or Plan.

After committing and pushing the reviewed branch and admitting it in `nkd-arc`:

```sh
gh workflow run nvkr-run.yml --repo dsx-ai-factory/nvkr --ref codex/fde-image-pipeline
```

The provider Run, exact head and retained disk digest must be recorded before claiming this workflow
has built an image. Source inspection and contract tests alone establish no live FDE bake or boot result.
