# Nightly site producer catalog

This profile-owned catalog selects the CLI, controller image, controller chart and NKTP chart for both
ARM64 nightly Definitions. It starts from `../../nvke-local-dev-arm64/site-catalog`; that retained local
catalog is unchanged. Each source role keeps its distinct vendor closure. The four source roles are pinned through `catalog pin --commit` to the reviewed NVKE commit
`04a6ed26c1e51d70167669a6882a04080eefadc7`, with owner-issued tree
`340e9452fb1d5d962d5553fb2d49b9bc86f82366` and committer epoch `1791924640`. This is an
exact source revision, without a release-tag claim.

The producer mappings follow the source-owned contracts rechecked at NVKE PR #1580's pushed
`04a6ed26c1e51d70167669a6882a04080eefadc7` head:

| Output | Source contract |
| --- | --- |
| `nvkectl` | `cli/Makefile` builds `cli/bin/nvkectl` and owns `NVKECTL_VERSION`, `NVKECTL_COMMIT`, `NVKECTL_DATE` and `NVKECTL_TREE`; its build target executes the binary's version assertion |
| `cwe` | `controller/Dockerfile` uses the existing Go 1.27.2 and Alpine digest pins and its source-owned controller Make target |
| `controller-chart` | Source `Chart.yaml` version `0.0.0`, no dependencies or `Chart.lock`; the final chart remains bound to the produced controller digest |
| `nktp` | Source `Chart.yaml` version `0.0.0`; its existing `Chart.lock` selects the three captured dependency-chart archives |

CLI vendoring verifies that CLI, controller API, chart-module and management-engine module metadata
are unchanged after the standard Go download, verify and vendor commands. The chart module has no
`go.sum`; absence remains an explicit guard. No module files are rewritten to make the build pass.

The existing rootless BuildKit and Helm toolchain pins are retained. The chart producers select the
bare local ARM64 Helm image ID
`sha256:8fae65b2d2366fe8370d8e10f34ef5c11c43a8341bb727bb48b2fa2b1035dbf0`; no
registry reference or verified hosted availability accompanies it. An admitted hosted toolchain is a
remaining prerequisite; this catalog supplies no guessed registry image or security waiver. A pin
alone is not a security or compatibility result. NKTP dependency chart archives remain acquired materials,
with the existing byte hashes; these checks do not authenticate their upstream signatures. Native
profiles remain separate explicitly pinned producers, including their existing exact Dockerfile guard.

Both profiles remain security-blocked at the nightly coordinator, direct script and runtime entry.
Generated manual producer workflows retain their existing main-branch admission; no producer was
dispatched. Source preparation and generated-bundle checks do not execute product or verification
tools, prove runtime compatibility, or establish a qualified release. The reviewed source
commit and owner-issued tree/epoch values are recorded in `pins.lock.yaml`; no branch name or
guessed release tag substitutes for that pin.
