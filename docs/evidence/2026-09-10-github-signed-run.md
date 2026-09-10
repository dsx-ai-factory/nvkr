# Improved hosted Run and real signing

## Prepared configuration

The GitHub Environment selects a small offline utility image and real keyless signing. The generated workflow
retains separate fetch, build, publication, signing and verification jobs; four offline post-build nodes share
one runner while retaining their individual commands, isolation and outputs. Finalization downloads the exact
planned carrier set concurrently and still restores and validates every required item.

The official cosign v2.6.3 Linux/amd64 release binary was downloaded with its official checksum manifest.
Its SHA-256 is `7c78a7f2efc00088bd788a758db6e0928e79f3e0eb83eb5d3c499ed98da4c4f4`; `file` identifies it as
statically linked Linux x86-64 ELF. Running those exact bytes in
`busybox@sha256:29989570aeecad61a019f684218ea74d4b8c1c74f9e0abeb34ca926b81174ee1` returned cosign v2.6.3,
commit `fecddd3c22045a39f52392e71e79f66854b41352`, platform `linux/amd64`. That same image ran the freshly
compiled static NKD helper. These are runtime compatibility checks, not signing-service acceptance.

The repository's GitHub deployment environment `nkd-release` was created with custom deployment branch
policy `main` (type `branch`), verified through the GitHub API. Live publication, signing and verification jobs
name this environment. Only signing requests `id-token: write`; verification receives registry read access.
This branch restriction is not evidence of independent review or governance ratification.

The complete local `make check` gate, uncached unit suite, Linux/arm64 signing and pipeline race checks,
Actionlint, YAML lint and exact generated-file drift check passed. Additional regression tests reject skipped
or reordered logical execution, changed isolation, corrupted raw-tool bytes, unsupported tool formats,
missing transport, and workflow commands outside the exact admitted rendering.

## Hosted outcome

[Run 34421960532](https://github.com/njhensley/nke-recipes/actions/runs/34421960532), commit `4313817`,
passed source fetch, CLI build, combined offline processing, publication, real keyless signing and registry
signature/provenance verification. [Repository CI](https://github.com/njhensley/nke-recipes/actions/runs/34421939098)
also passed. Run finalization correctly failed when the real verification record did not round-trip through
its canonical decoder; this attempt is a failed Run, not a successful pipeline-attested Run.

Published digest:
`ghcr.io/njhensley/nke-recipes/nkectl@sha256:0c3d9a1ef83cc38542cc9d4d6b7abe3cf5d0ee585a1ce7228ed49c10cab03241`.

The first experiment took 4m08s versus the 5m17s dry-run baseline, but its failure prevents treating it as a
successful end-to-end comparison. Its combined post-build stage took 18s versus 90s across the corresponding
critical-chain stages; finalization took 25s versus 34s, including restoration but failing record acceptance.
Real signing took 30s and verification 21s, replacing baseline dry-run signing at 9s and verification at 14s.
Tool acquisition took 14s alongside source/build preparation.

An independent Linux cosign-only consumer attempted the exact digest using the existing local GHCR login.
GHCR denied private-package access before signature retrieval. The temporary credential copy was removed;
that initial check stopped pending usable registry read access. No signature or policy failure is inferred from an
access denial. See the [performance baseline](../github-run-performance.md).


## Successful corrected Run

[Run 34422660746](https://github.com/njhensley/nke-recipes/actions/runs/34422660746), commit
`6954dfcd58631e254d7794c2b34e96f615a854cf`, passed every generated job after the producer stopped appending
newlines to canonical signing/verification records. The reader remains strict. Its
[repository CI](https://github.com/njhensley/nke-recipes/actions/runs/34422638531) also passed.

The downloaded Run record contains:

- Attempt: `34422660746-1`; status: `complete`; lane: `pipeline-attested`.
- Plan: `sha256:afbf72d8d16cb5c5e0a6346fa7d6638874e432b18dbc5535ff8cae25f889734a`.
- Definition: `sha256:e17bdea9d8953bffe64da71a90ebc94f1628f512bc012eee6280eaac62bc2d8b`.
- Environment: `sha256:e7b0f7e11c4db4fc4e78f41169180c4145a48051e1bd36572ee29566aefe235d`.
- Recipe: `sha256:439746ba5095230598792c43bcc7523946a1d2e31e4cf3c72525f72bd10a8861`.
- Observed executable output: `sha256:99cfd9c354a2c8719053dd1b69ce9917ed4b05d21fbe0478122d2b05d94ee969`.
- Content subject: `sha256:3f820f8adb8104f32906fe64af6aa645cecfa13c5ce904b462fca6ffad355d42`.

The verified published digest is the same `0c3d9a1e…03241` reference recorded above. The verification record
has `dryRun: false`, `signatureVerified: true`, `attestationVerified: true`, and cosign version `v2.6.3`.
Observed identity is `https://github.com/njhensley/nke-recipes/.github/workflows/nkd-run.yml@refs/heads/main`;
observed issuer is `https://token.actions.githubusercontent.com`. Its bytes have no trailing newline and the
strict lifecycle reader accepted them.

Elapsed time was 4m12s versus the 5m17s dry-run baseline: 65s / 20.5% faster while adding real signing.
The [complete timing table](2026-09-10-github-signed-run-timings.csv) and
[comparison](../github-run-performance.md#implemented-comparison) retain the measurement details and limits.
This proves hosted pipeline-attested publication, not qualified reproduction, runtime conformance, full Release
scope, or organizational ratification.


## Independent consumer acceptance

After the owner updated the local GHCR login with private-package read access, a separate Linux/amd64
container verified the exact published digest using the pinned official cosign v2.6.3 binary. These direct
cosign commands ran outside the generated workflow and independently of NKD's signing adapter:

```sh
cosign verify --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" --output json "$REFERENCE"
cosign verify-attestation --type slsaprovenance1 \
  --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" --output json "$REFERENCE"
```

The reference and exact identity/issuer policy are recorded above. Both commands exited zero. Cosign accepted
two signatures and two SLSA v1 attestations on the same digest, from the initial and corrected hosted attempts.
The initial attempt's valid signature is not evidence that its Run finalized successfully.

The same isolated consumer then invoked `nkdctl release verify` with explicit `pipeline-attested` lane,
`artifact-publication` scope, exact cosign executable digest, reference and identity/issuer policy. It exited
zero and returned a verified Release description with the expected `nkectl` artifact and content subject:
[consumer result](2026-09-10-github-consumer-release.json). Its policy digest is
`sha256:286c957506b24db6da0ed7da70971e33f546c8c5fbbf87f6373e4162fab130c6`.

The consumer image was the digest-pinned Go image already used by the Recipe. Only the GHCR credential entry
was copied into its private temporary Docker configuration, mounted read-only, and removed after completion.
No credential value entered this evidence or the repository. This establishes positive public consumer
acceptance for the stated scope; stronger unavailable scopes remain disabled.
