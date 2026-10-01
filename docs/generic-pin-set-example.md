# Use a generic pin set

Pin one platform release; NKD derives its controller and helper pins from that release's version file.
The format is generic because the catalog supplies the file path and field selectors. No NKE adapter is involved.

## Upstream declares the versions

Commit [versions.yaml](examples/generic-pin-set/versions.yaml) in the platform repository, then tag it `v1.4.0`:

```yaml
dependencies:
  - {name: controller, version: "0.9.3"}
  - {name: helper, version: "2.0.0"}
```

The controller repository must contain tag `controller/v0.9.3`; helper packages must exist for both architectures.

## The catalog maps them to pins

The complete [registry.yaml](examples/generic-pin-set/registry.yaml) makes these mappings:

| Upstream field | Catalog member | What NKD resolves |
|---|---|---|
| `dependencies[name=controller].version` | `source: controller` | Git tag `controller/v0.9.3` → exact commit and tree |
| `dependencies[name=helper].version` | `artifact: helper` | Helper `2.0.0` download → digest for each architecture |

`pinnedBy: platform` prevents either member from being bumped independently.
The URLs are illustrative: replace the three `example.invalid` locations with your repositories and downloads.

## Pin the platform

From the NKD repository root, with its pinned Go toolchain available:

```sh
mkdir -p .nkd/generic-pin-set
cp docs/examples/generic-pin-set/registry.yaml .nkd/generic-pin-set/
# Edit the copied registry to use your real URLs.
go run ./cmd/nkdctl catalog pin --dir .nkd/generic-pin-set --tag v1.4.0 platform
go run ./cmd/nkdctl catalog validate --data .nkd/generic-pin-set
```

NKD generates `pins.lock.yaml` with three records:

| Lock record | Locked value |
|---|---|
| `pins.platform` | `v1.4.0`, commit, tree and source date epoch |
| `pins.controller` | `controller/v0.9.3`, commit, tree, epoch and `derivedFrom: platform@<commit>` |
| `artifacts.helper` | `2.0.0`, amd64/arm64 digests and the same `derivedFrom` |

Do not edit the generated lock. No build, packaging or installation runs in this example.

## Preview the next release

```sh
go run ./cmd/nkdctl catalog bump --dir .nkd/generic-pin-set --check platform
```

This checks the newest tag matching `1.4.x`. Pending changes return `NKD-E-DIFF` without writing;
remove `--check` to apply them. The whole set updates together or the lock stays unchanged.

If the upstream list adds a third member, re-pinning fails until the catalog explicitly maps it.
Changing a member pin by hand also fails validation against the pinned upstream file.

See [ADR-22](adr/0022-catalog-pinning-and-the-lock-contract.md#7-derived-pin-sets-one-source-may-curate-other-sources-pins)
for the YAML/JSON selectors and limits.
