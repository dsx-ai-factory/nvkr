# Candidate OVN image and Helm OCI output evidence

Recorded October 7, 2026, on PR #113 parent
`49253d10ce48522b314646d6fd155535b13df323` plus the changes identified below.
This record separates compilation, chart interoperability, native producer testing
and missing production acceptance. Maintained M2 inputs/workflow remain unchanged.

## Exact authored inputs

| Input | SHA-256 |
|---|---|
| OVN catalog registry | `8683ea8a2365059684162fa3a5329da484fdb36f3772bb60c33eac160060b095` |
| Generated source lock | `63eb239d3b1a7a2aac8d08ac6af076d47097a8c3c43c6ffafe15e71ef3f81a67` |
| Four-output Definition | `20a440e3a841b5f2dff7be0cbd629e4bc00d5c8f126fc82719c1128f39997429` |
| GitHub Environment | `e7cdda4c3155ef204034e9ffbe88d2a446e6837867ab0dd995ecf13b42fb78be` |
| Native producer test source | `be91a87dcaeecc00fc05acf0bc86abbf6048888f7d17133c066eaed679236c4c` |
| Compiled Linux ARM64 producer test binary | `743a4f71f59c2d3903742a6bacee8e31b59f1a79d70c17c6f71258901aa5ccc4` |

The fork source remains commit `26cc199de8e814a7fe6d899a770fc04df0bbc2f9`, tree
`401ad2014d37d40082085b647dd4070a56042a5c`, epoch `1791151171`. The source lock
was not hand-edited. The local test clone's HEAD and clean Git status were checked
before the chart test. Compiler/runtime image pins remain those recorded in the
[original native producer evidence](2026-10-07-ovn-fork-source-build.md).

## Definition and generation contracts

The Definition selects `ovn-image-amd64`, `ovn-image-arm64`, `ovn-chart-amd64`
and `ovn-chart-arm64`. Each chart binds all six enabled slots to its matching
native image package. Exactly two image builds occur in this four-output graph;
charts depend on those packages rather than compiling additional images.

`TestNKEOVNForkReleaseOutputs` compiles the authored files, checks native ordinary
hosted routes, explicit-route precedence, image dependencies and all six mappings,
rejects missing slots/outputs and cross-platform bindings, checks target exclusion
from Build identity, and performs exact generated bundle write/check regeneration.
`TestNKEOVNForkGraphs` checks both native Debian composition graphs and rejects
either missing chart or missing image material. Helm format tests reject unknown
encodings, untyped hex and non-SHA256 inputs without changing chart values.

The CLI, built with `dev (unknown)` stamps, generated the candidate into a disposable
root, then passed `run generate --check`. Plan digest:
`sha256:ba96d0622042964c459f084d87b182520cc729d8546341ed48f8626f21155d4b`.
The generated workflow was 64,269 bytes. `actionlint` and the GitHub-compatible
`yamllint` profile passed. Its source secret is fetch-scoped; publication and OIDC
remain in their separate protected jobs. No branch, environment or signer policy
was broadened, and no maintained generated files were overwritten.

The first maintained M2 drift check caught the ordinary ARM64 default also
affecting non-disk jobs under the explicit ARC policy. The correction restricts
that default to the ordinary hosted route and leaves the complete ARC projection
unchanged. `make workflows-check` then passed against the untouched maintained
bundle, Plan `sha256:6cfd876ea915b6b3f42de7b697306dc4f138d31cfafb8773b486eb611090f391`.
The four-output candidate check also remained byte-exact at its digest above.

## Actual chart rendering and OCI interoperability

The real fork chart was copied into test-owned scratch, without changing the source
clone. `TestOVNForkHelmDigestRendering` used installed Helm **4.3.0+gbec5b06** and
the existing package/binding modules. A controlled `sha256:` followed by 64 `a`
characters was used, not an observed built image digest.

All six rendered image fields used that complete digest for both the candidate
GHCR repository override and `mirror.example.test/ovn`. The SHA-256-hex mapping
avoided duplicate algorithm prefixes; source subchart bytes remained unchanged.

The test packaged the bound chart into the existing Helm OCI envelope, pushed it
to a disposable loopback Distribution-compatible registry through the existing
OCI library, and used real `helm pull` with the output repository name
`ovn-chart-arm64`. Pulled chart bytes exactly equaled the packaged layer:

- Chart layer: **115,725 bytes**.
- Helm OCI manifest: `sha256:ff28c663e73b35f7366be943cbad328d7a0558b95646f771228e3784db86554f`.
- Initial test: **PASS, 11.31 seconds**; final uncached race rerun, explicitly
  requiring all six slots, **PASS, 6.54 seconds** (package 7.918 seconds).

This proves chart artifact shape/render/pull interoperability for these fixture
bindings. It is not a hosted Run, production push, signing/SBOM verification,
cluster installation or published-image-to-OS correspondence. The first test
attempt failed because the archive destination was outside the transport root;
the correction uses private copied source and an archive beneath its transport root.

## Updated native ARM64 producer execution

`TestNKEOVNForkLive` passed in **608.46 seconds**, exit 0, on the dedicated
Linux ARM64 Podman VM. Host-side Git acquisition used an exact clean fork clone;
no source credential was exposed to the producer containers. The test built Helm
from its pinned public source, then executed the held producer operations for
`ovn-kube-ubuntu`, `ovn-kubernetes-chart` and `nke-cni-ovn`. The chart producer and
Debian composer ran with `--network=none`; the image's source-owned Dockerfile
fetched live APT inputs and is not hermetic.

| Observed artifact | Bytes | SHA-256 |
|---|---:|---|
| OCI export archive | 351,955,968 | `614fa6dddaf91f98942c84e734fc1931fdcceb92b6ec6cff287ffe1726c3261e` |
| Debian bundle | 348,794,880 | `5083c814d71bffe5cfe6751813486756134df62c1dda309ac8a1f165791b37f1` |

The exported container manifest digest was
`sha256:55a573f71f0d49677e34c67b1b47be0d4e1bf94a8bb64afa4ef73523d69f2686`.
It is distinct from the archive's exact blob hash. Independent Debian inspection,
exact embedded-image correspondence and Helm image selection passed. Source chart
bytes now arrive through the second authenticated material rather than direct
copying in the Debian composer. These measurements are not reproduction targets;
the source-owned image has live APT inputs/build stamps. The test stops before
SBOM, signing, publication and OS-image execution.

## Contract and repository validation

Final focused uncached race contracts for native routing, ARC behavior, OVN graphs
and outputs, and Helm digest representation passed on both platforms:

| Platform | factory | buildgraph | catalog |
|---|---:|---:|---:|
| macOS ARM64 | 6.925 s | 4.233 s | 1.927 s |
| Native Linux ARM64, Go 1.27.1 | 6.907 s | 3.773 s | 1.031 s |

The Linux pass used the catalog's pinned Go image with the isolated worktree
mounted read-only and no source/publication credentials. The opt-in live build was
skipped in that contract pass; its separately completed producer result is above.
The initial full `make check` passed. A fresh full `make check` on the final
ARC-preserving correction and all final test files also passed, exit 0: formatting,
vet, lint (0 issues), shellcheck, both CI report tests, repository-wide race/coverage
and reachable Go vulnerability scanning (no vulnerabilities found). The final
factory package completed in 425.113 seconds; total coverage was 71.6%.
`git diff --check`, generated-workflow `actionlint`/GitHub-compatible `yamllint`,
candidate byte-exact regeneration and maintained M2 drift checking passed too.

## Production and integration limits

The configured destination is the confirmed existing
`ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate` namespace. Current interactive
GitHub credentials have no package scope: the organization package-list API
returned HTTP 403 requiring `read:packages`. No production GHCR push was attempted.
Generated production uses job-scoped `GITHUB_TOKEN`, not this interactive token.
`NKE_SOURCE_TOKEN` exists but fork readability is unverified. The current release
deployment policy does not admit PR #113's branch; the candidate Environment and
signer remain default-branch-only. Installing this candidate generated workflow
and obtaining the intended protected production route is separate acceptance.

[Issue #116](https://github.com/dsx-ai-factory/nke-recipes/issues/116) records the
generic authenticated cross-output handoff still required for one native image
build and final digest-bound chart to feed both publication and Debian/Packer.
The Debian diagnostic shares image/source-chart materials within its own output;
it does not stage the final published chart or reuse a sibling output's build.
No full OS bake, AMD64 live fork run, same-byte rerun, hermetic APT closure,
component/toolchain review approval or complete Release acceptance is claimed.
