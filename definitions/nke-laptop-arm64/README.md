# ARM64 local NKE example

Start with the [first-time developer guide](../../docs/local-development.md).
This example produces the ARM64 counterpart of the combined M2 artifact selection,
without publication or signing. It is not every artifact in the NKE monorepo.

| Selection | Definition | Outputs |
| --- | --- | --- |
| Complete selected set | `definition.json` | 21: disk, images, charts and executable bundles |
| Faster component loop | `definition-components.json` | 20: everything above except the disk |
| OS-only loop | `definition-os.json` | One disk with its complete material closure |

All use `environment-local.json`: native rootless Podman, Linux ARM64 and no
publication. The OS selects TCG and the maintained local Packer subject through
`os-tcg.json`. The CLI catalog adds an explicit ARM64 producer; its pin starts
at the maintained CLI release and its stamps follow machine-managed pins.
The operator catalog pins native ARM64 Helm and kubectl assets.

```sh
bin/nkdctl definition validate --definition definitions/nke-laptop-arm64/definition.json
bin/nkdctl run local \
  --definition definitions/nke-laptop-arm64/definition.json \
  --environment definitions/nke-laptop-arm64/environment-local.json \
  --attempt arm64-preview --dry-run

NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh arm64-components-01 1 components
```

Use a fresh attempt for execution. `NKE_LOCAL_CLONE` supplies pinned Git objects,
not dirty checkout bytes; omit it for canonical remote fetch. See the guide for
commit-based development, exact outputs, resource checks and the unresolved fresh
Packer bootstrap constraint. Full-set success is not established by the dry-run.
