# AICR lifecycle example

The maintained journey uses `definition.yaml`. The named `gpu-runtime` Helm OCI output selects an exact catalog profile and pinned AICR generator directly. It does not claim to build an OS image or compose a deployable cluster.

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

The adjacent `catalog/` contains the AICR profile and the generator source/lock pin. Multi-output worker compiler fixtures live under `testdata/examples/`; the NKE OS-image build owns the worker payload.

Local admission without output is also available:

```sh
nkdctl run local \
  --definition definitions/aicr/definition.yaml \
  --environment definitions/aicr/environment-local.yaml \
  --attempt aicr-check --dry-run
```

Plans are `static-unqualified`; executed local Run records use `unqualified-local-execution`. Verified Release consumption requires a digest-pinned reference, an explicit pipeline artifact-publication Verification Policy, and real signature plus provenance verification.

The Definition selects `aicr: {profile: h100-runtime, generator: aicr}`. Update its release through
`nkdctl catalog bump --dir definitions/aicr/catalog aicr`, then regenerate. Each Environment supplies one
publication prefix; the generated repository is `<prefix>/gpu-runtime`, published by digest without an
automatic version or channel tag.

Optional renderer settings belong directly to the output:

```yaml
aicr:
  profile: h100-runtime
  generator: aicr
  bundle:
    deployer: helm
    storageClass: gp3
    acceleratedNodeSelectors:
      - nvidia.com/gpu.present=true
    acceleratedNodeTolerations:
      - nvidia.com/gpu=present:NoSchedule
    workloadSelectors:
      - nkd.nvidia.com/workload=training
```

Omitting `bundle` means Helm with no overrides. Effective settings are bound into the generated Build Digest,
and the renderer consumes those exact graph settings. They do not create a cluster view or deployment claim.
