# Source-built local installer tools, October 8, 2026

## Scope and result

The additional `tools` lane compiles the five operator tools owned by NKE's
staged-installer candidate from pinned upstream Git. The complete Docker and
Podman Plans and no-work preflight contracts pass. A real Apple Silicon to
Colima Docker prerequisite check passed. **No five-component tool build,
verified tool bundle extraction, site installation or tenant is recorded here.**

This is an increment on PR #118's implementation base
`7c34073c19df0eced32de36da856340b9d4f16a2`. Its source-built tools are a separate
Definition, not silently substituted into the older 23-output `all` set.
The current OS bake executes a different committed source export and does not
exercise these tool inputs.

## Authoritative version and source map

Versions were read directly from NKE's
[`make/tool-versions.mk`](https://github.com/dsx-ai-factory/nke/blob/8fb2d53368823bd386b4fbd55b4ab6d3c0931345/make/tool-versions.mk).
The source pins were generated with the existing maintenance owner:

```sh
bin/nkdctl catalog bump \
  --dir definitions/nke-laptop-arm64/tools-catalog --all --timeout 15m
```

| Tool | Source release | Peeled Git commit |
| --- | --- | --- |
| Helm | `helm/helm` v4.2.4 | `3900f434fd3ef2b84065dc04508df48f288dba00` |
| kubectl | `kubernetes/kubernetes` v1.36.0 | `ecf6decece6a6de25a57aad9ba90b6ce580f6f78` |
| clusterctl | `kubernetes-sigs/cluster-api` v1.12.3 | `1a1852c74072febc3fbf9823c9dd32f4cd6cc747` |
| ORAS | `oras-project/oras` v1.3.3 | `210747c29c1d38732b3194878dfd8b5a6b9ad7eb` |
| Kind | `kubernetes-sigs/kind` v0.32.0 | `cda67ef8588f6bfdac5358233f2471cb2149ecaf` |

The machine lock also records exact root trees and source epochs. Pin resolution
is not source-owner approval or upstream signature verification.

Build instructions were checked against upstream release files: Helm's Makefile
and internal version package; CAPI's `hack/version.sh`; ORAS's Makefile;
Kind's version command; and Kubernetes's workspace/version namespaces. All five
use PR #113's digest-pinned native Go compiler candidate:
`docker.io/library/golang@sha256:27da97ffaed4bbb1263c44a8e423a72a1547830ee14a70fdb0cef3f6268a1b8b`.
The tool repositories have Apache-2.0 licenses at the selected releases.
Complete tool/transitive license and security review is not established here;
NKD's own repository vulnerability check is not that review.

## Exercised contracts

`TestLaptopInstallerToolsContract` compiles the complete selection for native
Docker and Podman, checks exact release versions and raw binary output slots,
requires five independently pinned source fetches and five offline builds,
checks preservation of compiler GOFLAGS and rejects foreign or duplicate tool
selections with coded errors and no Plan. No acquired release binary, signing
or publication fallback is selected.

`TestLocalDeveloperPreflight` adds Docker/Podman tools cases without OS-sized
capacity and a Mac-to-Colima check-only forwarding case. The actual command:

```sh
CONTAINER_RUNTIME=docker COLIMA_PROFILE=nkd-demo \
  bash scripts/nke-full-arm64-local.sh --check tools
```

reported:

```text
Prerequisite diagnostics passed for tools/docker. Git authorization and cgroup/resource/toolchain admission remain execution gates.
```

This diagnostic created no attempt, pulled no image and built no producer.
Independent read-only review checked the source/linker and local-fetch contracts.
Its scoped Git-ownership and fixture-test findings were addressed before handoff.
Focused race-enabled factory/preflight tests and the repository gate (`make
check`, including lint, shellcheck, tests and NKD vulnerability scanning) passed.
These gates do not execute or security-approve the five upstream tool builds.

## Local-only Helm preparation

Four exact upstream external symlinks are negative-test fixtures, not
`cmd/helm` compilation inputs. Diagnostic Local preparation checks their targets
and removes precisely those paths using `git -c safe.directory=/src rm`.
That override trusts only the already pinned private checkout, not every
repository or the user's global Git configuration. The resulting source closure
is authenticated by the existing receipt owner after module preparation.

The held hosted fetch route transports source before vendoring and rejects
these external links. It cannot run this Helm preparation as-is. This increment
changes no transport/symlink admission, receipt schema or qualified capability.
It is not evidence that a generated hosted tool workflow works.

## Corrected readiness wording

NKE's selected `00-check-readiness.sh` invokes `check_warn` when the Helm version
differs from its 4.2.4 pin. It fails on a missing required tool, but the version
mismatch itself is a warning. Earlier PR #118 wording calling that mismatch a
hard tool gate was corrected. The chart build wrapper still uses Helm 4.3.0;
the new 4.2.4 source producer does not automatically replace that wrapper or
establish a matching executable on the installer's PATH.

## Required runtime follow-up

After the OS bake releases laptop capacity, execute a fresh `tools` attempt,
require terminal `complete` with one required/observed output and no failure,
verify/extract the bundle through the existing delivery owner and execute each
tool's native version command. Then integrate it with the source-aligned site,
simulator, profiles, real QCOW2 and deploy-only adapter. Do not call any compiler,
preflight or mock startup result a working local tenant demo.
