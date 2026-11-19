# Preview limits

NVIDIA Kubernetes Recipes (NVKR) is available as a preview. Commands, input schemas and behavior can change between releases, and NVKR is not recommended for production use. This page lists what the preview supports and its known limitations. The [implementation and evidence record](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/README.md) tracks the current status of each capability.

## Supported at preview

- Select components, pin sources and acquired artifacts, and apply overlays: [Catalogs and components](../catalogs-and-components.md).
- Generate and compare Plans, and export producer and artifact graphs: [Plan and graph inspection](../plan-and-graph-inspection.md).
- Build artifacts in a local Run: [Quickstart](../quickstart.md).
- Generate a GitHub Actions workflow that builds, signs and publishes a Release: [Generate a pipeline](../generate-pipeline.md).
- Generate a native Tekton pipeline under an explicit cluster-key trust contract: [Tekton cluster execution](../generate-pipeline.md#tekton-cluster-execution). The execution limits below distinguish its recorded test scope from production acceptance.
- Build NVKE node OS images for `amd64` and `arm64` on a native Linux host with KVM, or in the generated workflow: [Build a node image](../node-images.md).
- Browse, export, pull and verify a published Release: [Browse, export, pull and verify a release](../release-quickstart.md).

## Known limitations

### Execution and publication

- **Hosted publication requires explicit trust and credentials.** GitHub Actions uses the configured workflow identity and signing policy. The [Tekton route](../generate-pipeline.md#tekton-cluster-execution) uses explicit cluster keys and a dedicated trusted cluster with an explicitly selected native Linux platform; its recorded core acceptance is AMD64 in a disposable kind cluster under test-only keys, not production cluster acceptance. That attempt completed the core Run but the overall fault-test harness failed; the record preserves that limit. See the [execution record](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/adr-18.md) for its tested scope. Local walkthroughs can publish unsigned releases to a local registry.
- **Generated workflows use GitHub-hosted runners, except for node-image disks.** Node-image disk jobs and their disk-retention finalizers run on the self-hosted `arc-amd64-kvm` and `arc-arm64-kvm` routes, which you provision. NVKR emits no other self-hosted runner labels.
- **Builds need their declared runtime.** A Run needs the runtime, source access and capabilities its Definition and Environment declare. Unsupported requirements fail before execution; a declared prerequisite that is unavailable can still fail the Run partway.
- **Signing and verification paths need Linux.** On macOS they return a capability or platform error. macOS can build the CLI, run dry-runs, diagnostics and local Docker builds, and browse, export and pull releases. To verify from macOS, run the verifier in Docker.

### Evidence and verification

- **Local Runs are unsigned.** A Run on your own host is in the `unqualified-local-execution` evidence lane, with no signatures or build provenance. A dry-run is `static-unqualified` and produces no artifacts. Optional local qualification signs its evidence with your own local keys; that is local trust only and does not create a `pipeline-attested` Release.
- **Verification covers publication, not reproduction.** Verifying a Release under your policy authenticates what the hosted pipeline signed and published. It does not establish exact-byte reproduction, full disk inventory or deployment readiness.
- **Exact-byte qualification is unavailable.** The `qualified` evidence lane is retired. Hosted qualification checks have [development evidence for an earlier candidate](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/feature-branch-pipeline-development.md), not production qualification or current-candidate consumer acceptance. GitHub release-manifest issuance remains [contract-tested without recorded hosted acceptance](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation/m4-release-manifest-issuance.md).
- **Release browsing does not verify.** Browsing a Release inspects its metadata without downloading payloads or verifying signatures. Verify under your policy before you use an artifact.
- **The maintained node images' SBOMs do not inspect the disk.** The maintained GitHub Environments select the producer-only `build-provenance-v1` disk-evidence mode. Its signed SBOM records each disk's exact producer and material subjects, with disk inspection marked `not-performed`, and does not establish what is installed on the disk. Strict disk inventory is deferred. See [Dependency SBOMs](../dependency-sboms.md).

### Node images

- **FDE node images make no confidentiality claim.** The full-disk encryption (FDE) variants use a public bootstrap passphrase. First boot binds the existing volume key to the TPM without re-encrypting, so the source audit concludes that nodes built from one image keep that image's volume key. Use FDE images for evaluation only, and do not rely on them for data-at-rest confidentiality. A recorded development Run built, signed and published the `amd64` and `arm64` FDE images; TPM first-boot behavior and a complete Release containing them are not yet established.

## Choose the next action

The [six worked journeys](../journeys.md) keep runtime, signing and publication prerequisites beside each command.
