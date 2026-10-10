# NVKR and NVKE naming cutover

This repository is now **NVKR**, targeting `dsx-ai-factory/nvkr`. Its downstream
product is **NVKE**, targeting `dsx-ai-factory/nvke`. The change is a breaking
namespace migration, not a compatibility alias or a claim that the future
repositories, registries and hosted configuration are already provisioned.

| Surface | New name |
|---|---|
| Framework and Go module | NVKR, `github.com/dsx-ai-factory/nvkr` |
| Public executable and Go command | `nvkr`, `cmd/nvkr` |
| SDK implementation packages | `pkg/nvkr` |
| Framework configuration, environment and schema namespace | `.nvkr`, `NVKR_*`, `nvkr.dev` |
| Structured digest families | `nvkr.*` |
| Generated workflow and ownership records | `nvkr-run.yml`, `.github/nvkr-run` |
| Downstream product and maintained inputs | NVKE, `definitions/nvke-*` |
| Local downstream demo | `nvke-demo`, `internal/nvkedemo` |

## Identities and generated files

Changing a schema namespace or digest-family prefix changes the canonical
identity. Old signed records are never edited, reauthenticated or treated as
NVKR records. Strict readers reject unsupported namespaces. Generated Plans and
workflow ownership records are regenerated from the renamed authored inputs;
catalog locks retain their pinned source, tool and material bytes.

The embedded, recorded publication and release examples in the demo retain
their original bytes and historical scope. Their old identifiers describe those
records, not the current product's naming or acceptance evidence. Historical
provider readbacks likewise retain the environment and branch names actually
observed at the time.

Demo links into those records retain the recorded output keys. Renaming a link
without changing its immutable record would make the saved output unavailable;
current authored examples and exported Plans use the new names.

## Exact upstream contracts

A Git repository rename does not rewrite a pinned source tree. The maintained
catalog therefore preserves the following source-owned interfaces until NVKE
provides a changed commit and the catalog is deliberately repinned:

- CLI source directory `cli/cmd/nkectl`, linker module
  `github.com/NVIDIA/nke/cli`, Make inputs `NKECTL_*`, and Make output
  `cli/bin/nkectl`.
- Packer selector `nke-node-non-fde.qemu.nke-non-fde`, producer manifest
  `nke.dev/os-image-materials/v1`, and output `non-fde/nke-node.qcow2`.
- The deploy pin-set's `controller/deploy/nke-sbom/nke-deploy.yaml`, its
  `nke-base-config` item, and the upstream controller chart filename.
- Installed packages, services, deployment selectors and helper files supplied
  by the pinned upstream tree. These describe producer bytes, not NVKR aliases.

Public component names, caller-owned output paths and documentation use NVKE.
Captured upstream test fixtures retain the actual upstream bytes.

## Deployment cutover

The rename patch alone does not establish a working hosted deployment. Before
using the new namespaces operationally:

1. Provision or rename the two repositories, preserving Git history, tags and
   access. Update external repository permissions, the renamed CODEOWNERS team,
   and consumers of the Go module. A repository rename does not rename an org team.
2. Build and publish the renamed helper, Packer and disk-inspector closures from
   their renamed sources. Measure the new binaries and replace the applicable
   OCI/executable pins. Relocating an old image cannot rename its embedded paths
   or `/usr/local/bin/nkd-disk-inspect` executable.
3. Coordinate the NVKE acquisition writer with the new
   `nvkr.dev/package-acquisitions/v1` envelope and repin its source. An older
   capture envelope is not converted into authenticated new-namespace evidence.
4. Migrate hosted environments, source-secret names, branch restrictions,
   package access, signing identities and external documentation domains to the
   authored new names. This PR does not change provider settings or secrets.
5. Regenerate the Run after those measured repins. Exercise both native
   architectures, publication and independent consumer verification against the
   actual new repository and registry identities.

Local compilation and contract tests validate the rename, not those external
cutover steps. Existing blob SHA-256 pins are retained, never replaced with
invented values to imply that rebuilt images or renamed producer bytes exist.
