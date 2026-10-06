# Combined OCI material and inspection diagnostics validation

Observed October 6, 2026, completed by 20:08:32 UTC. This records the same-base
integration of OCI correctness fixes with updated inspection diagnostics. It is
not complete QCOW2 inspection, native AMD64 execution, hosted acceptance or
publication approval. The [earlier content record](2026-10-06-disk-content-validation.md)
and its logs retain their original executable snapshot; their results are not
relabeled as evidence for this integration.

## Executable snapshot

The tested source is the resolved merge of these exact commits:

- OCI material fixes: `e8ceba27498798d1195ccb3c9e615d278e22bb35`.
- Inspection diagnostics: `4ea09f3be40623105d04a6d0d7d2109cd4b4c352`.

The sole manual conflict resolution unions the imports in
`pkg/nkd/sbom/disk_progress_test.go`. Both parents' tests remain. Shared material
code retains diagnostic phases/depth from the diagnostics parent and marker
cancellation classification from the OCI parent. No new production behavior,
Definition, pin, generated bundle or deadline was introduced during integration.
Independent review compared the union against both parents and found no blocker.

The sorted tracked Go/module/version manifest has 794 entries and SHA-256
`5632e710b7be0fad98c35658738ebce002a0de61af66437076d96a3180c0a01b`.
It remained unchanged during the retained native runs and after the complete
suite. The pre-staging observation was
`d832a02c0e870e5ff42d302ff5d6d4196bb2a638d45a5d9def2ecd24541397bf`:
the unmerged index listed the same resolved progress file three times. Adding
those two duplicate entries to the staged manifest reproduces that exact hash;
this is an index-enumeration change, not executable-byte drift.

```sh
git -c core.fsmonitor=false ls-files '*.go' go.mod go.sum .go-version | xargs shasum -a 256 | shasum -a 256
```

The fresh native race binary SHA-256 is
`f7e1012d0b73e5785e4b1d9957881988d1cc36fdf971bba70fbd506609007f26`.
Key integrated source hashes:

```text
aa627919964427f04254f667e8a45c0739e83ef18b01fb6c254fafc7c99576e4  internal/diskdiagnostic/progress.go
9271c55e5153659d24f4ee7cc673ff6a92e4635ca3eb84906d7eebd8c5200fc1  pkg/nkd/factory/github_disk.go
2234c1571b7b134d39a4df45da806722ce02b9ce1a83b06b34b52add1f25f626  pkg/nkd/factory/github_disk_diagnostics.go
ded652ff7afd8394f9982273a132e56b89d557d59f70722c8bf70b9f6031c52e  pkg/nkd/factory/github_reporter.go
d1d66e5d542c725aea1bb1402e444738f65ac50e1a9586d2f31ec326e6470eca  pkg/nkd/pipeline/disk_inspection_runtime.go
d2b20e77b7579e2ab3ef923c49716c2c350b29cb6c061a179db2e78995462e42  pkg/nkd/sbom/disk_filesystem.go
8d2e1f81cd3bf91da891ba049617179bbf7e0c319bda6496536c103d117662ad  pkg/nkd/sbom/disk_materials.go
ceb3dc9c13df9a674e7153b21d689020cb3a9d2b9f471f569c5fd7bcc2855fd2  pkg/nkd/sbom/disk_progress_test.go
```

## Commands and retained results

The [executed commands](2026-10-06-disk-content-parent-integration.commands.txt)
include the native build, both native run scopes, host non-test gates and
manifest command. Command-file SHA-256:
`2b30806c14790ea8cb95f01dbf0711c08658137262a11d164184ff8ae52ef981`.
All runs used already-local images without pulls or installations. Native
execution used no network, read-only source/modules, nonroot users and owned
scratch; HOME and production/test deadlines were not overridden.

| Scope | Actual result and retained output SHA-256 |
| --- | --- |
| Full native Linux ARM64 `go test -race -covermode=atomic ./... -count=1` | Exit 0, every package PASS. [Output](2026-10-06-disk-content-parent-integration-full-race.log): `8eb39d0641435c1fb11609aab0b1431001e0b006c1b6cc2fb20947fa23a0d3d5` |
| Fresh compiled SBOM binary with pinned Syft/dpkg, OCI guards and parent depth/member regressions | Exit 0, no selected skips. [Output](2026-10-06-disk-content-parent-integration-tools.log): `54f81849826f32d692065f77b99f1e8c63e78c9996f650471ac42836e4cc8d66` |
| Host `make fmt-check vet lint shellcheck vulncheck build workflows-check` | Exit 0; lint 0 issues, no vulnerabilities, generated digest unchanged. [Output](2026-10-06-disk-content-parent-integration-host.log): `53b3382ab9e0168386981febd9694ad92ba75e3fbff6df3052a2aff316aeac9e` |

Full-suite factory/lifecycle/CLI/SBOM/pipeline timings were
197.820/39.719/50.847/72.496/25.983 seconds. Actual startup reported
Linux aarch64, Go 1.27.1, jq 1.7, UID/GID 502:20 and disk-backed overlayfs
scratch (4096-byte blocks, 62,784,988 available blocks). The already-local
complete-tools image was
`1f87906be577585bff2bc2bf05662972bd9ddd2ddae29975f05e0501090d3abb`.
Only the ignored `.nkd` output directory was separately writable for fixtures.
Ordinary package PASS does not imply execution of opt-in external tests.

The pinned-tool run used compiler image
`266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`
and inspector image
`6fe5b341957b02b266610fdec2b7fdb11403a8f0160d7b5b03fcda8c76d8e15d`;
all three images were observed `linux/arm64`. The inspector still contains an
older helper: this run executes the newly compiled test binary with the pinned
tools, not a newly published helper closure. Tool hashes are the unchanged
values and explicit environment bindings in the command record. Actual startup
reported UID/GID 65534:65534 and cgroup CPU `200000 100000`, memory
`4294967296`, PIDs `256`.

All 26 OCI closure rows, eight upfront guards, four phase cancellation rows,
scanner-ready cancellation, nested closure contracts and both depth rows in
each parent diagnostic regression passed. Seven real-tool execution rows passed:
generated/held/plain-nested/gzip-nested/native-OCI Syft scans and two dpkg/Syft
capture rows. One foreign AMD64 configuration was rejected before scanning;
that is not native AMD64 evidence. Real Syft rows took
0.62/0.62/0.61/0.63/0.71 seconds, foreign rejection 0.12 seconds, and capture
rows 1.81/1.84 seconds. The first equivalent passing probe exceeded the display
budget; the complete retained transcript comes from the subsequent identical
command on the unchanged binary, not reconstruction of missing output.

## Limits retained

The prior macOS full-package timeout and first native harness failures remain
documented in the earlier record. This combined validation does not claim a
macOS `make check` PASS or an established cause for that timeout. Host non-test
gates plus the complete same-source native Linux race suite passed separately.
Generated Run digest remains
`sha256:2c2bf59ad5b300c55da19e2068006562f8ddf60b8899444dbdfd88cc058081f2`.

Complete NKE QCOW2 reconciliation, native AMD64 execution, the full retained
input matrix and multi-GB timing remain unmeasured. Per-operation byte limits,
member/image caps, pre-deduplication component limits and authenticated
inspection deadlines remain unchanged. The optional large-material timing
review request is not claimed fixed. A later merge/evidence commit identifies
the retained records, not a different tested executable snapshot.
