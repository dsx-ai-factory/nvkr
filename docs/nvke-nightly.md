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

## Current admission and acceptance

Both initial profiles are **blocked in the coordinator before dispatch or native
resource allocation**. They declare NVIDIA-fork OVN and upstream OVN ARM64 candidates
for the kind plus VM target. These declarations do not establish working nightly
service. Current blockers are:

- Installer Helm 4.2.4 and the newest stable 4.3.0 assets fail the pinned binary
  vulnerability scan. Their source trees contain external fixture symlinks that
  the current hosted source transport rejects before vendoring. The repository
  has no admitted module-archive or fixture-preparation source contract. No
  transport relaxation or security waiver is supplied here.
- The independent Cosign 3.1.3 ARM64 verifier matches its official release
  checksum but fails the pinned binary vulnerability scan. A checksum alone
  does not admit it for execution.
- The site chart producers retain a bare local ARM64 Helm image ID
  (`sha256:8fae65b2d2366fe8370d8e10f34ef5c11c43a8341bb727bb48b2fa2b1035dbf0`),
  with no verified registry reference or hosted availability. An admitted hosted
  chart toolchain is still required; no replacement image or waiver is supplied.

Existing runner and tool functionality has already been exercised. NVKR's
[ARM64](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963/job/112511485529)
and [AMD64](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963/job/112512054661)
disk jobs completed on their ARC KVM pools, and that historical Run completed
[release signature verification](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963/job/112539131258).
NVKE's existing
[kind plus ARM64 VM job](https://github.com/dsx-ai-factory/nvke/actions/runs/38459969744/job/115512893269)
also completed on `arc-arm64-kvm`. Existing NVKE CI uses Helm 4.2.4 and has
executed its deployment and E2E scenarios. These results establish the existing
capabilities; the binary-scan findings above are separate from functionality.
The new nightly job retains its host and network preflight. Its combined
publication-to-runtime cycle has not yet been recorded, and historical NVKR
records do not establish current-schema Release acceptance.

Both nightly Definitions now select the profile-owned site catalog at reviewed
NVKE commit `04a6ed26c1e51d70167669a6882a04080eefadc7` for the CLI,
controller image, controller chart and NKTP chart. The suite checkout selects the
same exact revision. Source contracts, module metadata, chart locks and the native
profiles Dockerfile guard were rechecked; the explicitly older profile producers
retain their own source pins. This source alignment and bundle validation do not
establish hosted toolchain access, completed production builds or runtime success.

Changing an admission marker does not fix these blockers. Admission requires
reviewed pins, passing security and compatibility checks, generated bundle
checks, and a manual full cycle with both test and cleanup evidence. Forge YTL,
AZ61, DSX and Redstone profiles remain subsequent work. Their component closures,
access, usable capacity and cleanup ownership require independent admission.
The special TestGrid integration remains deferred.

Generated producer workflows retain their separate default-branch manual entry
contract. The coordinator's admission marker does not disable that entry or
establish security acceptance of its inputs. These candidates must remain
unactivated until their recorded prerequisites are resolved.

## One cycle

1. Select an admitted profile on the default branch.
2. Dispatch its maintained generated producer once. Retain the exact run ID
   returned by dispatch; never use a latest-run search or retry an ambiguous
   POST. Join that run while checking repository, commit, workflow and attempt.
3. Download only that completed run's named lifecycle and publication locator
   artifacts. Run records and locators remain descriptive until verification.
4. Authenticate the complete publication, signing identity, production
   provenance, outputs and exact Definition, Environment and Plan. Export held
   bytes only for the originating invocation. Extract native tools, images,
   charts and disk through existing lifecycle and delivery owners.
5. Use a private checkout at the reviewed NVKE suite revision. Generate profile
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

The harness is a Go integration test under `internal/nvkedemo`, invoked directly
by the workflow. There is no new staging command or standalone user-facing test
binary. NVKE compiles and executes its own Go suite during the runtime step.
Its existing deployment dependencies remain required; this change adds no
Python implementation.

The publication lane remains `pipeline-attested`, with explicit
`build-provenance-v1` disk evidence. Runtime reports grant no qualified Release,
reproduction, promotion or independently signed conformance receipt. ADR-16
keeps artifact verification separate from external runtime assertions and
future qualified authorization.
