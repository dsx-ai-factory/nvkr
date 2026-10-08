# Real local NKE site integration, October 8, 2026

## Result and scope

Final replay `site-integration-20261008-06` exited 0 inside the dedicated native
ARM64 Colima profile `nkd-demo`, Docker 29.5.2, eight CPUs and approximately
24 GiB RAM. The deploy-only consumer verified existing completed NKD outputs,
installed a real NKE site and passed all selected source-owned stage checks.
It rebuilt no artifacts. This is not tenant/workload E2E, a complete source-built
distribution, qualified execution or Release authority.

The actual result included:

- Source-built installer-matched tools, real Kind management and CAPI installation.
- Six unchanged prepared chart archives and all six Helm releases deployed.
- Controller Available and `/readyz` database/Temporal checks passed.
- Four CAPNICo CRDs Established, manager Available and required Secrets present.
- CAPI SPIFFE exec-auth mounts/rollouts, local site registration, `nico CONFIGURED`
  and HTTP 200 from `/api/version` passed. `CONFIGURED` does not validate credentials.
- Controller and provider ready-container image IDs and the successful profiles
  init-container image ID matched the exact copied manifests.
- Owned registry, management node and anonymous volume removed; simulator
  stopped/joined and the temporary bridge restored by the replay harness.

No tenant, workload or guest was created in this replay. Standalone guest
evidence cannot fill that gap.

## Exact exercised code and inputs

The implementation worktree was based on PR #118 commit
`8c75859db354a8818fc71eaca4482f5baaa680e8` plus this increment. Exercised file
SHA-256 values, not a claim that an earlier commit contained these bytes:

| File | Exact SHA-256 |
| --- | --- |
| `scripts/nke-local-site.py` | `64e4e18ceaa7f1e6e391ff2213722dbeef85f787fcc48344ade51d1cb03bdd95` |
| `scripts/nke-local-management.py` | `2dfa7c7903598e682d3cb22c08b70622e351af977624a5e7b2a0d09d93539489` |
| `scripts/nke-prepare-chart-inputs.py` | `01c9dbb3ce749c0892c77c585740ccac7a8d3622e8fd8cb08fda6b3e206d7472` |

NKE installer pin: `8fb2d53368823bd386b4fbd55b4ab6d3c0931345`.
Simulator source pin: `6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4`, consumed
through its existing verified/extracted executable/assets bundle.

| Completed Run | Consumed selection |
| --- | --- |
| `laptop-colima-tools-arm64-20261008-01` | Five-tool bundle, manifest `sha256:fb5d5575c7a7953937c6bfdef1fa2d2322fb6a1f1296265bfee7d12ec895dc19` |
| `laptop-colima-site-arm64-20261008-01` | `nkectl`, `cwe`, controller chart and NKTP |
| `laptop-container-components-arm64-20261007-01` | PostgreSQL, Temporal, SPIRE and SPIRE CRDs chart acquisitions |
| `laptop-providers-arm64-20261007-01` | Source-built CAPNICo, not the provisioned provider |
| `laptop-colima-profiles-arm64-20261008-02` | Diagnostic Ubuntu 26.04 ARM64 plain profiles |
| `laptop-colima-nicosim-assets-arm64-20261008-01` | Real simulator/assets used by the private replay harness |

Separate Runs do not form a combined accepted Run or signed Release. The retained
OS Run was not selected by the site consumer. PR #113 direct-source integration,
full OS smoke and DSX IMDS gaps remain unchanged.

The copied/read-back manifests and actual Pod image IDs shared these digests
under `172.18.0.1:15501/nkd-local/{cwe,profiles,provider}`:

| Output | Exact manifest digest | Pod UID |
| --- | --- | --- |
| Controller | `sha256:ba55320fdf6efd39be43c15eedc8e60126914bd50f9eb758b984ae84bf5eb699` | `42f9f749-418c-4873-9fa9-3fba1d62974c` |
| Profiles init container | `sha256:e39ed458fea74cac2f5720c798d16c6d4dc863741ac1c5f93fa28bd004b6746f` | `42f9f749-418c-4873-9fa9-3fba1d62974c` |
| CAPNICo | `sha256:157c924444669866f12829204f6763034c592ef280b8f63953a8cb5938dec625` | `22cbf096-40c6-4f2a-b65a-3b82329dfa26` |

SHA-256 of each extracted, unchanged chart archive:

| Chart | Exact archive SHA-256 |
| --- | --- |
| Controller | `e7d71cc719a9baf04274971116319b27c9f88d518f983bf56efb982a3d6a2de1` |
| NKTP | `1788b65077c2247583864e57adb53f4aa4c3cc53a09764c6b16b1952c8e7cdb9` |
| PostgreSQL | `c350143d2e21c1e1a3670dd32ab64fd11d49198a66fae87c63a3112531c0cd17` |
| SPIRE | `7c32126bb1909076cdf9fd0419dbf9e5b012f152bb09a96a4259917d70581fac` |
| SPIRE CRDs | `ce982e63fc375e392b014fc99e621a55442ab886052413e9bb052f72d66580a8` |
| Temporal | `14a8efe1cc92c91befac151db91f20f9a13962788e9d710c86655add6d966d37` |

The [site guide](../local-site-integration.md) documents the exact adapter command
and required inputs. This replay used completed native exports under
`/mnt/lima-colima-nkd-demo/nkd-local/` and the existing component/provider Runs
under the implementation worktree's `.nkd/runs/`. The private harness started
the real simulator at `http://172.18.0.1:18089` and temporarily created bridge
`nkdvm0`; it performed no tenant routing, DHCP, BGP or disk serving. Those host
ownership operations are not shipped as a complete demo wrapper.

## Source-owned integration and explicit exceptions

NKE stages 00, 01, 02, 03, 05 and 06 ran and checked after the existing management
consumer ran stage 04. A private clean pinned clone supplied the scripts.
NKE's strict item reader validated the prepared chart metadata; NKD's chart
owner verified manifest/blob/archive bytes. No original NKE checkout edit or
dependency rebuild occurred. The local assembly manifest explicitly used
`lane: local-assembly`, `scope: none` and the exact installer source.

The disposable CA had critical CA/basic-constraints and certificate-signing
key usage, and passed OpenSSL strict validation. This does not establish guest
trust compatibility. The pinned NKE Kind telemetry fixture supplied its
development-only metrics-server insecure kubelet TLS setting before rendering;
chart bytes and real Helm readiness checks were retained. This is not production
TLS guidance. API authentication remained disabled in the reference setup. Its
additional external-client NetworkPolicy selected the Kind gateway `/32`;
existing in-cluster/node allowances remain. Other-client denial and network
policy enforcement were not tested.

The registry service, Kind node, dependency charts and network-pulled runtime
inputs are acquisitions, not extra source builds. CAPI's source-owned SPIFFE
exec-auth setup also acquires its plugin/Alpine inputs. Not every runtime image
was built or mirrored through the selected Runs. The dedicated VM/Docker daemon
and their privilege are operator-trusted diagnostics, not independent isolation.

## Terminal ownership and retained diagnostics

Private cluster: `nker-demo-7d237edba25040f9`.
Removed exact node:
`11e260301f75809b5d39b447dc6882c972d329a565dfc467eb2f4db7a4f6c39d`.
Removed anonymous volume:
`cbcea38d24de131d189a0c1acf476b492343b40720b1ab1235c57bc561864281`.
The registry used read-only container configuration, a bounded 1 GiB tmpfs and
only the Kind gateway binding. Exact ownership/read-back gates preceded removal.

Post-replay scoped Docker inventories found no owned node, registry or volume;
libvirt domain inventory was empty; no simulator process or bridge remained.
The original installer checkout stayed clean. Shared Kind network/image caches
and installed host prerequisites remain. No global prune or default Mac context
change occurred. A guard's final negative `pgrep` probe printed Colima's
`exit status 1` message; the guard's terminal exit was 0 and disappearance was
independently checked, not inferred from that message.

Diagnostics are retained privately under
`/mnt/lima-colima-nkd-demo/nkd-local/site-integration-20261008-06/site`:
phase logs, `site-inputs.json`, `node-ownership.json`, private source/chart/tool
extracts and configuration. Directory mode was 0700 and site configuration 0600.
No credential, CA key, token or kubeconfig content is published here.

## Earlier failures and contract validation

Replay 01 failed real simulator startup without its required bridge. Replay 02
found a registry startup race; the adapter now checks bounded readiness before
copy. Replay 03 failed the actual Kind metrics-server TLS/SAN check; reuse of
NKE's existing Kind fixture corrected it without suppressing readiness.
Replay 04 exposed a bootstrap CA missing certificate-signing key usage; a
verified adapter SIGTERM stopped/joined processes and cleanup completed. The
CA contract was corrected and strict validation added. Replay 05 passed with
the preceding adapter hash; replay 06 additionally exercised final Bash/curl
admission. Earlier failures are not labeled successful acceptance runs.

Thirty Python fixtures passed natively on Linux, including real orphan-group
stop/join, captures, missing prerequisites, symlink substitution, foreign image
IDs, registry ownership and no cleanup after failed producer join. macOS passed
the applicable cases with the Linux-only orphan fixture explicitly skipped.
These fixtures are separate from the real replay. Independent review found no
remaining code defect after its fixes. The repository gate, uncached Go suite,
targeted script race checks and CLI build also passed before publication of
this increment; GitHub checks for its new commit must be verified separately.

Remaining full-demo work: complete required artifact membership/direct-source
integration; owned simulator network/disk service; guest/bootstrap trust and
full OS contract; real QCOW2-backed tenant creation, node/workload acceptance
and complete joint teardown. No stronger result is inferred from this site pass.
