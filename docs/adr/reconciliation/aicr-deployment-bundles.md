## AICR deployment bundles

ADR-25's `bundle-oci` path is implemented through the existing Definition → Run → Release owners.
Eight source-built components each supply one gzip tar archive; GB300 generic training Flux instead
selects a pinned-tool layout root and its explicit recipe-file material. The eight source producers build `v1.2.0`,
render with `--output oci-layout:<dir>` and emit the layout's single layer, so the archive is the one AICR
packaged rather than an NVKR repack. Delivery preserves its exact bytes
in an OCI artifact; independent output observation reconstructs the wrapper from that output's producer.
The bounded archive reader rejects traversal, links, sparse files, unsupported extended metadata,
ambiguous paths and compressed/trailing-data substitutions. Always-on `bundle-files/v1` evidence records
the included regular files at the source/archive and final packaging subjects. It does not inspect
referenced charts, images or providers. Diagnostic pulling writes the exact archive without extraction
or deployment; authenticated consumption retains the normal signed Release/evidence requirements.

Delivery can also import a producer-written OCI image layout without rebuilding its manifest. The source
root must hold only the layout entries and exactly one pinned OCI image manifest with the empty config and
one gzip tar layer, an artifact type on the import allowlist (initially
`application/vnd.nvidia.aicr.artifact`), and no unreachable blobs. The layer must also pass the bounded
archive reader, all before output work. Contract tests import a layout copied from AICR v1.1.2's own
registry push with the same manifest digest, and they refuse each malformed shape with no output. The GB300 generic training Flux output now uses this import with the released AICR v1.2.0 ARM64 executable.
Its separate recipe producer supplies a regular-file material. Factory validates the captured layer with fixed
`aicr verify` arguments through the same pinned tool/image, offline and bounded, before accepting the layout
producer result. Packaging rechecks both the validation subjects and the material manifest. Publication
preserves the original manifest digest and retains original index bytes to bind it to its build subject.
Recipe composition, layout file inventory and final package inventory occupy separate evidence slots.
The other eight components continue emitting their original layer into NVKR's wrapper. Historical source-built
runs below do not establish acceptance of the converted pinned-tool path.

The [focused AICR example](../../../inputs/definitions/aicr/README.md) retains one GB300
selection across six renderer formats and one local build-only Environment. A second Definition
selects AICR's Preview `gb200-nvke-ubuntu-training-kubeflow` leaf in the `helm`, `flux` and
`argocd-helm` formats from the same catalog. AICR `v1.2.0` refuses criteria that no applied overlay covers;
these producers also fail the build unless the rendered recipe's last applied overlay is that leaf. The
admission lines of each compiled producer are contract-tested under the host's bash and awk against
hand-written recipe fixtures, including a removed or malformed leaf and a list outside top-level
`metadata`. Native Helm examples and publication Environments remain out of scope.

On November 2, the then-current six-output source-built GB300 Definition with the `v1.2.0` layout producers finished
local attempt `aicr-gb300-v120-local-20261102-r1` with exit code 0. Its immutable
`.nvkr/runs/aicr-gb300-v120-local-20261102-r1/lifecycle.json` records status `complete`, lane
`unqualified-local-execution`, Plan digest
`sha256:d5d22c231e30b66e8655ce9e80819eb185a170e5dc1a41b183d77de47228317b` and content subject
`sha256:b13b35ebc5efc02f442ac5f526798bd92471e01712f38df274f8fa7bdb0570ef`; all six outputs were observed.
Each producer required one manifest with one layer in AICR's layout, rechecked that layer's digest, passed
`aicr verify` on its extracted files and emitted the layer unchanged. The host was macOS ARM64 Go 1.27.3
and producers ran in the pinned Linux ARM64 Go 1.27.2 container. These are local records, not hosted
evidence or signed Releases.

On November 3, the three-output GB200 NVKE Kubeflow training Definition finished local attempt
`aicr-nvke-gb200-local-20261103-r7` with exit code 0 under the same host and container. Its
`lifecycle.json` records status `complete`, lane `unqualified-local-execution`, Plan digest
`sha256:cb28c9acee5559f1e8ab6d721b77de56c1873b51796a819984bf5b585ae40208` and content subject
`sha256:a259f44e50eec14aca41cac8008d14410434f2bf843d09a6292f564ecdc173d2`; all three outputs were
observed after the metadata-scoped leaf check passed. Each producer passed
`--dra-eviction-node-label nvidia.com/dra-kubelet-plugin=true`, and `helm` and `argocd-helm` also passed
`--readiness-hooks`. Inspection of each retained, extracted layer found the `dra-node-labeler` chart and
GPU Operator's `NODE_LABEL_FOR_GPU_POD_EVICTION` in all three formats, and readiness gates only in
`helm` and `argocd-helm`. A scratch attempt for the uncovered `dynamo`
platform failed in `aicr recipe` with AICR's coverage error and produced no output.
AICR's own [NVKE conformance record](https://github.com/NVIDIA/aicr/blob/935e0746dc54da890a23c1ecca998244b3a25de7/docs/conformance/cncf/v1.36/nvke/README.md)
used this leaf at AICR commit `864dd60065f1`, not the `v1.2.0` pin. NVKR claims no hardware or AICR
validation evidence for these bundles.

On November 10, the focused single-output attempt `aicr-pinned-tool-linux-arm64-r5` completed under
native Linux ARM64 Docker with the released AICR v1.2.0 tool. NVKR was cross-built with Go 1.27.3;
the execution image remained the catalog's pinned Go 1.27.2 image. The Run records
`unqualified-local-execution`, one observed output, Plan digest
`sha256:928cc0ebcb9bf1f3107fe4ecaecf4c4c1ec67abc2cc7b7efc10d461884296e3f` and content subject
`sha256:15b3cbed09b010c89f22e91006eb3966a0f19f04ed94f74db96e05fe6d0fa1d7`.
The recipe's criteria were generic/gb300/ubuntu/training (platform `any`), with recipe digest
`sha256:cb7e45a3ff6fbf0e1c2712c8499324f5f402cfa7c278bdb73b3392ad47c3f7e3`.
The fixed validator passed 34 checksums and explicitly reported **unverified** trust: no upstream
attestations were present. The admitted archive held 35 regular files including the checksum list.

Inspection compared producer and packaged manifest, empty config and layer bytes exactly. The original
index subject was `sha256:5bda9ada6e9ddb3b76c53320b0c10683c1afcf8d51ec6ad3f32c19b776b60af8`,
the unchanged manifest was `sha256:b1df1357f406dd47c45704c40e6a0397f20ccff1e05aaf5cd30bac42099bc53b`,
and the layer was `sha256:0dc28cb964c9b9ea5459dffe8bed6dc44774fa65893543a6e32a84842abc6d1d`.
Task evidence is retained outside Git under `data/aicr-pinned-tool/linux-run/`: `command.json`,
`execution.log`, `inspection.json`, and `.nvkr/runs/aicr-pinned-tool-linux-arm64-r5/lifecycle.json`.
These local records are not published/authenticated evidence. This run exercised acquisition, recipe,
rendering, fixed validation, observation, SBOM and packaging only. Publication/evidence membership and
malformed-input refusal are contract-tested separately.

Hosted AICR publication/signing and independent production consumption remain unexercised. Argo CD
reconciliation, cluster installation, hardware behavior, air-gap dependency closure and qualified bundle
reproduction remain outside these results. No test or render upgrades VR200 Preview to operational acceptance.
