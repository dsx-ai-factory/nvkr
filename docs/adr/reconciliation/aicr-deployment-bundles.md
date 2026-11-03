## AICR deployment bundles

ADR-25's `bundle-oci` path is implemented through the existing Definition → Run → Release owners.
Each named output selects one pinned source-built gzip tar archive. The AICR producers build `v1.2.0`,
render with `--output oci-layout:<dir>` and emit the layout's single layer, so the archive is the one AICR
packaged rather than an NVKR repack. Delivery preserves its exact bytes
in an OCI artifact; independent output observation reconstructs the wrapper from that output's producer.
The bounded archive reader rejects traversal, links, sparse files, unsupported extended metadata,
ambiguous paths and compressed/trailing-data substitutions. Always-on `bundle-files/v1` evidence records
the included regular files at the source/archive and final packaging subjects. It does not inspect
referenced charts, images or providers. Diagnostic pulling writes the exact archive without extraction
or deployment; authenticated consumption retains the normal signed Release/evidence requirements.

The [focused AICR example](../../../inputs/definitions/aicr/README.md) retains one GB300
selection across six renderer formats and one local build-only Environment. A second Definition
selects AICR's Preview `gb200-nvke-ubuntu-training-kubeflow` leaf in the `helm`, `flux` and
`argocd-helm` formats from the same catalog. AICR `v1.2.0` refuses criteria that no applied overlay covers;
these producers also fail the build unless the rendered recipe's last applied overlay is that leaf. The
admission lines of each compiled producer are contract-tested under the host's bash and awk against
hand-written recipe fixtures, including a removed or malformed leaf. Native Helm examples and publication
Environments remain out of scope.

On November 2, the current six-output GB300 Definition with the `v1.2.0` layout producers finished
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
`aicr-nvke-gb200-local-20261103-r5` with exit code 0 under the same host and container. Its
`lifecycle.json` records status `complete`, lane `unqualified-local-execution`, Plan digest
`sha256:f396614a348a292592c27742d850598c5cbfec754916dff40be16199fdfbf635` and content subject
`sha256:6086823c928396914011fa96195a2e5403fb56e34e03080426eea0f268e0489f`; all three outputs were
observed after the leaf check passed. A scratch attempt for the uncovered `dynamo` platform failed in
`aicr recipe` with AICR's coverage error and produced no output.
AICR's own [NVKE conformance record](https://github.com/NVIDIA/aicr/blob/935e0746dc54da890a23c1ecca998244b3a25de7/docs/conformance/cncf/v1.36/nvke/README.md)
used this leaf at AICR commit `864dd60065f1`, not the `v1.2.0` pin. NVKR claims no hardware or AICR
validation evidence for these bundles.

Hosted AICR publication/signing and independent production consumption remain unexercised. Argo CD
reconciliation, cluster installation, hardware behavior, air-gap dependency closure and qualified bundle
reproduction remain outside these results. No test or render upgrades VR200 Preview to operational acceptance.
