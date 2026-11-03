# Browse, export, pull, and verify a release

Explore the outputs and evidence in an existing release, download a named output, and apply your consumer
policy separately. Start at the repository root with `bin/nvkr` built using the [quickstart](quickstart.md).
Browsing, export, and output pulling work on macOS and Linux; verification runs on Linux, directly or
through Docker on macOS.

## Find an output

Set `RELEASE_REF` to the exact publication-root reference emitted by a completed publication:
the full registry/repository followed by `@sha256:` and its digest. A tag, URL, or individual output
manifest is not a release root. Obtain this reference from the release publisher. Current NVKR requires
publication-root v4; historical v3 publication roots are rejected.

```sh
: "${RELEASE_REF:?Set RELEASE_REF to an exact publication-root reference}"
bin/nvkr release browse --reference "$RELEASE_REF"
```

The listing shows named outputs, artifact types, components, Build Digests, and evidence. Registry access
uses your existing credentials; add `--anonymous` to each command for a public registry.

For an interactive overview, add `--ui=tui`. Use `/` to search names, kinds, platforms or digests,
Enter to inspect an output and follow its evidence, and Esc to return. Press `q` then `y` to close.
The [terminal browser reference](release-navigation.md#browse-interactively) covers selection and display options.

Set `OUTPUT_NAME` to an exact name from that listing. Keep using the same terminal:

```sh
: "${OUTPUT_NAME:?Set OUTPUT_NAME to an exact name from the listing}"
NVKR_RELEASE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-release.XXXXXX")
bin/nvkr release browse --reference "$RELEASE_REF" --output "$OUTPUT_NAME"
bin/nvkr release browse --reference "$RELEASE_REF" --output "$OUTPUT_NAME" \
  --format json > "$NVKR_RELEASE_DIR/output.json"
bin/nvkr release browse --reference "$RELEASE_REF" --output "$OUTPUT_NAME" \
  --format mermaid > "$NVKR_RELEASE_DIR/output.mmd"
```

JSON preserves complete identifiers. Open the Mermaid export in a compatible viewer to follow output,
component, and evidence relationships. This is release membership, not build ordering.

Browsing validates all release metadata before filtering, without downloading artifact payload layers,
including OS disk images. It does **not** verify payload bytes, signatures, or your acceptance policy.

## Export the complete description

```sh
bin/nvkr release export --reference "$RELEASE_REF" > "$NVKR_RELEASE_DIR/release.json"
```

This writes `nvkr.dev/release-description/v1` JSON with all named outputs, platform metadata and bound
source/evidence records. The release reference supplies every input; no Plan, mapping, platform selection
or producer checkout is required. Consumers own installation roles and compatibility checks. See the
[description contract](release-navigation.md#export-a-complete-release-description) for fields and inspection
limits. Export and unfiltered JSON browsing produce the same description, without verifying payloads or policy.

## Download an output

Use the exact name from the listing and choose a new destination directory:

```sh
bin/nvkr release pull --reference "$RELEASE_REF" \
  --output "$OUTPUT_NAME" --out ./downloaded-output
```

The destination contains the selected format's files: executable bundle members, a QCOW2 disk, a Helm chart
archive, an OCI image layout, or an APT repository tree. See the [output format table](release-navigation.md#pull-an-output)
for exact filenames and layout. For example, if the listing includes `nvkectl-linux-arm64`:

```sh
bin/nvkr release pull --reference "$RELEASE_REF" \
  --output nvkectl-linux-arm64 --out ./nvkectl-linux-arm64
```

This creates `./nvkectl-linux-arm64/nvkectl` with its recorded permission mode. For every format, `--out` is
required, must not already exist, and needs an existing parent directory. The command does not install or
execute the files, and it does not select a platform automatically. An aggregate image includes all its
declared platforms. A disk retains its plain or declared LUKS2-encrypted QCOW2 bytes; pulling does not decrypt it.
Add `--anonymous` for a public registry.

Pulling checks the complete release metadata and evidence, then downloads and checks only the selected output's
payloads. It does **not** authenticate signatures or apply your acceptance policy. Use the same exact release
reference in the verification step below before treating those bytes as accepted. Malformed or unsupported
format metadata fails with a coded error before payload or destination work.

## Verify against an accepted policy

On Linux, use a pinned, static Linux cosign executable and independently accepted signer identity pattern
and OpenID Connect issuer. Set the four variables below from your trusted tool configuration and consumer
policy; do not derive accepted identities from an untrusted candidate merely to make it pass.

```sh
: "${ACCEPTED_BUILD_IDENTITY_REGEXP:?Set the accepted signer identity pattern}"
: "${ACCEPTED_OIDC_ISSUER:?Set the accepted OpenID Connect issuer}"
: "${COSIGN_PATH:?Set the path to the static Linux cosign executable}"
: "${COSIGN_DIGEST:?Set the independently trusted cosign SHA-256 digest}"
bin/nvkr release verify --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope artifact-publication \
  --identity "$ACCEPTED_BUILD_IDENTITY_REGEXP" --issuer "$ACCEPTED_OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST"
```

This scope downloads and checks artifact bytes, signatures, and production attestations under that policy.
For a faster check that skips artifact payload layers, including disk images, use the same accepted policy
with `--scope authenticity`:

```sh
bin/nvkr release verify --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope authenticity \
  --identity "$ACCEPTED_BUILD_IDENTITY_REGEXP" --issuer "$ACCEPTED_OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST"
```

Authenticity verifies the root and every required output's signatures and production provenance, plus
required signed SBOMs and their metadata membership. Its result explicitly reports
`payloadIntegrityVerified: false`: **authenticity verified; payload integrity not checked**. It cannot
authorize payload export or exact-input matching. Both scopes enforce the selected disk-evidence minimum;
`--minimum-disk-evidence strict` requires signed inspected-disk evidence, but authenticity does not inspect
the disk itself. Neither scope provides qualification or the capability-gated full `release` scope.

To export the verified held closure, also supply the exact producing inputs and
originating invocation. Set these paths and identities from the accepted producing
Run; use a fresh directory:

```sh
: "${PRODUCING_DEFINITION:?Set the exact Definition path}"
: "${PRODUCING_ENVIRONMENT:?Set the exact Environment path}"
: "${PRODUCING_PLAN:?Set the complete producing Plan path}"
: "${EXPECTED_INVOCATION:?Set the exact originating invocation}"
: "${VERIFIED_LAYOUT:?Set a fresh output directory}"
bin/nvkr release verify --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope artifact-publication \
  --identity "$ACCEPTED_BUILD_IDENTITY_REGEXP" --issuer "$ACCEPTED_OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST" \
  --definition "$PRODUCING_DEFINITION" --environment "$PRODUCING_ENVIRONMENT" \
  --plan "$PRODUCING_PLAN" --expected-invocation "$EXPECTED_INVOCATION" \
  --export-layout "$VERIFIED_LAYOUT"
```

The lifecycle owner requires its own exact-input receipt before export. Metadata-only
`release export` does not verify payloads or signer policy. See
[NVKE nightly workflow](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/nvke-nightly.md)
for downstream testing and its current admission limits.

From macOS, use the [consumer script](https://github.com/dsx-ai-factory/nvkr/blob/main/scripts/demo-release-consumer.sh) to run the
artifact-publication verification inside its pinned Linux Docker image. It builds a Linux amd64 `nvkr`
helper with the repository's pinned Go toolchain; supply a static Linux amd64 cosign
executable and its trusted digest:

```sh
bash scripts/demo-release-consumer.sh --runtime docker --reference "$RELEASE_REF" \
  --identity "$ACCEPTED_BUILD_IDENTITY_REGEXP" --issuer "$ACCEPTED_OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST"
```

Add `--anonymous` for public releases. Private releases need `--registry-config`
pointing to a Docker auth directory with inline credentials; host credential helpers
cannot run inside that container. The script defaults to Docker on macOS and native
execution on Linux. Both require a current v4 publication and the same accepted policy.

See [release navigation](release-navigation.md) for local layouts and inspection details, and the
[verification lifecycle](lifecycle.md#run-and-verify) for policy and capability requirements.

## Install an NVKE site from the Releases

An NVKE GPU site consumes the NVKE family Releases: core, services and telemetry for the site controller
cluster, and the disks family for node images. The
[working Definitions and GPU target matrix](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/nvke/README.md#working-definitions-for-an-nvke-gpu-site)
list each family's outputs and the disk and AICR bundle for GB200, GB300 and VR200.

This path cannot yet run end to end. No hosted family Release is recorded, and the steps marked pending
need the named issue. The NVKE commands come from NVKE `v0.22.0`, the version `main` pins; NVKE owns them.

1. **Find each Release.** Set `RELEASE_REF` to the core family's publication root and browse it as in
   [Find an output](#find-an-output). Repeat for the disks family with `DISKS_RELEASE_REF`.
2. **Verify each Release** with `--scope artifact-publication`, as in
   [Verify against an accepted policy](#verify-against-an-accepted-policy). NVKE reads release descriptions
   without verifying them, so verify first. Then export the core description that NVKE reads:

   ```sh
   bin/nvkr release export --reference "$RELEASE_REF" > release.json
   ```

3. **Serve the node disks.** Pull each disk your node pools use, such as `nvke-node-image-arm64` for GB200
   workers. Pulling writes `<output-name>.qcow2`.

   ```sh
   bin/nvkr release pull --reference "$DISKS_RELEASE_REF" \
     --output nvke-node-image-arm64 --out ./nvke-node-image-arm64
   ```

   iPXE cannot pull OCI, so upload the QCOW2 to an HTTP(S) endpoint the machines can reach and register it,
   as NVKE's [node image guide](https://github.com/dsx-ai-factory/nvke/blob/400c24fb6465b042be883d6b62472528520d6291/docs/operate/build-node-images.md?plain=1#L26-L41)
   describes. NVKR does not build the `qcow-imager.efi` that writes the disk; take it from your NICo site.
   The Ubuntu 24.04 disks that NVKE's GB200 golden path uses are pending
   [#323](https://github.com/dsx-ai-factory/nvkr/issues/323).
4. **Install the Cluster API providers.** NVKE runs `clusterctl init`, which today reads the provider
   releases from GitHub
   ([NVKE](https://github.com/dsx-ai-factory/nvke/blob/400c24fb6465b042be883d6b62472528520d6291/docs/get-started/local-site.md?plain=1#L19)).
   A local repository delivered in the core Release is pending
   [#324](https://github.com/dsx-ai-factory/nvkr/issues/324).
5. **Render and install the management plane** with the `nvkectl` output for your host, against the
   management nodes' architecture:

   ```sh
   nvkectl render deploy-path --deploy-path deploy/nico \
     --sitespec sitespec.yaml --stack-config stack-config.json \
     --arch linux/arm64 --release release.json --out ./render
   ```

   Pending [#324](https://github.com/dsx-ai-factory/nvkr/issues/324): NVKE refuses a release without
   `controller-profiles`
   ([troubleshooting](https://github.com/dsx-ai-factory/nvke/blob/400c24fb6465b042be883d6b62472528520d6291/docs/operate/troubleshoot.md?plain=1#L209-L213)),
   and the core Release does not carry it yet. Until then, install without `--release` and name each image
   in the stack config's `images`. `--release` reads one description, and charts it lacks
   resolve from NVKE's tree, so the services and telemetry charts come from NVKE until the composite
   GPU site Release lands ([#326](https://github.com/dsx-ai-factory/nvkr/issues/326)).
6. **Set your profiles.** The core Release's `nico-reference-profiles-fixture` holds NVKE test profiles, not
   fleet configuration. Set `images.profiles` in the stack config to your site's profile image.
7. **Apply the GPU stack to a tenant.** The GB200 Kubeflow training bundle is pending
   [#325](https://github.com/dsx-ai-factory/nvkr/issues/325). Once published, pull it, verify it, and follow
   AICR's instructions for its deployer; NVKR does not apply it.

NVKR builds, signs and publishes these outputs. GPU driver, fabric and runtime acceptance belongs to AICR and
NVKE, and NVKR records none.
