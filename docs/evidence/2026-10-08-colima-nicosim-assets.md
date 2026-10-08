# Colima Docker NICoSim artifact-only startup, October 8, 2026

## Result and limits

Signed PR #118 implementation `55fe7478247762bf4136a5bfc0b70a1b4a302c34`
completed Run `laptop-colima-nicosim-assets-arm64-20261008-01` inside the dedicated
Colima `nkd-demo` Linux ARM64 Docker VM. The Run is `complete`: one required and
one observed executable bundle, three source-built components, no failure and
lane `unqualified-local-execution`.

NKD's existing delivery owner verified that final bundle, created an unqualified
local diagnostic publication wrapper and verified/extracted all three members.
A bounded offline non-root container then started the extracted server with the
extracted seed/UI archive. No NKE source checkout, Docker socket or kubeconfig
was mounted. SQLite initialized, and HTTP responses for the UI index and app.js
matched the extracted files byte-for-byte. The probe selected the **mock** VM
backend and disabled container/BGP execution.

This proves the simulator's artifact-only startup handoff. It does **not** prove
real VM provisioning, QCOW2 boot, NKE site installation, tenant/workload/delete,
full source-built distribution, signing, publication to a registry or qualification.
The [earlier two-component Run](2026-10-08-colima-nicosim-build.md) remains distinct:
that probe read seed/UI from its retained source, not its bundle.

## Inputs and output identities

The exact committed NKD source was exported with `git archive` into a new ext4
directory, without dirty files, Git metadata or earlier attempts:

```text
/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-55fe7478247762bf4136a5bfc0b70a1b4a302c34
```

Execution used the normal repository-relative `.nkd` storage and the optional
local NKE pinned-Git-object override. This was not a dirty NKE checkout build.

```sh
# Inside that committed native source export:
env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker \
  NKE_LOCAL_CLONE=/Users/ikislenko/Workspace/github/nke \
  bash scripts/nke-full-arm64-local.sh laptop-colima-nicosim-assets-arm64-20261008-01 1 simulator
```

| Identity | Exact value |
| --- | --- |
| NKE source commit | `6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4` |
| Source tree | `d0e0f5265d1917d6cd7ff594442e0c8d1e74044d` |
| Source epoch | `1791419815` |
| Native-validated attempt toolchain | `sha256:c28b7415c160e694a71ed637c406546ea3d50ef1f9d6acd901f7058954dfbebe` |
| Effective catalog digest | `sha256:4d2a0aac6ea6942eb790ba3f3156369ff58f4c9aa47273c3cbcebfc0e7a65f5e` |
| Build Digest | `sha256:c352fe62880c778cb8ffe887ceaab280880ab3ede44d4a2e7d2f2d35bd93dab9` |
| Plan digest | `sha256:a144acbb048503a2f53f516e422c517e4a4aea898386e50024c268d77c50d910` |
| Bundle OCI manifest | `sha256:be819c08681c9a848e138be5d4b34892ec6935110cf973b0696d73e4f30a0138` |
| Bundle semantic digest | `sha256:60615a420003717e61e95c16d8aaaaafb1c6d3b683dadc80f7616b3afc0559a4` |
| Diagnostic wrapper root | `sha256:623e94c471f2f3247138f33e062ef91354e7ad733c73aa3a10c545e4a5eacd52` |

The launcher projected all three selected producers before replacement toolchain
work, built/loaded/probed the pinned toolchain sources, and froze the measured ID
through the existing attempt-local overlay. Docker layers were cached from prior
bootstrap work; this is not an uncached clean-machine test or organizational
toolchain review approval. Source preparation verified/vendored Go modules;
all three producers ran offline from the run's verified source snapshot.
Three component composition SBOMs and an index preceded bundle packaging.

Independent measurements of the verified extracted members:

| Bundle member | Bytes | SHA-256 |
| --- | ---: | --- |
| `nico-sim` | 25,734,288 | `2f07d4fc6d7936b761ccc86cfd2d9a8e9f1f46f38169594664816b837d93f4e0` |
| `nico-sim-runtime-assets` | 136,820 | `dcdcf029cb3f2b72e06b9de987ab32c2ecb2e83ae73b92e1eca64235d21af5c6` |
| `nico-simctl` | 10,696,464 | `da75ef4ecb568d19f9aae5e3feba36cd737ef63d8a61f5903edaab095a6e7042` |

The asset producer-local filename is `nicosim-runtime-assets.tar.gz`; the
packaged/extracted member is `nico-sim-runtime-assets`. It is archive **data**,
not an executable, despite the generic binary bundle's mode/install hint.
It contains the pinned diagnostic seed, UI and vendor notices with source-owned
relative paths, not production fleet profiles, credentials or VM state.

## Artifact-only consumer and determinism probes

The existing `nkdctl publish root` and `publish extract` commands verified the
bundle and materialized its exact members in a fresh private directory. They
did not rebuild any component, invoke Make/Packer or create release authority.
The startup container mounted only that directory and the bounded probe script,
both read-only. It used the pinned toolchain, read-only root, temporary tmpfs,
dropped capabilities, no-new-privileges, no network, one CPU, 512 MiB memory and
128 PIDs. Its outer deadline was 90 seconds; the script stopped and joined the
server before the disposable container was removed.

The first probe used hard-coded UID 1000, which could not traverse the delivery
owner's mode-0700 extraction directory. That probe failed; it did not establish
startup. The retry used the observed owner UID/GID `502:1000` and passed, without
loosening permissions or changing the artifact bytes. Deployment must use its
actual admitted/extraction owner rather than assume a numeric UID.

The probe checked static ARM64 ELF framing, absence of a dynamic interpreter,
the CLI's expected usage exit 2, SQLite initialization and exact UI HTTP bytes.
The existing static SQLite `dlopen` warning remains; this does not test arbitrary
extension loading. Final output:

```text
PASS: packaged NICoSim initialized SQLite and served exact packaged UI without a source checkout
```

A separate bounded offline container repacked the exact retained seed/UI files
twice with the source producer's GNU tar/gzip settings. One temporary fixture
used different filesystem timestamps and permissions. Both repacks exactly
matched the produced archive's SHA-256 shown above. This is a measured diagnostic
byte-stability check, not an independent qualified reproduction verdict.

## Validation and remaining work

The change passed uncached script/buildgraph tests, actual Definition validation
and Plan compilation, `make check` including vulnerability scanning, and diff
checks. Independent review found and resolved the producer-local versus exported
archive filename documentation mismatch. The shared source/toolchain scope
contracts include the three-member selection and preserve fail-closed negatives.

Lifecycle, Plan, producer results, source snapshot, SBOMs, final OCI layout and
verified consumer files remain in this native export. Neither probe container
remains. The separate Colima OS bake still needs completion and boot evidence.
The combined selection, profiles/NKTP/source alignment, deploy-only adapter and
real QCOW2-backed site/tenant acceptance remain unfinished; see the
[whole-demo analysis](../lia-local-demo-analysis.md).
