# Browse, export, pull, and verify a release

Explore the outputs and evidence in an existing release, download a named output, and apply your consumer
policy separately. Start at the repository root with `bin/nvkr` built using the [quickstart](quickstart.md).
Browsing, export, and output pulling work on macOS and Linux; verification runs on Linux, directly or
through Docker on macOS.

If publishing failed before a release reference was produced, start with
[Helm and registry troubleshooting](publication-architecture.md#troubleshoot-helm-and-registry-rejections).
That guide covers the read-only API/authentication check, error guidance and explicit compatibility settings.

## Find an output

Set `RELEASE_REF` to the exact publication-root reference emitted by a completed publication:
the full registry/repository followed by `@sha256:` and its digest. A tag, URL, or individual output
manifest is not a release root. Obtain this reference from the release publisher. Current NVKR requires
publication-root v4 for plain Definitions or v5 for composites; historical v3 publication roots are rejected.

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
execution on Linux. Both require a current v4 or composite v5 publication and the same accepted policy.

See [release navigation](release-navigation.md) for local layouts and inspection details, and the
[verification lifecycle](lifecycle.md#run-and-verify) for policy and capability requirements.

## Install an NVKE site from the Releases

The NVKE GPU-site product path uses one composite Release built from the family Definitions. The
[working Definitions and GPU system matrix](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/nvke/README.md#working-definitions-for-an-nvke-gpu-site)
list each family's outputs and the disk and AICR bundle for GB200, GB300 and VR200. NVKE `v0.23.0`, the
version `main` pins, takes its management-plane inputs from the core member. The composite also supplies
PostgreSQL, SPIRE and Temporal charts through the services member. NVKE consumption of this 83-output
composite description, including those charts, is outside the
[recorded acceptance scope](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/adr-26.md#hosted-gpu-site-publication).

The [GPU site composite](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/nvke/README.md#gpu-site-composite)
publishes every family output under one reference; `qcow-imager.efi` and the GPU stack still come
separately, as in the steps below. The [accepted hosted publication proof](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/adr-26.md#hosted-gpu-site-publication)
for [#342](https://github.com/dsx-ai-factory/nvkr/issues/342) is the November 6 composite Run,
attempt 2, with 83 outputs under one signed root. It covers that exact candidate; a fresh Run is
not required for this proof. [#352](https://github.com/dsx-ai-factory/nvkr/issues/352) is the next
independent consumer verification step. The record keeps later-candidate execution, published-disk
guest boots and site installation outside that acceptance scope.

### Get the inputs from NVKR

1. **Find the composite Release.** Set `RELEASE_REF` to the exact root of a completed GPU-site Run,
   and browse it as in [Find an output](#find-an-output). The historical record above supplies one candidate
   in a private registry; it requires read access. For the containerized consumer script, pass
   `--registry-config` with a Docker auth directory containing those credentials.
2. **Verify the Release** with `--scope artifact-publication`, as in
   [Verify against an accepted policy](#verify-against-an-accepted-policy). Accept the composite workflow's
   signer identity on `main` through your own policy. This scope downloads every output, including all
   eight disks; it does not establish their boot acceptance.
3. **Export the composite description** that NVKE's `--release` reads. NVKE does not verify it, so export the
   reference you verified:

   ```sh
   bin/nvkr release export --reference "$RELEASE_REF" > nvkr-release.json
   ```

4. **Pull `nvkectl` and the node disks** you need. The core member has `nvkectl` (AMD64) and
   `nvkectl-linux-arm64`. A disk pull writes `<output-name>.qcow2`. For AMD64 management nodes on Ubuntu 26.04:

   ```sh
   bin/nvkr release pull --reference "$RELEASE_REF" --output nvkectl --out ./nvkectl
   bin/nvkr release pull --reference "$RELEASE_REF" \
     --output nvke-node-image-amd64 --out ./nvke-node-image-amd64
   ```

   Pull from the same reference you verified in step 2: `release pull` checks each payload against its
   Release but does not check the signer. Put the pulled `./nvkectl/nvkectl` first on `PATH`, or set
   `NVKECTL` to it, so the stages below and `nvke-deploy.sh` use this CLI.

   GB200 workers use the Ubuntu 24.04 ARM64 disk, `nvke-node-image-ubuntu24-arm64` or
   `nvke-node-fde-ubuntu24-arm64`, contributed by the Ubuntu 24.04 disks member of the same composite
   Release. The `nvke-node-fde-*` disks use a public
   bootstrap passphrase, so every node built from one shares its volume key and gets no data-at-rest
   protection.

### Install with NVKE

Each stage links the NVKE `v0.23.0` page that owns it. Run them from an NVKE checkout at that tag.

1. [Plan the installation](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/operate/plan-installation.md).
2. [Serve the disk over HTTP(S)](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/operate/build-node-images.md#serve-the-boot-artifacts-over-https).
   NVKR does not build the `qcow-imager.efi` that writes the disk; take it from your NICo site.
3. [Prepare the management nodes](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/operate/prepare-nodes.md).
4. [Bootstrap the management cluster](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/operate/bootstrap-management-cluster.md). `nvkectl bootstrap
   site -f sitespec.yaml --release nvkr-release.json` takes the CAPNICo image from the core member; its `--arch` defaults to the
   bootstrap host's architecture. Run the later stages with the `<CLUSTER_NAME>.kubeconfig` the bootstrap
   writes.
5. Give the cluster a default StorageClass; the bootstrap installs none
   ([troubleshooting](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/operate/troubleshoot.md?plain=1#L255-L259)). The services member's
   `local-path-provisioner` chart can supply one for one architecture.
6. [Install Cluster API](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/get-started/local-site.md#install-the-management-plane) at the version NVKE
   pins. `clusterctl init` reads the provider releases from GitHub; a local repository in the core member is
   pending [#324](https://github.com/dsx-ai-factory/nvkr/issues/324).
7. [Install the management plane](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/site-bootstrap/deploy-repository.md) with
   `deploy/nvke-deploy.sh install --deploy-path deploy/nico --sitespec sitespec.yaml --stack-config stack-config.json --release nvkr-release.json`.
   NVKE refuses a release without
   `controller-profiles`
   ([troubleshooting](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/operate/troubleshoot.md?plain=1#L249-L253)). The core member
   carries it as the same build as `nico-reference-profiles-fixture`: NVKE's reference test profiles, not fleet
   configuration. Set the stack config's `images.profiles` to your site's profile image; it replaces the image
   only after NVKE finds `controller-profiles` in the Release. If you leave `images.profiles` unset, NVKE installs
   the Release's image instead
   ([`site.go`](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/cli/internal/deploypath/site.go#L131-L139)).
   Its `nico-vms-cp` and `nico-vms-worker` profiles then appear among the controller's config profiles, and a node
   set uses one only when it names it in `configProfile`
   ([`get_node_config_profile.go`](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/controller/internal/temporal/activities/get_node_config_profile.go#L55-L87)).
8. [Register the image for provisioning](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/operate/build-node-images.md#register-the-image-for-provisioning).
9. [Create a workload cluster](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/workload-clusters/create.md).
10. Apply the GPU stack. The
    [GB200 Kubeflow training Definition](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/aicr/README.md#gb200-nvke-kubeflow-training)
    contributes three bundles to the composite Release. Pull the bundle for your AICR deployer from the
    same verified reference, for example the Helm bundle:

    ```sh
    bin/nvkr release pull --reference "$RELEASE_REF" \
      --output aicr-nvke-gb200-ubuntu-training-kubeflow-helm --out ./gb200-training
    ```

    The other outputs are `aicr-nvke-gb200-ubuntu-training-kubeflow-flux` and
    `aicr-nvke-gb200-ubuntu-training-kubeflow-argocd-helm`. Follow AICR's instructions for the chosen
    deployer. NVKR does not apply it.

NVKR builds, signs and publishes outputs. GPU driver, fabric and runtime acceptance belongs to AICR and NVKE,
and NVKR records none.
