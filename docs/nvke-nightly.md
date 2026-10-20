# Nightly NVKR builds and NVKE end-to-end tests

The requested flow is a scheduled GitHub Actions pipeline: build and publish a
complete candidate with NVKR, authenticate that exact publication, then deploy
and test it with NVKE. The first runtime target uses kind as management and real
KVM guests as tenant nodes on a dedicated Linux runner. Local describes the test
environment; GitHub Actions schedules and executes the entire cycle.

The coordinator is `.github/workflows/nvkr-nightly.yml`, with a daily 03:17 UTC
schedule and manual dispatch. `inputs/testing/nightly-profiles.json` selects the
project Definitions, named generated producer workflows, exact suite revisions
and runtime profiles. Each project selects its own complete output set. The
ordinary generated M2 workflow retains its default paths; named bundles use the
existing generator and manifest owner. The coordinator is authored separately.
The daily trigger is retained as explicitly requested by the user.

Binary carriers, source handoffs, OCI images, charts and disks use private CI storage under
`ghcr.io/dsx-ai-factory/nvkr/ci-artifacts`; Actions retain only their small location-and-digest records.
The existing NVKR helper owns this transport. Uploads and downloads check package visibility and its
repository association; any link or public/unknown access policy stops the transfer. A failed private transfer never
falls back to uploading the payload to Actions. Final release artifacts retain the profile-owned nightly
publication prefixes below the Environment's configured registry path.
Their GHCR destination packages receive private/internal visibility checks and reject public or unknown repository links
using the publication credential before product uploads; the checked metadata root binds the actual push.

Configure dedicated `NVKR_CI_STORAGE_READ_TOKEN` and `NVKR_CI_STORAGE_WRITE_TOKEN` secrets with access
only to the unlinked private/internal CI package. Set repository variables `NVKR_CI_STORAGE_READ_USER`
and `NVKR_CI_STORAGE_WRITE_USER` to the corresponding token owners. Transfer never falls back to the workflow token. The
nightly consumer and qualification reader receive only the read credential. Before making NVKR public,
verify independent package grants and actual anonymous, unrelated-user and fork access behavior.
Previously retained binary Actions artifacts, caches, active runs and old workflow refs require separate
handling; none is migrated or deleted by generation. Private transport contracts do not establish a
successful hosted transfer or public-repository readiness. Provider locator retention does not expire
private carrier blobs; registry retention is separate. The public runtime attachment contains the constructed
`cycle.json` summary only; raw suite reports are not attached.

Both nightly Environments opt into runtime Plan preparation. Each producer's first bootstrap reads the
current catalog pins from that dispatch's exact repository commit and freezes fresh Plan data. Other
helper bootstraps restore the same data and verify it against those inputs; every producer job and the
finalizer then receives it with its native helper. Source-version updates within the reviewed execution
contract need no regeneration commit. Changes to workflow/action bytes, member paths or Environment
policy still fail admission and require reviewed regeneration. This retains pinned inputs throughout
the cycle and does not follow newer versions after the build starts.

This is downstream repository CI test infrastructure outside NVKR's public
product lifecycle and v1 acceptance. It leaves M3 build/evidence qualification
and ADR-16's passive conformance boundary unchanged. Its schedule is authored
configuration, not evidence of a successful cycle or organizational approval.
The daily trigger is enabled. The first scheduled attempt is recorded below;
a successful combined cycle and its runtime cleanup verdict remain unrecorded.

## Runtime and tool bindings

The two initial profiles select NVIDIA-fork OVN and upstream OVN ARM64 candidates
for kind management plus real KVM tenant VMs. Selection validates the maintained
profile, exact suite revision, native runtime target and network. There is no
self-declared security approval field. Artifact authentication, source matching,
native-host preflight and owned-resource cleanup remain required.

Both Definitions select the profile-owned site catalog at NVKE `v0.22.0-beta.6`,
commit `8db5721ae3031bdcd945e559153a740d46ea209e`, for the CLI, controller image,
controller chart and NKTP chart. The test-only suite checkout selects
`a32067108ab89d1e2b2b6720766ffe97270a166a`.
The OS closure retains SPIRE at the explicit pre-DSX exception commit
`726f76944404e1106626b8333d3576bdf67b575b`. Clusterctl selects 1.13.6 to match the
selected installer. The separate test pin is a user-approved exception to ADR-23's proposed
matching-revision rule; it establishes no runtime compatibility or broader acceptance.
ADR-23 remains proposed and excludes CI trigger, runner and credential policy.

The adapter validates authenticated profile bytes and the exact selected disk through the
same profile projection as the local tenant flow, then emits `MachineImage`, `ClusterRelease`
and `NodePoolRelease` resources under `internal.nvke.nvidia.com/v1`. It preserves the full
disk digest, boot script and user-data template, retaining the candidate suite's fixed profile
name in its fresh management cluster. No source profile-converter executable is required.
The site specification uses the current `cluster` and `controllerStack` shape with private
credentials. These adapter paths have contract tests; they have no recorded runtime cycle.

The selected test source retains `controller/hack/ci/nico-vms-candidate.sh`, which the selected product source
omits. This entrypoint accepts the authenticated candidate artifacts and reports cleanup.
The generic suite's product rebuild and OS-tag download remain forbidden fallbacks.
The beta CLI validates and renders the selected test source's deployment templates in a
local contract check; a combined runtime cycle remains unverified.

The chart producers use the existing registry-addressable Go image and acquire
Helm 4.1.4 by its existing operator-catalog archive and executable digests.
Vendoring captures that executable and the pinned chart dependencies in the
source closure; packaging uses the captured executable offline. Installer Helm
4.2.4 and operator Helm 4.1.4 retain their separate acquired-executable bindings.
The independent runtime verifier is checksum-pinned Cosign 3.1.3 for Linux ARM64.
Generated source-fetch and disk jobs use the existing read-only preparation step
for the selected private M2 toolchains. Its temporary registry credentials are
removed before source or producer execution.

The existing repository vulnerability gate remains required. ADR-13 keeps
artifact vulnerability scanning and vulnerability clearance outside the v1
product scope; it grants no executed-tool security approval. Binary findings
recorded for Helm 4.2.4/4.3.0 and Cosign 3.1.3 do not constitute a clean scan or
an organizational disposition. A 2026-10-14 00:40:29 UTC diagnostic of the exact
Helm 4.1.4 Linux ARM64 executable verified both catalog-pinned archive and binary
digests before inspection. Pinned `govulncheck v1.8.0` in binary mode reported
64 symbol-level vulnerability findings across four modules and the Go standard
library, using `https://vuln.go.dev` updated 2026-10-08 22:31:09 UTC; its exit
status was 3. Helm was not executed. This observation is not a clean result,
exploitability assessment or security-owner disposition. Raw diagnostics remain
outside the repository. The adapter retains exact tool pins and existing checks
without inventing an approval marker or asserting security clearance.

Existing runner and tool functionality has already been exercised. NVKR's
[ARM64](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963/job/112511485529)
and [AMD64](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963/job/112512054661)
disk jobs completed on their ARC KVM pools, and that historical Run completed
[release signature verification](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963/job/112539131258).
NVKE's existing
[kind plus ARM64 VM job](https://github.com/dsx-ai-factory/nvke/actions/runs/38459969744/job/115512893269)
also completed on `arc-arm64-kvm`. These results establish those capabilities.
The new combined publication-to-runtime nightly cycle has not yet passed;
historical NVKR records do not establish current-schema Release acceptance.

Site-specific NICo deployment profiles remain subsequent work. Each needs its
own component closure, runtime configuration, access, usable capacity and cleanup
ownership. The special TestGrid integration remains deferred.

## First scheduled attempt

The [2026-10-15 coordinator](https://github.com/dsx-ai-factory/nvkr/actions/runs/38676875690)
started at 03:33:24 UTC on commit
`b77229c2cc7bedfc422af506c1cdfbbb655aba48`. Both producer workflows failed,
so runtime tests were skipped. This attempt provides no VM E2E or runtime
cleanup result.

The [NVIDIA-fork producer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38676906834)
failed fetching the private OVN fork. The Environment omitted that repository's
source-access binding; the existing organization token was not passed to those
fetch steps. The binding now selects the same `NKE_SOURCE_TOKEN` for that exact
repository, with regression coverage for private and public OVN fetches. This
fix changes credential delivery, not the token's permissions.

The [upstream producer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38677610324)
independently failed because the generic NICoSim seed/UI archive was sent to
the Helm metadata reader. Its disk job also failed its KVM admission check on
that attempt. A later runner repair was reported by the operator; no rerun of
this combined cycle is recorded here to verify that repair.

Selected source tarball assets in executable bundles use a bounded archive-content
inventory only when their component and producer/output bindings match the Plan.
The inventory records member paths and hashes. NICoSim's seed/UI archive uses that path;
Helm archives retain their strict metadata reader. The archive method records
file composition without claiming package discovery. The pinned Syft 1.51.1
scanner returned no packages for the source-selected simulator seed/UI archive
in a separate diagnostic, so treating it as a generic Syft scan would not supply
the required nonempty inventory. Neither that diagnostic nor local contracts
establish a completed hosted producer or E2E cycle.

## One cycle

The following is the required candidate cycle. The separately pinned test harness supplies
the candidate hook. A successful combined producer and runtime cycle remains unverified;
the earlier producer failures and reported runner repair above do not establish that result.

1. Select a maintained profile on the default branch.
2. Dispatch its maintained generated producer once. Retain the exact run ID
   returned by dispatch; never use a latest-run search or retry an ambiguous
   POST. Join that run while checking repository, commit, workflow and attempt.
3. Download only that completed run's named lifecycle and publication locator
   artifacts and, for runtime Plan profiles, its frozen `nvkr-runtime-inputs`.
   Run records, locators and the frozen Plan remain descriptive until verification.
4. Authenticate the complete publication, signing identity, production
   provenance, outputs and exact Definition, Environment and Plan. Export held
   bytes only for the originating invocation. Extract native tools, images,
   charts and disk through existing lifecycle and delivery owners.
5. Use a private checkout at the exact pinned NVKE suite revision. Require its candidate
   adapter before executing runtime work. Project authenticated profiles into current release
   resources bound to the selected disk. Produce the suite's
   `release-description/v1` input through NVKR's existing export renderer over
   the held publication; the description adds no verification authority.
   Execute the source-owned
   `nico-vms-e2e.sh` candidate path, without rebuilding product components or
   selecting newer tags. Initial coverage is `(nico-vms && ovn) || health`:
   controller health/readiness/version and two-node OVN tenant tests. This does
   not claim every existing suite or lifecycle operation runs.
6. Require nonempty executed specs, passed assertions, observed KVM domains and
   successful owned-resource cleanup. Join the disk service and restore
   journaled network controls separately. Retain public outcomes and redacted
   reports. Private credentials and guest disks are not report inputs.

The `runtime-test` job requires `cycle.json` to report the selected profile,
exact coordinator run, attempt and commit, with `phase: runtime` and
`runtimePassed: true`. A missing or skipped integration test cannot satisfy that
postcondition. Public outcomes use the `nightly-runtime-report-` artifact prefix;
the name conveys no qualification authority.

NVKE owns guest/cluster provisioning, assertions and guest/cluster teardown. The
parent repository harness owns host admission, serialization, outer leases,
the disk service and journaled network-control restoration. Each owner must
complete cleanup before the parent reports a successful cycle.

The harness is a Go integration test under `internal/nvkedemo`, invoked directly
by the workflow. There is no new staging command or standalone user-facing test
binary. NVKE compiles and executes its own Go suite during the runtime step.
Its existing deployment dependencies remain required; this change adds no
Python implementation.

This test entrypoint is a non-authority repository diagnostic, not a supported
NVKR product runtime API. The pinned NVKE code is fully trusted: sudo and the
Docker socket give it root-equivalent host access, and the shared UID can read
the parent's read-only job token despite child-environment scrubbing. Its
self-written outcomes are not independently authenticated evidence. Private
workspaces and separate jobs do not change this trust boundary.

The publication lane remains `pipeline-attested`, with explicit
`build-provenance-v1` disk evidence. Runtime reports grant no qualified Release,
reproduction, promotion or independently signed conformance receipt. ADR-16
keeps artifact verification separate from external runtime assertions and
future qualified authorization.
