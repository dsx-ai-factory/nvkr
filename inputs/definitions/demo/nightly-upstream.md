# ARM64 nightly producer with upstream OVN

This is the explicit upstream OVN v1.4.0 alternative to `arm64-nvidia-kvm.json`. It retains all 29
selected components, platforms and Helm image bindings from
`arm64-upstream-tcg.json`. The root `../../catalogs/nvke/module-tags` supplies only
the existing pinned GitHub Actions, Syft and cosign tools. It does not replace selected output producers.
The profiles image uses the public output name `controller-profiles` required by the native NVKE
deployment renderer; it selects the same upstream OVN profiles catalog and component. The former local
output name `controller-profiles-upstream-ovn` is not an additional output or a fallback.

The CLI, controller image, controller chart and NKTP chart share the profile-owned
[nightly site catalog](../../catalogs/nvke/nightly-site-arm64/README.md). `installer-tools-arm64` shares the
separate [nightly installer-tools catalog](../../catalogs/tools/installer-hosted-arm64/README.md);
other output catalog selections remain explicit. Four tools
remain source-built with executable `host-package` output types; clusterctl selects 1.13.6 to
match the beta installer, while kind, kubectl and ORAS retain their existing pins.
Helm 4.2.4 is acquired by exact archive/executable digest because its diagnostic source preparation
is unsupported by hosted fetching. Its recorded binary security scan has findings; this is an
exception disclosure, not a security-pass or complete-source-build claim.

The disk first applies `../../catalogs/node-images/variants/nvidia-kvm.json`, selecting the base catalog's required-KVM
ARM64 producer while retaining the explicit NVIDIA OVN replacements. `../../catalogs/node-images/variants/upstream-kvm.json` then
replaces the same three OVN producers with the original upstream producer definitions. Its reviewed
predecessor digest is recomputed by the catalog owner for that KVM stack. The standalone image/chart,
disk CNI packages and controller profiles remain one explicit matched selection. No NVIDIA fallback
or compatibility, FIPS/STIG or qualification claim is implied.

`../../environments/github-nightly-upstream.json` uses the existing native ARC ARM64 resource contract, `build-provenance-v1`
disk evidence and `per-output-v1` signed publication. The workflow namespace is
`nvke-nightly-upstream-ovn-arm64`; the Environment declares its exact main-branch signing identity and
`ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-upstream-ovn-arm64` publication prefix.
These candidate settings do not establish live runner, source, registry or protected-environment access.

```sh
bin/nvkr definition validate --definition inputs/definitions/demo/arm64-upstream-kvm.json
bin/nvkr run generate \
  --definition inputs/definitions/demo/arm64-upstream-kvm.json \
  --environment inputs/environments/github-nightly-upstream.json
```

The Environment selects `githubRuntimePlan: true`. Each dispatch freezes current catalog pins within
the reviewed workflow/action and Environment contract, so routine source-version updates need no
regeneration commit. CI checks that contract with `run generate --runtime --check`; changes to the
execution contract still require normal regeneration and review.

The downstream nightly workflow owns the real NVKE suite and its cleanup. Generating this producer
workflow does not execute that suite or establish operational acceptance.

`../../testing/nightly-profiles.json` selects this project's generated producer and exact NVKE suite revision.
The coordinator checks supported native ARM64 runtime and OVN selections, authenticates the complete
publication, and requires both test and cleanup verdicts. Checksum-pinned tool acquisition adds no
vulnerability-clearance claim. All four site-product sources select
NVKE v0.22.0-beta.6 at `8db5721ae3031bdcd945e559153a740d46ea209e`. The OS closure
retains the explicit pre-DSX SPIRE exception at `726f76944404e1106626b8333d3576bdf67b575b`.
The test-only suite selects `b4f90e2c4fc4ed3d07efc18a22066a7e0b9b99c8`, an explicit
user-approved exception to ADR-23's proposed matching-revision rule. Product sources select the exact main commit.

The adapter now projects authenticated profile bytes into current `MachineImage`, `ClusterRelease`
and `NodePoolRelease` resources, preserving the exact disk binding and the candidate suite's
fixed profile name. It renders the current site-specification shape with private tenant credentials.
These paths are contract-tested, without a recorded runtime cycle.

The selected test source retains the candidate entrypoint omitted from the selected product source. It accepts the
verified publication without rebuilding products. This separate test pin does not establish
runtime compatibility or operational acceptance.
The first combined nightly cycle remains unrecorded.
