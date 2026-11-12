## Support by owner

The Tekton route selects the existing per-output OCI publication owner with anonymous transport and explicit
cluster signing keys. Its complete in-cluster registry exercise remains pending; see [ADR-18](adr-18.md).

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 20 Publication | Digest-selected sibling repositories, signed evidence membership, native OCI/Docker subjects with consistent named-output aliases and independent evidence, aggregate runnable OCI indexes with exact native-child evidence, bounded metadata navigation, selected output payload-closure retrieval and per-output assembly/finalization; consumer held-closure export bound to exact-input receipts; candidate M2 hosted signing/publication completed; public-bootstrap FDE build-provenance publication supported; development attempt namespaces and scoped GHCR cleanup; hosted GHCR package privacy admission using metadata-derived destination references and an exact-root-bound push; required Environment transport selection with preserved omitted-OCI behavior, explicit OCI identity and early APT/both rejection; required S3 copies of original signed publications through a generic byte-provider interface | Package admission has local contracts, not a hosted private-publication or public-source access proof; independent grants, historical provider payloads and future visibility changes remain operational obligations. Hosted GHCR legacy layouts without the metadata contract fail closed. `release pull` shares the bounded HTTP/2 peer-reset reacquisition used by `delivery.Pull`, with local `httptest` contracts only, as recorded in [output-local publication](output-local-publication-and-metadata-assembly.md#output-local-publication-and-metadata-assembly); no external large-disk `release pull` has been re-run since the recorded peer-reset failures. Main-only Plan execution, independent M2 full-payload disk consumption, strict full-content publication and live S3/CDN operational acceptance remain unproved; registration is deferred. Direct APT service adapter, repository signing and authenticated read-back remain unimplemented. |

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
