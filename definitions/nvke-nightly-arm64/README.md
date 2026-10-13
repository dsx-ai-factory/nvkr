# ARM64 nightly producer inputs

This candidate selects the same 29 named outputs, components, platforms and Helm image bindings as
`../nvke-local-dev-arm64/definition.json`. The CLI, controller image, controller chart and NKTP chart
use the profile-owned [site catalog](site-catalog/README.md); installer tools use the separate
compatibility catalog described below. Other output selections retain their explicit catalogs. The root
`../nvke-v1/catalog` supplies only the existing pinned GitHub Actions, Syft and cosign tools; it does not
replace an output's producer selection. Acquired inputs and the mixed SPIRE source remain explicit.

`installer-tools-catalog` preserves the original clusterctl, kind, kubectl and ORAS source pins and
build commands, declaring their actual executable outputs as `host-package`. Helm is the explicit
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

`../nightly-profiles.json` records this profile as security-blocked. The nightly coordinator refuses
it before dispatch, verifier download or runtime allocation. The independently pinned ARM64 Cosign
3.1.3 executable matches its official release checksum but the repository-pinned binary scanner found
27 vulnerable-symbol findings; the newest official stable release is the same version. Combined with
the installer Helm findings above, this leaves no admitted verifier/installer tool selection. Generating
the committed bundle exercises configuration and drift contracts without executing those tools.
