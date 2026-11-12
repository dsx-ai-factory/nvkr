# Maintain native pipeline toolchain images

`njhensley` owns Packer and inspector image refreshes, failed refreshes, stale pins,
candidate exception review and retained rollback subjects. The authored
`Prepare native NVKR toolchains` workflow runs on relevant source changes to `main`
and every Monday at **06:00 UTC**. Ordinary generated pipeline runs continue to
consume digest pins; they never rebuild these images implicitly.

The workflow has two invocations. Preparation builds and measures both native
architectures without a publication credential. After the owner reviews and merges
per-image exceptions, a manual main-branch invocation publishes those exact retained
bytes and proposes the pin update. An exception never extends to a rebuilt image.

## Prepare and review

Use `.github/workflows/nvkr-native-toolchains.yml` on `main` with both inputs left
at their defaults, or let its source-change/weekly trigger run. The existing
`scripts/prepare-native-toolchains.sh` runs separately on native hosted AMD64 and
ARM64. It checks the pinned compiler and source locks, Packer/plugin/tools,
inspector helper admission, all inspector executable hashes and the libguestfs
appliance filesystem round trip before retaining each image archive.

The archive format is now one Docker archive per image (`packer.tar` and
`inspector.tar`) and a v2 `measurement.json` per architecture. No image is loaded
or executed in the later publication job. `proposal.json` describes the archive,
manifest, configuration, measurement-document and executable digests. Archives,
package inventories and build/validation logs use the existing private CI
transport; GitHub Actions artifacts hold its locators. The preparation Run,
attempt and exact source commit must be retained with the proposals.

Refresh reports use one open tracking issue labeled `native-toolchain-refresh`,
with `Native toolchain refresh` as the stable title. The workflow reuses the oldest
matching open issue (including the former Run-number titles) when no labeled issue
exists, and creates an issue assigned to `njhensley` only when neither lookup finds
one. Existing duplicates are left for the owner to resolve. Each report links the Run.
Only failures, cancellations, or successful measurements whose image manifests
differ from the committed pins post a report. Both native architectures must
compare successfully; unchanged successful preparation and successful publication
post nothing. Source-change triggers cover the image sources and locks, the static
Linux inspector helper's module-local dependencies, the Go pins, preparation and
verification scripts, and this workflow. The dependency path contract checks both
architectures during the ordinary Go test suite.

A failed architecture, missing measurements, stale preparation, expired exception,
publication error, reader failure or regeneration failure leaves the selected
consumer pins unchanged. Changed successful preparation needs owner action: its
report is not publication approval. Resolve the tracking issue only after
reviewing the resulting pin change or recording why the candidate is rejected.

The weekly refresh is also the dependency/security review checkpoint. Review the
current Go release, locked upstream archives, Ubuntu snapshot/package fixes and
current vulnerability findings. Update immutable source locks and both native
archive checksums through normal review when fixes are needed; the resulting
source-change trigger prepares another candidate. A repeated build of the same
locked snapshot does not acquire security fixes. License and security review
references must document the review or the precise accepted candidate exception;
the workflow does not invent a scanner result or promote unresolved source-lock
gates to general organizational approval.

## Record candidate exceptions

Retrieve both proposals and evidence through the private CI transport. Combine the
proposals into `inputs/policies/toolchain-maintenance/<preparation-run-id>.json`:

```sh
preparation_run_id=123456789
mkdir -p inputs/policies/toolchain-maintenance
jq -s --arg source '<full-source-commit>' --argjson run "$preparation_run_id" \
  --argjson attempt 1 \
  '{schemaVersion:"nvkr.dev/toolchain-refresh/v1", sourceCommit:$source,
    runID:$run, attempt:$attempt, images:(add | sort_by(.arch,.kind))}' \
  amd64/proposal.json arm64/proposal.json \
  > "inputs/policies/toolchain-maintenance/${preparation_run_id}.json"
```

The placeholder Run and commit above are synthetic. Use the exact successful
preparation Run and attempt, and preserve every generated subject field. Fill the
`exception` object on **each of the four images** with the actual reviewed values:

```json
{
  "approver": "reviewer-handle",
  "scope": "internal-m2-candidate-only",
  "expires": "2026-12-01T00:00:00Z",
  "licenseReview": "https://review.example/record-of-license-decision",
  "securityReview": "https://review.example/record-of-security-decision"
}
```

These are schema examples, not an approval or a prescribed expiry. The reviewer
sets the expiry and supplies real review references. Empty, expired, unknown or
duplicate fields, incomplete architecture sets and unbound hashes are rejected.
Merge the complete record through independent code-owner review. Do not rewrite
historical exception records to authorize different bytes.

The maintained scope is strictly
`ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/{packer,inspector}-native`.
Both existing packages must report **internal** visibility before any publication.
Approval applies only to the named manifests, configurations, archives,
measurements and executable hashes. It does not approve redistribution elsewhere,
all future toolchains, or qualified reproduction.

## Publish and propose pins

After the record merges, a maintainer dispatches the same workflow on `main` with
`approval_run` set to that preparation Run ID and `verify_published` left false.
The workflow authenticates the successful main-branch preparation attempt through
GitHub, checks that relevant source inputs have not changed, restores that exact
attempt's private artifacts, and authenticates all four subjects before the first
registry write. It rechecks expiry before each image write. Source comparison covers
`.go-version`, `go.mod`, `go.sum`, `go.work`, `go.work.sum`, `vendor`, `toolchains/nvkr-packer`,
`toolchains/nvkr-disk-inspect`, `scripts/prepare-native-toolchains.sh`, and the union
of the inspector's module-local dependency package directories at the preparation
commit and current `HEAD`. Dependencies are resolved for both Linux architectures
with CGO disabled; full package directories include embedded and non-Go files.
Ancestor `.gitattributes` files are included because checkout transformations can
change embedded or copied bytes.
Unrelated package edits do not invalidate the retained candidate. Missing commits
or a failed dependency listing refuse admission; workspace-based and vendored builds are unsupported. A changed guarded input, missing
archive or expired record requires another preparation/review cycle.

Publication runs inside `nvkr-release`. The independent pipeline reader then pulls
the selected manifests on both native architectures, repeats the existing native
tool/helper checks and compares every inspector executable measurement. The
proposal job additionally reads back manifest/configuration bytes and every
compressed layer using that reader. Only after both native jobs succeed does it
update affected authored catalogs, rebind overlay predecessor digests through the
catalog owner, and update Environment image/executable pins. The refresh also updates
the frozen producer pins in `inputs/policies/ovn-producer-overlays.json`, then invokes
`make producer-overlays-generate` before `make workflows-generate workflows-check`.
Updating only the generated overlays leaves their authored policy stale; regenerating
from that stale policy would restore old toolchain pins. The initial `make workflows-check`
still rejects pre-existing drift before repinning. Other policies, historical evidence,
exception records and source-lock approval gates are unchanged.

When a refresh changes the Packer version, review and update consumer `packerVersion`
contracts alongside the image pins; runtime preflight requires an exact version match.
Repin updates local configuration-ID pins and their frozen copies when an explicit
native substitution binds the old ID to a candidate Packer reference and architecture.
Ambiguous mappings fail closed; unbound local IDs remain untouched. Pull the approved
AMD64 registry manifest before using its configuration ID locally. Repin also updates
the architecture-bound predecessor references in `toolchains/nvkr-packer/local-bindings.json`
so local diagnostic substitutions continue to match the refreshed catalogs.

Within an affected authored file, the proposal replaces every exact occurrence of
a selected old pin or predecessor digest, including comments and descriptive text.
Review those incidental edits along with the executable fields. Unselected files
and historical evidence are not searched for matching text.

The release App opens a reviewable change on
`automation/toolchain-refresh-<preparation-run-id>`. It writes no default branch,
never force-pushes, never merges, and does not reopen a closed proposal. If that
branch already contains different bytes, owner review is required before another
proposal can use the same name. A failure after writing some registry manifests
can leave unselected candidate images; no consumer changes until the complete
pin proposal is reviewed and merged.

Required repository configuration:

- Protect `main` with independent code-owner review of exception records and
  maintenance code. A committed `approver` string does not authenticate a human;
  reviewed main-branch bytes and the protected environment are the trust boundary.
- Restrict `nvkr-release` to `main` and retain its required reviewer policy.
- Retain the existing private CI storage read/write credentials, the registry
  writer (`NVKR_REGISTRY_WRITE` / `NVKR_REGISTRY_WRITE_USER`) and independent reader
  (`NVKR_REGISTRY_READ` / `NVKR_REGISTRY_READ_USER`). Registry tokens also need read
  access to package visibility metadata. Their secret values never enter records.
- Retain the catalog-maintenance release App's contents, pull requests and workflows
  write permissions. Source generation checks use anonymous access to the public repositories.

The manual `verify_published=true` mode still checks the currently committed pins
without building or publishing. It cannot be combined with `approval_run`.

## Declare consumer access

Each GitHub Environment declares image access independently of its output publication prefix:

```yaml
toolchainAccess:
  privateRoots:
    - ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains
  publicRoots:
    - docker.io/alpine/helm
    - docker.io/library/busybox
    - docker.io/library/golang
    - docker.io/moby/buildkit
  reader:
    credentialRef: github:NVKR_REGISTRY_READ
    usernameRef: github:NVKR_REGISTRY_READ_USER
```

Roots are sorted, unique registry/repository paths. Each covers that repository and descendants at a
slash boundary. Roots must not overlap, including across classes; at most 128 are supported. Public roots
select anonymous pulls. Every selected execution image in a GitHub Plan, including producer, vendor, utility, inspector, startup
container and service images, must be digest-pinned and classified. Generation
fails before writing a bundle if a root is missing or a selected private root lacks a reader. Declare
only roots trusted for that reader; an authored declaration does not verify the account's actual grants.
Private pulls currently support GHCR only and require `native-kvm-v1`. The reader secret and username
variable stay confined to manifest preflight and per-job image preparation; no repository-token
fallback is admitted. After final runtime Plan validation, each selected runner placement checks
exact manifests and image configs before producer fan-out. Observations describe availability at that
time, download no layers, and do not replace fresh per-job pulls or runtime verification. Private
job/service startup images are rejected because they have no admitted provider startup credential route.
The reader must not reuse the publication credential or the fixed production/development writer
references; those comparisons are case-insensitive. Operators still own the grants of other secrets.

When adding a toolchain root, update the Environment, regenerate with `make workflows-generate`, and
run `make workflows-check`. Changing only the output publication namespace needs no access-code change.
Existing ordinary hosted and native ARC Environments without `toolchainAccess` must migrate before
generating workflows. Declare public roots for anonymous pulls. Private ordinary-hosted and startup
container/service routes remain unsupported, even when a reader is declared. Standalone CLI diagnostics
also require a declared anonymous route for a selected registry image; measured local image IDs stay local.
Ordinary local operation helpers also require declared public roots for registry-pinned images;
measured local image IDs and native bundles retain their local routes.
Access declarations change Environment and Plan digests, while logical Build identity is unchanged.

### Migrate source/location bindings

Replace `runtime.toolchainRegistry` with explicit `runtime.imageLocations` bindings:

```yaml
runtime:
  imageLocations:
    mirror.invalid/compiler: ghcr.io/example/tools/compiler
```

The catalog pins `mirror.invalid/compiler@sha256:<exact-digest>`; compilation requires that exact source
key and retains the digest at the declared destination. Both build and vendor consumers use the same
binding. Existing bare `nvkr-toolchain-*` names can be explicit keys during migration; their names no longer
select a registry. Missing mirror keys fail before bundle output. Direct public references need no relocation.
Declare access for the resolved destination separately. Native bundle digests and measured local image IDs
are never relocated. Transport changes preserve logical Build identity but change the complete Plan.

## Retention and rollback

Preparation locators request **14 days** of Actions retention. The private CI
payload retention policy must keep their referenced archives and evidence for at
least that interval; a locator alone cannot recover an expired private payload.
The owner must finish review within the available artifact lifetime or prepare
again. Retain accepted preparation evidence under the existing internal evidence
retention process before those temporary locators expire.

This workflow performs no registry deletion or tag replacement. Keep old digest
subjects available for every retained generated bundle and rollback window. Before
any separate garbage collection, the owner must inventory retained consumers and
their evidence; unselected partial publications are not permission to delete
previous pins.

To roll back, revert the adopted authored pin and profile changes together,
regenerate all dependent bundles through `make workflows-generate`, and check them
with `make workflows-check`. Review and merge that rollback, then use the existing
committed-pin native verification mode. Preserve both the superseded and restored
exception/evidence records. See [reconciliation](adr/reconciliation/adr-21.md) for
the implementation and external-exercise boundary; the first complete live refresh
must be performed and recorded by a maintainer after merge.
