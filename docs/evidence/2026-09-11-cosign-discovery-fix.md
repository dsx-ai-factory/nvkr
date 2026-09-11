# Cosign mixed-attestation discovery failure

## Hosted observation

[Run 34631274098, verification job 103372517305](https://github.com/NVIDIA/nke-recipes/actions/runs/34631274098/job/103372517305)
ran commit `3fb1d6c40f78917278a626bc922444a270934397`. SBOM attestation creation, publication, and
signing succeeded. Verification failed on 2026-09-11 at 18:19:04 UTC with `NKD-E-SIGNATURE-INVALID`:

```text
none of the attestations matched the predicate type: slsaprovenance1, found: https://cyclonedx.org/bom
```

The signing job recorded these immutable subjects:

| Output | Manifest digest |
|---|---|
| cwe | `sha256:922bd21d894328963104e97fc2dbe410d413181d2e4f219137839d2b49d4162c` |
| nke-helm | `sha256:1f8c7e4f9d59119be20561b09acb9c882b9170b403ecc003f7ee2036669efd8a` |
| nkectl | `sha256:d169456744fbef687739caa4f317079d44995c6c2a7083b68dc48f17be1d0778` |
| releases | `sha256:c2a0aaba5a67d1b2264c6b2b24fa68ea9aeadc59fbfadfe101de0ecb1c008a21` |

Repositories are under `ghcr.io/nvidia/nke-recipes/`. A read-only attempt from the development host using
its GitHub CLI credential received GHCR `DENIED`. GitHub reported that token's scopes as
`gist, read:org, repo, workflow`, which omit `read:packages`; package-level authorization was not separately checked.
The diagnosis therefore uses hosted logs and a local reproduction, not independent acceptance of these bytes.

## Cause and correction

Cosign 2.6.3 [remote attestation verification](https://github.com/sigstore/cosign/blob/v2.6.3/cmd/cosign/cli/verify/verify_attestation.go)
automatically selects bundle verification when it discovers any Sigstore bundle referrer. That selection
happens before predicate filtering. NKD publishes CycloneDX evidence as bundles but stores its separate SLSA
production attestations in legacy `.att` attachments. The bundle hides the existing SLSA attachment.

The same release's [signature verification](https://github.com/sigstore/cosign/blob/v2.6.3/cmd/cosign/cli/verify/verify.go)
also selects bundle verification. Its transformed JSON does not preserve certificate identity/issuer fields.
Changing provenance storage alone would therefore leave NKD's consumer/finalization identity checks unresolved.

The correction upgrades the catalog pin to cosign 3.1.3 and uses standard Sigstore v0.3 DSSE bundles for
artifact signatures, SLSA provenance and CycloneDX SBOMs. `sign` and `attest` explicitly select bundle format;
SBOM creation retains the same bundle format through `attest-blob`. Each required predicate remains distinct.

The verifier uses cosign's predicate-filtered `download attestation` to retrieve only the required bundles,
then uses `verify-blob-attestation` on their exact bytes and independently selected subject digest. It checks
the signature predicate `https://sigstore.dev/cosign/sign/v1` separately from SLSA provenance. Observed identity
and issuer come from the certificate embedded in the exact bundle that passed verification. They never come
from statement annotations or CLI summary output. SBOMs use the same verification argument builder.

Legacy reports and public-key bundles cannot satisfy the production keyless policy. Invalid policy and
cancellation fail before external work; download output and candidate counts are bounded, and verification
has a three-minute deadline. Private bundle files are checked for mutation and removed on success/failure.
Failures return coded errors without partial successful records. Dry-run describes the same command sequence.

The artifact-copy/snapshot workaround is removed. This approach downloads only attestation bundles and
requires existing legacy signature/provenance attachments to be reissued as bundles. An ordinary retry of the
old GitHub run still uses its old commit/helper; the updated pin and adapter require a new hosted run.

## Pin and generated workflow

The authored catalog constraint is `3.1.x`. The supported `catalog pin --tag v3.1.3` command regenerated the
cosign lock entry from upstream; the lock was not hand-edited. The official Linux/amd64 binary and published
checksum list agree on executable/archive digest
`sha256:4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71`.
The source pin is commit `11926fa5bbbbde47e88fc006b625a17769b743b2`, tree
`ffb2d2076b725abbeff294d5c02897a0da7a5f15`, epoch `1785973407`.

The generated workflow, plan and manifest were regenerated from their authored inputs. The workflow body,
permissions and hosted-runner choices are unchanged; its plan-digest header changes with the new pin.
Byte-exact generation checking passed with identity
`sha256:04ba5b90c27aa0d04bf45d42f98981c13fa1916c7cc8bfd12c7cf636673f41f8`.

## Reproduction and validation

The original cosign 2.6.3 fixture reproduced the exact hosted predicate mismatch in 1.71 seconds. The final
fixture uses the official pinned cosign 3.1.3 binary, a loopback registry and ephemeral local signing keys.
Its Linux/amd64 container runs with `--network none` and `--init`. Only the fixture substitutes local keys,
an empty test trust root and disabled transparency services; production retains identity/issuer and normal
transparency checks.

`TestCosignProvenanceWithSBOMReferrer` passed in 7.30 seconds. It covers a release-style OCI index containing
a manifest and an image with a CycloneDX bundle. It exercises all three standard bundle producers, including
NKD's actual SBOM creation/verification adapter. Signature and SLSA bundles are downloaded by predicate and
cryptographically verified using the production argument builder with fixture key trust. Missing signatures,
missing provenance and wrong digests fail. Both outputs are confirmed to have no legacy `.sig` or `.att` tags.

On Linux with the pinned binary available, run:

```sh
NKD_TEST_COSIGN_BINARY=/absolute/path/to/cosign-linux-amd64 \
  GOTOOLCHAIN=local go test ./pkg/nkd/sign -run '^TestCosignProvenanceWithSBOMReferrer$' -count=1 -v
```

This test is opt-in because the normal suite does not acquire or execute external tools. Always-run contracts
cover exact certificate-derived identity, wrong issuer/identity/subject/predicate, malformed and legacy reports,
separate signature/provenance crypto failures, mutation, cleanup, bounded output and admission before I/O.
Local-key interoperability does not establish hosted keyless, transparency-service or independent GHCR
acceptance. A hosted run with the updated pin and adapter remains required.

The full native `make check` gate passed with Go 1.27 and golangci-lint 2.13.1, including formatting, vet,
lint, ShellCheck and race/coverage tests (70.0% total coverage). Uncached `go test ./... -count=1` passed.
Linux/arm64 `go test -race ./pkg/nkd/sign ./pkg/nkd/pipeline -count=1` passed with external networking disabled.
Actionlint, byte-exact regeneration and `git diff --check` passed. Default yamllint still reports 187 existing
generated-format diagnostics (including long lines); its output is identical against the baseline workflow.
Those findings are reported separately from the passing repository completion gate.
