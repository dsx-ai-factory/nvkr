# Combined NKE M2 candidate

This is the Definition used by the committed `nkd-run.yml`. It joins independently pinned NKE CLI,
controller and chart builds, operator executables, native node images and third-party deployment images
into one release with **35 named outputs**. This remains a candidate; completed pipeline delivery and
full product acceptance are separate facts.

## Selected artifacts and their owners

| Output group | Owning inputs | Selection |
|---|---|---|
| CLI | `../nke-v1/catalog` | AMD64 `nkectl` and ARM64 `nkectl-linux-arm64`, from their shared independent CLI module pin |
| Controller and charts | `../nke-controller/catalog` | AMD64/ARM64 aggregate `cwe`, image-bound `controller-chart`, PostgreSQL, SPIRE, SPIRE CRDs and Temporal chart packages |
| Operator tools | `catalog` | AMD64 Helm and kubectl executables, selected from the NKE distribution lock |
| Node images | `os-catalog`, `native-toolchains.yaml` | Separate native AMD64 and ARM64 non-FDE QCOW2s, with explicit material producers |
| Deployment images | `image-catalog` | Twelve non-test runtime images for each native architecture: PostgreSQL, eight SPIRE images and three Temporal images |

The catalogs and their machine-managed locks own exact commits, versions, archive/member hashes and
native image digests. Do not maintain a second digest table in documentation. PostgreSQL and SPIRE Bash
retain their source-owned index digests as well as the selected native child pins; acquisition checks
actual parent-to-child membership before using the native image.

The OS, Calico and kubeadm sources remain machine-pinned to that capture-capable commit.
For the temporary branch build test, only `nke-spire-agent-source` is pinned to
`726f76944404e1106626b8333d3576bdf67b575b` using `catalog pin --commit`. That older package
omits the DSX IMDS plugin whose S3 download returned HTTP 403 on both hosted architectures.
This mixed-source candidate tests the build path; it is not the intended complete M2 image.
The exception's acceptance owner, expiry and production-content approval are not recorded. Exiting the exception
requires restoring the intended DSX IMDS payload with verified native materials and both-architecture content
checks, or a named, explicitly approved content-scope decision; a successful build cannot silently approve omission.
The [merged-source handoff](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-05-m2-input-acquisition.md#merged-capture-source-handoff)
records NKE #1211 as approved and merged, and the exact selected commit as protected `main`
at the recorded read-back. The agent version is `v0.2.1-81-gff21ba8`, derived through the source's
`agent/v*` tag pattern. Exact commit acquisition itself is not protected-ref or source-review
proof; that acceptance is separately recorded. The hosted candidate publication result is recorded below;
independent consumer acceptance remains unresolved.
The pinned commit's source-owned direct-Packer contract receives `capture_apt_acquisitions=true` and exports
`non-fde/apt-acquisitions.tar`, which the catalog declares through `packer.acquisitionArchive`.
The source's composition command receives the required explicit native `--architecture` argument.
The selected material projection binds `image-inputs` as `archive` and `installer-iso` as `disk-image`
under `nke.dev/os-image-materials/v1`; every selected material and disk target retains its own architecture.

This is a candidate Definition, not completed M2 acceptance. The production target Environment reuses
the existing registry credential, source access and workflow signing policy, but publishes only under
the separately authored `ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate` prefix. The maintained signer identity
admits only `main`; the temporary PR #97 branch-test exception has been removed after the
cancelled attempt. Candidate execution is not organizational publication acceptance. Native disk
execution classes select the two ARC KVM pools under `githubARC: native-kvm-v1`. They describe requested
capacity, not observed allocation. `native-toolchains.yaml` substitutes the exact published native
Packer pin for the nine selected AMD64 producers only, retaining the separate BuildKit pin.
The OS catalog directly retains its published ARM64 Docker Packer pin.
The two inspector profiles use measured executable hashes and published native image manifests.
Their bounded budgets cover up to an 8 GiB subject and 16 GiB filesystem: 28 GiB inspection memory,
224 GiB private scratch and a ten-minute deadline. Execution classes request 36 GiB memory to
include the held subject snapshot. These are admission budgets, not measured full-M2 usage.
The inspector and Packer source locks retain their outstanding organizational approval gates;
the [candidate exception and publication evidence](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-native-arc.md)
records the narrower authorization and exact registry read-back.
The retained measurements came from developer-host preparation and publication. A hosted toolchain build,
its matching measurements and provenance, and completed organizational clearance are not recorded here;
native preparation commands alone do not supply that evidence.
The candidate exception's organizational acceptance owner and expiry are not recorded. Leaving that exception
requires recorded license/security clearance and the hosted closure/measurement/provenance evidence; a
developer-host image push does not supply those approvals.
The candidate Environment selects Docker through the existing typed runtime controls;
it declares no storage-driver field or ambient runtime-variable workaround. Storage behavior
is admitted and observed by the existing runtime owner. The historical explicit-VFS probe
is diagnostic evidence, not this Environment's configuration or production approval.
Runner labels do not supply runtime-capacity or full-inspector profile authority.
The current candidate selects `diskEvidence: build-provenance-v1`, authenticating both held disks and their
material producers without deep inspection. Profiles remain available for strict mode but are not pulled or
executed in this mode; its signed evidence reports inspection as `not-performed`. Ilya Kislenko's October 7
task-chat instruction preserves main/#106's producer-provenance M2 floor and full scan plus material
reconciliation for M3 ([ADR-13](../../docs/adr/0013-supply-chain-evidence.md#6-pipeline-sbom-and-acquisition-scope)).
The product acceptance floor remains AMD64; this candidate retains the separately selected ARM64
extension, and both disk outputs remain mandatory for completion of this Definition.
Omitting producer `diskEvidence` still selects the strict contract. A complete producer-provenance
M2 candidate requires publication, signatures and consumer verification for every selected output.
The [failed delivery attempt](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-release-carrier-transport.md)
records the earlier aggregate transport failure; the per-output route below avoids aggregate payload downloads.
Per-output storage peaks still require measured admission.
The signed disk mode is descriptive. `VerificationPolicy.minimumDiskEvidence` defaults to `build-provenance-v1`
for pipeline-attested verification. Explicit `strict` rejects producer-only evidence; qualified policy defaults
to strict and rejects the producer-only minimum. The selected minimum is
policy-digest-bound. The historical validation run below predates these consumer-policy and timeout changes
and does not validate this updated head, current main execution, strict inspection or independent full-payload
consumer acceptance.

The [customer choices guide](../../docs/nke-m2-customer-choices.md) explains supplier replacement through
the existing catalog interfaces and the precise scope of its graph tests. The
[release transport evidence](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-release-carrier-transport.md) records the
failed candidate's storage exhaustion and why correcting carrier selection alone does not complete delivery.

The [toolchain preparation guide](../../toolchains/nkd-packer/README.md)
lists repository-owned native preparation and measurement commands, their current
toolchain license/security gates, and the missing production inputs. The inspector's source
README gives separate AMD64 and ARM64 assembly commands. The generated workflow uses isolated
read-only image preparation with the ephemeral GitHub token; grant this repository Read under
each internal toolchain package's Manage Actions access settings. Maintained manual dispatch and
signing admit only `main`. The October 7 branch attempt at `562a609` used a temporary exact
`codex/m2-native-arc-foundation` authored exception; it was cancelled after GitHub environment
protection rejected that branch before attestation, disk execution and finalization steps ran
([run 37661490433](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37661490433)).
That exception is removed from the authored inputs and regenerated bundle. No deployment-policy
entry was added, and existing unrelated entries are not changed here. Any further branch test
needs a separate non-merge candidate and actual environment admission; this maintained bundle
does not authorize it.
The equivalent validation-branch bundle completed all jobs in
[run 37533806963](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37533806963);
that run used a different exact branch exception and is not evidence for this PR's head.

Each disk selects its root and complete material closure: agent, Kubernetes distribution, Calico, OVN,
SPIRE, installer ISO and aggregate inputs. The source composer receives an explicit native architecture.
The direct-Packer contract opts into APT capture and declares `non-fde/apt-acquisitions.tar` under the
source-owned material projection. Capture byte correspondence alone does not authenticate repository
origin or establish a complete installed-software inventory.

The Environment explicitly selects `diskEvidence: build-provenance-v1`. M2 authenticates the held disk
and its live producer/material subjects and signs evidence describing the deferred inspection scope.
Deep content inspection and reconciliation move to M3. The existing strict inspection profiles remain
available, but this M2 mode neither prepares nor invokes the inspector. A successful M2 pipeline still
requires every named output, exact-subject evidence, publication, signing and verification; deferral
cannot substitute for those gates or establish qualified reproduction.

## Native execution and publication

`environment-github.yaml` admits only ADR-18's `githubARC: native-kvm-v1` route on
`arc-amd64-kvm` and `arc-arm64-kvm`, with required native KVM. Each execution class requests 8 CPUs,
36 GiB memory, 224 GiB workspace capacity and a six-hour disk-producer budget. Declarations are admission
budgets, not observed allocation or general runner-capacity guarantees. Docker uses the typed runtime
and producer-declared build-network controls; no ambient storage-driver workaround is selected.

`os-catalog` directly retains the historical published ARM64 Docker Packer candidate.
`native-toolchains.yaml` substitutes the measured published AMD64 Packer pin for its nine selected
source-map consumers, preserving OVN's separate BuildKit pin. These historical images do not establish
reproduction of the current Dockerfile or bootstrap compiler. Internal Packer/inspector images were
published under the user's scoped candidate exception. Their source locks retain unresolved general
license/security approval gates; the exception does not mark those reviews complete. See
[Packer preparation and selection](../../toolchains/nkd-packer/README.md#native-preparation-and-catalog-selection)
and [inspector measurement](../../toolchains/nkd-disk-inspect/README.md#local-measurement-not-publication-admission).

Strict profiles contain actual native image/executable pins and conservative bounds: up to an 8 GiB
subject, 16 GiB filesystem, 28 GiB inspection memory, 224 GiB private scratch and a ten-minute deadline.
These retained profiles do not demonstrate a successful full M2 inspection or measured peak usage.

Publication uses the separate `ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate` prefix and
`publication.mode: per-output-v1`. Each output is packaged, published, signed and verified independently.
Assembly authenticates same-Plan, same-workflow metadata and evidence; it does not download payload layers.
The release remains one signed entry point. Retry assembly may retain earlier attempt identities, but
hosted mixed-attempt reuse remains unexercised.

Only admitted source retrieval and pinned-source revalidation steps receive `NKE_SOURCE_TOKEN`; native build jobs receive source bytes without that
credential. Internal toolchain preparation uses separate ephemeral read-only package access, removed
before producer execution. Grant the workflow repository Actions Read access on each internal toolchain
package. Restrict `nkd-arc`, `nkd-release` and the signing identity to the selected refs. Maintained inputs
admit `main` only and contain no validation-branch dispatch exception. Ephemeral runner isolation and
provider access restrictions remain operator obligations.

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

## Recorded delivery and remaining acceptance

[Run 37533806963](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md) completed all 34 outputs and aggregate
publication, signing, verification and finalization. It used an exact validation-branch dispatch/signing
exception absent from the maintained main-only inputs. Independent full-payload signed disk consumption,
main-only execution and hosted mixed-attempt retry acceptance remain unexercised. Earlier deep inspections
timed out; the successful candidate used the explicit build-provenance mode.

The required checkpoint for NKE images outside the disk remains separately owned. The pinned deploy tree
mentions CAPNICo, while the catalog's provisioned-provider build is a different project. Its fleet-profile
content-image selection supplies no portable tag/digest. Neither should be guessed into required membership
or silently treated as deferred. Embedded SPIRE and chart-side SPIRE containers are distinct artifacts;
one cannot satisfy the other's evidence row. These unresolved ownership/payload questions remain product
acceptance gaps, alongside full M3 disk evidence and organizational toolchain reviews.

[Reconciliation](../../docs/adr/reconciliation.md) owns current implementation/evidence status;
[the epic specification](../../docs/epics-v1-spec.md) owns product acceptance. This README owns the
candidate input selections and the maintained workflow instructions.
