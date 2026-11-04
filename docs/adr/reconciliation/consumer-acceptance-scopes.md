## Consumer acceptance scopes

Current publication-root v4 binds the exact Definition, Environment and Plan identities. Optional consumer
expectations are checked before external work; the lifecycle owner authenticates the complete output match
and issues a policy/publication-bound opaque receipt. Current readers reject historical v3 roots, including
the Run 37533806963 demo release. That browsing record does not establish current-schema external acceptance.

A verified publication's held closure can be exported only with its owner's
exact-input receipt and expected aggregate invocation. Named binary/chart
extraction and image export retain delivery ownership. The downstream harness
uses the held publication directly rather than adding another command interface.

The standalone input-staging command and compile-only adapter are removed.
The [NVKE nightly workflow](../../nvke-nightly.md) now selects one public hosted
AMD64 job for build and native NVKR Go E2E. It freezes NVKE main once and derives
temporary source overlays through the existing catalog/local-input owners before
the normal toolchain preparation. The existing AMD64 NVIDIA Definition/catalogs
remain the authored selection. The v0.23.0 image-input producer retains its explicit
DOCA material edge; source freezing preserves commands, materials and toolchains.
Source preparation loads each distinct catalog stack once within its bounded
invocation, retaining full per-output overlays and graph validation. Its real-Git
regression checks that shared outputs do not repeat the pinned metadata fetch;
there is no cross-invocation cache or change to the five-minute deadline.
The runtime NVKE source freeze leaves committed catalog
inputs unchanged; the native helper self-pin follows its separate maintenance owner.
`TestEndToEnd` builds fresh local Runs, bootstraps the real site
VM through kind/Lima, checks readiness and tenant workload/DNS, and joins cleanup.

Ordinary manual branch dispatch and the daily 03:17 UTC trigger are retained.
There is no promotion helper, lock ref, NVIDIA runner, protected environment,
repository secret, signing/publication permission, Actions upload or shared cache.
Build output and ordinary test outcomes enter the public job log; raw deployment
errors and identity values remain private on the disposable runner. The retired
ARM64 build/publish/consume nightly and its legacy consumer remain uninvoked.

The six-hour job passes its remaining deadline to Go, which reserves two hours
for independent cleanup. Live KVM, disk and available memory are prerequisites.
The explicit small profile selects a 7-GiB site, 3 GiB per tenant, four vCPUs and
1 GiB headroom. Initial admission requires 14 GiB; the temporary 6-GiB bootstrap
backend joins before tenant creation rechecks 7 GiB beside the site. This is a
functional-test allocation, not a minimum hardware or OOM-causality claim.
The small profile also sets the site's system reservation and the tenant's
Reserved/SystemReserved/KubeReserved memory to 256Mi, appends the guest kubelet
`--kube-reserved=cpu=1000m,memory=256Mi,ephemeral-storage=1Gi` override and restarts
it, sets OVN control-plane/node-zone memory requests to 128Mi, and adds 64Mi
memory requests to the seven site telemetry collector roles and three target
allocators. Recorded small-profile runtime acceptance covers these settings,
not the default reservations.

Runtime fixes admit the installer's legacy/current controller identity, bounded
writable helper staging with source-supported shell, unchanged pinned-context
readiness convergence, current/retained image-URL placeholders and unfiltered
pool inventory with exact tenant ownership. Original private error capture,
identity rejection, actual host cloud-init/workload/DNS acceptance, unknown-completion
retention and natural tenant deletion/wait remain enforced. The existing deletion wait
can retry owner-classified joined read-command exit-1 failures after deletion starts,
without repeating the mutation. Fresh valid absence remains required; malformed
inventory, changed ownership and unsafe or canceled work are not retryable. Temporary diagnostic
classifiers, failure-only health/curl probes, console drop-ins and the resource
sampler are removed; required behavior retains focused regression coverage.

[Run #38](https://github.com/depohmel/nvkr/actions/runs/41684210103) completed on
November 3, 2026 (New York) on exact NVKR `6c9c0ec4b82876db5b9db9bdcf421a7c48f1fcbd`,
with frozen NVKE `cd0093ec4db83447f4cbab423d2948b75427d6af`. Fresh local builds,
identity policy/handoff/final proof, site and tenant CP/worker checks, actual host
cloud-init and exact workload/image/marker/DNS acceptance, natural deletion and
waiting, and joined VM/backend/DNS/network cleanup all passed in one attempt.
The parent passed in 5673.26 seconds, site in 7.90 and tenant in 468.92. All
post-steps succeeded and Actions artifact count was zero. Raw marker/host output
remained private; the passed tenant path enforces those checks. This record
covers that exact revision, including diagnostic removal, review fixes, the v0.23.0
main integration and preservation of its DOCA graph during source freezing.
It does not establish acceptance of later commits,
prove that earlier failed reads recurred, or resolve their private causes.

Trusted source code retains sudo/Docker access on the disposable hosted machine.
These unsigned local diagnostics grant no conformance or qualified release acceptance.

The archive correction selects source-built tarball assets in executable bundles
through exact resolved-component and producer/output membership. Helm and disk
inspection retain their separate paths. Bounded-reader, serialized-shape and
executor/finalization contracts cover exact subjects and reject method/scanner
substitution. A separate Linux ARM64 diagnostic reconstructed the simulator archive
from pinned source `6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4` with the catalog's
epoch `1791419815`. The archive digest was
`sha256:dcdcf029cb3f2b72e06b9de987ab32c2ecb2e83ae73b92e1eca64235d21af5c6`;
the owning API emitted a validated `archive-content/v1` document with 13 regular
file members and no scanner claim. This was a reconstructed input, not a downloaded
CI artifact or proof of hosted publication. Its file hashes do not establish
package versions or transitive dependencies.
Site-specific NICo provisioning targets and the special TestGrid publisher remain deferred.
Unsigned runtime reports do not mint qualified conformance or promotion receipts.

Consumer Verification Policy binds `minimumDiskEvidence` into its typed digest. Pipeline-attested policy
defaults to `build-provenance-v1`; explicit `strict` requires inspected-disk evidence. Qualified policy
defaults to strict and rejects the producer-only minimum. Every disk in the authenticated closure must
meet that policy before a capability is issued. Synthetic publication contracts cover enforcement and
failed-policy cleanup; they do not establish production disk-release acceptance.

Consumer authenticity uses the same pinned reference and policy with a separate closeable
`AuthenticatedRelease` capability. It validates the complete metadata/evidence closure, authenticates the
root and every required named output's signature and subject-bound production provenance, and verifies
required SBOM bundles. It applies the disk-evidence minimum without downloading or reinspecting artifact
payloads. Its description reports `payloadIntegrityVerified: false`; it grants no payload export,
exact-input receipt, qualification or revocation acceptance. Contract fixtures cover rejected signatures,
missing or substituted evidence, whole-release membership, policy enforcement, capability separation and
zero payload-layer retrieval. These are controlled contracts, not a recorded external authenticity run.

Consumer signature, provenance and SBOM bundle reads resolve registry access like the consumer's payload
reads. With neither `--anonymous` nor explicit transport credentials, the bundle reader consults the same
default keychain; an explicit signing credential still takes precedence, and explicit transport passwords
are not widened to it. Bundle verification by cosign stays offline against an empty private Docker
configuration. A registry 401 or 403 on the referrers index reports `NVKR-E-FETCH-FAILED`, distinct from
`NVKR-E-SIGNATURE-INVALID`. A denied candidate does not end the search: later valid evidence still
authenticates, and with none the last candidate failure sets the code, so a sole denied candidate reports
`NVKR-E-FETCH-FAILED` while a later malformed candidate reports `NVKR-E-SIGNATURE-INVALID`. Contract tests with an `httptest` registry cover keychain-authenticated
reads, anonymous-only reads with and without a keychain, a missing or rejected keychain credential, and
the credential-plus-keychain conflict. No private-registry consumer verification has been re-run since
this change; the earlier private GHCR readback that failed with `NVKR-E-SIGNATURE-INVALID` remains the
only external record.
