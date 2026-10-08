# Colima Docker source-built NICoSim, October 8, 2026

## Result and scope

Committed PR #118 implementation `f658032cb52e0b181f902f3f77653492e8a308f4`
completed Run `laptop-colima-nicosim-arm64-20261008-01` inside the dedicated
Colima `nkd-demo` Linux ARM64 Docker VM. The Run is `complete`, with one required
and one observed executable bundle, two source-built components, no failure and
lane `unqualified-local-execution`.

Both produced executables subsequently passed a bounded offline non-root startup
probe: ARM64 static ELF, no dynamic interpreter, CLI usage returned its expected
exit 2, and the server initialized SQLite and served its pinned UI. The probe
selected the **mock** VM runtime and disabled container/BGP execution. It is not
real VM provisioning, QCOW2 boot, site installation or tenant/workload evidence.
Seed/UI assets came from the retained pinned source, not the executable bundle;
their artifact delivery remains unfinished.

## Execution and input identities

The exact committed NKD source was exported with `git archive` into a new native
ext4 directory inside Colima:

```text
/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-f658032cb52e0b181f902f3f77653492e8a308f4
```

The Linux launcher used normal repository-relative `.nkd` storage. Neither old
attempts nor uncommitted source changes were copied. The simulator build was
separate from the concurrently running OS attempt and the 23-output `all` set.

```sh
# Inside that exact native source export:
env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker \
  NKE_LOCAL_CLONE=/Users/ikislenko/Workspace/github/nke \
  bash scripts/nke-full-arm64-local.sh laptop-colima-nicosim-arm64-20261008-01 1 simulator
```

| Input or observed identity | Exact value |
| --- | --- |
| Definition | `definitions/nke-laptop-arm64/definition-simulator.json` |
| Environment | `definitions/nke-laptop-arm64/environment-local-docker.json` |
| Runtime | Colima 0.10.3, Docker 29.5.2, Buildx 0.34.1, Linux ARM64 |
| NKE source commit | `6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4` |
| Git tree | `d0e0f5265d1917d6cd7ff594442e0c8d1e74044d` |
| Source epoch | `1791419815` |
| Native-validated attempt toolchain | `sha256:c28b7415c160e694a71ed637c406546ea3d50ef1f9d6acd901f7058954dfbebe` |
| Effective catalog digest | `sha256:71f4576d7d82b8f646579e287396ce71b9efa6c172ff8159809c11616810c9e2` |
| Build Digest | `sha256:97ce71a3062d0623a7a5f7f6decbe076fed553a5e455b87c985a6b301a371175` |
| Plan digest displayed as `run` | `sha256:0306512412ecd489492feddaf9f139448aba4d802bb313bc2cb3359ecca5722e` |
| Bundle OCI manifest | `sha256:d103e35983b35f776e4bb47a6e1186e3920d74a0e69eb361dc7b4508a7017a17` |

The bootstrap runner reported `dev`/`unknown` because the source archive has no
Git metadata. The recorded committed source export identifies the implementation;
that version string is not a release approval. The source checkout override
provides pinned Git objects, not its dirty files or checked-out HEAD.

The launcher compiled and projected the baseline Plan before replacement
toolchain work, built the pinned toolchain sources with Docker Buildx, loaded and
natively probed the result, then froze its measured ID through an attempt-local
catalog overlay for exactly `nico-sim` and `nico-simctl`. Existing Docker layers
were cached from the OS bootstrap; this does not prove a completely uncached
clean-machine build. Maintained locks and organizational publication approvals
were not changed. The actual catalog/Plan owner admitted the substituted inputs.

Source preparation downloaded, verified and vendored the pinned Go modules.
Both producers then ran with `--network=none`, using the verified read-only
prepared source. Two component composition SBOMs and an index were generated
before bundle packaging. These are not an installed-site or disk inventory.

## Produced bytes and startup probe

Independent SHA-256 and size measurements agreed with the retained outputs:

| File | Bytes | SHA-256 |
| --- | ---: | --- |
| `nico-sim` | 25,734,288 | `2f07d4fc6d7936b761ccc86cfd2d9a8e9f1f46f38169594664816b837d93f4e0` |
| `nico-simctl` | 10,696,464 | `da75ef4ecb568d19f9aae5e3feba36cd737ef63d8a61f5903edaab095a6e7042` |

These bytes and the bundle manifest match the earlier recorded Podman result.
Their logical Build Digests differ because the toolchain pin changed. This
observed equality is not authenticated independent reproduction or qualification.
GCC retained the existing static SQLite `dlopen` warning; the measured startup
does not establish arbitrary extension-loading compatibility.

The probe reused the retained bounded smoke script from the earlier Podman trial,
mounted the exact new produced binaries and pinned source read-only, and ran as
UID/GID 1000 with a read-only root, temporary tmpfs, dropped capabilities,
no-new-privileges and no network. It did not mount a Docker socket or kubeconfig.
The probe stopped and joined its server on exit and the disposable container was
removed. It used:

```text
NICO_SIM_LISTEN_ADDR=127.0.0.1:18080
NICO_SIM_DB_PATH=/tmp/nicosim.db
NICO_SIM_FILES_DIR=/tmp/files
NICO_VMS_VM_BACKEND=mock
NICO_VMS_BRIDGE_CIDR=172.30.0.0/24
NICO_VMS_BGP_ENABLED=false
NICO_CONTAINERS_ENABLED=false
```

Final output:

```text
PASS: source-built NICoSim initialized SQLite and served its pinned UI
```

## Validation and remaining work

The implementation passed `make check`, `make build`, uncached launcher tests,
shell syntax/diff checks and independent read-only review. Negative projection
tests reject missing/ambiguous selections, acquired producers, missing native
pins and mixed toolchains. Orchestration contracts establish that unsupported
scope stops before replacement toolchain construction/load/probe or local Run
execution. Real `--check simulator` passed against the selected Colima profile.

The completed Run retains lifecycle, producer results, prepared source, SBOMs
and the OCI layout inside that native source directory. Deleting/resetting the
VM disk would lose them. The separate OS bake still needs its own completion and
boot evidence. The demo needs delivered assets, profiles/NKTP/source alignment,
exact-byte deployment and real QCOW2-backed tenant/workload acceptance; see the
[whole-demo analysis](../lia-local-demo-analysis.md).
