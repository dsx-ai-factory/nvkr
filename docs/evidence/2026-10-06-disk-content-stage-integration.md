# OCI content validation integrated with ARC parent ba87e27

Observed October 6, 2026, completed by 21:24:08 UTC. This records the same-base
integration of the OCI correctness fixes with the updated diagnostics/ARC parent.
It does not establish full NKE QCOW2 inspection, native AMD64 execution, hosted
delivery or publication approval. The [earlier parent-integration record](2026-10-06-disk-content-parent-integration.md)
and [initial content record](2026-10-06-disk-content-validation.md) retain their
original snapshots and results.
The complete Go/module/version manifest digest below is the authoritative
identity of this executable snapshot; SHAs are pre-merge branch references,
not a promise of post-squash reachability. Historical scanner rows retain their
original scope, including the then-duplicated held/plain path, not current counts.

The [later cancellation review record](2026-10-06-disk-content-review-cancellation.md)
binds the subsequent callback/hash-owner repairs and their fresh native results;
the `3decca` manifest below identifies this historical parent-integration snapshot,
not the later shipping source.

## Exact tested snapshot

The tested source is the resolved merge of:

- OCI validation branch: `95fdd1e1027adffa4afba5b570c3dd9c0c8755a1`.
- Updated diagnostics parent: `ba87e272ada8c561c69819062a97fdcd66283ab8`
  (its parents are `4ea09f3be40623105d04a6d0d7d2109cd4b4c352` and
  `9ba0ad047658fcc0d0a67b0a2d0b48c5a56843e6`).

Only reconciliation row 13 required manual conflict resolution. It retains the
parent's distinction between the proposed strict M2 target and narrower
producer-provenance candidate, recorded inspection timeouts and historical
chart selection, while preserving the OCI contract and scoped evidence link.
All Go source merged without conflict: the ARC budgets, per-producer cancellation,
sealed Packer diagnostic route and upload order remain the parent's bytes;
the OCI material, scanner and progress implementations/tests remain the OCI
branch's bytes. No new source behavior or pin was introduced by this merge.

The sorted tracked Go/module/version manifest has 797 entries and SHA-256
`3decca033c54ab6032814f438794d12151ab7ceaa0b82b29a4550c04980d3a3d`.
The manifest was exactly unchanged before/after the native runs and host gates:

```sh
git -c core.fsmonitor=false ls-files '*.go' go.mod go.sum .go-version | xargs shasum -a 256 | shasum -a 256
```

Fresh native SBOM race binary SHA-256:
`1acd4e61ca3eed111f956f0943c06227233a1addd0756cd1622f70054c4dffaa`.
Selected integrated source SHA-256 values:

```text
e1171dca881abe7140d1c7dcaf60d5faad9c3cc8a0cfcac245c3e118a943c4c2  pkg/nkd/factory/github_disk.go
3dccb724a1e5437bca6481eb4a73c5f77677298e1c68169e9379b9a32f4485dd  pkg/nkd/factory/github.go
ed8c3ce10427c78544f768ddd804bb760510d79009d1e9989c06a7a638e707f9  pkg/nkd/factory/github_validate.go
b65650d883b1cd4983163fe38ba8721c1b920c3f3fe5b86d00abb30c56fbf810  pkg/nkd/factory/github_stages.go
e669103efd4440a8bf1eb414bd1adeed8a38cc81c8d9f6104d6a8e58678b7efc  pkg/nkd/pipeline/packer_diagnostics.go
8d2e1f81cd3bf91da891ba049617179bbf7e0c319bda6496536c103d117662ad  pkg/nkd/sbom/disk_materials.go
a8169ad5e64055a7c44ee93c3ad21a437aabfe6a725f306d3eff40e6379b9902  pkg/nkd/sbom/disk_material_oci.go
ceb3dc9c13df9a674e7153b21d689020cb3a9d2b9f471f569c5fd7bcc2855fd2  pkg/nkd/sbom/disk_progress_test.go
```

## Retained commands and results

The [exact command record](2026-10-06-disk-content-stage-integration.commands.txt)
has SHA-256 `ec1bae470525a8b6bd117caa94e815a502b38815649bce4e2aed8cbc475fe301`.
Native container runs used already-local images without pulls, network access or
package installations; source/modules were read-only, users nonroot and scratch
owned by the harness. HOME and test/production deadlines were not overridden.
The host vulnerability gate used its repository-native pinned Go-tool setup,
as recorded in the host output.

| Scope | Actual result and retained output SHA-256 |
| --- | --- |
| Complete native Linux ARM64 race/coverage suite, `./... -count=1` | Exit 0; all packages PASS. [Output](2026-10-06-disk-content-stage-integration-full-race.log): `5359931921855c84cf43eea69e560f0414c4426d12b9ce054864dd28a6cc2cbd` |
| Fresh SBOM binary with real pinned Syft/dpkg, OCI guards and depth/cancellation regressions | Exit 0; no selected skips. [Output](2026-10-06-disk-content-stage-integration-tools.log): `96edc0d7b4d1dd876427777670cb052be93e536cc91a33e7a4c2295709b5928d` |
| Host formatting, vet, lint, shellcheck, vulnerability check, build and generated drift | Exit 0; lint 0 issues, no vulnerabilities. [Output](2026-10-06-disk-content-stage-integration-host.log): `d50699cc06c93cc4613fc3c2847dc852c6345c13d5cbfbac637637b8d966094c` |

Full-suite factory/lifecycle/CLI/SBOM/pipeline timings were
230.284/38.756/50.825/75.330/21.642 seconds. Startup reported Linux aarch64,
Go 1.27.1, jq 1.7, UID/GID 502:20 and disk-backed overlayfs scratch
(4096-byte blocks, 62,727,584 available blocks). Only the ignored `.nkd`
output directory was separately writable. Ordinary package PASS does not
mean opt-in external tests executed.

The already-local complete-tools/compiler/inspector image IDs were, respectively:

```text
1f87906be577585bff2bc2bf05662972bd9ddd2ddae29975f05e0501090d3abb
266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6
6fe5b341957b02b266610fdec2b7fdb11403a8f0160d7b5b03fcda8c76d8e15d
```

Read-back identified all three as `linux/arm64`. The inspector contains an
older helper; this run uses the freshly compiled test binary with pinned tools,
not a newly published inspector closure. Its observed UID/GID was 65534:65534;
cgroup CPU/memory/PIDs limits were `200000 100000` / `4294967296` / `256`.
The command record retains all five exact tool bindings.

All 26 direct OCI closure rows, eight upfront guards, four phase cancellation
rows, scanner-ready cancellation, nested closure and depth/member regressions
passed. Seven actual-tool executions passed: generated, held, plain/gzip nested
and native-OCI Syft scans, plus two dpkg/Syft capture rows. One foreign AMD64
configuration was rejected before scanning; it is not native AMD64 evidence.
Syft execution timings were 0.99/0.78/0.69/0.74/0.89 seconds, foreign rejection
0.14 seconds and capture rows 2.19/2.27 seconds.

Generated Run digest is
`sha256:850cea27b78f621ccb48242b41a8210c4df6efaee139d4102bfc325270e6cb05`,
matching the inherited parent bundle. The earlier macOS timeout and native
harness failures remain in their original records; this is not a macOS
`make check` PASS or a diagnosis of that timeout.

## Remaining acceptance

Complete NKE QCOW2 reconciliation, native AMD64 execution, the complete retained
material/capture matrix remained unmeasured at this snapshot. Multi-GB timing
was also unmeasured then; the later [controlled dense 2 GiB ARM64 measurement](2026-10-06-large-oci-material-timing.md)
has a separate executable identity and does not establish real NKE complexity.
Per-operation
byte bounds, shared member/image caps, pre-deduplication report limits and
authenticated inspection deadlines were not relaxed. The optional large-material
timing request was still open at this snapshot. A later merge/evidence commit identifies these
retained records; its documentation bytes are not a separate tested executable
snapshot.
