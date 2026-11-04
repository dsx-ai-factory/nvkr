## Experimental OS source producers and OVN outputs

The separate `inputs/experiments/public-os-sources/catalog` catalog builds public OS binaries from
pinned Git sources. Its read-only `nvke-os-source-build.yaml` workflow exercises
`pipeline.Local` diagnostics on generic native AMD64/ARM64 hosted runners without
internal-source credentials. This is not full package-folder migration, OS-image
integration or a generated Run/Release. Helm's held `ExecNode` preparation remains
blocked by pristine external fixture links; local diagnostic preparation does not
admit that production route.

[Hosted run 37829955849](https://github.com/dsx-ai-factory/nvkr/actions/runs/37829955849)
passed all public-source producer builds on native AMD64 and ARM64 at
`335ace1e75829c061eac92caf42d8da0bc2fbd80`. It exercises unqualified
`pipeline.Local` execution, not OVN, full QCOW2 builds, production publication
or the held `ExecNode` preparation route.

The separate `inputs/definitions/diagnostics/ovn.yaml` Definition selects native image and
digest-bound Helm OCI outputs for both architectures. Contracts compile the
four-output Plan, enforce six same-platform image bindings, reject missing
slots/outputs and cross-platform joins, exclude publication destinations from Build
identity and check byte-exact generation. `values.digestFormat: sha256-hex` supports
templates that supply their own algorithm prefix; unknown formats fail before
chart mutation. Ordinary hosted ARM64 producers select `ubuntu-24.04-arm` when
`runsOn` is unset; explicit selections and VM/ARC routing retain precedence.

These are experimental candidate inputs, not maintained M2 membership or proof of
GHCR publication, signed Release acceptance or OS integration. Internal fork access
requires separate acceptance. Authenticated cross-output image/final-chart handoff
into OS material composition remains absent ([issue #116](https://github.com/dsx-ai-factory/nvkr/issues/116)).
Rebuilding equal pins does not establish same-byte correspondence; opt-in producer
and Helm OCI interoperability tests do not establish hosted or release acceptance.
