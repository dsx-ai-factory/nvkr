# NKE M2 inputs and customer choices

The [M2 Definition](../definitions/nke-m2/definition.yaml) selects 34 named outputs, including independent
`linux/amd64` and `linux/arm64` non-FDE disks. The catalog owns producer commands, material edges and
component-exposed Packer variables. The Environment owns runner, runtime, registry and signing choices.
There is no generic Definition `overrides` field or arbitrary Packer/HCL rewriting.

## Input evidence map

| Field or choice | Owning source | Required check |
|---|---|---|
| Each disk's exact root and complete material set | M2 Definition and `os-catalog/registry.yaml` | Resolve the explicitly selected closure for its native platform. |
| OS, Calico and kubeadm source | `os-catalog/pins.lock.yaml`, NKE `ff21ba8c07cafff74b68e238a8876af11e5beded` | Exact commit/tree/epoch; retain this separately from controller/CLI release selection. |
| Temporary embedded SPIRE exception | Same lock, source `nke-spire-agent-source` at `726f76944404e1106626b8333d3576bdf67b575b` | Do not call this mixed-source image the intended complete M2 image. The DSX IMDS asset remains excluded by this older package. |
| Runtime image selection | `image-catalog` plus NKE deploy values at `33fb71b2f4224b470ebc628a9165fc4b92870406` and their pinned charts | Exact native manifest pins for the twelve selected non-test images on both architectures. |
| Release-component pins | CLI/controller/operator catalogs and their own locks | Validate each source independently; one shared repository URL does not imply one release. |
| Customer replacement | Ordered catalog overlay, bound to its exact predecessor snapshot | Preserve the logical component's output type and supported platforms; validate its new material closure. |

The [maintained evidence map](m2-input-evidence-map.md) records the detailed source paths and remaining
acceptance gaps. Catalog/graph validation authenticates selections, not installed containment, build
compatibility or publication acceptance.

## Replace a supplier without changing NKD

1. Copy the Definition into your project and copy or re-point its referenced catalogs and overlays:
   relative catalog base and overlay paths are resolved from the Definition's directory.
   Select your catalog explicitly. Retain every required disk
   material, or deliberately change the disk producer's complete material contract and selection together.
2. Pin sources and acquired bytes with `catalog pin` / `catalog pin-artifacts`. Do not edit lock hashes or
   replace them with floating tags. These commands maintain a catalog directory's lock, not supplier-overlay
   `prebuilt.digests`. Those inline digests are reviewed, authored values for the exact supplier bytes,
   as shown in the authoring lab. A source release update is not a producer override.
3. To keep a logical component name but change how it is supplied, use a whole-producer catalog overlay.
   A source-built `nke-cni-calico` can become a digest-pinned acquired tarball with the same output type and
   supported platforms. Its contents must still satisfy NKE's image-input composition interface.
4. Apply the supplier overlay **after** `native-toolchains.yaml`. Its predecessor is the resulting native
   catalog snapshot, not the earlier base snapshot. Changing any OS-catalog pin changes its snapshot:
   reissue `native-toolchains.yaml` and each later overlay against its new exact predecessor through a
   reviewed decision. The current predecessor digests do not survive a repin; admission rejects them.
   Omitting or reordering an overlay fails admission.
5. Put that catalog stack on only the outputs that should use it. ARM64 and AMD64 are independent output
   selections, so a customer can retain the original supplier for one and substitute the other.

Use the existing [runnable authoring lab](examples/authoring-lab/README.md) for schema-exact complete
Definitions, supplier overlays, per-output stacks and retained before/after Plans. It works offline after
building `nkdctl`; its supplier is explicitly a fixture, not a production NKE package. It avoids inventing
an unverified package URL or digest in this guide.

The M2-specific `TestMaintainedM2CustomerMaterialReplacement` applies catalog stacks and compiles both
native disk graphs with fixture source and acquired Calico suppliers. It checks changed Build identity
and exact material dependency edges. Its negative cases check Resolve selection errors for a missing
material, an unknown component and a platform mismatch; they do not test supplier downloads, missing
supplier digests, overlay reordering or the complete public document-loading path. These are graph
contracts, not container or image-build tests.

## Validate the complete maintained graph

With authorized access to the exact private source objects:

```sh
NKD_TEST_M2_ALL_OUTPUT_GRAPHS=1 go test ./pkg/nkd/buildgraph \
  -run '^TestMaintainedM2All34OutputGraphs$' -count=1
go test ./pkg/nkd/buildgraph -run '^TestMaintainedM2CustomerMaterialReplacement$' -count=1
```

The complete test invokes public `factory.CompileDocuments`, including production's ordered overlay
loader, Environment admission and cross-output packaging relationships. A local Git `insteadOf` mapping
may supply identical pinned source bytes without changing any catalog identity; the
[deploy guide](nke-deploy-pipeline.md#local) shows invocation-scoped configuration.
Regenerate the bundle from the same Definition and Environment and check drift.
Neither graph compilation nor regeneration proves a successful hosted delivery.
