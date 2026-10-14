# Nightly NVKR builds and NVKE end-to-end tests

The requested flow is a scheduled GitHub Actions pipeline: build and publish a
complete candidate with NVKR, authenticate that exact publication, then deploy
and test it with NVKE. The first runtime target uses kind as management and real
KVM guests as tenant nodes on a dedicated Linux runner. Local describes the test
environment; GitHub Actions schedules and executes the entire cycle.

The coordinator is `.github/workflows/nvkr-nightly.yml`, with a daily 03:17 UTC
schedule and manual dispatch. `definitions/nightly-profiles.json` selects the
project Definitions, named generated producer workflows, exact suite revisions
and runtime profiles. Each project selects its own complete output set. The
ordinary generated M2 workflow retains its default paths; named bundles use the
existing generator and manifest owner. The coordinator is authored separately.
The daily trigger is retained as explicitly requested by the user.

This is downstream repository CI test infrastructure outside NVKR's public
product lifecycle and v1 acceptance. It leaves M3 build/evidence qualification
and ADR-16's passive conformance boundary unchanged. Its schedule is authored
configuration, not evidence of a successful cycle or organizational approval.
Merging the configured daily trigger enables unattended execution; the first combined
manual cycle and its cleanup verdict have not yet been recorded.

## Runtime and tool bindings

The two initial profiles select NVIDIA-fork OVN and upstream OVN ARM64 candidates
for kind management plus real KVM tenant VMs. Selection validates the maintained
profile, exact suite revision, native runtime target and network. There is no
self-declared security approval field. Artifact authentication, source matching,
native-host preflight and owned-resource cleanup remain required.

Both Definitions select the profile-owned site catalog at exact NVKE candidate commit
`04a6ed26c1e51d70167669a6882a04080eefadc7` for the CLI, controller image,
controller chart and NKTP chart. The suite checkout selects exact NVKE candidate revision
`17eee6e02541d20ffdb8611ffa3754a07f7d8116`, which includes the runtime
`^TestE2E$` selection fix. The intervening changes affect runtime test entrypoints
and unit-test fixtures; CLI, controller and chart producer code is unchanged.
Product source and consumer harness revisions remain separately pinned.
These are candidate revisions from NVKE PR #1580, not claims of merged-main
membership or human approval. Their exact source bytes remain required; a pin
does not guarantee future provider reachability or authorize the privileged suite.
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
The new combined publication-to-runtime nightly cycle has not yet been recorded;
historical NVKR records do not establish current-schema Release acceptance.

Forge YTL, AZ61, DSX and Redstone profiles remain subsequent work. Each needs its
own component closure, runtime configuration, access, usable capacity and cleanup
ownership. The special TestGrid integration remains deferred.

## One cycle

1. Select a maintained profile on the default branch.
2. Dispatch its maintained generated producer once. Retain the exact run ID
   returned by dispatch; never use a latest-run search or retry an ambiguous
   POST. Join that run while checking repository, commit, workflow and attempt.
3. Download only that completed run's named lifecycle and publication locator
   artifacts. Run records and locators remain descriptive until verification.
4. Authenticate the complete publication, signing identity, production
   provenance, outputs and exact Definition, Environment and Plan. Export held
   bytes only for the originating invocation. Extract native tools, images,
   charts and disk through existing lifecycle and delivery owners.
5. Use a private checkout at the exact pinned NVKE suite revision. Generate profile
   resources through NVKE's existing Go owner. Produce the suite's
   `release-description/v1` input through NVKR's existing export renderer over
   the held publication; the description adds no verification authority.
   Execute its existing
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
