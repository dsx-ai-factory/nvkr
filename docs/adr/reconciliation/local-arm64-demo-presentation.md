## Local ARM64 demo presentation

The local NVIDIA native KVM Definitions select stock NVKE `v0.23.0` at
`2cfd00081b468d85a4894ec17e9deac13c9d8169` for agent, kubeadm, OS assembly,
CLI, controller, provisioned provider, profiles, SPIRE packaging and DOCA OVS acquisition.
The source MachineImage fixture's local QCOW slot moved to
`http://127.0.0.1:18080/files/images/nvke-node.qcow2`, and the demo's boot projection requires that slot. The release
incorporates the public upstream SPIRE/TPM packaging fix and revision-4 package;
there is no separate older SPIRE source selection. NVIDIA OVN image/chart producers
retain `8af51273` / 2.4.2. The custom OVN Debian
producer changes its actual package metadata and payload paths to `nvke-*` while
retaining source-compatible virtual package aliases and bounded image import.
Version stamps derive from the selected NVKE release tag and grant no NVKR Release
authority; the selected controller retains its source-owned legacy per-node compatibility form.
The owning catalogs inspect Debian package names and architectures before accepting
outputs. The disk uses the renamed Packer build, artifact path and material schema.
The 32-output demo and 38-output local conformance selections share the same deployment
closure. The native site catalog derives its four acquired chart pins from that
source's `deploy/nico/nvke-deploy.yaml`; source-local controller and NKTP chart
names and versions come from their exact path-bound `Chart.yaml` bytes. An explicit accounted `chartName`
binds a reviewed local chart rename; otherwise its name must match the release declaration. External member
names and repositories must match their reviewed acquisition identities. Contract tests cover reviewed and
unreviewed renames, unsafe metadata, exact path/version
checks and unchanged external selections; they do not establish installation or fresh runtime acceptance.
The native runtime-image variant
keeps its separately authenticated images and binds their selection to the same NVKE
source. Mac and Linux tools share the same NVKE source pins. Maintained release
and native demo selections use this release baseline. Nightly runtime-source
selection instead binds source-built products to the cycle's exact NVKE main
commit. Its ARM64 NVIDIA disk additionally uses the
`current-main-doca-ovs.json` image-inputs overlay, authored at
`82c8b7e8a3d4fa0ca301cb8eead4a6abea043755` and rebound to that same cycle commit.
The source-owned locked DOCA package acquisition runs before offline image
composition; this exception does not change the release or local-demo selections.
The native consumer's matching AMD64/ARM64 profile, Plan disk and host admission
is contract-tested, not evidence of an AMD64 hosted nightly. Selection grants no
Release authority; prior failed Runs remain failed.

The private Ubuntu attempt `ubuntu-9905-e2e-02` used consumer
`990519c1a53335390ce9464c3eee3d8135076b51`, this stock NVKE source and NVIDIA
OVN. Its recorded native result passed site/tenant readiness, per-node cloud-init
and workload/DNS checks with one site and three tenant nodes. Original conformance
capture completed with harness exit 0: 446 passed, zero failed or pending and
7,133 skipped, in 7,416.042 seconds. The original JUnit SHA-256 is
`f1a0ad57581388bdc770c0ed6a1b9cd9f96adc60f869f7194e78f7a319a5a909`;
seven additional passing suite hooks explain its 7,586 XML testcase records.
The frozen Plan includes the existing Hydrophone report-download patch,
SHA-256 `d0014a0f2e39033d5a63a5938b46793e908ccf2cdc1282c8cba1cc7ffe85770b`.
These are private diagnostic records, not published qualification evidence.
The foreground command's exit was not captured independently. Subsequent inventory
and the original CLI's network recovery confirmed no session VMs remaining and
restored network controls on 2026-10-26. This result does not validate later
consumer revisions or the startup configuration below.

The later private Ubuntu deployment attempt `ubuntu-startup-01` on 2026-10-26 used consumer
`453ec2a4fe961c37e4bcebeac793fdbe76c0443c` and reused the completed artifact/tool Runs
`ubuntu-9905-e2e-02` from the earlier source checkout; it was not a cold artifact build of that consumer.
Its original owner `128715751` reached `retained-ready` with one Ready site node and three Ready tenant
nodes at Kubernetes 1.36.3, after the native node, cloud-init and workload/DNS gates. Conformance capture
`nvkr-conformance-2646420987` completed with harness exit 0: 446 passed, zero failed or pending and 7,133
skipped in 7,577.002 seconds. The original JUnit SHA-256 is
`0f3a66ad97caf60347adc56d1e20a4e71fa881d6ddabc72cc100f8197bd389e0`.
This private diagnostic uses the stock NVKE source and Hydrophone patch identified above. The foreground
exit code was not captured independently. It does not establish a fresh Mac build or later consumer acceptance.

The private Mac attempt `mac-readiness-01` at consumer
`8e432f23a47f9934fcff7be523c942aa2742635b` used the same stock NVKE source,
TCG, a 12-GiB site and two 6-GiB tenant selections. Its site API became Ready,
but controller installation failed before tenant launch. The foreground exited
17 at `host-nvke-deploy`; the retained owner reported `held-partial` with no
completed Ready check. Kubernetes recorded a liveness-probe restart during the
first controller startup, followed by persistent migration-lock conflicts.
Read-only inspection found 35 of 71 ARM64 migrations applied; no database repair
was performed. The original failed invocation remains failed. A fresh Mac
site/tenant run is required to validate the startup configuration below.

Default native Mac BUILD explicitly selects TCG and `cpu_model=max`. The source
owns its ARM64 64-KiB kernel and image-composer network files; the native catalog
adds no kernel override or duplicate network archive members. The existing HVF
preparation remains available for separately selected compatible source inputs.
Plan/capture contract tests do not establish a fresh QCOW build. Latest-source
Mac simulator networking/TPM probes and adapter tests remain bounded checks;
a complete selected-image guest boot, site/tenant deployment, conformance and
joined cleanup remain unverified. BUILD does not start those deployment paths.
An October 19 bounded GET for the preceding `8040adb` SPIRE package's exact
ARM64 DSX IMDS binary returned HTTP 403. That remains historical evidence for
that source selection. The current release's
[SPIRE package](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/os-image/packages/spire-agent/Makefile)
uses revision 4 and its
[locked public SPIRE 1.14.1 and TPM plugin 1.9.0 archives](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/os-image/packages/spire-agent/lock/1.14.1%2Btpm1.9.0.lock).
That default package does not acquire or provide the DSX IMDS plugin. Native tenants
select join-token attestation and the site uses TPM. This is the selected release's
packaging contract, not an older-source workaround or complete native runtime acceptance.

The shared native host tools select kubectl 1.35.9 on both platforms. This is
within one minor of the temporary Kind 1.36.1 bootstrap API and the permanent
site/tenant 1.36.3 APIs; the separate conformance client selects 1.36.3. The
complete 32-output catalog smoke-policy fixture is explicitly deferred by a
named skipped subtest. Full catalog compilation and production smoke admission
remain required; no version stamps or smoke policy are weakened.

The Mac address adapter reads actual kernel neighbor bindings for MACs in the
fresh private NICo database and projects them through the backend's configured
address-file interface. The existing pinned Go routing codec and system SQLite
read-only CLI own their formats. No host DHCP change, simulator DB mutation or
manual readiness update is used. Initial ambiguity is rejected; after NICo records
its primary address, only that still-observed address is projected, without
selecting another alias. Controlled tests cover binding scope, primary preservation,
missing observations, ambiguity, cancellation and join; full native VM acceptance
of this adapter remains pending.
Linux keeps its existing owned dnsmasq path.

Native cleanup retains the pinned backend's serial files before deleting its
instance: Darwin's `console.log`, or Linux libvirt's `disk.qcow2.ttyS0.log` and
`disk.qcow2.ttyS1.log`. Existing no-symlink file access and bounded context-aware
copying retain the observed prefix even if a live log appends; this is descriptive
diagnostic text, not immutable artifact evidence. Copies remain private and never
replace original retained files. This corrects diagnostic loss, not a demonstrated
guest-startup failure or runtime acceptance.

The demo CLI automatically selects a terminal dashboard for interactive standard
input/output. Its phase map, observed state and bounded public activity use typed
owner events, not log-derived authority; narrow terminals stack the panes. `--plain`,
pipes and redirected output retain scrolling progress; `--verbose` builds use plain
public-log streaming. Tab changes focus, PgUp/PgDn scroll activity and End returns
to live activity. Tenant stage, count and closed condition changes add activity
immediately; unchanged reported waits add a heartbeat at most once per 30 seconds.
These reports do not prove successful API polls or guest progress. The tenant pane
now prioritizes its blocker, four observed checkpoints, role-bound VM/console
metadata, node counts and independently aged observations. Bounded polling joins
the exact tenant Machine/NicoMachine pair to an owned libvirt domain before
describing its state or console-file size. Only positive growth of the same
file identity counts as activity; baseline, replacement and truncation do not. A closed
role-bound `InstanceCreateFailed` report remains visible before a domain exists.
Unknown bindings, states and reasons cannot advance readiness. No private console
text, instance identifiers or provider messages are projected into public output.
Controlled observer and simulation tests cover binding substitutions, unsafe
files, unavailable observations, cancellation, privacy and 80×24 layout; this
observer has not yet been exercised in a fresh live E2E. Fixed Packer summaries
expose selected boot, transfer, provisioning and post-processing reports without
copying variable paths or addresses. An opted-in, attempt-bound read-only observer
now samples bounded OS build-log metadata into a separate private descriptor
channel. The dashboard accepts it only for the exact reported running build
operation and distinguishes sample age, aggregate bytes and same-file growth
from execution health. Baselines, replacements, truncation and rejected entries
do not establish activity. Names, console text and paths remain private; this is
not a live console viewer, progress receipt or ETA. Controlled tests cover bounded
frames, pipe backpressure, descriptor isolation, collector stop/join, unsafe file
entries and compact layouts. An unjoined stop or attachment fails the build;
bounded diagnostic collection is still attempted. Retention keeps the exact
observer, its work volume, identity workspace and runtime executable/configuration.
Cleanup still attempts independent containers and the input volume and closes
completed staging descriptors. A joined observer's failed removal is retried by
the final reaper; persistent cleanup failure rejects any collected artifact.
Controlled native Linux race tests exercise these cases, not termination of
kernel-blocked runtime launches or successful cleanup under every daemon failure.
Fresh live build acceptance remains unexercised.
While the dashboard owns input, q and Ctrl+C ask for confirmation;
only y/Y requests cancellation. Escape closes details or dismisses confirmation,
never canceling work. Plain/verbose output and the initial sudo prompt retain
signal-driven cancellation without a dialog. Normal cancellation waits for owned
work and cleanup before terminal restoration. During cancellation, f then y
restores the terminal and exits 130 without joining cleanup; resources may remain
and no success is claimed. The exit warning includes the configured workspace
and actual executable as recovery guidance only; it does not prove admission,
journal existence or producer ownership. Recovery requires the original journal
and stopped owned producers and restores only recorded network controls. Renderer
panic, output/input failure and a closed event stream restore the terminal before
waiting for cleanup, while preserving the mandatory owner join and failure.
Normal confirmed cancellation retains its cleanup screen.
Plain guest summaries suppress unchanged
snapshots and independently advancing ages.
Failed deployment stops unfinished active panes without
claiming that their individual checks failed. [CLI contracts](../../../cmd/nvke-demo/dashboard_test.go)
and [simulation-screen tests](../../../cmd/nvke-demo/dashboard_view_test.go) exercise
layout, bounded/sanitized snapshots, concurrent reports, cancellation joining and
terminal failures. These are controlled presentation/race contracts, not a completed
fresh E2E or live interruption acceptance for this candidate.

The downstream demo propagates its work deadline through payload transfer,
bundle verification/extraction, host compilation, site startup, DNS rollout,
workload completion and the conformance wrapper. The retained launcher records
that deadline as private scheduling data and its owner preserves it after
detachment; it grants no persistent execution or ownership authority. Generated
guest preparation enforces the absolute deadline through a process-group timeout
with bounded termination grace. Context/transport and real-subprocess tests cover
deadline propagation, cancellation and joining; these do not establish a fresh
E2E or conformance result. Connection/probe bounds, identity validity, independent
cleanup and authored/upstream producer policies remain separate.

Read-only native readiness queries retry an independently bounded command timeout
only after the command owner confirms joined execution and successful diagnostic
drains, while the original deployment deadline remains active. Partial timed-out
responses cannot establish readiness. Caller expiry/cancellation, unjoined work,
simulator exit and changed ownership still stop the attempt; mutation commands are
never replayed. Controlled real-subprocess tests cover timeout recovery and caller
deadline precedence on both hosts; fresh runtime acceptance remains separate.

Site installation uses the selected NVKE installer's existing Helm post-renderer
handoff. A private Helm subprocess plugin delegates YAML transformation to the
selected kubectl's Kustomize, targeting only the site controller Deployment and
container. It adds a startup probe before the unchanged liveness/readiness
probes and derives the startup allowance, Deployment progress deadline and Helm
timeout from the caller's remaining work budget. The original process context
still bounds the whole installation. This prevents the source controller's
migrations from being interrupted by its HTTP liveness check before the HTTP
listener starts; it neither changes NVKE source nor repairs an abandoned
migration lock. The plugin uses private per-invocation scratch, removed after
its joined subprocess returns. Contract coverage and offline rendered-resource
comparison do not establish fresh Mac deployment acceptance.

The interactive Linux CLI authenticates before opening the
dashboard or starting work, then refresh its existing sudo session using bounded
non-interactive checks while that workflow runs. Deployment and independently
bounded cleanup must not open password prompts. Redirected commands retain the
cache-independent unattended check. No password is handled by the Go reader or
diagnostic writer, no sudo policy is modified, and privileged operations are not
replayed after failure. The initial prompt names its deadline and hidden password
input. Failure diagnostics distinguish deadline expiry, cancellation and other
authentication failures without asserting that a password was submitted or
accepted. Controlled tests and targeted race checks cover authentication before
work, periodic refresh failure, cancellation and refresher joining, including
preservation of unjoined-child errors. Isolated Linux ARM64 PTY tests exercised
foreground ownership/restoration, dashboard checks without suspension and actual
same-terminal sudo validation/refresh. Validation/refresh wiring passed on a non-interactive
sudo-policy host; it does not prove timestamp-cache use, a real password policy, hour-long credential
retention or a complete fresh deployment with this change.
Interactive refresh uses ordinary sudo timestamps rather than a helper-scoped
credential: policy may allow other terminal-session processes to reuse it for
their full sudoers command scope. The host policy is unchanged; this route
requires a trusted execution host, not an untrusted launcher sandbox.

Both native and legacy tenant smoke paths use the same readiness gate for the pinned CoreDNS Deployment and
node-local DNS DaemonSet rollouts, within four minutes and the remaining tenant
deadline. Node Ready alone does not establish DNS rollout readiness. The
unchanged zero-retry workload still has to prove real cluster DNS and ARM64
execution. The explicit Kind/Calico diagnostic additionally requires the tenant
Calico node DaemonSet rollout before those DNS checks, within four minutes and
the remaining tenant deadline. A deployment-only replay observed both nodes
Ready and DNS rollouts passing while Calico was still unready; its unchanged
smoke then failed on DNS timeout. The added CNI admission is not proof that the
underlying datapath is fixed. Controlled failure/cancellation tests cover these
gates. An owned-subprocess fixture exercises the actual tenant orchestration:
CNI/DNS rollout failures and canceled waits prevent the single smoke submission.
Kubeconfig, Ready-node, CNI and DNS waits retain their named stage and private
diagnostic location; cancellation or a tenant deadline retains the timeout code
at the public demo boundary. These fixtures do not start real guests or establish
functional DNS. An author-reported local
continuous invocation at `bda67f0` subsequently
observed both ARM64 tenant nodes Ready, both DNS rollouts and the unchanged
workload/DNS smoke passing, but exited 17 at `tenant-deletion-wait`. Its original
recording remains failed, not a successful E2E video. The downstream three-minute
deletion wait is shorter than the selected NVKE teardown's worker/control-plane
waits; the last actual teardown phase was not retained, so a timeout-budget
mismatch does not by itself establish the runtime cause. Complete fresh E2E
acceptance, including successful NVKE deletion and owned cleanup, remains open.
Cleanup now best-effort retains three bounded private command-output slots and
the final command status in one exclusive private observation file. A canceled
empty poll preserves the last useful inventory. Controlled tests show that
storage failure neither prevents deletion nor changes its result, and no raw
cleanup response reaches public progress. Tenant cleanup now has an explicit
one-hour ceiling with a 45-minute disappearance wait, six-minute diagnostics,
two-minute delete request and two-minute guest-route withdrawal. The subsequent
owned host-runtime cleanup retains its independent eight-minute bound. This finite
downstream policy gives room for the pinned worker-then-control-plane teardown;
it is not an upstream maximum because retries and other activities can extend it.
Controlled deadline tests cover timeout/cancellation and exact ownership. The
later deployment-only replay naturally completed NVKE deletion and owned cleanup
with the longer budgets, while preserving its failed smoke result. This does
not establish a fresh successful E2E; that acceptance remains unproven.

[Ordinary adapter tests](../../../internal/nvkedemo) cover controlled ownership,
journal, cleanup, artifact and tenant-description cases. Runner tests exercise
bounded subprocess cancellation and joining; CLI tests exercise output-failure
cancellation with controlled callbacks. The live cluster projection now crosses
the concise CLI filter as an owner-generated public result rather than subprocess
log text. Composed `run`/`e2e` and dashboard tests cover immediate tables,
activity eviction, historical post-terminal replay and later cleanup failure.
Retained-owner progress carries the same owner-generated table through its
existing authenticated private control as a single typed result, with unchanged
32 KiB queue/envelope and sequence-gap refusal. Mixed payloads and oversized
results fail before presentation. JSON-relay contracts and native retained
launch fixtures cover typed/plain output and cursor/error behavior; their
synthetic cluster answers are not fresh retained NVKE runtime acceptance.
Synthetic Kubernetes answers establish presentation wiring only, not fresh NVKE
cluster acceptance. DNS/DHCP port admission now invokes a
fixed Go helper through the existing bounded sudo runner before recording host
intent; executable admission precedes artifact inspection. Controlled tests
cover fixed targets, rejected helper arguments, failures before journaling and
child error propagation. This does not prove a fresh tenant deployment.
Set-ID and replaceable-ancestor refusal run in the ordinary helper contracts.
Foreign executable ownership requires the isolated Linux root fixture; the real
TCP/UDP 53 and UDP 67 collision rows additionally require native ARM64 root.
Ordinary hosted tests do not establish those privileged socket observations.
These tests do not prove real SIGINT during
NVKE stages, privileged sudo/libvirt failure, runner/VM restart, or cancellation
at every build/deployment phase. Network-only recovery retains version-3 journals
for the legacy host Kind bridge and version-4 journals for native host-originated
bootstrap transport without a host Kind subnet. The latter rejects Kind/BGP bindings,
Kind forwarding and raw Kind rules; it never infers host grants from a Docker subnet.
Recovery never kills serialized PIDs; restore legacy live controls before upgrading.
A full tenant crash/restart and recovery matrix remains unverified.

The native Linux bootstrap adapter now reuses the private temporary Lima owner,
with explicit QEMU/9p, reviewed source version and singleton image/archive pins;
Mac retains its separate platform prerequisite. Version admission precedes BUILD.
The private upstream `nerdctl.lima` transport uses Lima's supported
`--log-level=error` option through an invocation-owned PATH adapter. This keeps
Lima informational/version warnings out of NVKE's combined image-inspection
output; guest command stderr and exit status remain unchanged. Direct Lima
version, startup and cleanup diagnostics remain intact. Real subprocess
contracts cover exact digest output and guest failure propagation for both
platform bindings. This does not change NVKE sources or establish fresh E2E
acceptance; NVKE's combined-output parsing remains an upstream limitation.
The owner retains each concrete readiness process until joined, stops polling on
an unjoined group and preserves dependent controls when cleanup cannot join it.
Contract tests cover both platform bindings, version refusal and retained-probe
cleanup. The opt-in backend-only pilot checks restricted mounts and attempt-bound
loopback forwarding, retaining rendered configuration and private diagnostics.
One approved private pilot passed actual KVM, exact rendered material selection,
mount and loopback forwarding checks, with natural exit 0 and joined removal of
its VM/state/sockets. Native gateway traffic and complete demo/conformance
acceptance remain separate runtime obligations; see [native Linux bootstrap](../../native-linux-bootstrap.md).

[TestEndToEnd](../../../internal/nvkedemo/e2e_test.go) is explicitly opt-in; ordinary
tests do not build images, deploy tenants or mutate host networking. No durable
fresh-build/deploy acceptance record is supplied by this PR. Private logs,
retained Runs, chart renders and tool probes are not public operational evidence,
complete OS smoke, full-source completion, signing or qualification. Validate
the terminal result and cleanup of the exact invocation being accepted.

Attempt-scoped native toolchain substitution does not update maintained pins.
`publish chart` locally verifies/extracts a pinned Helm archive; diagnostic
registry copies do not constitute NVKR publication or Release authorization
(ADR-18/20). The current NVKE `bootstrap site` route is not a demonstrated
drop-in replacement for the Docker/Kind and NICoSim adapter.
