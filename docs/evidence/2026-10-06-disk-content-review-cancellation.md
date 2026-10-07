# OCI material cancellation review validation

Observed October 6, 2026, completed by 23:21:21 UTC. This records the callback
and post-copy hashing cancellation repairs after the [historical stage
integration](2026-10-06-disk-content-stage-integration.md). It is targeted native
ARM64 content evidence, not full NKE QCOW2 inspection, native AMD64 execution,
original-Run reconciliation, hosted acceptance or publication approval.

## Executable snapshot

The tested source is pre-merge branch HEAD
`9b2636527c89d821b9952190e90de019a426813e` plus these seven review-fix files.
The [complete 797-entry Go/module/version manifest](2026-10-06-disk-content-review-cancellation.source.sha256)
has SHA-256 `ec68e5997f173b17aa1990a9d27e538d1b9d78bb78fd36b121b7899bcddfe5de`;
it was exactly unchanged before and after all runs. That digest is the executable
snapshot identity; branch SHAs need not remain reachable after a squash merge.
A later evidence commit is not presented as the tested executable commit.

```text
3ad9c3055b09bc08725ad8d557b856cd6f32d4fb91a37f8137824e656962baec  pkg/nkd/sbom/disk_content_runtime.go
737fc95934fa4a1c48ed353fb147409ac3a901103b4db7b5c99f88e1979da69d  pkg/nkd/sbom/disk_filesystem.go
9086d66de8af6af4222e37b8a586c2e78daf8645cc27bba9ccbf45857ca58c70  pkg/nkd/sbom/disk_generated_file_test.go
0d7a1a0bcef6f9c2bfdf9140854af1dc97706fc64f32266078cbc95ddfad67bd  pkg/nkd/sbom/disk_material_oci.go
8bd2645011abcc4ff3197b33a97eca663eeb1ab5b837924f41660d1f622a2ec7  pkg/nkd/sbom/disk_material_oci_test.go
72b208f207e088600597f2eb7eac4ce7e6519fbc3ded5884fab2a250624b5b54  pkg/nkd/sbom/disk_oci_material_test.go
f70508759db870c7046134170783125c375919243dfbbfad761b70e962109c69  pkg/nkd/sbom/disk_scanner_integration_test.go
```

The shared hashing owner now returns `CodeTimeout` on actual context-canceled
reads; ordinary failures remain `CodeVerifyFailed`. The OCI scan callback maps
canceled non-timeout errors to `CodeTimeout`, while retaining ordinary scanner
codes and existing timeout diagnostics. Tests cover post-copy hashing-owner
cancellation, six callback error controls, and distinct admission/marker/extraction/
sealing reasons. Redundant copy remapping and a duplicate held/plain scanner row
were removed; no production deadline, capability or inventory gate changed.

## Commands and retained results

The [exact commands](2026-10-06-disk-content-review-cancellation.commands.txt) have
SHA-256 `8ff5d2a332b2fbe548d3f61bb18468dd1a62ff9971a68e8785465814fbfa1b34`.
Native containers used already-local images without pulls, network access or
package installation, with read-only source/modules and owned scratch. HOME was
not overridden. The host vulnerability check used repository-native pinned-tool
setup, as shown in its output.

| Scope | Terminal result and retained output SHA-256 |
| --- | --- |
| Complete native Linux ARM64 race/coverage suite, `./... -count=1` | Exit 0; all packages PASS. [Output](2026-10-06-disk-content-review-cancellation-full-race.log): `d3483cdc65d103e15e53203c772d36670f163b5de58a43a5dc798808b6b3b814` |
| Fresh SBOM race binary with pinned Syft/dpkg and OCI/cancellation regressions | Exit 0; all selected rows ran, no skips. [Output](2026-10-06-disk-content-review-cancellation-tools.log): `8ab59530482b706e1bb0ea398f3158dbee594b085ab56a5be9ad9dd42495e3f1` |
| Host formatting, vet, lint, shellcheck, vulnerability check, build and generated drift | Exit 0; lint 0 issues, no vulnerabilities. [Output](2026-10-06-disk-content-review-cancellation-host.log): `aeeebab19fe32bf723f5ab6b418b4c5284985775ecf6d6c741f4d7f94ad64fa7` |

The complete suite reported Go 1.27.1, Linux aarch64, jq 1.7, UID/GID 502:20,
and disk-backed overlayfs scratch (4096-byte blocks, 62,782,504 available blocks).
Factory/lifecycle/pipeline/SBOM took 226.657/35.046/20.760/72.793 seconds.
The ignored `.nkd` output directory alone was separately writable. Ordinary
package PASS does not imply opt-in external tests ran. This is the full same-source
Linux race stage plus host non-test gates, not a claim of macOS `make check` PASS;
earlier macOS package deadline failures remain recorded in the historical records.

Complete-tools/compiler/inspector image IDs were respectively:

```text
1f87906be577585bff2bc2bf05662972bd9ddd2ddae29975f05e0501090d3abb
266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6
6fe5b341957b02b266610fdec2b7fdb11403a8f0160d7b5b03fcda8c76d8e15d
```

The fresh race binary used the inspector's pinned tools, not its older embedded helper. Startup
verified all five tool hashes; the command record binds their exact digests.
The tool run reported UID/GID 65534:65534 and cgroup CPU/memory/PIDs limits
`200000 100000` / `4294967296` / `256`, with private 1 GiB tmpfs scratch.
Four Syft rows (generated archive, plain nested archive, gzip nested archive,
native OCI) and two captured-package rows executed successfully. The foreign
AMD64 helper row rejected before scanning with the exact platform-mismatch
reason and empty report; it is not AMD64 execution proof.

## Remaining acceptance limits

These small controlled materials do not establish complete retained NKE disk
inventory, both original live joins, native AMD64 behavior, published inspector
clearance, or full-matrix runtime acceptance. The separate [dense 2 GiB timing
measurement](2026-10-06-large-oci-material-timing.md) retains its original source
snapshot and synthetic ARM64 scope; it was not rerun or relabeled as proof for
these cancellation changes. Diagnostics preserve per-image scanner-transport
ordinals; root-scan starts at ordinal 0 and completes with its component count.
