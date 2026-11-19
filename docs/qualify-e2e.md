# Forge hardware end-to-end lane

The [workflow](../.github/workflows/nvkr-nvke-forge-e2e.yml) runs NVKE's Forge site-controller suite on real Forge
machines, against a committed NVKE plan. It shows whether a configuration installs and runs on hardware, which the
[hosted nightly](nvke-nightly.md) cannot show because that job boots a local VM. It informs and gates nothing.
It signs, publishes and tags nothing, it is not a precondition of an NVKR Release, and the run summary it writes
always says `qualificationAuthority: "none"`. [ADR-18](adr/0018-pipeline-ir-and-ci-adapter-emission.md#repository-nvke-e2e-adapter-outside-generated-execution)
owns the contract and its [reconciliation row](adr/reconciliation/adr-18.md) owns what has run.

A person starts it with `workflow_dispatch`. Nothing triggers it, so a producer release or a catalog change never
takes a Forge machine on its own.

## What a run does

| Job | Purpose |
|---|---|
| `plan` | Refuses what it can before any checkout, resolves the commits, checks out NVKE at the harness commit, prepares the plan, builds the matrix and builds `qualify` once. |
| `verify-node-images` | `nvkr-outputs` only. Checks each node image by the method its output names: fetch the URL the way Forge's imager will and hash it, or record what the publication run read back from the object store. |
| `forge-e2e` | One leg per stream, one at a time, amd64 before arm64, in the protected `nvke-forge` environment. Stages or builds the node's inputs, joins Teleport, runs the suite and records coverage. |
| `summarize` | Assembles the run summary from every leg's evidence and fails the run unless the summary meets its contract. |

The committed plan holds one combination with two streams, `forge-gb200-amd64-ubuntu-24.04-kube-1.36` and
`forge-gb200-arm64-ubuntu-24.04-kube-1.36`. Every stream in the plan runs; there is no stream selector. Each stream
holds two CPU machines and one GPU machine for about an hour at the `full` stage on the Forge site NVKE's own lanes
use. Never cancel a run in flight: teardown belongs to the suite, and a killed runner leaks its machines. A real run
is its first attempt only. The plan job and every leg refuse a later one, because GitHub's "re-run failed jobs" does
not run the plan job again and a re-run leg would sit beside the first attempt's legs in one summary. Dispatch a new
run instead.

The committed plan is an Ubuntu 24.04 bring-up fixture, taken from a green NVKE nightly because it names node images
that already exist. Ubuntu 26.04 is the lane's first hardware target. Moving to it means replacing the plan fixture and
its provenance, and the harness commit default that matches them, in one reviewed change once a 26.04 plan and its node
images exist. Until then a passing run says nothing about 26.04.

## Choose an artifact source

`artifact-source` is required and has no default. It decides what the node runs, and the run says which it was.

| | `nvkr-outputs` | `build-subject` |
|---|---|---|
| Controller, capi-provisioned, nvkectl | The images NVKR built, pulled by the digest the outputs manifest names | Built in the job from the subject commit by NVKE's own `make site-inputs` |
| Node image | The one NVKR built, at the HTTPS URL the manifest names, checked by the method the manifest names | The one the plan itself names, which NVKR did not check |
| Plan | The fixture with NVKR's references substituted | The fixture, byte for byte |
| NVKR-built | Controller, capi-provisioned, nvkectl and node image | Nothing |
| Shows | NVKR-built artifacts install and run on Forge | The lane drives NVKE's suite at the subject commit on Forge |

Either way the deploy path, the imager EFI and the site profiles are NVKE's, and the run summary says so. A
`build-subject` run proves nothing about artifacts NVKR builds, so it is labelled NVKE-built throughout.

## Commits

The subject commit is the NVKE commit the plan describes. The committed provenance record names it, and the
substitution script checks the fixture's bytes against that record before anything else.

The harness commit is the NVKE commit whose suite and scripts run. It defaults to the subject commit. `nvkr-outputs`
accepts another full commit, and the summary records both. `build-subject` builds from the harness checkout, so it
requires the harness commit to be the subject commit. The harness checkout holds every tag and its whole history,
because the plan describes each component against its own tags and the deploy path is exported from the subject
commit's tree. The orchestrator commit is the NVKR `main` commit that dispatched the run, and the workflow is
dispatched from `main` only for a real run.

For `nvkr-outputs`, the lane does not check that the artifacts NVKR built were built from the subject commit. It
checks the NVKE version each node image names against the plan's, and refuses a different version unless
`allow-node-image-version-skew` is set. A skewed run records the skew in the summary, and the flag does not make
the combination a tested one.

## Run NVKR outputs

`outputs-manifest` names a JSON file directly under `inputs/qualification/`. Add it in a reviewed change after a
producer run, so the references a run uses are in the repository. The manifest has the schema
`nvkr.dev/forge-e2e-outputs/v1`, the producer's run id and one entry per slot: `controller`, `capi-provisioned`,
`cli-amd64`, `cli-arm64`, `os-image-amd64` and `os-image-arm64`. An OCI slot is an `oci://` reference pinned by
digest. A node image slot is a plain HTTPS `.qcow2` URL with its SHA-256, a source naming where it came from and
the NVKE version it was built at, and a verification naming how its bytes are checked before a machine is taken. A
missing slot, an unpinned reference, a node image with no verification or a result that still names an `nvcr.io`
artifact is refused with a coded error and no partial output.

A verification is one of two methods:

| Method | Shape | What `verify-node-images` does |
|---|---|---|
| `http-get` | `{method: http-get}` | Fetches the exact URL with a GET, anonymously, without following redirects, streams the whole body and requires the declared length, the SHA-256 and a plain qcow2 header. |
| `s3-readback` | `{method: s3-readback, run, bytes, qcow2Version, virtualSizeBytes}` | Fetches nothing. Records what the [publication workflow](#publish-the-node-images) run named by `run` reported: it read the object back from the object store with credentials and hashed it. |

`http-get` proves what that URL served to a hosted runner at that moment. A GitLab artifact URL answers HEAD differently
from GET and does not honour Range, which is why the check uses GET and records Range support instead of requiring it.
`s3-readback` exists because the HTTPS front end for a node image can refuse a hosted runner's address while Forge's
imager reaches it, so a fetch from the runner would say nothing about the imager's path. The summary labels it
`observedByThisRun: false` and cites the publication run. The lane checks the shape of that citation and that the
check record repeats it; it does not look the run up, so the citation is a description until a machine boots the image.
Neither method shows that Forge's network can reach the host, that the imager trusts its certificate, that the image
boots, or that the object remains when a later run starts. The `provision` stage is the first thing that does.

NVKR's own publication of a boot asset is the [selective boot-asset contract](adr/reconciliation/object-store-publication.md#selective-boot-asset-publication),
which would replace everything on this page that serves a node image. The publication workflow is a stand-in until then.

## Publish the node images

The [publication workflow](../.github/workflows/nvkr-nvke-forge-node-image.yml) copies the two node images of an NVKR
release to the HTTPS location Forge's imager reads. Forge cannot present a token, and the registry that holds the
release needs one. A person starts it with `workflow_dispatch` from `main`; a dry run may start from another ref.

| Input | Holds |
|---|---|
| `release` | The release's publication root, `oci://REPOSITORY@sha256:DIGEST`. Digest only. |
| `amd64-output`, `arm64-output` | The exact disk output name of each architecture's node image. |
| `nvke-version` | The NVKE version both images were built at, such as `v0.24.0-beta.12`. |
| `dry-run` | Pull and check each image and name where it would go, with no credentials, upload or record. |

Start with a dry run, which pulls and checks both images and prints where each would go:

```sh
gh workflow run nvkr-nvke-forge-node-image.yml --repo dsx-ai-factory/nvkr --ref main \
  -f release=oci://ghcr.io/OWNER/REPOSITORY@sha256:DIGEST -f amd64-output=AMD64_OUTPUT \
  -f arm64-output=ARM64_OUTPUT -f nvke-version=v0.24.0-beta.12 -f dry-run=true
```

Each architecture is one job, in the protected `s3-publish` environment. It pulls the output by exact digest with
`nvkr release pull`, requires the pulled file to be exactly the bytes the pull reported and a plain qcow2 (no backing
file, no qcow2 encryption), and refuses an output built for the other architecture. It copies the file to
`ROOT/nvkr/os-image/VERSION/ARCH/SHA256/OUTPUT.qcow2`, so the key holds the digest: a second publication of the same
image rewrites identical bytes and a different image can never land on an existing key. It then reads the object back
through an authenticated request, requires the same length and SHA-256, and writes a sidecar holding the digest.
A later attempt is allowed for that reason, unlike a hardware run.

Each job leaves `nvkr-forge-node-image-publication-<slot>-<run>`, whose `manifestEntry` is the entry to paste into the
outputs manifest: the URL, the SHA-256, the source (the release digest and version) and the `s3-readback`
verification naming this run. The record carries `qualificationAuthority: "none"`, is not signed and is a description.
The workflow signs, verifies and gates nothing.

It shows that a release's disk image reached the object store intact. It does not show that the HTTPS host serves the
object at `https://CDN_DOMAIN/KEY`, the URL the record assumes from the key, that Forge can fetch it, or that the
image boots.

Configuration it needs, all outside the repository:

- The `s3-publish` environment, holding the variables `S3_PUBLISH_S3_URI` (the bucket and path root, `s3://BUCKET[/PATH]`),
  `S3_PUBLISH_AWS_REGION`, `S3_PUBLISH_CLOUDFRONT_DOMAIN` (the host that serves that root over HTTPS) and
  `S3_PUBLISH_AWS_ROLE_ARN`. The workflow stops before a checkout when one is missing; a dry run needs no role.
- An AWS role whose trust policy admits this repository's `s3-publish` environment through GitHub's OIDC token. It
  needs `s3:PutObject` and `s3:GetObject` on `ROOT/nvkr/os-image/*` and `s3:ListBucket` on the bucket. Without
  `s3:ListBucket` a missing key answers 403 instead of 404, and the publisher reports that as an object store failure.
- Read access to the release's package for this repository's token.

Each leg pulls the controller and capi-provisioned images by digest and refuses one that does not report that
digest or the leg's architecture. It retags them under a `localhost/` name and saves the archives the suite loads
onto the node. No registry answers to a `localhost/` name, so a node that tried to pull would fail instead of
running NVKE's image of the same tag.

## Dispatch

The workflow must exist on the default branch before GitHub lets you dispatch it. Start with a dry run, which plans,
checks, stages and lists the specs without joining Teleport or taking a machine:

```sh
gh workflow run nvkr-nvke-forge-e2e.yml --repo dsx-ai-factory/nvkr --ref main \
  -f artifact-source=build-subject -f stage=provision -f dry-run=true
```

Then run the `provision` stage, which stops at a booted node, before `full`. The stages are `provision`, `bootstrap`,
`deployment` and `full`; `full` adds a tenant and every check. A dry run still waits for the `nvke-forge`
environment's reviewers, and the environment's branch rules decide whether a branch may use it. A real run starts
only from `refs/heads/main`, on its first attempt.

```sh
gh workflow run nvkr-nvke-forge-e2e.yml --repo dsx-ai-factory/nvkr --ref main \
  -f artifact-source=nvkr-outputs -f outputs-manifest=inputs/qualification/OUTPUTS.json -f stage=full
```

The matrix lists amd64 first and the legs run one at a time. GitHub's expansion order is not a scheduling guarantee,
so the summary records the order the legs actually started in.

## Configuration outside the repository

The workflow reads these and fails closed when one is absent. They are repository settings, not files here.

- The `nvke-forge` environment with required reviewers. Every Forge credential is scoped to it.
- A Teleport provision token, named by `FORGE_TELEPORT_JOIN_TOKEN`, whose GitHub allow rule covers this repository.
  NVKE's own token admits only its repository.
- Variables for the Forge site: `FORGE_TELEPORT_PROXY`, `FORGE_ORG_ID`, `FORGE_SITE_ID`, `FORGE_VPC_ID`,
  `FORGE_SUBNET_ID`, `FORGE_SSH_KEY_GROUP_ID`, the two instance type ids and the `FORGE_SITE_2_*` set the second site
  uses.
- Secrets `FORGE_API_KEY`, `FORGE_JUMP_HOST` and `FORGE_SITE_2_JUMP_HOST`. The API key reaches only the suite's own
  step.
- `QUALIFY_E2E_NGC_ORG`, which NVKE's registry configuration needs before `qualify` loads, and optionally
  `QUALIFY_E2E_NODE_USER`, the login the node image bakes. An image whose baked user differs fails as a public-key
  rejection late in the run.

`nvkr-outputs` also needs this repository's token to read the packages the producer wrote. Cross-repository package
read access is a setting to confirm before the first run. The publication workflow's own configuration is listed under
[Publish the node images](#publish-the-node-images); the hardware lane holds no object store credential.

## Evidence a run leaves

| Artifact | Holds |
|---|---|
| `nvkr-forge-plan-<run>` | `plan.json` as the run used it and `substitutions.json` |
| `nvkr-forge-node-image-checks-<run>` | One check record per node image (`nvkr-outputs`): the fetch this run made, or the read-back the cited publication run reported |
| `nvkr-forge-leg-<stream>-<run>` | The leg's stamp, staged record, spec list from a Ginkgo dry run, coverage record and `qualify result` |
| `nvkr-forge-run-<stage>-<stream>-<run>` | The suite's log, JUnit and the diagnostics it redacted itself |
| `nvkr-forge-run-summary-<run>` | The run summary |

A publication run, which is a different run from the hardware lane's, leaves `nvkr-forge-node-image-publication-<slot>-<run>`.

Each leg runs the suite's spec list without a machine, with the same label filter and flags as the real run. After
the real run, the JUnit must contain every listed spec as passed, no spec the list lacks, and the same label filter,
and the provisioning, post-deploy site check and tenant groups must each be non-empty in the spec list. This catches
a harness change that quietly shrinks the lane, which `-ginkgo.fail-on-empty` does not. Below the `full` stage the harness
skips later specs by design, so only the listing and the absence of failures are checked there, and the record says
the coverage is incomplete.

The summary describes the run and does not judge it. `scripts/forge-e2e-summary.sh assert` rejects a summary with an
extra field, a changed authority value, a source that differs from the leg records, a node image that names other
bytes or another verification than the plan, a read-back recorded as observed by the run, or a dispatched leg with
no evidence. The final job runs it against the file on disk before the
run is allowed to pass.

## What a passing run says

It says NVKE's suite passed on Forge, at the stage chosen, for the artifacts the summary names, and covered every
spec its dry run selected. It does not say the combination is qualified, that an NVKR Release is acceptable, or that
NVKR-built artifacts come from the subject commit. Nothing is published to TestGrid, whose identity trusts only
NVKE's own refs, so a failure before the suite starts leaves only the stamp and the staged record in the leg's
evidence.

## Limits

- A build from the subject commit on the arm64 stream runs on an amd64 hosted runner and installs no emulation. If
  NVKE's arm64 build needs it, the leg fails at the build step, before a machine is taken.
- The node images in [pipeline 82279983](https://gitlab-master.nvidia.com/nke/releng/dsx-nvke-recipe/-/pipelines/82279983)
  were built at NVKE v0.23.1 while the plan describes v0.24.0-beta.12, so using them needs
  `allow-node-image-version-skew`.
- The leg admits only a GitHub-hosted AMD64 runner. NVKE measured that such a runner reaches both the Forge API and
  the Teleport proxy. A node image host is not assumed reachable from one: an `http-get` output fails the run when
  `verify-node-images` cannot fetch it, and an `s3-readback` output is not fetched at all. The summary says which, so a
  read-back image is a claim about the object store that the `provision` stage then tests from Forge.
- The lane does not look up the publication run an `s3-readback` output cites. A manifest can cite a run that did
  not publish that image, and the summary would repeat it as a description.
- A cancelled or timed-out run leaks its machines. Delete them by hand, using `keep-site` only when debugging.
- The contract tests in `internal/cicontract` cover the scripts and the workflow's authored boundaries offline. They
  do not exercise Forge, Teleport, a registry or GitHub; the ledger row cites any run that did.

Replace the plan fixture by retrieving a newer NVKE `qualify plan` output and updating the fixture and its provenance
record in one reviewed change. See `scripts/README.md` for each script's usage and exit codes, and
`inputs/README.md` for the fixture's ownership.
