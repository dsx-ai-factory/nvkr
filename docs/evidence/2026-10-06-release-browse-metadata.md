# Release browsing without artifact payload downloads

On 2026-10-06, the changed release browser inspected the release published by
[Run 37533806963](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963)
from macOS ARM64 using Go 1.27.1 with `GOTOOLCHAIN=local`. The run completed
successfully at source commit `625980e30b1a9f5ef27a9b46534de65eefff385a`; this
exercise used the local browser changes on that commit.

This establishes metadata inspection of that exact external release. It does not
establish payload integrity, signer authenticity, policy acceptance, qualification,
or deployment readiness. This exercise did not run explicit release verification.

## Exact external subject

The publication job and the run's `nkd-publication-release` artifact both report:

```text
ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/releases@sha256:292524ba607d5a45326ae4801e81867f5358921824df3e12c59d274360baaf0e
```

The release metadata config is 88,076 bytes, with exact blob digest
`sha256:75ec2768aa4174d2d7f2fc6daad43040f0a8a05d0d1c96e3c325f4ebe889577d` and
schema `nkd.dev/publication-root/v3`. It declares 34 outputs and 60 evidence members,
including these disk images:

| Output | Config platform | Declared QCOW2 bytes |
| --- | --- | ---: |
| `nke-node-image-amd64` | `linux/amd64` | 3,578,528,256 |
| `nke-node-image-arm64` | `linux/arm64` | 3,298,230,784 |

The run's `nkd-metadata-release` carrier contains 193 files totaling 2,213,704
bytes. Its output manifests declare 111 unique payload-layer digests; none of
those layers is present in the carrier. These are metadata declarations, not
measurements of downloaded QCOW2 payloads.

## Live registry and CLI results

Using the existing registry credential keychain, the rebuilt CLI completed:

```sh
nkdctl release browse --reference "$RELEASE_REF" --format json \
  --timeout 10m --workspace "$WORKSPACE"
```

The command returned exit 0 in 198.486 seconds, producing 96,092 bytes of JSON
with all 34 outputs and 60 evidence members. Its inspection label was:

```text
metadata inspection; payloads, signatures and policy not verified
```

A separate live call through the public lifecycle inspection API wrapped the
registry transport with a guard rejecting every request for any of the 111
declared output-layer digests. The guard used the exact output manifests from the
run's metadata carrier and recorded only request methods, hosts and paths, without
credentials or query strings. It completed successfully:

| Observation | Recorded result |
| --- | ---: |
| Output payload-layer requests | **0** |
| Unique output payload-layer digests guarded | 111 |
| Response bytes read, including authentication and metadata | 2,249,434 |
| HTTP requests, including authentication and redirects | 908 |
| Outputs / evidence members displayed | 34 / 60 |
| Elapsed seconds | 174.276 |

The direct CLI JSON, guarded live inspection JSON and local-carrier JSON were
byte-identical, with output blob SHA-256
`ab2e7d4fd77f76b581cac8838b6d4d19bc01b8529c0df7418ab19014d6ef17b2`.
Both live invocations removed their private acquisition directories and left their
caller-owned workspaces empty. The 6,876,759,040 declared disk payload bytes were
not downloaded by the guarded inspection.

## Presentation and filtering

The same CLI inspected the metadata carrier downloaded from this run using:

```sh
nkdctl release browse --reference "$RELEASE_REF" --layout "$LAYOUT" --format text
nkdctl release browse --reference "$RELEASE_REF" --layout "$LAYOUT" --format json
nkdctl release browse --reference "$RELEASE_REF" --layout "$LAYOUT" --format mermaid
```

Full text, JSON and Mermaid inspection passed. Filtering `nke-node-image-amd64`
passed in all three formats; filtering `nke-node-image-arm64` passed in JSON. Each disk-filtered
JSON view retained `totalOutputs: 34` and displayed exactly one output with its
11 evidence memberships. An unknown output failed with `NKD-E-USAGE`, exit 2,
and zero standard output. Both full and disk-filtered Mermaid views were checked
for a `flowchart TD` header and valid `%%` metadata-inspection comment.

After preserving the native-image manifest field-name checks, the final rebuilt
CLI, SHA-256 `33aebe5e474ae40d11ca9d5a510c62ee46545bb289ffacecd18857a6fefb90ab`,
inspected this carrier again. Its full JSON remained byte-identical to the recorded
live registry result. The final metadata, presentation and browse race regressions
also passed, including all three formats and output filtering.

These checks consume the complete bounded metadata closure before filtering.
They do not authenticate signatures contained in evidence bundles or inspect
artifact payload bytes. The repository's transport regression tests separately
exercise successful browsing while rejecting output-layer downloads.

## Local completion checks

Formatting, vet, golangci-lint 2.14.0, shellcheck, the pinned Go vulnerability scan
and `git diff --check` passed. The vulnerability scan reported no vulnerabilities.
The older installed linters were unsuitable: the default binary was built with
Go 1.26, and 2.13.1 reported macOS pipeline diagnostics that also reproduced on
the untouched base commit. The installed 2.14.0 passed without source workarounds.

Uncached normal and race tests passed for every package. The initial full-suite
commands each reached Go's default ten-minute package timeout in the factory
suite during filesystem work; every other package passed. Isolated factory
reruns with `-count=1 -timeout 20m` passed normally in 237.534 seconds and with
`-race` in 414.918 seconds. Final metadata, browse and rendering regressions were
also rerun with `-race -count=1` after the last schema guard.

Regression coverage includes zero artifact-layer downloads for AMD64 and ARM64
disk releases, text/JSON/Mermaid and output filtering, corrupt unselected metadata,
exact root pins, schema and evidence membership rejection, and metadata inspection
with absent or corrupted local payloads. Full publication inspection still rejects
those payloads; the registry test also confirms its full pull requests payload
layers that browsing never requests. These are contract tests, not external
signature or payload verification.
