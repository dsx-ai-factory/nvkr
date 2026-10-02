# NKE OS build validation on ARC

**Date:** 2026-10-01 (UTC)
**Lane:** `unqualified-local-execution`, invoked by the repository's diagnostic workflow

## Inputs and scope

The trusted workflow is `.github/workflows/nke-os-validation.yaml` at main commit
`4799c7cfece081bd817c842bf56c5d2b708c216a`. It selects the exact candidate through the
PR #59 copy-pr-bot mirror and transfers the pinned NKE source from a separate fetch job.
These runs use NKE commit `726f76944404e1106626b8333d3576bdf67b575b`, tree
`3d6ea77f85d5da2648038425f805a598e23464ba`, and request both architectures in full mode.

This is diagnostic execution on `arc-arm64-kvm` and `arc-amd64-kvm`, not the generated
hosted Run adapter, publication, qualified reproduction, or release acceptance.

## Completed observations

[Run 36807594216](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36807594216)
executed candidate `269509ac63f9e0797cf68d41e89a5f7ddbb4818f` on both architecture runners.
The source handoff, native runner preflight, Docker toolchain export/loading, component
builds, patched installer ISO, and aggregate image-input archive completed. Both jobs
then failed before Packer started:

```text
[NKD-E-ENGINE-CAPABILITY] revalidating admitted KVM capability: invalid argument
```

Initial KVM admission uses the return-value form of `KVM_GET_API_VERSION`. The second
check incorrectly used `unix.IoctlGetInt`, whose pointer argument is incompatible with
that operation. Commit `253834305ec5726bdf1cd9cc2cb6a67bc4e7cf8c` corrects the recheck to
`unix.IoctlRetInt`, preserves the device and API-version checks, and separates Linux
validation from the unsupported-platform implementation. The local completion gate
passed; Linux AMD64 and ARM64 pipeline test binaries compiled. Compilation does not
establish real KVM interoperability.

The failed run uploaded diagnostics for
[ARM64](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36807594216/artifacts/11138640393)
and [AMD64](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36807594216/artifacts/11138770508).
These are expiring Actions artifacts, not published image artifacts.

## KVM recheck and volume setup

[Run 36809040484](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36809040484)
executed candidate `253834305ec5726bdf1cd9cc2cb6a67bc4e7cf8c`. Both architectures passed
KVM revalidation, prepared their input transport, then failed in the separate ownership
bootstrap with `chown: cannot access '/nkd/work/build-logs': Permission denied` and the
same error for the other work directories. No Packer or QEMU execution is established.
The candidate's [Linux Merge Gate](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36808975090)
passed, including the race-test gate; this did not catch the runtime permissions failure.

The next change removes that redundant bootstrap and assigns both private volumes to
the host UID/GID in the existing archive-verification/extraction container. That stage
already has `CHOWN` and `DAC_OVERRIDE`; Packer consumers gain no capabilities. The logs
also exposed Docker's `/dev/null/config.json` warning. The scrubbed Linux client now uses
`/proc/self/fd`, whose kernel-owned numeric entries cannot supply `config.json`, contexts
or CLI plugins. This preserves absence of ambient configuration without an `ENOTDIR`
warning contaminating Packer's strict version/validation output checks.

## Packer validation and namespace boundary

[Run 36810552034](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36810552034)
executed candidate `275068b49b0db97b1412ab9c1799f0e6b6780b3a`. Both architectures completed volume preparation,
the exact Packer version check, generated-key validation and `packer validate`, which printed
`The configuration is valid.` Docker then failed to start the build container because its daemon could not
resolve the client's `/proc/359/fd/14` device path. No VM started on either job.

The next adapter change projects the admitted character-device numbers through Docker's fixed `/dev/kvm`
mapping. A guard compares the host's kernel boot ID and device numbers inside the build container before Packer
runs. The held-device/API admission check remains. This follows the OCI device model, which represents device
type, major and minor numbers rather than transferring a client file descriptor. See the
[OCI Linux device contract](https://github.com/opencontainers/runtime-spec/blob/main/config-linux.md#devices)
and [kernel boot ID documentation](https://docs.kernel.org/admin-guide/sysctl/kernel.html#random).
The subsequent run below exercised this guarded handoff on both ARC architectures.

## Successful ARM64 and AMD64 images and boot smoke

[Run 36812260448](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36812260448)
executed NKD candidate `bb6e870bb2477776a007acb3d35e6cbaaadeb16b`, tree
`ebbc67c3a7d63d45ea714634ade6a95984c3713e`, with the exact workflow and NKE inputs above.
[ARM64 job 110209980925](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36812260448/job/110209980925)
completed successfully on `arc-arm64-kvm-84grx-runner-bzhvk`.
[AMD64 job 110209980960](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36812260448/job/110209980960)
completed successfully on `arc-amd64-kvm-lfdl7-runner-g22qw`. The full workflow concluded `success`.

Each job built one non-FDE QCOW2 through the NKD lifecycle, independently measured its
digest and size, passed NKE's source-owned boot smoke, and uploaded both diagnostics and
the image. Each retained lifecycle record has `status: complete`, lane
`unqualified-local-execution`, and exactly one observed `disk-image` output with
`format: qcow2`, `encryption: none`, and `outcome: succeeded`.

| Recorded field | ARM64 | AMD64 |
|---|---|---|
| Output | `nke-node-image-arm64` | `nke-node-image-amd64` |
| QCOW2 SHA-256 | `e1dd5d3ba0dcd06fd66b790b81deca2cfa81f6d53bdabc884449011ce1f3cfff` | `a8bc3ddc29236131aefc2f51dc3dcf898af2db2a6c8e39a7b064fa3e8cbc61cb` |
| QCOW2 size, bytes | `3300327424` | `3528363520` |
| Plan digest | `sha256:a71ae57748a2b102e1d9cd58b6ae2f6bdeb9c52780e6ea8046ef2a6c24e87fff` | `sha256:73ba5c70944e70d33fa668cda5b96833d4b04a0d454cfa2795d9028ad04148f5` |
| Observed toolchain image ID | `sha256:dcc2d031e4e074fa2079e87ba34e338b95eb7548f0a932528af765a406ad7fa2` | `sha256:27cf0f4524e993860a196b4eacdcc60b6d777e63c3fb0907e0bf452f0f5602b8` |

For each architecture, the lifecycle observation and workflow `evidence/metadata.json`
agree on the disk digest and byte count. Both boot logs report successful cloud-init, the NKE image contract, and
root-filesystem growth. This is a boot smoke test, not a Kubernetes deployment or runtime
qualification test.

- [ARM64 QCOW2 artifact](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36812260448/artifacts/11140737361)
- [ARM64 evidence artifact](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36812260448/artifacts/11140163996)
- [AMD64 QCOW2 artifact](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36812260448/artifacts/11140829417)
- [AMD64 evidence artifact](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36812260448/artifacts/11141240359)
- [Successful Linux Merge Gate for the tested candidate](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36812207472)

Actions artifacts expire. Their ZIP download size is not the QCOW2 byte count above.
The executable evidence applies to the exact candidate above, not automatically to a
later commit or a differently pinned NKE source.

## Review-fix runtime validation

[Run 36878232719](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36878232719)
completed successfully on both architectures for runtime commit
`1f989e9dfed343258ae063867fb156f90cbcb9a4`, tree `05b20937a9ba856f14733938e1e5fc69b017f75c`.
It used the same trusted main workflow and NKE source pin stated above.
ARM64 ran on `arc-arm64-kvm-84grx-runner-hl8c6`; AMD64 ran on
`arc-amd64-kvm-lfdl7-runner-psjbr`. Both jobs built one non-FDE disk, completed
source-owned boot smoke and uploaded the QCOW2 and diagnostic evidence.

| Recorded field | ARM64 | AMD64 |
|---|---|---|
| QCOW2 SHA-256 | `99c2358ef5d917135574616392aeec2a58370cf0714280317d251ad3efde2969` | `b7323b76e3227264cee84d8b5009625f21c7d559fd1b35d77e032c1be68e7509` |
| QCOW2 size, bytes | `3331064320` | `3607953920` |
| Plan digest | `sha256:4a1d663c3ab21cc96e0d21c8a8ec77b6cc64dd1e55d10aed43c9c2c8ed0e92a9` | `sha256:86622cbb6ad9ba35f226c2c4df3b68f72053b99a986e71857ae32a8cf5cf9125` |
| Observed toolchain image ID | `sha256:980c1cdb69f7804743c07a3fbe81fe19b0b9d63034e016e66e70689f5f76c28e` | `sha256:888a6fb6748841ed29ed9a1e5cd1271771e7a22402a3e3e85adcb83d2945bfa4` |

Downloaded evidence confirms that each Run is `complete` in `unqualified-local-execution`,
with exactly one successful unencrypted QCOW2 observation. Its digest and size agree
with the workflow metadata, and the recorded toolchain ID agrees with the executable Plan.
Both boot logs confirm cloud-init completion, the image contract and root-filesystem growth.

- [ARM64 QCOW2](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36878232719/artifacts/11171457907)
- [AMD64 QCOW2](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36878232719/artifacts/11172165645)
- [ARM64 evidence](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36878232719/artifacts/11171612009)
- [AMD64 evidence](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36878232719/artifacts/11171229214)

The runtime commit's first Linux test gate failed because a test helper used a direct
type assertion on errors wrapped by `errors.Join`. Test-only commit `fa8cff9` changes
that helper to `errors.As`, matching the public exit-code handler, without weakening
the primary-code, cause or cleanup assertions. Its
[Linux Merge Gate 36880164063](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36880164063)
passed, including race tests, binary build and vulnerability scanning.

Later documentation, test and workflow-transport changes do not alter this runtime code.
The main-owned workflow edits in this PR, including Plan-derived boot-image selection,
disabled trusted bootstrap caching and narrowed evidence upload paths, were not executed
by this pre-merge run. They require validation after landing in main. The original main
upload glob also included source-tree directories named `diagnostics`; the proposed
transport correction restricts uploads to the selected attempt's own records and Packer logs.
Repository-scoped source-credential policy, publication and qualification remain unresolved.

## No-chmod runner-access probe

[Run 36929803967](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36929803967) completed successfully
using the branch-owned workflow at exact commit `336e6d7fc95de297004ea656dcae38d6e25ef8ec`, after explicit
authorization of this secret-free preflight. It executed the same YAML-anchored admission script used by
full validation, without fetching NKE, running a toolchain/image build or changing device permissions.

| Architecture | Job | Runner | Observed device ownership/mode | Result |
|---|---|---|---|---|
| ARM64 | [110596094638](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36929803967/job/110596094638) | `arc-arm64-kvm-84grx-runner-8jbdf` | `root:UNKNOWN 666` | Read/write check and open succeeded |
| AMD64 | [110596094679](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36929803967/job/110596094679) | `arc-amd64-kvm-lfdl7-runner-6jhnn` | `root:UNKNOWN 666` | Read/write check and open succeeded |

Both jobs ran as UID/GID 1001, with supplemental groups 27 (`sudo`), 100 (`users`) and 123 (`docker`).
The source-fetch, toolchain and full-build jobs were skipped. This proves existing access without `chmod`
on these observed runners. Mode 0666 permits access without KVM group membership; the logs do not establish
whether it originated in scale-set configuration or an earlier host-device change. No durable permission
baseline for newly provisioned hosts, new QCOW2 or boot test is established by this probe.

## Automatic post-merge validation

[Run 36933187521](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36933187521) was triggered by a
push at `156d32c51b43c2ae8ecbfc02da117ea9ab7e07a2`. Both full architecture jobs completed successfully,
including lifecycle build, QCOW2 identity recording, source-owned boot smoke and image upload.

[Run 36939569803](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36939569803) was triggered by a
push at `53ba88b39b049afc0928a7303e5a7c4a4eee89d5`. Its
[AMD64 job](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36939569803/job/110628080228) completed
those same steps successfully. Its
[ARM64 job](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36939569803/job/110628080327) passed
native KVM admission and the dispatcher regression, then failed while vendoring the agent's Go modules:

```text
google.golang.org/protobuf@v1.36.12: ... stream error: stream ID 31; INTERNAL_ERROR; received from peer
```

The lifecycle returned `NKD-E-FETCH-FAILED`, exit code 20, before Packer or QEMU started. Toolchain recording
then failed because `.nkd/evidence/attempt-arm64` had not been written. Diagnostic upload succeeded; boot
smoke and image upload were skipped. This is acquisition failure evidence, not a VM-build failure.

These observations were rechecked through GitHub's run/job records and the failed ARM64 log. They establish
automatic triggering at those exact heads, not successful execution of subsequent reliability changes.

## Reliability candidate regression

[Push run 36944892985](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36944892985) completed
successfully on both full architecture legs at main commit
`91567c2717dd195829d6a88a19a6d6e4baec3dd9`. This is successful base-code execution, not validation of
the later reliability candidate.

[Dispatch run 36958996866](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/36958996866) executed
candidate `db4b8d92d2da31e9be18064a9ab4681277f8550f` through the trusted workflow at that main commit.
It used the NKE pin recorded above. Both full legs failed in the new module-file Git guard after download:
the rootful vendor container could not use the differently owned checkout, so `git diff` emitted its
`--no-index` usage and exited 129. The lifecycle returned `NKD-E-FETCH-FAILED` before Packer/QEMU.
The main-owned toolchain-recording step also failed on the absent attempt file. Neither leg produced a QCOW2.

The corrected command compares exact module-file checksums instead of invoking Git. Local fault tests cover
metadata/ZIP transport and guard behavior. The corrected candidate's full bake is recorded below. Candidate
dispatch cannot exercise the reordered attempt recording and outcome guards in candidate workflow YAML;
their acceptance still requires the first post-merge main push and a clean rerun.

## Corrected reliability candidate, 2026-10-02

[Run 37043068395](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37043068395) completed successfully
with both full architecture jobs. It executed NKD candidate
`d1e28cec8d66a4aa1def782bedf88392a73cc4b2`, tree `0c0984cee6cfc78384e6405d45459e4bd08fa619`, through the
trusted main workflow at `91567c2717dd195829d6a88a19a6d6e4baec3dd9`, using the exact NKE commit and tree
recorded above. [ARM64 job 110958231126](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37043068395/job/110958231126)
and [AMD64 job 110958231246](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37043068395/job/110958231246)
passed acquisition, the lifecycle QCOW2 build, identity recording, source-owned boot smoke and image upload.

| Recorded field | ARM64 | AMD64 |
|---|---|---|
| QCOW2 SHA-256 | `d31c8c53ccac6cc4b4a0593a45b52e94660a47e9cd7e1272323cfbbfef5bac14` | `1b8dd35744c328bc0d9dcc5ee281cc6491601fb51ea8d3bd43880a4f3de1d9b6` |
| QCOW2 size, bytes | `3350724608` | `3622699008` |
| Plan digest | `sha256:da34e35b1dbe41f47f0655806a5e97790e5b9ba44e1e095ffba3ca6bd717f34b` | `sha256:0570e594c997aeba57ee5ec4b7b22ad1ab7d695491d3fcafe1e454eedfafbb02` |
| Observed toolchain image ID | `sha256:2dd4c0502dde289d10842f354cd9e860d0d468d2823a0bf80987c3af6483123c` | `sha256:782a1e6444618f86b62180b097c3f3f503eede2e8cffc201b94d9b859539e81d` |

Downloaded evidence binds the candidate, trusted workflow and NKE identities separately. Each retained lifecycle
record is `complete` in `unqualified-local-execution`, with exactly one successful, unencrypted QCOW2 observation.
Its digest and size agree with the workflow metadata. Both boot logs confirm cloud-init completion, the NKE
image contract and root-filesystem growth. The workflow labels these artifacts `unqualified-diagnostic`.

- [ARM64 QCOW2](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37043068395/artifacts/11244063827), [ARM64 evidence](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37043068395/artifacts/11244300302)
- [AMD64 QCOW2](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37043068395/artifacts/11245430043), [AMD64 evidence](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37043068395/artifacts/11244441294)

This proves the corrected candidate's native Docker/KVM bake and boot path. It does not exercise its changed
workflow YAML, a later documentation-only commit, generated hosted execution, publication or qualification.
The post-merge workflow validation remains pending. Actions artifacts expire; their compressed transport sizes
are not the QCOW2 byte counts above.

## Remaining proof

FDE, full-disk SBOM, publication, qualified execution, and the generated hosted VM adapter remain outside
this record. The diagnostic workflow invokes the local lifecycle on ARC; running on
GitHub does not upgrade its evidence lane to `pipeline-attested`.
