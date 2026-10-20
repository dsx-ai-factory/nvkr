# NVKE inputs and customer choices

The [maintained NVKE Definition](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/nvke/definition.yaml) selects 69 named outputs, including independent
`linux/amd64` and `linux/arm64` plain and public-bootstrap FDE disks. FDE publication carries build provenance
without inspected-content or deployed-confidentiality claims; see the [candidate's FDE scope](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/nvke/README.md#native-execution-and-publication).
The catalog owns producer commands, material edges and
component-exposed Packer variables. The Environment owns runner, runtime, registry and signing choices.
There is no generic Definition `overrides` field or arbitrary Packer/HCL rewriting.

## Input evidence map

| Field or choice | Owning source | Required check |
|---|---|---|
| Each disk's exact root and complete material set | M2 Definition and `inputs/catalogs/node-images/release/registry.yaml` | Resolve the explicitly selected closure for its native platform. |
| OS, Calico and kubeadm source | `inputs/catalogs/node-images/release/pins.lock.yaml`, NVKE `v0.22.0-beta.6` at `8db5721ae3031bdcd945e559153a740d46ea209e` | Exact commit/tree/epoch; each producer retains its own source binding. |
| Temporary SPIRE packaging selection | Same lock selects `nvke-spire-agent-source` at `726f76944404e1106626b8333d3576bdf67b575b` | The older package omits DSX IMDS. Do not claim full current-NVKE parity; [NVKE #1641](https://github.com/dsx-ai-factory/nvke/issues/1641) tracks restoration. |
| Runtime image selection | `inputs/catalogs/runtime-dependencies` plus NVKE deploy values at `8db5721ae3031bdcd945e559153a740d46ea209e` and their pinned charts | Exact native manifest pins for the twelve selected non-test images on both architectures. |
| Release-component pins | CLI/controller/operator catalogs and their own locks | Validate each source independently; one shared repository URL does not imply one release. |
| Reference-profile fixture | `inputs/catalogs/nvke/release-components`, output `nico-reference-profiles-fixture` | Test configuration and runbooks only; not the demo's runnable `controller-profiles` or approved fleet configuration. |
| Customer replacement | Ordered catalog overlay, bound to its exact predecessor snapshot | Preserve the logical component's output type and supported platforms; validate its new material closure. |

The [maintained NVKE input guide](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/nvke/README.md) records the detailed source paths and remaining
acceptance gaps. Catalog/graph validation authenticates selections, not installed containment, build
compatibility or publication acceptance.

## Replace a supplier without changing NVKR

1. Copy the Definition into your project and copy or re-point its referenced catalogs and overlays:
   relative catalog base and overlay paths are resolved from the Definition's directory.
   Select your catalog explicitly. Retain every required disk
   material, or deliberately change the disk producer's complete material contract and selection together.
2. Pin sources and acquired bytes with `catalog pin` / `catalog pin-artifacts`. Do not edit lock hashes or
   replace them with floating tags. These commands maintain a catalog directory's lock, not supplier-overlay
   `prebuilt.digests`. Those inline digests are reviewed, authored values for the exact supplier bytes,
   as shown in the authoring lab. A source release update is not a producer override.
3. To keep a logical component name but change how it is supplied, use a whole-producer catalog overlay.
   A source-built `nvke-cni-calico` can become a digest-pinned acquired tarball with the same output type and
   supported platforms. Its contents must still satisfy NVKE's image-input composition interface.
4. Apply the supplier overlay **after** `native-toolchains.yaml`. Its predecessor is the resulting native
   catalog snapshot, not the earlier base snapshot. Changing any OS-catalog pin changes its snapshot:
   reissue `native-toolchains.yaml` and each later overlay against its new exact predecessor through a
   reviewed decision. The current predecessor digests do not survive a repin; admission rejects them.
   Omitting or reordering an overlay fails admission.
5. Put that catalog stack on only the outputs that should use it. ARM64 and AMD64 are independent output
   selections, so a customer can retain the original supplier for one and substitute the other.

Use the existing [runnable authoring lab](examples/authoring-lab/README.md) for schema-exact complete
Definitions, supplier overlays, per-output stacks and retained before/after Plans. It works offline after
building `nvkr`; its supplier is explicitly a fixture, not a production NVKE package. It avoids inventing
an unverified package URL or digest in this guide.

The maintained-Definition test `TestMaintainedM2CustomerMaterialReplacement` applies catalog stacks and compiles both
native disk graphs with fixture source and acquired Calico suppliers. It checks changed Build identity
and exact material dependency edges. Its negative cases check Resolve selection errors for a missing
material, an unknown component and a platform mismatch; they do not test supplier downloads, missing
supplier digests, overlay reordering or the complete public document-loading path. These are graph
contracts, not container or image-build tests.

## Validate the complete maintained graph

With authorized access to the exact private source objects:

```sh
NVKR_TEST_M2_ALL_OUTPUT_GRAPHS=1 go test ./pkg/nvkr/buildgraph \
  -run '^TestMaintainedM2AllOutputGraphs$' -count=1
go test ./pkg/nvkr/buildgraph -run '^TestMaintainedM2CustomerMaterialReplacement$' -count=1
```

The complete test invokes public `factory.CompileDocuments`, including production's ordered overlay
loader, Environment admission and cross-output packaging relationships. A local Git `insteadOf` mapping
may supply identical pinned source bytes without changing any catalog identity; the
[deploy guide](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/examples/nvke-deploy/README.md#execute-locally) shows invocation-scoped configuration.
Regenerate the bundle from the same Definition and Environment and check drift.
Neither graph compilation nor regeneration proves a successful hosted delivery.
