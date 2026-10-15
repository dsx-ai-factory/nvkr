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
remain source-built at their original pins and commands, with executable `host-package` output types.
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

The downstream nightly workflow owns the real NVKE suite and its cleanup. Generating this producer
workflow does not execute that suite or establish operational acceptance.

`../../testing/nightly-profiles.json` selects this project's generated producer and exact NVKE suite revision.
The coordinator checks supported native ARM64 runtime and OVN selections, authenticates the complete
publication, and requires both test and cleanup verdicts. Checksum-pinned tool acquisition adds no
vulnerability-clearance claim. The suite and all four site-product sources select
`a8391755d12d99f859783c6cf390338debd1e085`, including its `^TestE2E$` runtime entrypoint
selection and candidate-input fixes. This candidate comes from open NVKE PR #1580;
it is not a merged-main or human-approval claim. The shared revision follows ADR-23's
proposed matching-revision rule without establishing overall interface adoption.
The first combined nightly cycle remains unrecorded.
