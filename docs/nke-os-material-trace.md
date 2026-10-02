# Local disk material tracing

This is a factory-package-internal, process-local API for the staged producer graph of a local disk Run.
Recording is connected to local execution, but the observation API has no lifecycle, CLI or production SBOM
consumer yet. Contract tests use fake producers; no NKE Run has exercised trace observation. It does not
claim a complete acquisition inventory, installed composition, reproduction or publication acceptance.

## Evidence ownership

| Trace field | Owning source |
|---|---|
| Plan and Build identities, output membership | Validated factory Plan and exact lifecycle output |
| QCOW2 digest, size and successful root | Package-owned held disk completion, independently streamed by the existing disk observer |
| Material binding and producer edge | Exact selected producer IR in the Plan |
| Consumed material digest, component, kind and architecture | Held material handoff, frozen while its inputs are supplied to a successful invocation |
| Manifest digest | Exact held manifest compared with the successful producer's result before cleanup; empty when the producer declares no materials |
| Built output digest and size | Successful pipeline return and independent byte check at its Plan-selected output/result slot |
| Acquired output digest and size | Successful pipeline acquisition, checked against the IR's exact content pin |
| Coverage gaps | Always scoped to staged producer outputs; network-enabled build nodes are listed explicitly |

Only disk targets retain these private consumption records. Sibling CLI, chart and other outputs do not acquire
additional material-tracing work. Reopening or decoding a result file cannot create a missing record. Records
are synchronized with the local result's existing lifetime lock, excluded from its serialized JSON, and cleared
on close. A returned description is an independent copy, not a receipt constructor.

The internal observer follows direct and transitive material edges, compares every consumed digest
with its producer's retained output, and includes shared producers once. It fails on missing or substituted
observations and binds the observation to the exact named output, Plan, Build and QCOW2 subject. Equal-byte
sibling outputs cannot reuse each other's invocation observations. It issues no signed or
cross-process authority. A serialized local Run cannot reconstruct this observation.

The trace's immutable subject check does not prove that its issuing Run remains open, distinguish equal-byte
attempts of the same Plan, or grant access to held disk bytes. Future consumers must use the owning live-disk
capability separately. Non-VM output/result pairs are re-hashed at recording time, intentionally adding I/O
even for a multi-GB ISO; a consumer's held copy alone cannot establish that producer pairing.

## Declared NKE material map

The declarations in `definitions/nke-os/catalog/registry.yaml` and its exact source/artifact lock are the source
of this map. Both architectures use the same trace rules; the ISO and disk roots are explicitly architecture-scoped.

| Member | Expected staged observation | Still outside this trace |
|---|---|---|
| NKE agent | Built Debian package | Individual Go modules inside the prepared source closure |
| Kubernetes/runtime distribution | Built package archive | Leaf artifacts obtained by the source's bundle preparation |
| Calico | Built package archive | Individual manifests and image contents acquired during preparation |
| SPIRE and TPM plugin | Built package archive | Release artifacts acquired during preparation |
| OVN image and chart | Separate built outputs, then CNI package archive | Network acquisitions inside the wrapped BuildKit build |
| Original installer ISO | Digest-verified acquired bytes | Installed Ubuntu package closure inside the ISO |
| Patched installer ISO | Transform output with original-ISO edge | Filesystem/package contents are not inspected here |
| Aggregate image inputs | Composer output with all selected package/material edges | Member archives are not equated with installed-file bytes |
| Final QCOW2 | Held disk output with aggregate-input and patched-ISO edges | Guest-acquired packages/security updates and installed placement |
| Packer/toolchain | Existing independent runtime admission | Toolchain contents are not material-member receipts in this trace |

Source pins and prepared-source digests remain distinct from exhaustive per-URI/per-artifact acquisition records.
The source owner's vendoring process can populate verified bytes without exposing each acquired leaf through
this boundary. This implementation does not infer those leaves from logs or commands.

## Inspection handoff

The factory owns the graph and live disk subject. The SBOM owner must not reconstruct that graph from paths,
accept a description as authority, or compare archive digests directly with installed-file hashes. Inspection
must consume the held disk through its existing export boundary and return its own exact-byte observation.
Factory then joins the exact subject identities. Installed-only inventory and staged-only tracing together
still do not establish full acquisition coverage or a reconciled production disk SBOM.

Embedded-content declarations are a separate schema change. Under ADR-10, members already built by NKD retain
their material edges; they must not be relabeled as declaration-only `embeds`.
