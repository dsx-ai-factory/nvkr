# Combined NKE M2 candidate

This is the Definition used by the committed `nkd-run.yml`. It joins independently pinned NKE CLI,
controller and chart builds, operator executables, native node images and third-party deployment images
into one release with **34 named outputs**. This remains a candidate; completed pipeline delivery and
full product acceptance are separate facts.

## Selected artifacts and their owners

| Output group | Owning inputs | Selection |
|---|---|---|
| CLI | `../nke-v1/catalog` | `nkectl`, from its independent CLI module pin |
| Controller and charts | `../nke-controller/catalog` | `cwe`, image-bound `controller-chart`, PostgreSQL, SPIRE, SPIRE CRDs and Temporal chart packages |
| Operator tools | `catalog` | AMD64 Helm and kubectl executables, selected from the NKE distribution lock |
| Node images | `os-catalog`, `native-toolchains.yaml` | Separate native AMD64 and ARM64 non-FDE QCOW2s, with explicit material producers |
| Deployment images | `image-catalog` | Twelve non-test runtime images for each native architecture: PostgreSQL, eight SPIRE images and three Temporal images |

The catalogs and their machine-managed locks own exact commits, versions, archive/member hashes and
native image digests. Do not maintain a second digest table in documentation. PostgreSQL and SPIRE Bash
retain their source-owned index digests as well as the selected native child pins; acquisition checks
actual parent-to-child membership before using the native image.

Each disk selects its root and complete material closure: agent, Kubernetes distribution, Calico, OVN,
SPIRE, installer ISO and aggregate inputs. The OS, Calico and kubeadm sources use the capture-capable NKE
pin. The source composer receives an explicit native architecture. The direct-Packer contract receives
`capture_apt_acquisitions=true` and exports `non-fde/apt-acquisitions.tar`, declared through
`packer.acquisitionArchive`. The material projection binds `image-inputs` as `archive` and `installer-iso`
as `disk-image` under `nke.dev/os-image-materials/v1`; every material retains its target architecture.
Capture byte correspondence alone does not authenticate repository origin or establish a complete
installed-software inventory.

The temporary `nke-spire-agent-source` pin selects an older package that omits the DSX IMDS plugin whose
S3 download failed on both hosted architectures. This mixed-source candidate tests the build path;
it is not the intended complete M2 image. The exception's acceptance owner, expiry and production-content
approval are not recorded. Exiting it requires restoring the intended DSX IMDS payload with verified native
materials and both-architecture content checks, or a named, explicitly approved content-scope decision.
A successful build cannot silently approve omission. [Reconciliation](../../docs/adr/reconciliation.md#maintained-input-and-schema-boundaries)
retains the source handoff and acquisition history.

The [customer choices guide](../../docs/nke-m2-customer-choices.md) explains supplier replacement through
the existing catalog interfaces and the precise scope of its graph tests.

## Native execution and publication

`environment-github.yaml` admits only ADR-18's `githubARC: native-kvm-v1` route on
`arc-amd64-kvm` and `arc-arm64-kvm`, with required native KVM. Each execution class requests 8 CPUs,
36 GiB memory, 224 GiB workspace capacity and a six-hour disk-producer budget. Declarations are admission
budgets, not observed allocation or general runner-capacity guarantees. Docker uses the typed runtime
and producer-declared build-network controls; no ambient storage-driver workaround is selected.

`native-toolchains.yaml` substitutes measured published Packer pins for each architecture's nine selected
source-map consumers, preserving OVN's separate BuildKit pin. Internal Packer/inspector images were
published under the user's scoped candidate exception. Their source locks retain unresolved general
license/security approval gates; the exception does not mark those reviews complete. Its organizational
acceptance owner and expiry are not recorded. Leaving it requires recorded license/security clearance
and hosted closure, measurement and provenance evidence; developer-host preparation or publication alone
does not supply those approvals. See [Packer preparation and selection](../../toolchains/nkd-packer/README.md#native-preparation-and-catalog-selection)
and [inspector measurement](../../toolchains/nkd-disk-inspect/README.md#local-measurement-not-publication-admission).

The Environment explicitly selects `diskEvidence: build-provenance-v1`. M2 authenticates the held disk
and its live producer/material subjects and signs evidence describing inspection as `not-performed`.
Deep content inspection and reconciliation move to M3 under [ADR-13](../../docs/adr/0013-supply-chain-evidence.md#6-pipeline-sbom-and-acquisition-scope).
The retained strict profiles are neither prepared nor invoked in this mode. They contain native
image/executable pins and conservative bounds: an 8 GiB subject, 16 GiB filesystem, 28 GiB inspection
memory, 224 GiB private scratch and a ten-minute deadline. They do not demonstrate successful full M2
inspection or measured peak usage. Runner labels do not supply runtime-capacity or profile authority.

Omitting producer `diskEvidence` selects strict inspection. Consumer `VerificationPolicy.minimumDiskEvidence`
defaults to `build-provenance-v1` for pipeline-attested verification; explicit `strict` rejects producer-only
evidence. Qualified policy defaults to strict and rejects the producer-only minimum. The selected minimum
is policy-digest-bound. Deferral cannot replace exact-subject evidence or establish qualified reproduction.

Publication uses the separate `ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate` prefix and
`publication.mode: per-output-v1`. Each output is packaged, published, signed and verified independently.
Assembly authenticates same-Plan, same-workflow metadata and evidence; it does not download payload layers.
The release remains one signed entry point. Retry assembly may retain earlier attempt identities, but
hosted mixed-attempt reuse remains unexercised. Per-output storage peaks require measured admission.

Only admitted source retrieval and pinned-source revalidation steps receive `NKE_SOURCE_TOKEN`;
native build jobs receive source bytes without that credential. Internal toolchain preparation uses
separate ephemeral read-only package access, removed before producer execution. Grant the workflow
repository Actions Read access on each internal toolchain package. Restrict `nkd-arc`, `nkd-release` and
the signing identity to the selected refs. Maintained inputs admit `main` only. Any branch test needs a
separate non-merge candidate with authored dispatch/signing refs and actual environment admission;
these inputs do not change provider deployment policies. Ephemeral runner isolation and provider access
restrictions remain operator obligations.

## Generate and check the maintained workflow

Run from the repository root with the pinned Go toolchain and authorized access to the exact NKE tree.
Catalog loading re-derives source-owned pin sets and may need Git access even for generation.

```sh
GOTOOLCHAIN=local go run ./cmd/nkdctl run generate \
  --definition definitions/nke-m2/definition.yaml \
  --environment definitions/nke-m2/environment-github.yaml \
  --engine github --root .
make workflows-check
```

These commands regenerate the workflow, Plan and manifest; they do not execute the pipeline.
The generator rejects a workflow above 500,000 bytes before writing. YAML compaction preserves the
expanded job graph and credential boundaries. Changes to authored inputs must regenerate these files;
never edit the bundle by hand.

Source/artifact maintenance belongs to reviewed catalog changes using `catalog pin`, `catalog bump`
and `catalog pin-artifacts`. Do not copy historical preparation commands that would silently undo the
temporary SPIRE selection. The opt-in full graph integration runs with
`NKD_TEST_M2_ALL_OUTPUT_GRAPHS=1` and authorized pinned-source access; ordinary tests receive no
private-source credentials. Pinning and graph tests establish their own contracts, not production launch.

## Acceptance status

[Reconciliation](../../docs/adr/reconciliation.md#native-arc-disk-route) owns current implementation,
recorded runs and their limits; [the epic specification](../../docs/epics-v1-spec.md) owns product acceptance.
A historical candidate run does not validate an updated head or independent full-payload signed consumption.
The product acceptance floor is AMD64; this candidate also selects ARM64. Both disk outputs and every other
named output remain mandatory for its complete Run and Release, including publication, signing and verification.

The required checkpoint for NKE images outside the disk remains separately owned. The pinned deploy tree
mentions CAPNICo, while the catalog's provisioned-provider build is a different project. Its fleet-profile
content-image selection supplies no portable tag/digest. Neither should be guessed into required membership
or silently treated as deferred. Embedded SPIRE and chart-side SPIRE containers are distinct artifacts;
one cannot satisfy the other's evidence row. These unresolved ownership/payload questions remain product
acceptance gaps, alongside full M3 disk evidence and organizational toolchain reviews.

This README owns candidate input selections and maintained workflow instructions, not a second run-history ledger.
