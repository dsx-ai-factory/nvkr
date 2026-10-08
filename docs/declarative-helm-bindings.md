# Bind the controller chart to CWE

The Definition says **which image the chart uses**:

```yaml
outputs:
  - name: controller-chart
    components: [controller-chart]
    format: helm-oci
    platform: linux/amd64
    helm:
      images:
        controller: cwe
  - name: cwe
    components: [cwe]
    format: oci
    platform: linux/amd64
```

Read `controller: cwe` as “use the `cwe` output for this chart's `controller` image.”
To bind another image, add another slot-to-output entry. NKD derives the build dependencies.

The [catalog producer](https://github.com/dsx-ai-factory/nke-recipes/blob/main/docs/examples/nke-controller-bindings/registry.yaml) maps the slot to the existing value:

```yaml
helm:
  chart: .
  images:
    controller:
      values:
        digest: image.digest
```

NKD writes the verified CWE image digest to `image.digest`. For root charts, upstream checksum files
are preserved; nested wrappers refresh binder-owned checksums outside the chart. Templates, repository
and tag defaults stay unchanged. Values are reserialized with the YAML library: formatting can change,
and document-level comments separated from the root mapping may be omitted. Template bytes stay unchanged. Unknown or unbound slots and missing or ambiguous value paths fail.

## Registry choice

The installer supplies the repository holding that image:

```sh
helm template controller ./controller --set-string image.repository=registry.operator-a.test/nke/cwe
helm template controller ./controller --set-string image.repository=mirror.operator-b.test/platform/cwe
```

Both use identical chart bytes and the packaged image digest. The operator must make that image available
at the supplied repository. Normal Helm overrides still apply: overriding `image.digest` changes the selected
image. NKD does not enforce deployment values.

## Shared paths

Catalog pin sets and chart mappings use the same path reader:

| Path | Meaning |
|---|---|
| `image.digest` | The `digest` field under `image`. |
| `components[name=cwe].version` | The version of the component named `cwe`. |

Missing fields and ambiguous matches fail. Pin sets also reject unaccounted list members.

## Concrete NKE example

[Definition](https://github.com/dsx-ai-factory/nke-recipes/blob/main/docs/examples/nke-controller-bindings/definition.yaml) and
[catalog](https://github.com/dsx-ai-factory/nke-recipes/blob/main/docs/examples/nke-controller-bindings/registry.yaml) select six outputs: CWE, its source controller chart,
SPIRE, SPIRE CRDs, Temporal and PostgreSQL. They share the pinned NKE controller source; external charts
remain acquired and byte-preserving. The controller source archive contains only its chart.

On an AMD64 Linux Docker host, with NKE source access:

```sh
mkdir -p .nkd/controller-bindings
cp docs/examples/nke-controller-bindings/*.yaml .nkd/controller-bindings/
go run ./cmd/nkdctl catalog pin --dir .nkd/controller-bindings \
  --tag controller/v0.1.1-rc.9 nke-controller
go run ./cmd/nkdctl run local \
  --definition .nkd/controller-bindings/definition.yaml \
  --environment .nkd/controller-bindings/environment-local.yaml \
  --attempt controller-bindings-01 --runner-source .
```

The example was pinned against real upstream chart repositories and compiled successfully; its six-output
plan makes `package/controller-chart` depend on `package/cwe`. This is not a recorded full six-output build. No new image build, signed publication,
installation or qualification is claimed. Acquired raw-chart dependency scanning remains unsupported.

## Validation and migration

The [compiler contracts](https://github.com/dsx-ai-factory/nke-recipes/blob/main/pkg/nkd/factory/compiler_contract_test.go) and
[binding tests](https://github.com/dsx-ai-factory/nke-recipes/blob/main/pkg/nkd/factory/helm_mapping_test.go) cover slot accounting, unchanged templates,
multiple images and registry-independent package bytes. Real Helm rendering is opt-in; see
[Development](https://github.com/dsx-ai-factory/nke-recipes/blob/main/DEVELOPMENT.md#opt-in-integration-checks) for prerequisites and commands.
These tests do not establish a full six-output build, installation or signed publication.

Replace `helm.imageOutput` with `helm.images: {controller: cwe}`. Put the chart location and digest paths
under the selected catalog component's `helm` field, then regenerate plans and workflows.
