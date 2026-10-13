# ARM64 nightly producer with upstream OVN

This is the explicit upstream OVN v1.4.0 alternative to `../nvke-nightly-arm64`. It retains all 29
selected components, platforms and Helm image bindings from
`../nvke-local-dev-arm64/definition-upstream-ovn.json`. The root `../nvke-v1/catalog` supplies only
the existing pinned GitHub Actions, Syft and cosign tools. It does not replace selected output producers.
The profiles image uses the public output name `controller-profiles` required by the native NVKE
deployment renderer; it selects the same upstream OVN profiles catalog and component. The former local
output name `controller-profiles-upstream-ovn` is not an additional output or a fallback.

The CLI, controller image, controller chart and NKTP chart share the profile-owned
[nightly site catalog](../nvke-nightly-arm64/site-catalog/README.md). `installer-tools-arm64` shares the
separate [nightly installer-tools catalog](../nvke-nightly-arm64/installer-tools-catalog/README.md);
other output catalog selections remain explicit. Four tools
remain source-built at their original pins and commands, with executable `host-package` output types.
Helm 4.2.4 is acquired by exact archive/executable digest because its diagnostic source preparation
is unsupported by hosted fetching. Its recorded binary security scan has findings; this is an
exception disclosure, not a security-pass or complete-source-build claim.

The disk first applies `../nvke-nightly-arm64/os-kvm.json`, selecting the base catalog's required-KVM
ARM64 producer while retaining the explicit NVIDIA OVN replacements. `os-upstream-ovn.json` then
replaces the same three OVN producers with the original upstream producer definitions. Its reviewed
predecessor digest is recomputed by the catalog owner for that KVM stack. The standalone image/chart,
disk CNI packages and controller profiles remain one explicit matched selection. No NVIDIA fallback
or compatibility, FIPS/STIG or qualification claim is implied.

`environment-github.json` uses the existing native ARC ARM64 resource contract, `build-provenance-v1`
disk evidence and `per-output-v1` signed publication. The workflow namespace is
`nvke-nightly-upstream-ovn-arm64`; the Environment declares its exact main-branch signing identity and
`ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-upstream-ovn-arm64` publication prefix.
These candidate settings do not establish live runner, source, registry or protected-environment access.

```sh
bin/nvkr definition validate --definition definitions/nvke-nightly-upstream-ovn-arm64/definition.json
bin/nvkr run generate \
  --definition definitions/nvke-nightly-upstream-ovn-arm64/definition.json \
  --environment definitions/nvke-nightly-upstream-ovn-arm64/environment-github.json
```

The downstream nightly workflow owns the real NVKE suite and its cleanup. Generating this producer
workflow does not execute that suite or establish operational acceptance.

`../nightly-profiles.json` records this profile as security-blocked. The coordinator refuses it before
dispatch, verifier download or runtime allocation. Both the installer Helm releases inspected and the
independently pinned newest stable Cosign 3.1.3 ARM64 binary have unresolved vulnerability findings.
The latter matches official checksum metadata and has 27 vulnerable-symbol findings from the pinned
binary scanner. Configuration generation does not execute these unapproved tools or waive their findings.
