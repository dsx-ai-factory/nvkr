# NKD v1 milestones

These milestones define production acceptance for the `pipeline-attested` lane. The
[acceptance specification](epics-v1-spec.md) contains the criteria; the
[implementation record](adr/reconciliation.md) records progress and external evidence.
A local dry run or a passing contract test does not complete a milestone.

M2 requires producer provenance for native AMD64 and ARM64 disks. M3 adds deep disk/content inspection and
material reconciliation. The [evidence decision](adr/0013-supply-chain-evidence.md#6-pipeline-sbom-and-acquisition-scope)
records the October 7, 2026 scope decision; organizational ratification remains separate.

| Milestone | Outcome | Exit criterion |
|---|---|---|
| M1 — Vertical slice | One pinned NKE CLI build through a generated GitHub workflow | A green default-branch Run builds from the pinned source entrypoint, publishes to OCI, keyless-signs and attaches production provenance; independent cosign verification succeeds |
| M2 — Breadth | The complete in-scope artifact set, including native AMD64 and ARM64 NKE OS images | One Run delivers both exact-subject disks and every required output, with signatures, producer/source-lane provenance and independent consumer verification; recorded fallbacks remain visible |
| M3 — Qualification | Automated build and evidence checks under a separate qualification identity | Schema, pin consistency, signature/provenance, tooling smoke checks and exact-disk inspection with material reconciliation pass; signed evidence records verdicts and approved waivers; tampering or missing evidence fails closed |
| M4 — Automated releases | Reviewed pin updates produce immutable, qualified v1 Release manifests | Scheduled detection opens a reviewable update; merging produces a signed manifest and immutable tag; manual dispatch re-verifies an existing manifest without rebuilding; overlapping and interrupted release runs never move an immutable tag; a signed revocation withdraws a release from the channel |

M3 qualification does not establish cluster conformance or byte-identical reproduction. M4's Release manifest
belongs to the v1 evidence contract; it does not grant qualified-lane clearance. Artifact-publication
verification establishes build evidence and cannot satisfy M3/M4 acceptance.

Evidence remains scoped to exact producer outputs. A package with independently demonstrated exact-byte
reproduction evidence or a digest-verified acquisition may feed the wrapped NKE OS build without making the
aggregate disk reproducible. M2 requires each disk's exact delivered digest, producer/source-lane
provenance, authenticated materials and complete release membership. Producer-provenance evidence explicitly
reports inspection as not performed. M3 requires exact-disk inventory and material reconciliation.
A later build with different QCOW2 bytes is a different Artifact
even if the same content checks pass.

## Artifact scope

M2 requires native AMD64 and ARM64 OS images, each independently produced and verified. M3 inspects each disk.
Other outputs retain their explicit selected architectures. Source lanes are `hermetic-build`, `wrapped-build`, or `acquired`; production provenance
records the actual lane. Every separately delivered Artifact is signed and attested in OCI. The Release binds exact named outputs without a deployment topology.
Each named output selects its producers and concrete target directly. Shared build content does not collapse
separate required outputs or let one output's evidence satisfy another.

| Artifact or capability | Production source | Delivery / milestone |
|---|---|---|
| GitHub workflow generator | NKD | Committed workflows / M1 |
| NKE CLI | Pinned NKE source, hermetic build | OCI executable package / M1 |
| nke-agent | NKE OS build | Inside OS image / M2 |
| CWE controller | Pinned NKE build; recorded acquisition fallback | OCI image / M2 |
| NKE container images | OS-embedded or separately built/acquired, enumerated in Definition | Inside OS image or OCI images / M2 |
| NKE OS image | Pinned wrapped NKE build; pre-authorized prebuilt-image acquisition fallback | OCI disk-image artifact / M2 |
| Kubernetes node and control-plane components | NKE OS build | Inside OS image / M2 |
| CNI | NKE OS build | Inside OS image / M2 |
| Helm | Digest-pinned acquisition | Operator tooling package / M2 |
| Spire-agent | Placement decision: embedded or acquired | Inside OS image or OCI artifact / M2 |
| Third-party container images | Digest-pinned acquisition when outside OS image | OCI images / M2 |
| NKE Helm charts | Pinned chart sources | OCI packages / M2 |
| Operator tooling | Acquired kubectl and Helm | OCI package / M2 |

The NKE OS build team owns embedded-software correctness and runtime validation. M3
requires an inventory reconciling traced materials with read-only inspection of the exact disk. Embedded components retain matrix rows for
traceability, but do not imply separate NKD builds or delivery. A deferred member stays explicit in authored
requirements and planned qualification waivers; it must never disappear because a build failed.
That inventory proves composition for the exact disk subject. It does not establish qualified reproduction of
the QCOW2, decoded RAW stream, or FDE ciphertext.

## Dependencies and limits

| Owner | Required decision or evidence |
|---|---|
| NKE build owners | Buildable pinned entrypoints, vendored private inputs, external image/chart list, OS contents and Spire-agent placement |
| CI/release engineering | Admit the selected hosted execution route before execution, including virtualization, privilege, disk and wall-time capacity; native NVIDIA disk builds use the explicitly admitted operator-trusted runner pools |
| Registry/identity owners | Registry blob limits and retention, protected publication environment, keyless identities, source access and public-transparency disclosure approval |
| Qualification owner | Separate signing identity, explicit waivers and independent negative verification transcripts |
| Release owner | Pin-bot permissions, immutable-tag enforcement, rerun/concurrency behavior and channel policy |

Generated pipelines use generic hosted runners except for OS disk jobs on the explicitly admitted
operator-managed native virtualization pools. This pipeline-attested exception does not admit arbitrary runner pools or qualification.
If an OS build cannot run on the admitted hosted
environment, the recorded M2 fallback is digest-pinned acquisition of a prebuilt image. This changes the
source claim and must be visible in provenance and the acceptance record.
An acquired-disk fallback must retain exact acquired-byte identity and source-lane provenance; M3 additionally
requires its exact-disk inspection contract.

Outside v1: non-OCI delivery, upstream from-source replacement builds, acquired upstream-signature verification,
vulnerability scanning, deployment topology, cluster E2E qualification, air gap, builder-profile reproduction and byte-identity
guarantees. GitHub is the sole hosted workflow, publication and signing provider. Per-built-artifact SBOM
composition is included in the current graph.
