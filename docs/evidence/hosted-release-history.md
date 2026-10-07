# Early hosted release observations

These September 2026 observations retain their original commits, subjects and evidence lanes.
They explain failures and interoperability limits; they do not establish current-head acceptance.
The successful [signed CLI Run](2026-09-10-github-signed-run.md) and
[combined release](2026-09-10-combined-run.md) retain their independent consumer results.

## First hosted Run

On September 9, [Run 34416283747](https://github.com/njhensley/nke-recipes/actions/runs/34416283747)
at `3c55dcfd6aed7d5d23b2c76897a5bcf179ee9af7` passed bootstrap, regeneration and helper transport,
then failed fetching NKE commit `ed9fb09b04fd40bf331d25df2222206ffaca593e` with
`NKD-E-FETCH-FAILED` (Git exit 128). GitHub reported `NVIDIA/nke` as internal; the destination
repository had no Actions secrets or cross-repository fetch credential. Builds and publication were skipped.

The [failed Run artifact](https://github.com/njhensley/nke-recipes/actions/runs/34416283747/artifacts/10129234024)
recorded attempt `34416283747-1`, status `failed`, lane `static-unqualified`, and the required
`nkectl` output. Finalization reported `NKD-E-PROVENANCE-MISSING` because its mandatory carrier
was not restored from a successful job. Plan identity was
`sha256:04a2fd55836d9bef3d3b34b52d5df6b9fb65a8ff6a6a265089203cc8a500d504`.
The corresponding repository CI exposed coverage shutdown outside the Landlock write root;
the fixture correction retained production sandbox policy.

## Authenticated dry-run

[Run 34419180257](https://github.com/njhensley/nke-recipes/actions/runs/34419180257),
`c15e534bb9ea8d6955e4cd61cb57619241b67555`, completed all eleven jobs on September 9 Pacific
(September 10 UTC). Environment-owned source access used `NKE_SOURCE_TOKEN` for the private fork,
while verifying canonical NKE commit `ed9fb09b04fd40bf331d25df2222206ffaca593e` and tree
`309887e8cdb8ee24a39785b864390eda3afce5ec`. The canonical build selection was unchanged.

The [downloaded Run artifact](https://github.com/njhensley/nke-recipes/actions/runs/34419180257/artifacts/10130368187)
recorded attempt `34419180257-1`, status `complete`, lane `static-unqualified`, Plan
`sha256:eb5425d44ec91650c3ddcce825f2274e567b6e64200b63ef22171bc59b018102`, observed executable
`sha256:99cfd9c354a2c8719053dd1b69ce9917ed4b05d21fbe0478122d2b05d94ee969`, and content subject
`sha256:3f820f8adb8104f32906fe64af6aa645cecfa13c5ce904b462fca6ffad355d42`.
Publication was `ghcr.io/njhensley/nke-recipes/nkectl@sha256:ac82b6eb2aec07d3febce776b9d4f0a769387f7d17db1f45585e389a7eec8455`.
Signing records remained `dryRun: true`, `signatureVerified: false`, `attestationVerified: false`.
This proves hosted build/publication mechanics, not real signing or qualified acceptance.

## Cosign bundle discovery

On September 11 at 18:19:04 UTC,
[Run 34631274098](https://github.com/NVIDIA/nke-recipes/actions/runs/34631274098/job/103372517305),
`3fb1d6c40f78917278a626bc922444a270934397`, created SBOM bundles, published and signed artifacts,
then failed verification with `NKD-E-SIGNATURE-INVALID`: the discovered predicate was CycloneDX
instead of the required SLSA provenance. Its subjects under `ghcr.io/nvidia/nke-recipes/` were:

| Output | Manifest digest |
|---|---|
| cwe | `sha256:922bd21d894328963104e97fc2dbe410d413181d2e4f219137839d2b49d4162c` |
| nke-helm | `sha256:1f8c7e4f9d59119be20561b09acb9c882b9170b403ecc003f7ee2036669efd8a` |
| nkectl | `sha256:d169456744fbef687739caa4f317079d44995c6c2a7083b68dc48f17be1d0778` |
| releases | `sha256:c2a0aaba5a67d1b2264c6b2b24fa68ea9aeadc59fbfadfe101de0ecb1c008a21` |

Cosign 2.6.3 selected bundle verification before predicate filtering, hiding legacy SLSA `.att`
attachments. The exact mismatch reproduced locally in 1.71 seconds. Cosign 3.1.3, executable digest
`sha256:4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71`, passed
`TestCosignProvenanceWithSBOMReferrer` in 7.30 seconds using a loopback registry and ephemeral
local keys. Signature, provenance and SBOM bundles were downloaded by predicate and verified on exact bytes.
The production identity/issuer checks were not replaced by those fixture keys.

Independent access to the NVIDIA GHCR subjects returned `DENIED`; the inspected token lacked
`read:packages`, and package-level authorization was not separately established. These historical
local-key results do not establish updated hosted keyless finalization or independent NVIDIA-package
acceptance. The later [M2 candidate](2026-10-06-team-demo-pipeline.md) records separate cosign 3.1.3
workflow results and preserves its consumer-verification limits.
