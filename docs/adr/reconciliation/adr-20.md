## Support by owner

ADR-20's 2026-11-20 revision accepts separate destination staging and release-by-reference operations,
with repository-local GHCR/job-token access as the adopter default. Implementation still combines payload
upload with the hosted publisher. The stage step, full streaming readback receipt and release-by-reference owner are not implemented
or externally exercised. Current tests and recorded publications below do not establish the revised path,
zero-registry-secret adoption or release-root byte equivalence. The [ADR-18 migration gates](adr-18.md)
separate development disks, all outputs and residual input-edge removal.
The revised contract retains the post-sign verifier's own complete payload readback; no cross-process
readback-receipt protocol is specified. Dedicated stage-writer references and secret homes must be fixed
by the implementation before their profiles are admitted. Platform-token publication does not authorize
either production or development qualification's dedicated-credential pair.
The platform-token profile is admitted for the existing hosted publisher with provider-authenticated exact
package identity, publishing-repository linkage and private/internal visibility. `publication.allowPublicPackages`
defaults off; explicit `true` permits public packages only for this profile. The field is frozen into
Environment and Plan identity. Every metadata-derived output, evidence and release destination is checked
before payload writes. Missing packages fail without seeding. The dedicated GHCR profile retains its
private/internal and unlinked-package checks, including its existing metadata-only initialization.

The operator must disable inheritance and configure Write-only Actions access, retaining package administration
outside workflows. GitHub's public REST schema exposes neither Actions roles nor inheritance, and Packages
GraphQL does not support GHCR's granular permissions. Role/inheritance verification, operator attestations
and canary probes are not admission requirements. Admin misconfiguration is accepted operator risk; NVKR
does not prove a job token cannot delete or restore versions. Signed development continues to require
dedicated credentials; per-attempt setup for platform-token development remains deferred.

`TestPlatformPublicationPackagePolicy`, `TestPlatformPublicationChecksWholeSetAndRechecksVisibility`,
`TestIndependentPublicationPublicOptInCannotRelaxPolicy` and the hosted publication tests cover these
boundaries with local API fixtures. `TestPublicationVisibilityStrictLoadingAndIdentity` and
`TestPublicationVisibilityBindsPlanAndBundle` cover default-off decoding and frozen identity, including
unchanged logical Build identity. No hosted platform-token publication or live package-grant acceptance
is established by these tests.

The revised platform-token profile accepts repository-linked packages and fork exposure; independent package principals
and binary confidentiality from public-source forks are not promised. `NVKR_REGISTRY_WRITE` remains an opt-in
maintained-production/external-registry policy, requiring a distinct producer stage writer. Existing
production qualification and dedicated development profiles still enforce their current credential rules
until explicitly migrated. No maintained lane selects platform-token publication in this change; secrets,
package grants and retention policy are unchanged. Cleanup of unsigned staged objects must preserve active attempts
and retained signed release closures; the existing development cleanup evidence is not proof of that migration.

Post-seed GHCR privacy confirmation retries only API absence with bounded backoff.
`TestPrivatePublicationWaitsForSeedConfirmation` covers delayed visibility, success on the last attempt,
exhaustion with the original trust-policy code, explicit unsafe policy, denied requests and cancellation;
the whole-set check and no-repeat-seed boundary remain mandatory. `TestSeedConfirmationPreservesRateLimitBudget`
uses virtual time to cover a 120-second Retry-After and consecutive 60/120-second rate-limit waits
after an absent response. These are local API/registry fixtures.
The triggering [operator publication failure](https://github.com/dsx-ai-factory/nvkr/actions/runs/44577627888/job/135756330227)
establishes a successful metadata seed followed by API absence; eventual consistency is an inference
from that sequence. Retry outcomes and a corrected hosted publication remain unverified.

The Tekton route selects the existing per-output OCI publication owner with anonymous transport and explicit
cluster signing keys. Its complete in-cluster registry exercise remains pending; see [ADR-18](adr-18.md).

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 20 Publication | Digest-selected sibling repositories, signed evidence membership, native OCI/Docker subjects with consistent named-output aliases and independent evidence, aggregate runnable OCI indexes with exact native-child evidence, bounded metadata navigation, selected output payload-closure retrieval and per-output assembly/finalization; consumer held-closure export bound to exact-input receipts; candidate NVKE hosted signing/publication completed; public-bootstrap FDE build-provenance publication supported; development attempt namespaces and scoped GHCR cleanup; hosted GHCR package privacy admission using metadata-derived destination references and an exact-root-bound push; required Environment transport selection with preserved omitted-OCI behavior, explicit OCI identity and early APT/both rejection; required S3 copies of original signed publications through a generic byte-provider interface | Package admission has local contracts, not a hosted private-publication or public-source access proof; independent grants, historical provider payloads and future visibility changes remain operational obligations. Hosted GHCR legacy layouts without the metadata contract fail closed. `release pull` shares the bounded HTTP/2 peer-reset reacquisition used by `delivery.Pull`, with local `httptest` contracts only, as recorded in [output-local publication](output-local-publication-and-metadata-assembly.md#output-local-publication-and-metadata-assembly); no external large-disk `release pull` has been re-run since the recorded peer-reset failures. Main-only Plan execution, independent NVKE full-payload disk consumption, strict full-content publication and live S3/CDN operational acceptance remain unproved; registration is deferred. Direct APT service adapter, repository signing and authenticated read-back remain unimplemented. [Selective boot publication](object-store-publication.md#selective-boot-asset-publication) has core lifecycle APIs; generated execution, production rendering and external acceptance remain unavailable. |
| 20 Publication-bound inventory | `aicr-inventory-v1` compiles the exact destination prefix and named sibling Build identities into a distinct tagged Build identity. `TestInventoryBuildIdentity` and `TestDerivedInventoryPlan` cover destination changes without changing native producer identities; provenance contracts bind observed sibling subjects and detect missing or substituted materials, including prefix-related output names. The checked publication owner retains only bounded, explicitly marked inventory layers and compares their exact document/tar envelope through a revocable API; different gzip encodings are accepted. | Local contract coverage only; no new registry-service, signing-service or hosted qualification acceptance. Re-derivation is required after relocation. The current member does not render a catalog or installer bundle. |

For [issue #425](https://github.com/dsx-ai-factory/nvkr/issues/425), `publication.ociRepresentation` explicitly
selects construction before identity freezing. Omission preserves the existing `artifact-v1` format;
`nvkr-compatible-v1` selects documented NVKR wrapper changes with logical types retained in checked
annotations and publication membership. A digest-bound root-index annotation records explicit intent
without adding default-valued canonical root fields. The local Distribution fixture models the
reported unknown-type rejection: it rejects a retained
Helm wrapper, then accepts a complete diagnostic image-index, Helm, CLI and raw-SBOM publication with all
seven declared members. Full and metadata-only readback preserve root and membership bindings. Opt-in
Helm `show chart` and `pull` against that loopback fixture compare the exact chart archive bytes.
The reproducible opt-in procedure is in `DEVELOPMENT.md`; the fixture logs the installed client version.
It is outside the default gate and supplies neither Helm layout consumption nor ORAS interoperability evidence.
Public-reader contracts accept legacy, annotated and matching dual claims for Helm, binary bundles,
Debian carriers and root metadata, and reject missing/conflicting claims and untyped OCI empty configs.
These are local contracts and client interoperability evidence, not acceptance by the affected GitLab
deployment, hosted signing, qualification or a completed producer Run. Native image/index subjects,
disk wrappers and imported producer manifests retain their exact bytes; disk and producer-specific custom
artifact types can still encounter registry restrictions. No registry-side rewrite or universal artifact
compatibility is claimed.

The separate explicit `helm-standard-v1` profile uses standard Helm chart config/content media types
and no chart manifest `artifactType`. Raw Helm evidence and metadata use opaque canonical binding configs;
signed envelopes retain their standard bundle artifact type and empty config. `TestHelmStandardPublication`
checks complete and metadata-only readback, selected chart retrieval, evidence preservation and exact archive
bytes. Its unsigned registry fixture models GitLab's unknown-type restriction; its signed case uses a generic
registry and synthetic attestation encoding. `TestHelmStandardRootProfileBinding` rejects mismatched root
profiles and preserves exported bootstrap identity. Main's historical default artifact/release goldens remain
unchanged. Stock Helm 4.2.3 pull/show/template passed locally on 2026-11-10 with `NVKR_TEST_HELM_OCI=1`. This does not
establish acceptance by the affected GitLab deployment, a signing service or a hosted producer Run. Other output families and imported
manifests are unchanged by this profile. Existing publications remain immutable; new opt-in construction
requires fresh affected evidence subjects and updated readers.

Retained-root regressions cover exact metadata export, including an index with retained whitespace,
and attested completion with legacy preparation and final bootstraps. Explicit compatible generic configs use NVKR's
artifact-config media type rather than claiming to be CycloneDX or Sigstore payload documents.
The explicit choice changes Environment/Plan identity without changing producer Build identities.
Unknown choices fail at Environment, constructor and executable boundaries. Standard Sigstore envelopes
remain with the attestation owner; the compatible generic constructor rejects substitute signed envelopes.
Compatible raw-SBOM referrer descriptors report config type, with separately checked logical annotations;
local fallback-index roundtrips check these descriptors and exact evidence membership. A synthetic signed
bundle remains discoverable under the standard Sigstore artifact-type filter; this checks transport, not
cryptographic validity. Universal CycloneDX-filter discovery is not claimed. Native/default construction is not silently stripped,
and compatible-profile consumer support is separate from the old-layout reader contract.
Publication failures are classified from the pinned registry library's structured status and diagnostics:
authentication/authorization, permanent rejection, unambiguous unsupported operations and integrity
failures remain distinct from timeouts and transient failures. The public-boundary contracts preserve
typed causes while omitting untrusted response bodies, messages, details and request URLs from diagnostic
presentation. No error triggers repackaging or a weaker evidence policy. Delivery supplies a read-only
base API/authentication report and observes ordinary publication traffic for per-operation diagnostics.
The internal `factory registry-check` adapter prints the same report. Local response fixtures cover
optional/missing version headers, authentication, missing endpoints, proxy HTML, distinct blob/manifest/
evidence/referrer failures and safe network causes. The exact OCI specification version and untested
features remain unknown. These protocol-response contracts do not require a vendor certification matrix.
Regressions keep local cleanup and integrity errors separate from registry status observations while
preserving their coded causes and applicable guidance; joined registry/cleanup failures retain both causes.
Helm tag refusals classify HTTP 401/403 and structured denials as `registry-auth-failed`, preserving
authentication precedence and the existing handling of other permanent and transient tag failures.
The final per-output metadata-root push and its member readback use the same classification and redaction;
response fixtures cover HTTP 401/403 and permanent manifest rejection without copying server text or URLs.
Image aliases apply the same auth precedence and redact timed-out readback URLs while retaining uncertainty
and reconciliation guidance. Read-only checks identify their credential source and can use the factory's
explicit credential pair; usage rejection emits no report, and output failure preserves the owning error code.
Missing/misrouted base endpoints are non-retryable input errors, separately from incomplete digest readback.
Metadata-only readers share the full reader's empty-config rule. Verified metadata copies bind the expected
published digest and retain exact bootstrap bytes; Packer selection JSON cannot set the representation.
Historical default artifact/release goldens come from base `664905a46cb0b0be2ce111da24d833cf318efd96`.
Registry-side next-action checks, compatible bundle readers and selected-output pulls are contract-tested.
Unclassified protocol failures following a successful HTML/non-registry base response use non-retryable endpoint
classification and proxy guidance. The read-only ORAS check has a documented limit: HTTP 404 bodies are
not exposed by its pinned Ping implementation, so those checks report endpoint configuration even when
the body contains a structured refusal. Response fixtures cover that difference without parsing bodies
in the observation transport. Metadata-member cancellation preserves the member name and read-only
guidance before any write, and nested alias readback classification does not duplicate its message.
Write probes remain absent; the library's mutating, asynchronously cleaned-up push-permission helper is not
used. Live registry/version coverage and hosted validation of these changes remain unverified.

The development [Run 44255595374](https://github.com/dsx-ai-factory/nvkr/actions/runs/44255595374)
completed source sharing but failed publication of the second output in a two-image batch: a successful
metadata-only seed was followed by a package API 404. Publication remained closed and the release job
was skipped. The exact error is reproduced by the package-admission fixture. Bounded authenticated
post-seed confirmation now covers delayed visibility, exhaustion, cancellation and immediate explicit
policy refusal; whole-set admission still rechecks every package before payload upload. The later package
API read showed the metadata root as internal and unlinked, which is consistent with delayed visibility
but does not establish the original credential's provider response sequence. The subsequent [Run 44270895414](https://github.com/dsx-ai-factory/nvkr/actions/runs/44270895414), at
`8fdf3737`, completed on attempt 2: all 83 named artifacts/evidence and the aggregate release were verified,
and finalization recorded 83 observed outputs. Its separate disk blob-readback HTTP 403 recovered through
an authorized failed-job-only retry without changing credentials, target construction or code. No automatic
authentication-error retry or privacy-policy relaxation is introduced. The published release is
`ghcr.io/dsx-ai-factory/nvkr/development/runs/44270895414/attempts/2/releases@sha256:3b73371f34c31ca91ce8387d85f3826a5728ed2a5c1615c1bccefe0ab76c661b`.
This supplies hosted evidence for that exact selection and revision, including successful post-seed admission;
it does not prove every bounded retry branch occurred remotely. [ADR-18](adr-18.md) records timing and
cross-Run byte-comparison limits. The earlier failed attempts supply no release-completeness evidence.

## GitLab ordinary unsigned publication

The GitLab publication path is implemented as anonymous HTTPS OCI transport or current-job
`gitlab:CI_JOB_TOKEN` authentication confined to provider-reported same-project registry metadata.
It checks every output/evidence repository, isolates registry/token endpoint traffic and independently
reads back the complete closure before recording success. The anonymous route enforces HTTPS while
retaining the HTTP client's ordinary redirect policy, without the authenticated route's one-hop or
same-project authority bounds. A local TLS test of authenticated publication also checks one-hop blob
redirects to credential-free HTTPS storage, refusal of manifest/token/foreign-repository delegation, and
zero requests to an insecure redirect destination. This is transport contract coverage, not live object-store
acceptance. Both isolated GitLab routes now share ADR-18's direct TLS trust owner and require
provider-authenticated branch protection when publication is selected. The default non-isolated transport
and signed publication lanes are unchanged. These current trust/admission changes are not exercised by
the historical real-server snapshots below.
The [GitLab validation record](adr-18.md#gitlab-ordinary-unsigned-profile) separates the real server/Runner
CLI-build acceptance from the 26 controlled Linux HTTPS lifecycle subcases. The latter exercise publication
and refusal with the production clients; the real recipe selected no registry destination. Neither establishes
publication to a live GitLab registry, signing or qualification. API-reader access remains separate from
registry-write authority. Protected publication also requires API-reader access to the exact branch
endpoint; the optional job-token reader's access to it remains unverified. Full repository checks and
maintainer review remain separate gates.
