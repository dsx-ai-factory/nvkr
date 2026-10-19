# ARM64 nightly producer inputs

This candidate selects the same 29 named outputs, components, platforms and Helm image bindings as
`../nvke-local-dev-arm64/definition.json`. The CLI, controller image, controller chart and NKTP chart
use the profile-owned [site catalog](site-catalog/README.md); installer tools use the separate
compatibility catalog described below. Other output selections retain their explicit catalogs. The root
`../nvke-v1/catalog` supplies only the existing pinned GitHub Actions, Syft and cosign tools; it does not
replace an output's producer selection. Acquired inputs and the mixed SPIRE source remain explicit.

`installer-tools-catalog` selects clusterctl 1.13.6 to match the beta installer and retains the
existing kind, kubectl and ORAS source pins. Their executable outputs use `host-package`. Helm is the explicit
acquired exception: the same upstream 4.2.4 ARM64 release, pinned by the catalog owner to its archive
and executable hashes. Hosted source fetching rejects Helm's external test symlinks before its local
diagnostic preparation could remove them. The profile does not weaken that fetch boundary or claim
that acquired Helm is source-built. See the [tool evidence and security finding](installer-tools-catalog/README.md).

The disk selects the base M2 catalog's required-KVM ARM64 producer. `os-kvm.json` retains the local
definition's three NVIDIA OVN replacements and their exact predecessor catalog digest, while omitting
its TCG-only disk replacement. The alternative upstream OVN profile is authored separately in
`../nvke-nightly-upstream-ovn-arm64`; there is no automatic fallback between them.

`environment-github.json` selects the admitted native ARC KVM route and the M2 candidate's ARM64
resource budgets, with `build-provenance-v1` disk evidence and `per-output-v1` signed publication.
The workflow namespace is `nvke-nightly-arm64`; its exact main-branch signing identity is declared in
the Environment. Publication uses `ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-arm64`.
These are authored candidate settings, not evidence of available runners, protected-environment
admission, completed publication, runtime compatibility, FIPS/STIG compliance or qualification.

Validate and regenerate through the existing owner:

```sh
bin/nvkr definition validate --definition definitions/nvke-nightly-arm64/definition.json
bin/nvkr run generate \
  --definition definitions/nvke-nightly-arm64/definition.json \
  --environment definitions/nvke-nightly-arm64/environment-github.json
```

The downstream nightly workflow owns executing the NVKE runtime suite. Definition validation and
workflow generation establish neither runtime E2E success nor hardware acceptance. Runner capacity,
toolchain access, source access and protected-environment policy require independent admission.

`../nightly-profiles.json` selects this project's generated producer and exact NVKE suite revision.
The coordinator checks supported native ARM64 runtime and OVN selections, authenticates the complete
publication, and requires both test and cleanup verdicts. Checksum-pinned tool acquisition adds no
vulnerability-clearance claim. All four site-product sources select
NVKE `v0.22.0-beta.1` at `6991e36bef8f15b42b6f6519ccff089c587a6e49`. The OS closure
retains the explicit pre-DSX SPIRE exception at `726f76944404e1106626b8333d3576bdf67b575b`.
The test-only suite selects `a32067108ab89d1e2b2b6720766ffe97270a166a`, an explicit
user-approved exception to ADR-23's proposed matching-revision rule. Product sources remain beta.

The adapter now projects authenticated profile bytes into beta `MachineImage`, `ClusterRelease`
and `NodePoolRelease` resources, preserving the exact disk binding and the candidate suite's
fixed profile name. It renders the current site-specification shape with private tenant credentials.
These paths are contract-tested, without a recorded runtime cycle.

The selected test source retains the candidate entrypoint omitted from beta. It accepts the
verified publication without rebuilding products. This separate test pin does not establish
runtime compatibility or operational acceptance.
The first combined nightly cycle remains unrecorded.
