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

[#514](https://github.com/dsx-ai-factory/nvkr/issues/514) proposes the [ADR-20 selective contract](../0020-artifact-publication-and-target-configuration.md#selective-boot-asset-publication-and-machineimage-binding). It has no implementation, ratification or selective test/provider/boot evidence yet; existing full-mirror evidence is unchanged.

| Owner | Outstanding scope |
|---|---|
| [#515](https://github.com/dsx-ai-factory/nvkr/issues/515) | Selection, raw-member proofs, receipts, S3-only verification, serving, recovery and compatibility tests. |
| [#517](https://github.com/dsx-ai-factory/nvkr/issues/517) | Predeclared render, pinned schema/profile, dependency joins, trusted import and chainload/imaging/first-boot evidence. |
| [#516](https://github.com/dsx-ai-factory/nvkr/issues/516) | Signed EFI member/platform/profile binding; separate mkosi producer lane. |
| [#384](https://github.com/dsx-ai-factory/nvkr/issues/384) | Selective configuration, regeneration and required finalization, preserving full-mirror defaults. |
| [#383](https://github.com/dsx-ai-factory/nvkr/issues/383) | Provider/Run selected-only inventory, original proofs, pinned-read/fault evidence and independent S3-only client access. |
| [#385](https://github.com/dsx-ai-factory/nvkr/issues/385) | CLI/SDK adapters with limited results and preserved full-mirror behavior. |

The last three issues still describe whole-copy acceptance and need coordinated scope updates. Record contract, UI, provider/client and boot evidence separately; none substitutes for air-gap trust distribution, firmware trust, qualification or FDE confidentiality.
