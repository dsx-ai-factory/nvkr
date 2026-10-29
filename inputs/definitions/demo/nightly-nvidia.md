# ARM64 nightly producer inputs

`arm64-nvidia-kvm.json` retains all 29 existing named outputs and adds five native
runtime outputs: the CAPI init and exec-plugin images, the workload image, the
complete Linux host-tool bundle and the guest startup-tool bundle. The resulting
34-output producer remains in the existing `nvke-nightly-arm64` namespace; no
second architecture producer is introduced.

CLI, controller, provisioned-provider and chart outputs reuse
`../../catalogs/nvke/native-site`. Simulator and host/site tool outputs reuse
`../../catalogs/tools/native`. Controller profiles reuse
`../../catalogs/controller-profiles/native-nvidia`. These are the existing
native Go bootstrap inputs, not copied nightly catalogs. The retained
`installer-tools-arm64` output continues to use the hosted installer catalog and
its explicit acquired Helm exception.

The disk applies the existing `native-nvidia.json` and `native-local-kvm.json`
overlays to the release node-image catalog. Its NVIDIA OVN 2.4.2 producer agrees
with the standalone OVN image and chart outputs. The overlays retain exact
predecessor bindings and required native KVM execution; there is no upstream OVN
fallback. The CAPI init output selects the separately reviewed current-main
acquisition from the shared native-tools catalog. The tagged local demo retains
its original acquisition.

The existing `github-nightly-nvidia.json` Environment retains ARC ARM64 KVM disk
production, `build-provenance-v1` evidence, private/internal artifact storage and
`per-output-v1` signed publication. Its publication prefix remains
`ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-arm64`; its exact main-branch
signing identity remains in the Environment. Source credentials are passed only
to the selected fetch owners.

Regenerate this existing bundle through its owner after changing output membership:

```sh
bin/nvkr definition validate --definition inputs/definitions/demo/arm64-nvidia-kvm.json
bin/nvkr run generate --engine github \
  --definition inputs/definitions/demo/arm64-nvidia-kvm.json \
  --environment inputs/environments/github-nightly-nvidia.json --root .
```

The Environment selects `githubRuntimePlan: true` and an explicit
`githubRuntimeSource` policy for NVKE main. The coordinator resolves main once
and passes its full commit through the generated producer's required
`source_commit` dispatch input. The catalog owner authenticates that exact Git
tree and applies it after existing overlays to every source-built NVKE product,
including disk materials and the installer. Commands, toolchains and separately
acquired dependencies retain their reviewed selections. No catalog lock or
authored overlay is rewritten.

Bootstrap freezes that source in the existing same-Run Plan transport. Secondary
bootstrap and finalization recompile with the same exact commit; mixed-source or
changed dispatch inputs fail. Routine source changes within the reviewed workflow
contract do not require regeneration; changes to membership, scheduling or
Environment policy do. This producer does not emit a qualification input handoff.

The downstream native Go test consumes the completed, authenticated producer
artifacts. Definition validation and Plan compilation prove neither successful
publication nor VM bootstrap, cleanup, FIPS/STIG compliance or qualification.
