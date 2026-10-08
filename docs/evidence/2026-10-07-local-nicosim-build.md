# Source-built NICoSim tools for the local NKE demo

## Result and limits

Run `laptop-nicosim-arm64-20261007-01` completed with one expected/observed
executable bundle, two source-built components and no failure in the
`unqualified-local-execution` lane. It used the existing rootless Linux ARM64
Podman VM and a local Git transport override. This is a separate incremental
Definition, not an addition to the existing 23-output `all` selection.

An offline non-root smoke container subsequently executed both produced tools.
`file` reported ARM64 static ELF executables; `readelf` found no interpreter.
The server initialized SQLite, loaded its source-pinned seed and served a
nonempty UI response from the same prepared checkout. A trap stopped and joined
the server before the disposable container was removed.

This does not prove a Colima installation, Docker-backed node creation, a running
NKE site/tenant, OS disk boot, a clean-machine toolchain bootstrap, publication,
signing or qualification. The probe selected the mock VM runtime and disabled
container/BGP execution. It did not connect to a Docker socket or cluster.
Seed/UI assets came from the retained source checkout, not the executable bundle;
their separate delivery remains unfinished.

## Inputs and command

```sh
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh laptop-nicosim-arm64-20261007-01 1 simulator
```

Use a fresh attempt name when repeating the command. The launcher uses
`definitions/nke-laptop-arm64/definition-simulator.json` and the local Podman
Environment. The source catalog was pinned through `nkdctl catalog pin --commit`,
not by hand-writing a release-tag claim.

| Input | Exact value |
| --- | --- |
| NKE source commit | `6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4` |
| Git tree | `d0e0f5265d1917d6cd7ff594442e0c8d1e74044d` |
| Source epoch | `1791419815` |
| Existing host-local Go/GCC toolchain ID | `sha256:03a8b35467adfd1fc3ca496adf87ac0cd23ce35782290409d2832578eaad7d51` |
| Build Digest | `sha256:79642b365fbefb3d1cc9712d82894bc8d2270ce110a1fb3bd54ea70d82959579` |
| Plan digest displayed as `run` | `sha256:224b6121a806e56d3b3a46187a9d80a016f87ffa21cb7e2841d34635ea58cb63` |
| Bundle OCI manifest | `sha256:d103e35983b35f776e4bb47a6e1186e3920d74a0e69eb361dc7b4508a7017a17` |

The bootstrap runner reported `dev`/`unknown`. The producers were exercised from
uncommitted draft catalog/launcher bytes; repository gates and later preflight
changes are separate from that Run's observation. No committed-head or portable
toolchain claim follows from this record.

Preparation downloaded Go modules, checked that `go.mod`/`go.sum` remained
unchanged, verified modules and vendored them offline. Both build nodes then used
read-only prepared source and `--network=none` with `CGO_ENABLED=1`. The server
invoked NKE's `make -C controller/nico-sim build-static`; the CLI used a static
external-link Go build. The same verified source snapshot served both producers.

| Produced file | Bytes | SHA-256 |
| --- | ---: | --- |
| `nico-sim` | 25,734,288 | `2f07d4fc6d7936b761ccc86cfd2d9a8e9f1f46f38169594664816b837d93f4e0` |
| `nico-simctl` | 10,696,464 | `da75ef4ecb568d19f9aae5e3feba36cd737ef63d8a61f5903edaab095a6e7042` |

Two per-component CycloneDX documents and an index were generated. These are
component composition evidence, not a complete installed-site or disk inventory.

## Smoke execution

The successful probe ran the produced files inside the already-cached toolchain
with network disabled, a read-only root/source mount, a writable temporary tmpfs,
UID/GID `1000:1000`, dropped capabilities and no-new-privileges. It used:

```text
NICO_SIM_LISTEN_ADDR=127.0.0.1:18080
NICO_SIM_DB_PATH=/tmp/nicosim.db
NICO_SIM_FILES_DIR=/tmp/files
NICO_VMS_VM_BACKEND=mock
NICO_VMS_BRIDGE_CIDR=172.30.0.0/24
NICO_VMS_BGP_ENABLED=false
NICO_CONTAINERS_ENABLED=false
```

The working directory was the prepared checkout's `controller/nico-sim`, which
provides the default seed and UI. The CLI's usage invocation returns exit 2 by
source design; the probe checked that status rather than treating it as a passing
`--help` contract. Final output was:

```text
PASS: source-built NICoSim initialized SQLite and served its pinned UI
```

The first probe failed due to nested shell quoting. A retained script removed
that ambiguity, then stopped early because it assumed CLI usage returned zero.
After checking the CLI source's explicit exit-2 behavior, the corrected probe
passed. Neither failed probe is a successful server-startup result.

GCC emitted the existing static SQLite `dlopen` warning. The inspected files had
no dynamic interpreter and the observed SQLite startup succeeded in this
container; arbitrary extension loading or another host libc is not established.

The retained Run includes its lifecycle record, producer results, OCI bundle,
SBOMs and prepared source under `.nkd/runs/laptop-nicosim-arm64-20261007-01`.
The successful probe script is under the matching laptop-bootstrap directory.
These attempt files are diagnostic local state, not committed release evidence.

## Remaining integration

The launcher now fails before bootstrap downloads if the simulator lane lacks
its exact cached Podman toolchain. Fresh-toolchain substitution and Docker
execution remain unsupported for this incremental lane. The complete demo still
needs node-image/profiles/NKTP/deploy assets, exact-byte install handoffs, local
runtime setup and tenant/workload acceptance. See the
[whole-demo analysis](../lia-local-demo-analysis.md).
