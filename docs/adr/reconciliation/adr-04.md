## Support by owner

The Tekton change adds explicit OCI public-key trust, role-key snapshots and separate keyless/keyed bundle
admission. Focused controlled tests cover policy separation, credential scrubbing and error redaction.
On 2026-11-09, `TestTektonKeyedCosignInteroperability` passed against a loopback OCI registry with the real
Cosign v3.1.3 Darwin ARM64 executable (SHA-256 `5cf948c2f4dfe59687bdd0b8523709067383e03982cc543475c8a7dc70e92a76`).
It used the adapter's unmodified explicit-key arguments for signature/provenance, SBOM, qualification and
manifest signing and verification, and rejected another key and changed predicates. The injected process
runner permits macOS testing; this does not establish Linux held-executable execution or complete cluster
publication acceptance. See [ADR-18](adr-18.md).

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 04 Signing and consumption | Linux held-executable exact-byte signing/verification, pinned cosign keyless OCI adapter, policy-bound authenticity and artifact-publication consumers with separate capabilities; authenticity checks root/output signatures, production provenance and SBOM bundles without payloads; qualification predicate attachment/read-back verification with real local-key cosign interoperability | Authenticity has contract coverage only; one recorded external consumer attempt failed closed before authentication. `release verify` reads signature bundles anonymously, so a private-registry Release fails closed, as recorded in [output-local publication](output-local-publication-and-metadata-assembly.md#output-local-publication-and-metadata-assembly). Earlier independent cosign and public/private artifact-publication acceptance is recorded; qualification hosted OIDC, M2 independent full-payload disk consumption and root rotation remain unproved. Full release and qualified scopes remain closed |
