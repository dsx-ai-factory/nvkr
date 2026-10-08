# Local site chart preparation, October 8, 2026

## Scope

This records an isolated toolchain/dependency/package probe on the user's Colima
`nkd-demo` Docker host, not a complete four-output NKD Run or installation.
Application source is NKE PR #1160 candidate commit
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345`, root tree
`36569cf4ec0e2360ee023cd7e53da65b96ce770d`.

## Failure and fix

The existing pinned `alpine/helm` image has entrypoint `helm` and reports
`v4.3.0+gbec5b06`. A fresh bounded container running the original catalog's
`dependency build nktp/deploy/charts/nktp` failed with missing repository
definitions for all three source-owned chart repositories.

The local wrapper retains that exact parent
`alpine/helm@sha256:a6cf54599ccb99d90cf0712b30f03fdb3cab062e6b94e0418cc4db7e8a1464b2`
and only clears the entrypoint. It installs no packages or binaries. The native
Docker image ID, read back after fixed-epoch build without generated Buildx
attestations, is
`sha256:8fae65b2d2366fe8370d8e10f34ef5c11c43a8341bb727bb48b2fa2b1035dbf0`.
An offline read-only probe verified Helm 4.3.0, shell and SHA-256 utility.

## Real probe

A separate read-only, capability-dropped container used a private writable source
volume, temporary home/cache, 512 MiB memory cap and a 300-second outer deadline.
Only the exact pinned chart tree was copied into it; no Git credentials or NKE
checkout mount was supplied to Helm. It executed the site Plan's declared
repository-registration and dependency-build command. Helm fetched:

| Dependency archive | Bytes |
| --- | ---: |
| `opentelemetry-kube-stack-0.22.0.tgz` | 267,660 |
| `prometheus-node-exporter-4.56.1.tgz` | 15,359 |
| `metrics-server-3.13.1.tgz` | 14,144 |

The command exited 0, reported both `Chart.yaml` and `Chart.lock` unchanged by
SHA-256 verification, and Docker reported `OOMKilled=false`.

A second container mounted those prepared bytes read-only and packaged the chart
with **network disabled**. `helm package` succeeded as `nktp-0.1.0-rc.2.tgz`.
That diagnostic package's SHA-256 was
`ffddd3789d940938467211894bcba6c3d2d3398bafc6bf8d351322fe82252cbd`.
It was ephemeral probe output, not a packaged NKD Artifact, accepted Run output
or reproducibility proof. Helm's package metadata is not claimed bit-reproducible.

## Limits

The real Mac-to-Colima `--check site` invocation passed on `nkd-demo` with
`CONTAINER_RUNTIME=docker` and the accessible local pinned NKE checkout. It
performed no pulls, builds or attempt creation. The two stopped probe containers
and their private volumes were then removed; the toolchain cache, retained Runs
and ongoing OS bake were not removed.

The four-output site Definition/compiler/preflight contracts are tested, but the
complete CLI/controller/chart Run is not recorded. No upstream chart signatures,
complete runtime image mirror, publication/signing, site/tenant installation or
QCOW2 boot was proved. The installer requires Helm 4.2.4; this isolated build
wrapper's Helm 4.3.0 does not establish that installer gate. Its source-tree
inventory-rendering path still prepares chart dependencies; installation's
packaged-chart support alone does not make the whole journey rebuild-free.
