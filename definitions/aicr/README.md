# AICR lifecycle example

The maintained journey uses `definition.yaml`. One reusable Recipe invokes the exact pinned AICR generator and produces one named `gpu-runtime` Helm OCI output. It does not claim to build an OS image or compose a deployable cluster.

The local script executes the complete supported path: acquire AICR by archive and executable digest, render without network in the pinned Linux utility container, record the terminal observation, package the Helm OCI payload, and build its lifecycle publication envelope. Optional publication uses an owned loopback registry on port 5001, separate from the NKE M1 registry on port 5000.

```sh
scripts/demo-ai-factory-local.sh
scripts/demo-ai-factory-local.sh --publish
scripts/demo-ai-factory-github.sh
```

The GitHub script generates and byte-checks the hosted form without contacting GitHub, a registry, OIDC, or a signing service.

```sh
nkdctl definition validate --definition definitions/aicr/definition.yaml

nkdctl run plan \
  --definition definitions/aicr/definition.yaml \
  --environment definitions/aicr/environment-local.yaml

mkdir -p .nkd/ai-factory-github
nkdctl run generate \
  --definition definitions/aicr/definition.yaml \
  --environment definitions/aicr/environment-github.yaml \
  --engine github --root .nkd/ai-factory-github
```

The adjacent `catalog/` contains only the pinned AICR profile. Multi-output worker and cluster compiler fixtures live under `testdata/examples/`; the NKE OS-image build owns the worker payload.

Local admission without output is also available:

```sh
nkdctl run local \
  --definition definitions/aicr/definition.yaml \
  --environment definitions/aicr/environment-local.yaml \
  --attempt aicr-check --dry-run
```

Local results are `static-unqualified`; verified Release consumption requires a digest-pinned reference, an explicit pipeline artifact-publication Verification Policy, and real signature plus provenance verification.
