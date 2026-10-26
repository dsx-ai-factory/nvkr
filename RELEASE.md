# Source releases

NVKR's own OSS release deliverable is versioned source, build instructions,
and a validated software bill of materials (SBOM). Users build the CLI from
source; distributing prebuilt NVKR binaries is not a release requirement.

This procedure releases the NVKR project itself. The
[release quickstart](docs/release-quickstart.md) describes a different task:
consuming artifacts produced by NVKR. The source-release procedure does not
change those artifact publication or verification contracts, and it does not
qualify NVKE outputs or a demonstration deployment.

## Version and source identity

Use `vMAJOR.MINOR.PATCH` for repository source releases, with `-rc.N` for a
release candidate. Choose patch, minor and major increments according to
backwards-compatible fixes, backwards-compatible additions and breaking
changes respectively. Review changes to public CLI/SDK behavior and contracts
when choosing the version; do not infer compatibility from the tag alone.

Use a signed annotated Git tag targeting the approved full commit. Once
published, preserve the tag and commit. A final tag may use candidate evidence
only if that evidence covers the exact same source commit, toolchain/build
inputs and declared test scope. Source or dependency changes require fresh
validation; renaming the candidate does not transfer its evidence.
The Makefile stamps a Git-derived version into the CLI. If a final tag changes
that stamp, check the resulting build separately; matching source commits do
not establish identical candidate and final binaries.

## Build from a released tag

Use a full clone, and replace the example tag with the release's tag:

```sh
git clone https://github.com/dsx-ai-factory/nvkr.git
cd nvkr
RELEASE_TAG=vMAJOR.MINOR.PATCH
git fetch origin tag "$RELEASE_TAG"
git verify-tag "$RELEASE_TAG"
git checkout --detach "$RELEASE_TAG"
git rev-parse HEAD
cat .go-version
GOTOOLCHAIN=local make build
bin/nvkr --version
```

Obtain the maintainer's signing-key identity through a trusted channel and
compare the full commit with the release record. Signature validity alone does
not establish trust in the key. Use the exact Go patch in `.go-version` on
`PATH`; the build refuses a different patch. The output is `bin/nvkr`.
Git source archives omit tag signatures and the history used for version
stamping, so the supported instructions use a Git checkout.

[Development](DEVELOPMENT.md) owns tool prerequisites and
`make check`; [CLI setup](docs/quickstart.md#build-only-the-cli) covers building only NVKR; the
[quickstart](docs/quickstart.md#build-the-artifacts) covers the first NVKE artifact build.
Compiling the Go CLI does not require building NVKE artifacts. Executing
producers requires their declared container/runtime tools, and
authority-bearing execution and verification retain their documented Linux
requirements. A macOS build is not proof of Linux runtime acceptance.

## Candidate checks and approval

The [maintainers](MAINTAINERS.md) own release decisions under
[governance](GOVERNANCE.md). Name a release owner and record maintainer approval
and RelEng evidence review before publishing. This procedure does not alter
repository access or live branch-protection rules.

1. Freeze the candidate commit, version and release notes after source and
   dependency changes are complete. Describe compatibility changes and known
   limitations for that revision.
2. Run `GOTOOLCHAIN=local make check` and `GOTOOLCHAIN=local make build` with
   the pinned Go patch. Record command, toolchain, host OS/architecture and
   results. Apply the additional targeted/Linux checks required by
   [AGENTS.md](AGENTS.md#completion-gate) when the changed paths require them.
3. Verify required repository checks and reviews at the exact candidate
   commit. Record check names and result links. Missing, skipped or cancelled
   required checks are not passes; an earlier green head is insufficient.
4. Link any integration or external-service evidence used for a release claim,
   and keep its subject and scope explicit. Unit tests, a successful CLI build,
   generated plans and older demo runs do not prove new runtime qualification.
   [Reconciliation](docs/adr/reconciliation.md) owns current capability and
   evidence limits.
5. Complete the source-release SBOM checks below and record the release
   decision. Unresolved required checks, evidence or approval block publication.

## Validated SBOM

The release SBOM describes NVKR source and its declared dependencies at the
released commit. State its format/version and scope explicitly. Distinguish
production dependencies from development/tooling inputs, account for module
replacements and document excluded inputs. An inventory of NVKE artifacts
produced by NVKR is not an inventory of NVKR itself.

The release owner must retain:

| Check | Required evidence |
|---|---|
| Subject | Repository, release tag, full source commit, SBOM filename and SHA-256 |
| Generation | Reviewed generator version and identity, exact invocation/configuration, input scope and exclusions |
| Format and semantics | Successful result from a maintained validator for the declared SPDX or CycloneDX version, with validator version, command, log and exit status |
| Coverage | Comparison with the revision's Go module manifests and locks, relevant tool modules and declared non-Go inputs; gaps and exclusions reviewed explicitly |
| Integrity and trust | An authenticated release record binding source identity, SBOM digest and validation-report digest; consumers can check its signer and bytes |
| Review | Named reviewer and recorded disposition of validation or inventory findings |

Use an established generator and the format owner's maintained validator;
record reviewed versions rather than relying on an unpinned latest install.
For SPDX, [the upstream SPDX tools](https://github.com/spdx/tools-python)
provide document validation. Do not replace it with a custom field-presence
check. Generate into a separate directory so outputs and local caches do not
become inventory inputs. A reference build may support the inventory without
requiring binary distribution; identify that build's platform and subject
separately from the source dependency inventory.

Format validation does not prove completeness or absence of vulnerabilities.
Review coverage separately, and retain vulnerability findings and disposition
under the existing [CVE procedure](docs/cve-management.md). A checksum or
signature on an SBOM does not establish those checks occurred.

The [dependency-SBOM guide](docs/dependency-sboms.md) describes inventories for
NVKR-produced artifacts. It does not establish a validated SBOM for the NVKR
project's own source release. This repository procedure requires that evidence;
it does not claim a self-release SBOM generator/validator is already automated.
If the SBOM or its validation report is missing, mark the release incomplete.

## Publish and verify

After approval, create and verify the signed annotated tag at the approved
commit, then push only that tag to the canonical NVKR repository. Before
pushing, inspect the tag/release triggers in `.github/workflows/`; source
release documentation is not a switch that disables existing automation.

Publish release notes with the tag and full commit, links to the tagged build
instructions, SBOM and validation report, their authenticated digests, required
check/test results, coverage limitations and recorded approval. Document how
consumers verify the release-record signer, tag and downloaded evidence.
Do not publish internal hosts, credentials or provider logs.

Read back the published source tag and release evidence from the consumer's
access path. Verify signature/commit, build-guide availability and the SBOM
and report digests against the authenticated record. Record post-publication
verification. Missing evidence leaves the release incomplete; the absence of
a binary asset is expected for source-only delivery.

## Hotfixes and recovery

For a hotfix, branch from the affected tag, review the fix, increment the patch
version and backport to `main` when applicable. Repeat the candidate checks and
SBOM validation on the new commit. Follow [SECURITY.md](SECURITY.md) for
security-sensitive reports.

For a partial release, preserve its tag, commit and evidence, identify the
failed step and mark the release incomplete. Retry only with the same approved
source and exact evidence bytes. Changed source or evidence requires a new
candidate/version, validation and approval. Do not move a published tag or
silently overwrite mismatched evidence.

For a bad published release, mark it withdrawn in its release notes and link
the replacement and correction instructions. Preserve the original identity
and evidence; verify the replacement through the consumer path and record the
result. Use the project's [support channels](SUPPORT.md) for the correction
notice, respecting security disclosure requirements.
