# One authenticated release entry point, independently consumable artifacts

The release index and its metadata live in `<publication.repositoryPrefix>/releases`. Deliverables and their
SBOM attestations live in sibling repositories named after the Definition outputs. For NKE these are
`ghcr.io/nvidia/nke-recipes/nkectl`, `…/cwe`, and `…/nke-helm`.

## Registry graph

Solid arrows are OCI descriptors within a repository. Dashed metadata arrows bind exact sibling locations
and manifest digests; they are not cross-repository OCI descriptors. Signature attachments are external to
what they sign.

```mermaid
flowchart TB
  subgraph release["…/releases"]
    R["Release OCI index"] --> M["Metadata manifest"]
    M --> J["Canonical config: outputs, repositories, digests, evidence membership, document digests and stages"]
    RS["Release signature and SLSA attestation"] -. authenticates .-> R
  end
  subgraph cli["…/nkectl"]
    C["CLI manifest"] --> CC["Binary file/platform config"]
    C --> CL["CLI payload layer"]
    CB["SBOM referrer manifest"] -. "OCI subject" .-> C
    CB --> CE["Empty OCI config"]
    CB --> CD["Sigstore bundle: verification material and signed DSSE statement containing CycloneDX; exact binary subject"]
    CS["Artifact signature and SLSA attestation"] -. authenticates .-> C
  end
  subgraph image["…/cwe"]
    I["Native image manifest"] --> IC["Native image config"]
    I --> IL["Native image layers"]
    IB["SBOM referrer manifest"] -. "OCI subject" .-> I
    IB --> IE["Empty OCI config"]
    IB --> ID["Sigstore bundle: verification material and signed DSSE statement containing CycloneDX; native manifest subject"]
    IS["Artifact signature and SLSA attestation"] -. authenticates .-> I
  end
  subgraph helm["…/nke-helm"]
    H["Helm artifact manifest"] --> HE["Empty OCI config"]
    H --> HL["Final chart archive; exact CWE digest embedded"]
    HB["Final-package SBOM referrer manifest"] -. "OCI subject" .-> H
    HB --> HE
    HB --> HD["Sigstore bundle containing CycloneDX; final archive subject, packaging stage"]
    SB["Source SBOM referrer manifest"] -. "associated artifact" .-> H
    SB --> HE
    SB --> SD["Sigstore bundle containing CycloneDX; original source-archive subject, source-build stage"]
    HS["Artifact signature and SLSA attestation"] -. authenticates .-> H
  end
  J -. "artifact digest" .-> C
  J -. "artifact digest" .-> I
  J -. "artifact digest" .-> H
  J -. "evidence digest" .-> CB
  J -. "evidence digest" .-> IB
  J -. "evidence digest" .-> HB
  J -. "evidence digest" .-> SB
  RUN["Run completion: separate descriptive claim"]
```

## Why each wrapper exists

The release index remains one release signature target. An index has no config, so one metadata manifest
carries its canonical config. That config alone owns output names, sibling repository locators, exact
manifest identities, build bindings, and evidence membership. No deliverable or SBOM payload
is copied into the release repository, and no separate lifecycle-binding object is published.

The CLI retains its file/platform config for extraction. CWE retains its native manifest, config and layers.
Helm has one artifact manifest and archive layer, without a nested publication root.

Each SBOM has one standard OCI referrer manifest with an empty config and one Sigstore v0.3 bundle layer.
The bundle contains the signed in-toto statement, CycloneDX predicate, certificate/key verification material
and transparency evidence. The raw CycloneDX document is not published separately. The OCI manifest provides
registry discovery and transport; the bundle supplies the SBOM's own cryptographic authentication. There is
no additional signature on the SBOM wrapper and no redundant SLSA attestation about the SBOM wrapper.

Evidence shared by multiple output projections belongs to the first sorted output membership's repository.
Other memberships reference that same exact bundle through the release. Independent referrer discovery is
available at the owning output; this avoids pretending that one OCI subject can name multiple repositories.
The maintained CLI/CWE/Helm outputs each own their evidence, with two distinct inventories for Helm.

## Signing and publication order

1. Build and observe outputs, generate private raw SBOMs, bind Helm to the exact CWE image, and prepare the
   complete local release closure.
2. Create and verify a CycloneDX attestation for each SBOM's exact subject. Replace the private raw evidence
   with the verified bundles, then derive the final release metadata and index digest.
3. Publish deliverables first, their SBOM bundles next, and the release index last. Verify served manifest
   digests at the owning repositories.
4. Sign and attest each deliverable manifest, then the release index. SLSA predicates describe the producing
   Run; signing operations do not claim new builds.
5. Authenticate the root, retrieve the complete distributed closure, verify all deliverable signatures and
   provenance, and independently verify every SBOM bundle before writing successful hosted verification.

The SBOM-attestation job uses the same protected workflow identity and issuer as artifact signing. It needs
OIDC access but no registry credentials. Artifact publication/signing retain their existing scoped registry
access. These effects are not an atomic remote transaction: failure can leave payloads or partial evidence;
no rollback or cross-run resume is claimed.

Dry-run and local development can retain raw SBOM preparation layouts. They do not create signed bundles.
Production publication and the authenticated Release consumer reject raw preparation evidence. Production
SBOM creation uses pinned cosign `attest-blob --new-bundle-format --type cyclonedx --hash …`; the adapter then
runs `verify-blob-attestation` with the exact digest, configured identity and issuer, and normal transparency
verification. No production verification bypass is enabled.

## Subjects and evidence scope

OCI `subject` associates an evidence manifest with a deliverable for discovery. The signed statement's subject
is the precise thing inventoried: a CLI binary, the native CWE image manifest, the original Helm source archive,
or the final bound Helm archive. These can be different digests and are not interchangeable.

The CycloneDX predicate retains the covered stage and scanner input. Release metadata binds the canonical
document digest, exact bundle blob and manifest digests, subject kind/digest, stage, and sorted output membership.
The consumer checks the signed statement's subject and reconstructs the exact canonical document before
checking those bindings. Helm source evidence cannot satisfy the final-package slot.

The release signature authenticates evidence membership and exact bytes through metadata. Individual artifact
signatures authenticate their deliverables; SBOM bundle signatures authenticate their subject-bound inventories.
Neither standalone check establishes complete release membership. Run completion remains a separate descriptive
claim, and none of these checks establishes qualified reproduction, vulnerability clearance, or deployment acceptance.

## Independent consumption

Use a trusted cosign installation and an externally selected workflow identity/issuer. Artifact signatures and
SLSA attestations are standard Sigstore v0.3 bundles under the cosign 3.1.3 contract. Select each required
predicate explicitly at the deliverable's exact manifest reference:

```sh
cosign verify-attestation --type https://sigstore.dev/cosign/sign/v1 \
  --certificate-identity 'https://github.com/NVIDIA/nke-recipes/.github/workflows/nkd-run.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' "$ARTIFACT_REF"
cosign verify-attestation --type slsaprovenance1 \
  --certificate-identity 'https://github.com/NVIDIA/nke-recipes/.github/workflows/nkd-run.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' "$ARTIFACT_REF"
```

Discover SBOM bundles through OCI referrers, or select their exact manifest digests from an authenticated
release. Fetch the single `application/vnd.dev.sigstore.bundle.v0.3+json` layer and verify it independently:

```sh
cosign verify-blob-attestation --bundle sbom.bundle.json --type cyclonedx \
  --digest "$EXPECTED_SUBJECT_SHA256_HEX" --digestAlg sha256 \
  --certificate-identity 'https://github.com/NVIDIA/nke-recipes/.github/workflows/nkd-run.yml@refs/heads/main' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```

The expected digest comes from independently checked bytes or authenticated release metadata, not from blindly
trusting the downloaded bundle. For CWE it is the native manifest digest; for CLI/Helm it is the specific binary
or archive digest. Referrer discovery alone establishes no trust. The registry adapter uses the standard
referrers API or the library's OCI fallback index; discovery indexes are not additional SBOM documents.

The NKD adapter downloads only the required signature and provenance bundles, verifies their exact bytes with
`verify-blob-attestation`, and derives identity from the embedded certificate after successful verification.
It does not copy the artifact or fall back to legacy attachments. Existing legacy signatures/provenance must
be reissued as bundles before the updated consumer can accept them.

## Validation boundary

Contract tests cover missing, substituted, duplicate and unbound evidence, wrong subjects/stages/documents,
failed signing/verification, raw-evidence rejection, isolated repositories, referrer discovery and failed fetches
that leave no accepted output. The finalizer compares decoded SBOMs with independently restored handoffs and
final Helm package inspection. The extra attestation stage adds one internal handoff; it does not add a product.

A synthetic offline experiment with pinned cosign 2.6.3 created a real CycloneDX bundle, preserved the exact
canonical document through NKD decoding, and verified an ephemeral local-key signature. That experiment had
no transparency-log entry and used an explicit fixture trust root; it is not evidence of keyless policy success.
The native completion gate, uncached suite, Linux sign/SBOM race tests, actionlint and byte-exact workflow
regeneration passed. A new authorized GitHub/GHCR run remains required for production signing, repository permissions, transparency
verification and hosted finalization.

The earlier loopback rehearsal at
`localhost:5000/nkd-separated-rehearsal/releases@sha256:35232ed384a2bc7d1bfdecfda8238086d0e0e341de85602e064c145c3aac2fc2`
predates SBOM co-location and attestations. It demonstrated recovery of the original CLI/Helm bytes and exact
CWE binding, but its five-manifest release index is not the current layout.

See the pinned [cosign attestation options](https://github.com/sigstore/cosign/blob/v3.1.3/cmd/cosign/cli/options/attest_blob.go)
and [verification options](https://github.com/sigstore/cosign/blob/v3.1.3/cmd/cosign/cli/options/verify.go) for the adapter's command contract.
