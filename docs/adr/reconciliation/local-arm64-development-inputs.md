## Local ARM64 development inputs

The shared native downstream path uses host Go CLIs/TUI, one QEMU site and two tenant guests. NVKR owns
tool acquisition/builds and payload verification; the source-owned NVKE CLI owns bootstrap, deployment
and tenant operations. Both host selections and the older Linux simulator output use one dependency
catalog and one simulator producer. The simulator server declares CGO; its API-only CLI does not.
Platform-specific acquired assets have distinct producer IDs and a verified private command-name view.
The shared profile owner rejects Darwin diagnostic payloads for qualified execution and receipt issuance.
Both local execution and terminal result verification apply that admission before fetch/runtime observation;
Linux toolchain identity does not grant qualified authority to a Darwin payload. Unqualified Darwin diagnostics
retain their separate platform admission.
The Darwin simulator uses an explicit local `native-go` toolchain selection prepared by NVKR from the
installed Go SDK and Clang. SDK/compiler byte admission, private compilation, receipt lifetime and rejection
of hosted/signing/publication paths have contract coverage. A local Run at NVKE revision
`69ef9924c4bf0487beba8c3613ae6e0a0260d8f3` built the three Mac CLI executables and completed terminal
observation and packaging: Run `sha256:2ecfb84092206f23acbae658f7575216c00337feb3ab3d10ce8aed701e7f5ae6`.
This local record is not published release evidence. A separate native startup probe opened SQLite, seeded
two sites, served health and authenticated site reads, and joined server shutdown with exit 0, using NVKE's
mock VM backend. It establishes SQLite execution, not guest readiness or full demo acceptance. Linux retains
its source-owned static Make build in the same catalog producer.
The standard `run` and `e2e`
entry points now select that same three-VM deployment owner; the old Kind-site path is no longer their
production action. Build prepares native tools through NVKR and selects the complete local runtime image
closure. Default native `run` and `e2e` now launch the fixed retained service owner: successful
checks return to the shell while the native resource stack stays held until explicit
`cleanup --session`. Presenter cancellation stops work without requesting deletion.
Interactive authentication keeps terminal context through startup privilege
checks. An unattended owner starts in an independent process session; both paths
acknowledge the authenticated held-state handoff before successful return. Linux binds its control to SO_PEERPIDFD;
macOS binds it to the originally captured process audit token and a kqueue exit
observation, requiring CGO/public SDK support. The generic Darwin peer API remains
refused. Controlled launcher/native-scope tests exercise retention and joined
cleanup; a fresh NVKE VM/conformance run of this lifecycle change is not yet recorded.
Native presentation now uses individual guest/preparation/bootstrap/deployment
boundaries instead of the legacy Kind-site checklist. Requested conformance has
its own phase and failed-suite retention does not imply successful
cleanup. Native tenant presentation joins the existing CAPI role/instance mapping
to the private NICo inventory for both ordinary and conformance topologies, and
uses the existing held-path console metadata reader for each exact instance.
Native Linux reuses its admitted libvirt backend and existing held-domain
observer. Mac process state remains unobserved rather than inferred from provider
readiness. Generic CAPI observations use exact Cluster/Machine owner UID joins;
legacy host-id/spec-label checks remain unchanged. Controlled
subprocess and dashboard tests cover the model; a live display of this revision
and complete Mac/Linux runtime acceptance remain unverified.

The downstream Go owner exposes a callback-scoped native site and independently
created/deleted tenants through the same native engine. The provisional
`pkg/nvkr/demo.WithSite` facade and descriptive/opaque type aliases make that
owner importable by separate NVKE or customer modules without another lifecycle
or deployment engine. Independent external-module compile and nil/canceled/zero-handle
admission coverage establish importability, not real VM acceptance. Existing matching
`nvkr` and `nvke-demo` helpers and native host controls are still required. Go
tests can reuse one site for sequential tenants and run conformance explicitly
against a still-owned tenant, preserving each suite's original output in a fresh
directory. Opaque handles expire at scope exit; cancellation joins active calls,
and an unjoined outcome cannot be swallowed by a callback or lost during panic
or test fatal exit. Controlled subprocess tests cover sequential tenants, failed
validation retention, natural deletion and those ownership boundaries. These
contracts do not establish a real site-reuse run or complete Mac/Linux acceptance.
Native pool observations use NVKE's supported unfiltered JSON inventory, then
select the already-observed cluster ID and exact worker identity/infrastructure.
This removes the filtered CLI's separate parent-name lookup during deletion;
foreign or ambiguous worker identities remain refused. Missing workers are
accepted only after owned deletion starts. Query failures still require fresh
valid cluster absence; unavailable reads may wait
only under the existing bounded deletion owner. Raw cleanup captures stay private.
The same downstream owner accepts one or two tenant workers independently of
suite selection through `run.tenantWorkers` or `Config.TenantWorkers`. Omission
retains the ordinary one-worker or conformance two-worker default; conformance
rejects an explicit one-worker selection before work. Controlled tests cover
count propagation through capacity, tenant requests, readiness/cloud-init,
inventory/cleanup and retained-child transport without changing artifact or
source selection. The [AMD64 nightly record](../../nvke-nightly.md#evidence-and-privacy)
exercises the small profile at its exact source revisions; other topologies remain separate.

A separate fixed site-identity helper now has one deep `pkg/nvkr/siteidentity`
owner and thin private Linux ARM64/AMD64 build targets. It reuses the upstream
generated NodeAgent client and GoSPIFFE/gRPC, admits its bounded output before
remote work, and distinguishes not-started, joined failure and unknown remote
completion. Mutation now delegates to NVKE v0.23.0's unchanged `ReconfigureSpireAgent` and
`GetSpireAgentStatus`; the duplicated guest shell rewrite and renderer producer
are removed. Failed outcomes and missing responses remain unknown; client
termination does not prove manager-job completion. The upstream Workload API
picker retains the exact caller identity across updates.
Fresh peer metadata requires operation-fresh issuance and remaining
validity as well as unchanged boot/identity and a changed serial. Helper and
native caller revalidate those descriptions with one shared predicate; endpoint
authentication, the expected boot identity and source-owned TPM policy remain
separate requirements. It issues no
bootstrap-stop or release receipt. The native owner requires the shared
helper startup member before guest launch or native network configuration and
delegates captured completed-Run input replay to the lifecycle owner. All native demo
output selection now uses that owner rather than a second Run decoder and membership
validator. Artifact preparation reuses one descriptive Plan/Run per distinct selected
Run within the operation, including installer and identity-source checks; tool
preparation and conformance also reuse their inspected descriptions for sibling outputs.
Every selected Run must retain its original captured compiler inputs and Plan; uncaptured,
noncanonical, nonlocal and unfinished-commit records are refused. Disk selection and
subsequent asset preparation retain separate replay boundaries. The owner checks every
frozen Run binding; delivery payload acceptance remains separate. Controlled captured-input,
selection and payload-substitution fixtures cover these boundaries, not a fresh native
build/deployment. Source host-policy decoding is local and uses the existing
strict YAML admission; CRD schema validation remains at the server create.
After deployment and before temporary bootstrap teardown, it imports
the original host policy once without replacement, using the caller's work deadline
for API discovery, schema validation and creation. Private identity diagnostics
retain the underlying command failure before the public error is redacted.
It checks the controller/CSI context,
permanent registration, derived TPM allowlist, new TPM agent and renewed
same-boot host certificate, then issues its private completion receipt. Joined
site-stage failures retain their resources under the independent owner while the
foreground command returns; unknown completion keeps dependencies unsafe to tear down. The site-stage/retention presentation
leaves tenant and conformance checks pending. Private Linux ARM64 Run
`site-startup-tools-arm64-acceptance-20261021-02` built and verified the shared
three-member output from public source pins for the preceding shell implementation,
not the reduced RPC revision. The delivered helper passed an
isolated invalid-request subprocess check; its command-owned inherited pipes
are adopted before bounded I/O. This producer and command-boundary evidence
does not establish an authenticated handoff through integrated startup, a fresh
customer first run or complete current-source Mac/Linux runtime acceptance.
The [local guide](../../local-nvke-demo.md#private-site-identity-operation-helper)
records that boundary.

The native controller values now provide a dedicated disk-backed 256-MiB
`emptyDir` for helper staging. Runtime admission requires its exact writable
mount, and the staged-path/digest receipt stays confined to that directory.
Runtime admission and the exclusive Linux stdio adapter accept both declared
Linux architectures; other platforms are refused before descriptor or remote
work. Existing tenant join-token settings and site TPM attestation remain unchanged.
PATH-resolved `sh` supports both retained Alpine and current
distroless debug images without assuming `/bin/sh`; each selected source's
root-filesystem/user/security context remains unchanged. The existing
`native-site-identity-handoff` progress stage remains; raw command output and error
details stay private. The policy convergence loop keeps its existing two-minute deadline and
exactly-once creation. It may wait only for readiness of fully reobserved running
containers whose pinned pod/container, configuration and boot identities are
unchanged. Other runtime-check failures remain terminal; initial admission,
probes, host mutation and final proof still require Ready containers. Local pinned-base staging checks under AMD64 emulation on an ARM64
Docker host establish filesystem/tool compatibility only, not authenticated
handoff or native KVM acceptance. The completed runtime
evidence and remaining full E2E gap are recorded under consumer acceptance below.

The Mac network owner now consumes the verified upstream vmnet helper's JSON report and holds its Unix
datagram connection through joined guest/service cleanup. Gateway binding is observed before services
start; API, local disk delivery, callbacks and temporary Lima forwarding share that address. Contract and
race tests cover network bounds and ownership, and an actual helper start/stop/join passed. One real
2-GiB HVF guest using the newly built simulator completed cloud-init, checked the local disk HTTP endpoint and was
naturally deleted in 24 seconds. This local guest probe is not a fresh NVKE OS bake, site deployment,
tenant Kubernetes, conformance, or full Mac/Linux parity result.

Full native Linux runtime parity, complete fresh-machine host dependencies,
a single fresh build-and-run wrapper, NVIDIA OVN acceptance and qualified release claims remain unproved.
QCOW construction remains owned by NVKR's disk-image Run path. The demo-owned direct Packer builder
has been removed. The native Mac runtime can consume an existing local QCOW. An October 17 private
diagnostic at source `15a2accd7d492c629abf63296ce6a701abf188c3` completed all 32
artifact outputs and both native-tool outputs in fresh Runs. Its site started and
both tenant guests joined Kubernetes, but neither became Ready: the observed
OVN node-pod requests exceeded the selected nodes' available scheduling resources.
The original command exited 17 after readiness timeout; three VM deletions,
empty simulator instance records and joined processes were observed. Workload,
DNS and conformance did not run. These retained private records establish that
attempt's build and failure scope, not a public acceptance record, proof of the
subsequent sizing fix, or a successful complete Mac/Linux demo.

The `inputs/definitions/demo/arm64-nvidia-tcg.json` combined example selects 32 outputs in one
Run without publication/signing: OS, components, installer-aligned site, tools,
profiles and simulator. Focused selections remain available; the historical
22-output component subset is not the complete demo. This is not a complete
source-built distribution. See the
[quickstart](../../local-nvke-demo.md) and [source gaps](../../full-nvke-source-build.md).

Local static-unqualified mixed Plans admit offline, non-disk utility
observation/SBOM/packaging in the child helper while VM/held-disk execution
stays with the original Run owner. Network utility acquisitions remain
unsupported. Relocated image observation retains compiled Plan identity.
[Mixed-utility tests](../../../pkg/nvkr/factory/local_mixed_utility_test.go)
use controlled OCI bytes and a descriptive, unexecuted VM; they do not prove
a real joined bake or extend hosted, signing or qualification admission.

The historical legacy Kind-site consumer invokes `nvkr` for build, verification
and extraction, and NVKE's pinned staged installer for deployment. In that consumer, Kind hosts
management services; CAPNICo/NICoSim provisions QCOW2 tenant VMs. Full E2E success
requires two Ready nodes, workload/DNS checks, exact disk/artifact bindings and
owned cleanup. The explicit `--cleanup` route retains that full acceptance
scope. Default `run`/`e2e` instead hand off a retained local environment to the
shell, with separate `status` and explicit `cleanup` commands. A live retained
owner, its serialized description and historical Ready time are not current
Kubernetes health, joined cleanup or a release receipt. The actual launch/peer
selection, detach, operation cancellation, partial retention and explicit
teardown have controlled native Linux ARM64 process fixtures; these are not a
fresh NVKE retained deployment or proof of SSH/logout/cgroup persistence.
Live access presentation executes bounded read-only bundled kubectl API/node
queries for the site and tenant and keeps raw responses private. Its focused
tests do not establish real Ubuntu runtime acceptance. New retained work does
not retroactively change the exact-revision complete recording below.
The shared bootstrap/SPIRE upstream CA for new demos has a finite 3,650-day
lifetime, matching NVKE's local E2E CA convention. The real certificate regression
verifies trust after 48 hours and rejects expiry; it does not prove live SPIRE
renewal or full retained runtime acceptance. Existing retained roots are unchanged,
and automatic upstream-root renewal remains unavailable.
Cobra flags and typed YAML configuration supply descriptions, not receipts.
The combined build reuses bootstrap executables and the measured toolchain;
catalog substitutions retain their own exact predecessor binding. `--jobs`
bounds concurrent ready producer and packaging operations in the existing
factory scheduler. The upstream NVKE readers
still require Python; the demo does not reimplement them.

E2E prepares its exact pinned installer automatically with bounded Git in a
fresh private attempt directory; an explicit matching checkout remains read-only.
Replay still requires a checkout. Preparation checks build prerequisites before
acquisition; controlled tests cover pin/dirty/source/path/cancellation failures
and prepare-before-build ordering, not current external Git authorization or
fresh tenant acceptance.

Ordinary local source Git and installer preparation now use the existing
`internal/sourcegit` owner to exclude ambient global hooks, fsmonitor, filters,
templates and line-ending conversion. Developer credential and selected proxy/TLS
trust settings are projected only into network fetch; explicit absolute local-repository redirects remain
available. Its transport override admits HTTPS, SSH and local files, not cleartext
HTTP/Git or external helpers. Clone fixes upload-pack, disables local-path
optimization with `--no-local`, enables transferred-object checks and disables
recursive submodules; ref discovery also fixes upload-pack.
Neither expands developer credential projection beyond fetch. Real Git fixtures
exercise TLS-verified helper fetch, plaintext refusal and exact local clone/ref
results; these are not current private-source access or held receipt evidence.
Ordinary local sources reject indexed gitlinks and nonempty, untracked, missing,
symlinked or mismatched root `.gitmodules` descriptors. A regular tracked empty
descriptor requires Git's empty-HEAD and index/worktree correspondence checks;
the maintained OVN fork's inert descriptor does not authorize nested sources.
Git owns the bounded index-mode query; its Git 2.38+ capability floor is named on
query failure. Ignoring submodules during status is not evidence of clean nested sources.
The ordinary adapter accepts operation-first arguments only; its explicit
directory owns repository selection. The demo may consume one leading `-C`
before that admission; additional global options are refused before repository
queries or command-log creation. Git still owns operation argument syntax.
Privacy-safe refusal diagnostics name static configuration families and
redirect indices, never configuration values, paths or URLs. Ordinary fetch and
configuration-query failures explain the supported fetch-only credential policy
without assigning an authentication cause or changing cancellation codes.
Real Git fixtures cover exact checkout bytes, callback exclusion,
local redirects and authenticated fetch. The E2E build adapter preserves these
bounded, owner-validated redirects, and the NVKE clone launcher appends its two
redirects instead of discarding other local source inputs. These fixtures are not proof of current
private-repository access; the stricter held hosted-source admission is unchanged.
The downstream site installer now takes a full detached private checkout of its
exact source pin instead of enabling sparse checkout, which Git configures through
the deliberately rejected `extensions.worktreeConfig` delegation. Real Git
regressions cover this failure, unchanged source bytes, the exact historical pin,
cancellation and continued rejection of delegated configuration. Command admission
errors retain the failing phase without exposing arguments or source paths. These
tests do not establish a completed Ubuntu site replay.

The local demo now selects stock NVKE `v0.23.0` at
`2cfd00081b468d85a4894ec17e9deac13c9d8169`, including that release's public SPIRE
packaging. The obsolete full Kind-site adapter and its typed NICo tenant-release
workflow are removed. The maintained host workflow still uses NVKE's temporary
Kind bootstrap. Earlier beta source-rendering and installer dry-run checks retain
their historical scope and do not establish acceptance of the current host workflow.

Maintained OS Definitions, catalog execution and Packer toolchain assembly select Docker.
The older authored Podman Environment and its TCG Definition are removed; generic
NVKR Podman support remains. The ARM64 catalog baseline retains the already-published
historical M2 Docker candidate, not a newly measured current toolchain. Local Docker
launchers still freeze their freshly validated image into an attempt overlay.
Static Plan and wrapper tests cover these selections, not a new native bake.

The local OS selects the PR #113 NVKE/NVIDIA OVN fork at pinned tag `v2.4.2`,
commit `8af51273d52dfa07454435d18d37230e582d797a`; profile packages and tenant
requests select its `2.4.2` chart stream. The combined Definition also declares standalone
OVN OCI image and bound Helm OCI outputs. Disk-embedded and standalone outputs
are independently produced; this does not close original-owner cross-output
reuse or establish equal bytes. The recorded exercises below were performed
against the superseded pin `d7dc5d8c811fc037322a619775f5ef9bfe51c7a0` and its
`1.6.0` stream; they retain that scope and do not transfer to the current pin.
Fork graph/profile tests and actual pinned-chart
Helm rendering with loopback OCI push/pull were reported locally, without a
retrievable CI/Release evidence attachment for these exercises. An author-reported
local attempt at `de31a87` completed the combined artifact Run,
including the fork OS bake, with 29 expected and 29 observed outputs in the
`unqualified-local-execution` lane. Its exact Run identity and original records
remain private; no retrievable external artifact is cited here.
Artifact completion does not establish tenant acceptance or default OVN startup.
For the current `2.4.2` pin, static catalog/overlay/graph contract checks and a
local pinned-chart Helm rendering with loopback OCI push/pull are recorded, again
without a retrievable evidence attachment. The equivalent exercises recorded
earlier against the superseded pins `9480bbe1377e9d9dae550c6ae08f3d7b6460dbf7`
(stream `2.4.1`) and `12ca20e67b516f1a023da85a22deeaf8d2d70d0b` (stream `2.4.0`)
retain that scope. The `2.4.2` chart adds an always-installed node-annotation
`ValidatingAdmissionPolicy` and control-plane resource limits; no running
cluster has exercised either. No Run, OS bake or tenant exercise has
been executed against the current pin, which also ships its single-node-zone
values file as a link to `values.yaml`. Image binding admits regular files
only, and a copy would carry the source image digests that binding replaces in
`values.yaml`, so an install selecting the copy would merge them back over the
bound values. The chart producer withdraws that member instead; the packaged
chart omits it and the source-documented install path using it fails closed on
a missing file.


Separately, the retained continuous attempt above used the explicitly selected
`kind-calico` diagnostic and ended with a two-node readiness timeout, not a
successful E2E video or an OVN startup test. That timeout's cause was not
established by this recording; it cannot be attributed to the uninstalled
NVIDIA OVN startup path. Later acceptance evidence below retains its own exact
revision and network selection, rather than retroactively explaining this failure.

The downstream demo now selects those standalone NVIDIA OVN outputs for the
Kind site as well as selecting the fork's stream for the QCOW2 tenant. Its private
Kind configuration disables the default CNI and kube-proxy. The site adapter
copies the selected native image through the existing owned registry and exports
the observed bound chart through `nvkr publish chart`; it starts node-local OVS
using that image's existing entrypoint before site OVN, node readiness and CAPI.
These privileged local CNI processes are not an independently isolated provider.
Already-loaded Open vSwitch/Geneve modules are admitted before installer
acquisition or deployment mutation. The adapter does not issue module-load or
unload commands, but the privileged source-owned OVS entrypoint can manipulate
the shared kernel; prerequisite admission is not an isolation boundary.
The changed site topology has no completed live E2E acceptance. Fresh
dual-cluster runtime checks remain required, separately from artifact and
adapter tests.
Read-only rendering of the retained chart with its built Helm 4.2.4 executable
was reported locally to preserve the selected image digest across all six site
chart slots. No retrievable rendering artifact is cited for that exercise;
it does not establish validation of the exact values currently installed by the
demo, or a running cluster.

The explicit `upstream-ovn` demo option is additive; NVIDIA OVN remains the
default and no mode silently falls back. Its authored local Definition replaces
four output names (disk, image, chart and profiles), preserving a 29-output
combined Run. Upstream `v1.4.0` resolves to commit
`d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d` and tree
`ce0cb49cd14f2fc260457e61a5a222c027a07f79`. Whole-producer overlays bind the
upstream image, chart and offline CNI packages; a separate authenticated profile
patch installs stream `1.4.0` and names its distinct served disk. Tenant requests
select that same stream. Opposite-mode outputs are rejected before deployment.
The source-owned image Dockerfile still acquires OVS/OVN and kubectl through APT;
this does not claim hermetic, FIPS/STIG, qualification or Kubernetes compatibility.

The complete alternate Plan compiled locally. Local rendering of the exact pinned
upstream Helm templates was reported to produce seven image references, each bound to the
selected digest through the existing repository/tag helper, without a template
patch; no retrievable external rendering artifact is cited for that local exercise.
Controlled tests cover mode propagation, four distinct output names,
profile package/disk alignment, image binding, six-container node observations
and site-network readiness before Cluster API. These checks alone do not
establish real startup or tenant acceptance. Separately, a fresh complete E2E
on `71b356f5899efb759571f0ea51781ead3f7c466a` passed on 2026-10-11: one
29-output Run, matched upstream site/tenant inputs, two Ready ARM64 QCOW2 nodes,
unchanged zero-retry workload/DNS and natural deletion plus joined owned cleanup.
The [exact-revision evidence](../../evidence/local-upstream-ovn-2026-10-11.md)
retains the bindings, runtime checks and recording integrity. This is an
unqualified local diagnostic, not live acceptance of later changes, a supported
release combination, M2 or qualification. This option neither fixes nor
validates the NVIDIA fork's open startup issue.

Fresh profiles selections produce a native OCI image through the existing
rootless BuildKit lane and pinned NVKE profiles Dockerfile. A named build context
selects the existing exact Alpine image digest instead of the Dockerfile's tag;
the additive source overlay changes no upstream NVKE files. ORAS transports the
observed image to the local registry without changing its manifest. The consumer
uses the chart's standard image init container and read-only shared volume,
checking the exact image and successful init exit instead of injecting files
into a Kind host directory. Local-only APT installation and uplink naming are
explicit profile choices, not a general NVKE offline boot contract.
[Profile factory tests](../../../pkg/nvkr/factory/nvke_laptop_profiles_test.go)
cover additive patch authentication, native-image compilation, exact staging
bytes and modeled APT/uplink cases. [Consumer tests](../../../internal/nvkedemo/site_test.go)
reject substituted images, incomplete/failed init states and alternate volumes.
The opt-in exact pinned source-chart rendering check and the following native
exercises were reported locally; their private attempt paths are not a
retrievable external acceptance record. An isolated
native ARM64 rootless BuildKit test produced manifest
`sha256:197fe058d0fbcff5d7841abfee58067aa05f7cdd21040642fe7e327096b574a0`.
ORAS transport/readback, manifest retrieval from the Kind network and the image's
init copy command passed with exact staged profile/runbook comparison. This is
not by itself a completed NVKR profiles Run or Kubernetes init-container observation.
The retained 29-output attempt above subsequently observed `controller-profiles`
as `kind: oci` with that exact manifest digest. Its site handoff and the actual
`materialize-profiles` init container retained the matching image ID, `/profiles`
path and pod UID; the init container exited zero with reason `Completed`.
This exercises the changed producer/consumer, but its tenant readiness failure
still leaves full E2E acceptance unverified.

E2E now admits cached ARM64 Kind and registry images before installer acquisition
or artifact builds. Deployment authenticates its owned registry's direct IPv4
address on the admitted Kind network and probes `/v2/` from the actual management
node before uploads. Scoped proxy bypass applies only to that private endpoint.
Registry admission measures deduplicated image bytes, the largest serial upload
and 64 MiB headroom against the same 2 GiB ceiling used by its ephemeral tmpfs.
Tests cover the measured 1,122,458,894-byte closure, the exact ceiling and one
byte over, blob-count bounds and the launch argument. This is a capacity
correction, not fresh deployment or workload/DNS acceptance.
Contract tests cover ownership, network/address substitutions and probe failures;
the isolated native registry test exercised the direct route, but does not prove
deployment across all supported host configurations. Network progress now groups
private commands into preflight/setup/cleanup; expected absence probes no longer
emit misleading failures.

The local network owner now admits a single temporary Kind-to-simulator INPUT
rule, scoped to the admitted Kind subnet, its exact gateway and TCP port 18089.
Its version-2 journal records that optional gateway and the exact owner-commented
rule before insertion; existing version-2 journals without this grant remain
recoverable. Site admission probes health from the owned Kind node using its real
proxy environment before installing the site. Host-side health alone cannot
admit this route. Controlled tests cover bounded response checks, cancellation,
private diagnostics, rule substitutions and recovery. Hosts with an INPUT-drop
policy require this scoped route even when host-side health succeeds. A separate
nftables/firewalld input-hook drop can still reject it; the owned-node probe,
not an iptables accept alone, decides admission. The demo does not override that
administrator policy; see the [operator firewall guidance](../../local-nvke-demo.md).
Live acceptance of the new route remains unexercised.
