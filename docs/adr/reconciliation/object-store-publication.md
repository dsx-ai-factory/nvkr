## Object-store publication

ADR-20's [object-store publication contract](../0020-artifact-publication-and-target-configuration.md#s3-file-delivery-contract)
uses the original signed OCI publication and whole-Plan consumer receipt. Environment v2 configures required
S3 destinations. A generated ordinary publication operation retrieves the complete original payload/evidence
closure, retains original signatures/provenance, verifies every remote byte and writes an immutable transport
inventory last. Existing Run finalization requires every provider operation to succeed, including each S3 copy.
The source artifact and signature identities are unchanged; object metadata supplies no signing authority.

`delivery.ObjectStore` is the byte-provider boundary; the first implementation uses pinned official AWS SDK
clients, conditional creation, version/ETag-pinned reads and bounded sequential multipart cleanup. Workload OIDC
is exchanged once for a temporary session covering the operation/cleanup deadlines. Ambient profiles,
background refresh, namespace enrollment, cross-Run CAS state and a second signed delivery protocol are absent.
`sign` retains and verifies original bundles. `lifecycle` owns exact-input and aggregate/local descriptor joins
both before copying and during independent consumption. Raw/semantic subjects remain distinct from OCI
wrapper descriptors. Independent registry-disabled consumption is exposed through
`lifecycle.VerifyObjectPublication`; no equivalent CLI or `client/v1` route is implemented. The
[publication guide](../../publication-architecture.md#required-s3-copy) describes configuration and links this
boundary. No new native-file taxonomy is required to transport existing artifacts.

The linked provider dependencies are official AWS SDK for Go v2 core `v1.47.5`, credentials `v1.20.13`,
S3 `v1.114.6`, STS `v1.51.6` and Smithy Go `v1.28.7`, with their required transitive AWS modules pinned in
`go.mod`/`go.sum`. The official source/tag and Apache-2.0 license review is reflected in the generated linked
module notice inventory; no SDK config/profile/SSO resolver or transfer-manager dependency is added.
Notice drift and the repository-pinned vulnerability scanner are automated dependency gates.
`go mod verify` is a separately recorded manual local check, outside `make check` and Merge Gate;
the dated [dependency scan and notice evidence](../../evidence/2026-11-04-object-store-dependency-admission.md)
records the exact scanned head, scanner outcome and engineering-admission scope. Dependency engineering admission is separate from
operator IAM review, provider acceptance and named-decider ratification.

Local contract tests and repository checks are recorded with the implementation review. They are not a live
S3 service exercise, provider qualification, default-branch workflow acceptance or independent operational
consumer transcript. No new storage credentials, IAM permissions, boot network or deployment were provisioned
by this change. Such external evidence remains outstanding and must name the actual provider and exact Run.

S3 copying does not establish actual EFI chainload, imaging/reboot, direct VM boot, native-package installation,
Secure Boot or qualification. Downstream serving and intended-client authentication remain separate. The pinned
NVKE source documents its [imager parameter compatibility boundary](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/efi/README.md#L36);
matching transport bytes cannot resolve that product decision. No live boot/install acceptance is asserted.

### Selective boot-asset publication

[Issue #514](https://github.com/dsx-ai-factory/nvkr/issues/514) adds the proposed
[ADR-20 selective contract](../0020-artifact-publication-and-target-configuration.md#selective-boot-asset-publication-and-machineimage-binding).
This is design for review, not a new named-decider acceptance, implemented mode or external provider result.
The current full-mirror implementation and its evidence above are unchanged. No selective schema, receipt,
copy-first graph, MachineImage renderer or selective consumer is implemented by this documentation change.
No local contract tests or external S3/provisioning/boot runs establish the proposed path yet.

| Work owner | Required implementation and evidence |
|---|---|
| [#515 selected copy/discovery/serving](https://github.com/dsx-ai-factory/nvkr/issues/515) | New versioned selection/inventory, original raw-member proof bindings, opaque preliminary/completion/consumer receipts, S3-contained proofs, pinned raw reads, admission and recovery matrix, old full-mirror compatibility. Record local contracts separately from provider/client runs. |
| [#517 MachineImage rendering](https://github.com/dsx-ai-factory/nvkr/issues/517) | Predeclared render membership, deterministic pinned-schema/profile output, authenticated metadata and dependency joins, no root/content/inventory self-reference, trusted import, actual chainload/imaging/first-boot evidence. |
| [#516 EFI producer](https://github.com/dsx-ai-factory/nvkr/issues/516) | Original signed EFI member/hash/size/platform and compatible imager binding; producer work stays in its separate mkosi lane. |
| [#384 configuration/generation](https://github.com/dsx-ai-factory/nvkr/issues/384) | Coordinated selective configuration and required finalization, preserved full-mirror defaults and regenerated bundles. |
| [#383 provider acceptance](https://github.com/dsx-ai-factory/nvkr/issues/383) | Exact provider and Run, selected-only object inventory, original proofs, pinned readback/fault evidence and independent S3-only consumer/client access. |
| [#385 public verification](https://github.com/dsx-ai-factory/nvkr/issues/385) | Thin CLI/SDK selective adapters and explicit limited results; retain the full-mirror route. API evidence cannot claim UI acceptance. |

The last three rows assign coordinated work; their current whole-copy acceptance text does not cover the
selective contract. Full-mirror compatibility and selective acceptance must remain distinct in that work.
The dependency contract requires exact named selection, complete OCI membership, no unselected S3 payloads,
all selected proof bytes, refusal of stronger policy, acyclic ordering and interruption recovery at every phase.
All selected assets and proofs remain mandatory; the smaller transport set is not best effort.
Operational destination/read access/retention and real provider/client/boot acceptance remain separate from
schema/graph and fake-provider tests. S3-only proof retrieval does not establish air-gap trust distribution,
EFI chainload authentication, firmware Secure Boot, qualification or FDE confidentiality.
