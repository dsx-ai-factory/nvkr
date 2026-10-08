# Completed Colima site and installer-tool Runs

## Scope

October 8, 2026, committed PR #118 implementation
`ae535278b8afe70e07a69ddb51c86087773ac34f`, exported without dirty files or old
attempts to native ext4 in the dedicated `nkd-demo` Colima Docker VM. The OS bake
had finished before these one-job builds started. Docker was native Linux ARM64;
the laptop had eight CPUs and 24 GiB configured VM memory.

These are two independently completed diagnostic local Runs, not a combined
Definition, signed Release, qualified reproduction, site deployment or tenant.
NKE site sources match installer candidate
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345`, not an approved release revision.
The local Git transport override selects pinned committed objects, not dirty
files from the NKE checkout.

## Source-aligned site artifacts

```sh
CONTAINER_RUNTIME=docker NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh laptop-colima-site-arm64-20261008-01 1 site
```

The actual invocation ran inside the native export using `colima ssh`. The
launcher exited 0; the record is `complete`, lane
`unqualified-local-execution`, four required/observed outputs, no failure.
Plan: `sha256:d498118a496833f98cfbc8c86641b5c387ffb9b365975ba96f9b45fba1116a29`.

| Output | Kind | Exact observed manifest SHA-256 |
| --- | --- | --- |
| controller-chart | helm-oci | `924b2d52b3a5717afdeb46a8341ac537f831b3250e4090b751676ffaba6d7ec6` |
| cwe | oci | `ba55320fdf6efd39be43c15eedc8e60126914bd50f9eb758b984ae84bf5eb699` |
| nkectl | executable | `b43c91a322cd9cdb1e27e19b29d411e51f592f4ab60fb2c388272c0ff76469b8` |
| nktp | helm-oci | `6733f335915ea6d0b2348e4b4a76e3d98d15dfd2786b67ccacb796979ccd2cf5` |

CLI's source-owned Make build executed its version probe, identifying exact
commit `8fb2d533`. Controller used its source-owned Dockerfile; the resulting
archive loaded into Docker as the observed manifest identity above, reported
`linux/arm64` and passed a bounded, read-only, capability-dropped, networkless
`/usr/local/bin/controller --help` probe. This is startup, not reconciliation.
The first import inspection incorrectly expected Docker's image ID to be the
OCI config digest; Docker 29's actual load/inspect readback used the manifest
identity. No artifact bytes were changed to resolve that diagnostic mismatch.

The controller chart's `image.digest` equals the produced controller manifest.
NKTP's three source-locked dependencies were prepared before network-disabled
packaging; `Chart.yaml`/`Chart.lock` checksums stayed unchanged. Dependency charts
and source Dockerfile inputs are not independently source-built or fully
upstream-signature-verified by this result. The build wrapper uses Helm 4.3.0.

Both completed-Run chart layouts were copied unchanged to a private Mac probe,
then admitted by the new NKD chart-export owner alongside the four charts from
completed component Run `laptop-container-components-arm64-20261007-01`.

| Export | Exact archive SHA-256 | Bytes |
| --- | --- | ---: |
| controller.tgz | `e7d71cc719a9baf04274971116319b27c9f88d518f983bf56efb982a3d6a2de1` | 38,107 |
| nktp.tgz | `1788b65077c2247583864e57adb53f4aa4c3cc53a09764c6b16b1952c8e7cdb9` | 336,466 |

The four external archive identities match the earlier
[chart handoff evidence](2026-10-08-local-chart-handoff.md). Actual pinned NKE
rendering passed with all six completed-Run charts, six Helm template calls and
28 unique image references. Its executor dry-run again selected all six archive
paths. No Helm dependency/repository/pull, Make or source packaging operation
ran during this handoff/render. The pinned installer checkout remained clean.
The private diagnostic values, temporary CA, unreachable image references and
absence of a release-manifest override remain the same explicit limitations as
that earlier probe. This is not registry readback or deployment.

## Source-built installer tools

```sh
CONTAINER_RUNTIME=docker \
  bash scripts/nke-full-arm64-local.sh laptop-colima-tools-arm64-20261008-01 1 tools
```

The launcher exited 0; record `complete`, lane `unqualified-local-execution`,
one required/observed `installer-tools-arm64` bundle containing all five source
components, no failure. Plan:
`sha256:8a6a9fc0a73f4a5bdd5355d28d928c31174922ba254c2c191bc8a051f3d5457b`.
Observed bundle manifest:
`sha256:fb5d5575c7a7953937c6bfdef1fa2d2322fb6a1f1296265bfee7d12ec895dc19`.

Sources, linker fields and local Helm fixture preparation match the
[earlier contract/pin evidence](2026-10-08-local-installer-tools-plan.md).
Each compilation ran with networking disabled. Existing delivery commands
verified the bundle through a new diagnostic publication root and extracted
all five exact files, without overwriting destinations or installing globally.
Independent `sha256sum` matched the owner's extraction results.

| Tool | Native version readback | Exact executable SHA-256 | Bytes |
| --- | --- | --- | ---: |
| clusterctl | v1.12.3 | `3446f2d1477250329d9b71d933d8b14c37b060e2066e3f4a6bc0c280bdf20f3d` | 46,989,436 |
| Helm | v4.2.4+g3900f43 | `ce6aae57d055869a62878f2c379ccb75185a339a8256eb5d8175ace9a04153fd` | 59,244,668 |
| Kind | v0.32.0 | `92250af198263dfb999db31e591c9ea3c20f11b4ac65c2bd1c17fe734186431a` | 10,158,204 |
| kubectl | v1.36.0 | `ad8292fa4dd3021de5394443d742ac5f770d22544dddf58fd8fbf2bd7f0c1e01` | 55,312,508 |
| ORAS | 1.3.3 | `02febf041616de0da344db546fd6acaa1d1c27f8daf415effdb558a8e1e159d0` | 11,599,996 |

All five native version commands exited 0 inside Colima. They are Linux ARM64
executables, not macOS binaries. No API, cluster or registry operation was used
as a version test. These match the selected installer's tool pins; they have not
yet been integrated into the complete deploy-only journey. Toolchain/tool/transitive
organizational approval is not established by build or version success.

## Remaining acceptance

The separate OS Run's accepted QCOW2 is recorded in
[disk evidence](2026-10-08-colima-local-os-build.md). Do not combine these separate
records into a fabricated aggregate success. Still required: complete selected
membership/source exceptions, exact registry transport, corrected plain Ubuntu
26.04.1 profiles, real NICoSim VM services, source-aligned staged install,
tenant/workload acceptance and owned cleanup. No boot-smoke pass is claimed here.
