# Conformance tools for the native 1.34 demo

This variant keeps the existing four named ARM64 outputs and reuses the parent
Hydrophone producer, bounded report-download patch and BusyBox image. It selects
the Kubernetes v1.34.2 conformance suite to match the maintained demo tenant,
with the same-minor v1.34.12 kubectl client built from source. The original
four-output Definition remains available independently.

The suite's official image index is
`sha256:1fd2b8043fab3e2858ec181649161a9acc592eff033f3c9be78856d7b408fc66`;
catalog resolution selects ARM64 manifest
`sha256:6ea19a03ca4da93c406066e56c263a6a8cd0b2a8336170fd531af691712926c1`.
The Kubernetes v1.34.2 source pin records suite selection provenance, rather
than authentication of the image's upstream build or signatures. The existing
`evidence: none` image limitations in the parent guide apply.

Kubectl retains upstream v1.34.12 commit
`26c89157669bdc7e3657302bc1abde758507095b`. Its declared vendor command applies
the parent recipe's explicit module updates and checks exact original and
resulting `go.mod`, `go.sum`, `go.work` and `go.work.sum` hashes. Go's module
commands derive the dependency closure and verify it before offline workspace
vendoring. The binary reports `v1.34.12-dirty` and the original source commit;
it is a transformed source build, not the official prebuilt kubectl.

The selected Go SDK image is inherited from the parent reviewed recipes. Any
source, dependency or SDK change requires fresh source and binary vulnerability
scans and upstream kubectl version/get/apply tests. Module-only findings must be
reported separately from imported-package and reachable-symbol findings.

From the repository root:

```sh
bin/nvkr run plan \
  --definition inputs/definitions/kubernetes-conformance-arm64/demo-1.34/definition.yaml \
  --environment inputs/environments/local-conformance-tools-arm64.yaml \
  --json
bin/nvkr run local \
  --definition inputs/definitions/kubernetes-conformance-arm64/demo-1.34/definition.yaml \
  --environment inputs/environments/local-conformance-tools-arm64.yaml \
  --capture-inputs-root . \
  --attempt conformance-tools-demo134-01
```

Use a fresh attempt name. This builds/acquires local diagnostic tools and retains
the original compiler inputs; it does not create a cluster, execute conformance,
publish or sign a Release, or establish passing tests or qualification.
