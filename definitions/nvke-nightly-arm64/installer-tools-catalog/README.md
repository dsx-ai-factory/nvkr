# Nightly ARM64 installer tools

This profile-owned catalog retains the local installer tools' source pins, vendoring and build
commands for clusterctl v1.13.6, kind v0.32.0, kubectl v1.36.0 and ORAS v1.3.3. Their produced binaries
are `host-package` outputs, matching executable packaging and the existing production SBOM inspector.
The local source catalog uses the same tool versions.

Helm remains version 4.2.4 but is an acquired ARM64 executable. The hosted source fetch owner rejects
the external test symlinks in that source before the local diagnostic vendor command can remove
them. This catalog uses the existing release-executable contract rather than weakening source capture.
`nvkr catalog pin --dir definitions/nvke-nightly-arm64/installer-tools-catalog --tag v4.2.4 helm`
generated its lock entry on October 13, 2026. Cluster API is pinned to v1.13.6 with
`catalog pin`, matching the v0.22.0-beta.1 installer; kind, kubectl and ORAS retain their source pins.

| Binding | Verified value |
| --- | --- |
| Upstream release | [Helm v4.2.4](https://github.com/helm/helm/releases/tag/v4.2.4) |
| Source commit | `3900f434fd3ef2b84065dc04508df48f288dba00` |
| Source tree | `16e7ec45b01a241f5011caad2fe8751208a7ba94` |
| Asset | [helm-v4.2.4-linux-arm64.tar.gz](https://get.helm.sh/helm-v4.2.4-linux-arm64.tar.gz) |
| Archive SHA-256 | `564de2191b881e9f71b5606b25345821ea1682f06ab90499d3ab22b530176da1` |
| Executable SHA-256 | `0690653ac2aad19150f0c22383ceb8aaf4fd96666fcfabb9d4641fbf757e0490` |
| Executable member | `linux-arm64/helm` |
| License | [Apache-2.0 at the exact source commit](https://raw.githubusercontent.com/helm/helm/3900f434fd3ef2b84065dc04508df48f288dba00/LICENSE) |

The archive hash agrees with the [upstream checksum](https://get.helm.sh/helm-v4.2.4-linux-arm64.tar.gz.sha256sum)
and the release page. The catalog owner independently measured the archive and executable bytes.
This is acquisition integrity and source-version binding; the v1 acquisition contract does not
authenticate upstream signatures or prove reproduction of the release binary from source.

The repository-pinned govulncheck v1.8.0 binary scanner reported 28 vulnerabilities from three modules
and the Go standard library in this exact executable on October 13, 2026. Examples include
[`GO-2026-6629`](https://pkg.go.dev/vuln/GO-2026-6629) in `golang.org/x/text` v0.40.0,
fixed in v0.41.0, and HTTP findings in Go 1.26.5 and `golang.org/x/net` v0.56.0.
Binary scanning detects included vulnerable symbols and does not establish reachability in the
nightly's Helm commands. This candidate keeps the original local fixture's version; that choice is
not a user requirement. The scan does not establish a clean security result or vulnerability clearance.

The authoritative upstream release API and release list also reported
[v4.3.0](https://github.com/helm/helm/releases/tag/v4.3.0) as the newest stable Helm 4 release on
October 13. Its ARM64 archive matched the published SHA-256
`31c5794dd55c66a51e6b7d2e2ac7a114ae8b1de41ff1d9ba51748ac973b06a08`, but the same pinned
binary scanner reported 15 vulnerabilities in one module and the standard library. Examples require
`golang.org/x/net` v0.60.0 instead of v0.57.0 and Go 1.27.2 instead of 1.27.1. The candidate did not
adopt that asset. These dated scan observations do not introduce a new nightly approval field or
replace the repository vulnerability gate. ADR-13 keeps artifact vulnerability evaluation and clearance
outside the v1 product scope; acquired executables retain their explicit evidence limitations.

Compiling or generating the nightly inputs does not establish a source build, successful installation,
live runner capacity or runtime qualification.
