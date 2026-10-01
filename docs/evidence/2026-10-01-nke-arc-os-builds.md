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
The guard must be exercised on ARC before claiming cross-namespace KVM handoff works.

## Remaining proof

No successful QCOW2 production or boot smoke is established by the completed run above.
The KVM handoff from the NKD process through Docker into QEMU, Packer execution, exact
QCOW2 collection, and boot smoke still require a successful full run. FDE, full-disk
SBOM, publication and qualified execution remain outside this record.
