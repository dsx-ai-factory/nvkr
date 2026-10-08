# Deploy the completed local NKE site artifacts

This development entrypoint connects the completed NKD output Runs to NKE's
source-owned site installer. It is not yet the complete first-time laptop
tenant/workload tutorial. The full journey remains tracked in the
[demo handoff](nkd-to-nke-demo.md). Do not interpret a site-only result as
QCOW2 tenant provisioning, workload acceptance or a verified Release.

## Inputs and execution boundary

Run inside the dedicated Linux ARM64 Colima VM described in the
[local build guide](local-development.md). Native Docker, Git, Python 3.12+,
OpenSSL, Bash, curl and the completed matching tools Run are required. The management
diagnostic's CPU/memory/storage admission applies; it is not fresh OS-bake
capacity admission. The pinned registry service must be cached:

```sh
docker pull docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8
```

An already running private NICoSim HTTP endpoint on port 18089 and its private
token file are required. Use the real source-built simulator and libvirt backend,
not its mock or container backend. The current site entrypoint does not start or
stop that external simulator, configure tenant routing/DHCP/BGP, serve the disk,
or grant native host privileges. These still need integration into the final
owned full-demo entrypoint. Its health check proves only the supplied HTTP
interface, not that a VM can be created. NICoSim's selected source accepts a
nonempty bearer token; this is diagnostic authentication, not production NICo
authorization. Keep the endpoint inside the trusted dedicated VM.

| Completed Run selection | Consumed outputs |
| --- | --- |
| `tools` | Exact installer-matched Kind, kubectl, clusterctl, Helm and ORAS bundle |
| `site` | Controller image, image-bound controller chart, NKTP chart, nkectl |
| `components` | PostgreSQL, Temporal, SPIRE and SPIRE CRDs chart acquisitions |
| `providers` | Source-built CAPNICo image; provisioned provider is not selected |
| `profiles` | Diagnostic ARM64 Ubuntu 26.04 plain profiles image |

The disk/simulator Runs remain necessary for the complete journey but are not
claimed consumed by this site-only entrypoint. Passing separate Runs does not
create a combined Run or signed Release. PR #113's direct-source OS graph remains
a separate integration requirement.

## Site command

From the native NKD checkout, set absolute paths to your already completed Runs
and bootstrap. These may reside in different exports; do not rebuild merely to
move them beside this script. Use a clean NKE installer clone at
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345`, and a new private output directory:

```sh
set -euo pipefail
: "${NKD_NATIVE_BOOTSTRAP:?Existing native nkdctl from the local launcher}"
: "${NKE_INSTALLER_CLONE:?Clean clone at the exact documented installer pin}"
: "${NKD_TOOLS_RUN:?Completed tools Run directory}"
: "${NKD_SITE_RUN:?Completed site Run directory}"
: "${NKD_COMPONENTS_RUN:?Completed components Run directory}"
: "${NKD_PROVIDER_RUN:?Completed providers Run directory}"
: "${NKD_PROFILES_RUN:?Completed profiles Run directory}"
: "${NICO_SIM_ENDPOINT:?Private non-loopback IPv4 HTTP endpoint on port 18089}"
: "${NICO_SIM_TOKEN_FILE:?Private regular token file, mode 0600}"
: "${NKD_SITE_DIAGNOSTIC:?New output directory outside source and all Runs}"

python3 -B scripts/nke-local-site.py \
  --nke-checkout "$NKE_INSTALLER_CLONE" --nkdctl "$NKD_NATIVE_BOOTSTRAP" \
  --tools-run "$NKD_TOOLS_RUN" --site-run "$NKD_SITE_RUN" \
  --components-run "$NKD_COMPONENTS_RUN" --provider-run "$NKD_PROVIDER_RUN" \
  --profiles-run "$NKD_PROFILES_RUN" --nico-endpoint "$NICO_SIM_ENDPOINT" \
  --nico-token-file "$NICO_SIM_TOKEN_FILE" --out "$NKD_SITE_DIAGNOSTIC"
```

The source seed's organization defaults to `novxwn552ssc`; `--nico-org` selects
another operator-admitted organization. The entrypoint creates private Kind
management resources, consumes exact chart archives through the existing NKD
chart export owner, verifies/extracts the CLI and copies the completed controller,
profiles and CAPNICo manifests through source-built ORAS to its private registry.
Remote descriptor digests must match their local Run observations. Node access
uses containerd's [host configuration](https://github.com/containerd/containerd/blob/main/docs/hosts.md),
not a change to Docker's global daemon configuration.

A private pinned installer clone supplies NKE's readiness, scheduling/storage,
image staging, rendering and install/check scripts. The local assembly manifest
uses `lane: local-assembly`, `scope: none`, and the exact installer commit. It is
not relabeled as pipeline-attested. Six prepared archives replace chart source
paths, so the installer does not rebuild them. A fresh one-day local bootstrap
CA and private site/tenant credentials are generated for this disposable site;
they do not assert trust compatibility with an existing tenant disk.
The CA has explicit certificate-signing key usage and passes OpenSSL's strict
validation before installation. NKTP consumes the pinned NKE-owned Kind fixture,
including its development-only metrics-server `--kubelet-insecure-tls` setting;
the produced chart bytes and Helm readiness gate are unchanged. This setting is
not production TLS guidance.

The installer also runs stage 06: CAPI SPIFFE exec authentication, local site
registration and API reachability. The additional external-client NetworkPolicy
selects the Kind gateway `/32`; existing in-cluster/node allowances remain.
Denial or enforcement against other clients was not tested. The reference API
authentication remains disabled, so this is
strictly a trusted disposable development environment. CAPNICo `CONFIGURED`
means its CRDs, Deployment and credentials Secret exist; it is not proof of
successful NICo provisioning. The adapter additionally compares actual Pod
image IDs for the controller, completed profiles init container and provider
against the exact copied manifests, rather than accepting Deployment intent.

The registry stores copied artifacts in a bounded tmpfs, binds only the Kind
bridge gateway and is removed after the consumer. The exact owned management
node/anonymous volume are also removed. Shared Kind network/image caches and
private logs/source/chart extracts remain. It does not prune Docker or delete
accepted Runs. An unjoined producer prevents concurrent cleanup and fails closed.

## Tested result and remaining acceptance

The [real Colima replay](evidence/2026-10-08-local-site-integration.md) passed all
site stages/checks, six Helm releases, controller database/Temporal health,
CAPNICo readiness, registration, API access, exact runtime image IDs and owned
cleanup. No artifact build ran in the consumer. Development failures and fixture
tests are recorded separately.

The final full-demo command must still integrate exact disk serving,
NICoSim ownership/networking, guest bootstrap trust, tenant
creation, a real Kubernetes workload, produced-to-deployed identity checks and
complete teardown. None is inferred from chart rendering or a management node.
