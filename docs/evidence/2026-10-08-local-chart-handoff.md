# Local exact-chart handoff to NKE

## Scope

October 8, 2026, macOS ARM64, PR #118 implementation checkout based on
`b70db4c31cb73b75013f1c3a277ceec7decbe775`, plus the chart-export/adapter changes
recorded with this evidence. This is diagnostic local transport and rendering,
not a complete artifact Run, site installation, tenant boot or Release authority.

A clean real sparse Git clone of NKE at
`8fb2d53368823bd386b4fbd55b4ab6d3c0931345` supplied the strict deploy-item reader,
tracked `deploy/nico` files, renderer and executor. The original NKE checkout
was not edited. The pinned clone's `git status --porcelain` remained empty after
preparation, rendering and executor dry-run.

## Inputs and exact exported bytes

The four external charts came from completed Run
`laptop-container-components-arm64-20261007-01`: status `complete`, lane
`unqualified-local-execution`, 22 expected/observed outputs, no failure.
Controller and NKTP were separately packaged from private copies of the pinned
NKE source using actual Helm `v4.3.0+gbec5b06` and existing NKD Helm packaging.
NKTP's source-owned locked dependencies were prepared before packaging. Those
two packages are **manual diagnostic probes, not a completed four-output site
Run**. No source-built-distribution claim is made for the acquired charts or
NKTP dependencies. Organizational tool/dependency approval is not established.

| NKE item | Version | OCI manifest SHA-256 | Exported archive SHA-256 | Bytes |
| --- | --- | --- | --- | ---: |
| controller (`nke-controller`) | 0.0.0 | `74efcbf6a7a59f6afd12d347e356dbaf4ba967ff1621cc1bf9e2d90ebe865090` | `3cc8ff515eb771265697300368dd664df545e8d9ab4740940bcd05b56501d757` | 39,371 |
| nktp | 0.1.0-rc.2 | `c1bc5337d71eb18f6f5bfd87c43894215621d487d45f8f66f6413d4c27826d4d` | `7a08bcc6ffa25da6a72e461d187d7dede26d826baa94a7a91b92bdcc7a603255` | 357,665 |
| postgresql | 18.5.15 | `879c8373581613b0a50e10d8bd88c4a07ed362e26f10b31549dc783a96d7d753` | `c350143d2e21c1e1a3670dd32ab64fd11d49198a66fae87c63a3112531c0cd17` | 89,389 |
| spire | 0.30.1 | `8a98b44f8ae62577b3703ee20759149cc0d725c4af07951edf13a595d134ccb2` | `7c32126bb1909076cdf9fd0419dbf9e5b012f152bb09a96a4259917d70581fac` | 346,108 |
| spire-crds | 0.6.1 | `b995224bcedc0d270c9a527e54bf2204033329638ed9b6a87d626be948904d04` | `ce982e63fc375e392b014fc99e621a55442ab886052413e9bb052f72d66580a8` | 4,488 |
| temporal | 0.74.0 | `5175a6f550767e4160470fd6dd93f75bfe3293c61c4bd2e321db9d3fbb5e2f3c` | `14a8efe1cc92c91befac151db91f20f9a13962788e9d710c86655add6d966d37` | 181,251 |

These manifest identities are distinct from archive identities. Export used
`nkdctl publish chart` through `scripts/nke-prepare-chart-inputs.py`; it checked
the exact selected manifests and all layout blobs, the owning archive validator,
name/version and archive/config metadata correspondence. Existing destinations
are never replaced. No chart is repackaged during export.

## Real consumer probes

The adapter created a new private `deploy` tree whose six chart sources point to
`charts/<item>.tgz`. NKE's own item reader revalidated unchanged release names,
namespaces, values and other fields.

The actual pinned `deploy/lib/describe_site.py` renderer and actual host Helm
were exercised with private diagnostic values. Six successful Helm template
calls consumed the six prepared archives and emitted 28 unique image references.
Subprocess instrumentation rejected any Helm invocation other than `template`,
and required every template chart argument to be an archive. No Helm repository,
dependency build, chart pull, Make or source-package command ran during handoff
or rendering.

The first render failed because the controller correctly required profiles image
identity. A private diagnostic-only Bootstrap configuration was then produced
using NKE's own validation/build functions. It used non-deployable
`example.invalid` image references, loopback endpoints and a temporary one-day
self-signed CA. Private keys and configuration remain outside Git. Passing this
render does not establish registry availability, valid production trust, profiles
compatibility or deployability.

The pinned executor also completed a bounded dry-run:

```sh
deploy/nke-deploy.sh install \
  --deploy-path /absolute/path/to/prepared/deploy \
  --config /absolute/path/to/diagnostic-bootstrap.json \
  --kubeconfig /absolute/path/to/nonexistent-diagnostic-kubeconfig \
  --dry-run
```

It exited 0 and printed six Helm upgrade commands pointing at the prepared
archives, plus the source-owned manifest/certificate operations. The dry-run did
not contact a Kubernetes API or deploy resources. No release manifest was passed;
its chart references could otherwise take precedence over the local paths.
This proves the executor's local-path selection only, not the complete staged
wrapper's manifest, readiness, registry, bootstrap or install path.

## Validation and remaining acceptance

`make check` passed, including lint, shellcheck, repository tests and the Go
vulnerability scan (`No vulnerabilities found`). Focused export/adapter tests
also passed with the race detector. Independent review identified an unsafe
multi-file `jq -e` example and missing Bash failure propagation; the example now
slurps both records, requires both complete and enables fail-fast execution.
Focused contracts
cover exact bytes, wrong identities, tampered blobs, unsafe paths/links/root
metadata, gzip checksum failure, dirty/wrong source, incomplete membership,
failed extraction, rollback, no-replace destinations and source/layout containment.

Still required: complete source-aligned site and tools Runs, consistent staged
manifest/override integration, reachable exact-image registry transport,
matching profiles, real NICoSim VM services, full site/tenant/workload acceptance
and owned cleanup. The ongoing OS bake is independent evidence and is not
converted into successful disk or tenant evidence by this chart probe.
