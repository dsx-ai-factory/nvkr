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
this check remains pending usable registry read access. No signature or policy failure is inferred from an
access denial. See the [performance baseline](../github-run-performance.md).
