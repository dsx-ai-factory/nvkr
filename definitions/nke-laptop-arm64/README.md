# ARM64 local NKE example

Start with the [first-time developer guide](../../docs/local-development.md).
This example selects the ARM64 counterpart of the combined M2 artifact selection
plus CAPNICo and the provisioned provider, without publication or signing. It is
not a full source-built NKE distribution. See the
[full-source gap map](../../docs/full-nke-source-build.md).

| Selection | Definition | Outputs |
| --- | --- | --- |
| Complete selected set | `definition.json` | 23: disk, images, charts and executable bundles |
| Faster component loop | `definition-components.json` | 22: everything above except the disk |
| OS-only loop | `definition-os.json` | One disk with its complete material closure |
| Source-built provider loop | `definition-providers.json` | Two native provider images, no disk |

All use `environment-local.json`: native rootless Podman, Linux ARM64 and no
publication. The OS selects TCG and the maintained local Packer subject through
`os-tcg.json`. The CLI catalog adds an explicit ARM64 producer; its pin starts
at the maintained CLI release and its stamps follow machine-managed pins.
The operator catalog pins native ARM64 Helm and kubectl assets.
The CAPNICo catalog builds the public v0.0.45 Git source using its source-owned
Dockerfile; the provisioned provider reuses the maintained controller catalog's
existing source producer. Neither provider is replaced with an acquired image.

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
