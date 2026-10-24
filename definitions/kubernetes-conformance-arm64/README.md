# Kubernetes conformance tools on ARM64

This ordinary Definition produces four named Linux ARM64 outputs: source-built
`hydrophone-arm64` and `kubectl-arm64` executable bundles, plus acquired
`conformance-image-arm64` and `busybox-image-arm64` OCI images. The existing
catalog, source preparation, offline build, observation and packaging machinery
owns acquisition. It does not require manually installed harness binaries.

Hydrophone retains upstream commit
`5fe47daaff5fe025215cb257118dc2a0e643e1d6`. Its declared vendor command updates
four existing modules before vendoring: `golang.org/x/net` 0.60.0, `x/text` 0.42.0,
`x/sys` 0.48.0 and `x/term` 0.46.0. Kubernetes retains the v1.36.3 commit
`0f29094e5b73085e3802ecc1298ecae13866bfe6`; its workspace additionally updates
OpenTelemetry `otel`, `metric` and `trace` to 1.42.0. These are transformed source
recipes, not pristine upstream builds. Kubectl reports the original commit with
version `v1.36.3-dirty` and tree state `dirty` in both upstream version packages.

Each source command checks exact original and resulting module/workspace hashes,
downloads the complete dependency closure, and vendors before the build runs
with network disabled. Run `go version` in the selected native toolchain image
and review the resulting dependency closure before admitting its outputs.
Generated locks are maintained by `catalog pin` and `catalog pin-artifacts`,
not edited by hand.

The Hydrophone component applies the hash-bound `patches/bounded-download.patch`
after source vendoring. It limits each downloaded log/report to 64 MiB and exec
stderr to 1 MiB, cancels the stream on overflow/write failure, and exposes a final
filename only after a successful transfer and file close. Staging and final files
use mode `0600`; staging files are removed after failed transfers, and late
client-go writes are rejected. The ordinary offline build runs the focused
patched client tests before compiling the binary. These limits cover this
maintained catalog producer, not arbitrary authenticated tools.
The adapter disables upstream progress-status collection, whose separate exec
buffer path is therefore unused by this invocation.

Before using newly produced tools, run the upstream Hydrophone tests, the
kubectl version/get/apply tests, and source and binary vulnerability scans against
the current database. Record the exact source, dependency closure, produced bytes,
scanner version and database revision with the Run artifacts. Repeat these checks
when a source, patch, dependency or toolchain pin changes. The commands below are
reproduction procedures, not a recorded acceptance result for either architecture
or for hosted publication.

The shared native demo selects `demo-1.36/image-catalog` for its Kubernetes 1.36.3 suite image and the root `kubectl-catalog` for matching source-built clients on both hosts. Its site and tenant version comes from the selected NVKE installer template. The older `demo-1.34` selection and the root image catalog retain their original diagnostic scope; they do not establish a match for the current native demo.

The image references preserve the original local diagnostic suite selection.
They are acquired without upstream evidence (`evidence: none`) and retain their
native manifest digests. Their generated SBOMs currently contain no package or
license inventory; acquisition and blob integrity do not establish image CVE or
license clearance. The Hydrophone source pin records selection recipe provenance,
not an upstream build/signature claim for those images.

```sh
bin/nvkr definition validate \
  --definition definitions/kubernetes-conformance-arm64/definition.yaml
bin/nvkr run plan \
  --definition definitions/kubernetes-conformance-arm64/definition.yaml \
  --environment definitions/kubernetes-conformance-arm64/environment-local-docker.yaml \
  --json
bin/nvkr run local \
  --definition definitions/kubernetes-conformance-arm64/definition.yaml \
  --environment definitions/kubernetes-conformance-arm64/environment-local-docker.yaml \
  --attempt conformance-tools-arm64-01
```

Use a fresh attempt. Planning is `static-unqualified`; local execution produces
unqualified diagnostic artifacts. Running this Definition does not contact a
cluster, run Kubernetes conformance, publish, sign or issue qualification
receipts. The current validation adapter consumes authenticated published tool outputs; a
local Run description cannot restore that capability. To use these producers
with the published product source, select their catalog components as ordinary
outputs in the product Definition and publish them through its existing Run.
Validation may use the same publication root for product and tools, with exact
output/member/platform selection and the separate deployed-artifact mapping.
