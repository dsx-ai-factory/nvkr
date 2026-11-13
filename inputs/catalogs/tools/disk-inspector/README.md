# Disk inspection helper

This catalog builds `nvkr-disk-inspect` as a static Linux executable for AMD64 or
ARM64. The helper package does not import catalog, buildgraph or pipeline. Its
source revision, per-architecture Go SDK images and native Syft scanners are pinned;
the ordinary producer fetch freezes dependencies before offline compilation.

Select the helper as an internal tool alongside a disk output. Paths below are
relative to a Definition in `inputs/definitions/nvke/`:

```yaml
internalTools:
  - name: inspector-arm64
    platform: linux/arm64
    catalog:
      base: ../../catalogs/tools/disk-inspector
    components: [nvkr-disk-inspect]
    result: nvkr-disk-inspect
    format: executable
    sbomTool: syft-arm64
```

Set the selected Environment's `diskInspectionProfiles.linux/arm64.helperTool`
to `inspector-arm64`. Keep the profile's existing appliance and measured-tool
digests: the helper is injected into that appliance at execution time, after its
producer, terminal observation and SBOM complete. An explicit `sbomTool` resolves
from this catalog; an omitted selector inherits the Definition's scanner only
when its native platform matches.

The same profile must set `bootstrapInterpreterDigest` to the exact SHA-256 of
`/usr/bin/perl` in its selected appliance and `bootstrapProgramDigest` to the
SHA-256 of `pkg/nvkr/pipeline/inspection_bootstrap.pl` from the executing NVKR
revision. Both values use the `sha256:` prefix. These observation pins belong to
the Environment and Plan; they do not change the helper or disk producer's
logical Build. The compiler rejects a program pin it does not implement.

Before producer work, NVKR checks the interpreter bytes and exercises sealing and
descriptor execution in the native appliance. Missing support fails admission.
During inspection, the original owner streams the held helper bytes followed by
the subject. The bootstrap seals and measures an anonymous executable, then
executes its descriptor while preserving the subject stream. No host executable
path is mounted. The appliance digest binds the interpreter's module and library
closure; the helper's original producer receipt remains required.

Execution currently requires the `native-kvm-v1` hosted disk group and one disk
inspection consumer per helper selection. The helper remains in the original
execution owner through inspection. It is required for Run completion and is
excluded from delivered outputs and Release membership. A path or digest supplied
by a caller cannot authorize injection. This catalog does not enable deep inspection
for a `build-provenance-v1` Environment; select the existing strict inspection
contract explicitly when exercising this path.

For AMD64, use `linux/amd64`, a distinct helper name and `syft-amd64`. Separate
native selections can share this catalog. The maintained disk Definition has not
been cut over; full disk execution and hosted acceptance are separate evidence.

Update source and scanner pins through the CLI:

```sh
bin/nvkr catalog pin --dir inputs/catalogs/tools/disk-inspector --commit <full-source-commit> nvkr-inspector
bin/nvkr catalog pin --dir inputs/catalogs/tools/disk-inspector --tag v1.51.1 syft-amd64
bin/nvkr catalog pin --dir inputs/catalogs/tools/disk-inspector --tag v1.51.1 syft-arm64
```

The existing appliance-image directories remain until both architectures pass the
runtime replacement and cutover gates. Building this executable does not establish
qualified or hosted-workflow evidence.
