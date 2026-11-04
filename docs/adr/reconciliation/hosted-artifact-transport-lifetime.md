## Hosted artifact transport lifetime

The FDE build jobs in [Run 38480478189](https://github.com/dsx-ai-factory/nvkr/actions/runs/38480478189)
succeeded, but the later hosted SBOM-attestation jobs failed restoring their prepared layouts: the
[AMD64 job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38480478189/job/115601071511)
exhausted disk while snapshotting its carrier, and the ARM64 job failed during extraction. This records a
storage failure, not successful FDE publication.

The local transport implementation now streams extraction while hashing the held carrier, then rehashes
that same inode before publishing extracted roots. It retains the incoming carrier and private extracted
tree without an additional full tar snapshot. Delivery's attestation handoff consumes the disposable preparation:
signed evidence and metadata are prepared first, unchanged reachable blobs move into the final stage, and
the final closure is verified before no-replace publication. It preserves the preparation on pre-claim
failure; after claiming it, a failed operation requires restore/build retry. Retained preparation paths and
final payloads do not share hardlinks. Exact artifact subjects, evidence membership and hosted credential
boundaries are unchanged.

`TestUnpackRejectsTransientSourceMutation` and `TestUnpackChecksCompleteStreamBeforePublication` in
`internal/transportarchive/snapshot_test.go` cover a transient payload or trailer mutation restored before
the final rehash, complete plain/gzip stream validation, and cleanup after growth, truncation or cancellation.
`pkg/nvkr/delivery/attestation_move_test.go` covers the consuming handoff:
`TestAttestPreparedEvidenceTransfersDiskWithoutCopy` checks that the final disk retains its original inode
on both native-platform fixtures, `TestAttestPreparedEvidenceRejectsBeforeClaim` checks admission without
signing or consumption, and `TestAttestPreparedEvidenceFailureLifetime` checks coded failures, exact document
binding, source substitution, cancellation on both sides of the claim and destination collision. Its
same-metadata payload mutation case keeps inode, length and modification time unchanged and still fails
the final byte check after ownership transfer.
`TestAttestedLayoutCommitSyncFailureRollsBack` exercises a returned parent-sync failure. These are local
contract tests with synthetic artifacts and signer callbacks; they do not exercise hosted signing or prove
a complete crash/recovery matrix.

Local validation used Go 1.27.2. After rebasing onto `39c636d0046ddceaa6f4f5677a84db78558eeddd`, the complete
Linux repository suite (`go test ./... -count=1`) passed. The container included an init process for child
reaping, read-only worktree Git metadata and a writable `.nvkr` scratch mount; initial container runs lacked
required fixture support. Post-rebase workflow drift checks, vet, Linux-target lint, license, format,
shell/Python, vulnerability, build and diff checks passed, with no generated-file changes. Before rebasing,
targeted Linux race checks covered transport, attestation ownership and factory carrier operations, and
workflow regeneration was byte-identical. The earlier uncached macOS repository suite reached the ten-minute
package timeout in delivery, factory and lifecycle; all other packages passed. Post-rebase native macOS
`make check` still stops at the two existing Linux-only `smokeWorkerFiles` helper methods reported as unused.
These limits prevent a claim that the complete native macOS gate passed.

The expected local full-payload peaks are carrier plus extraction during restore, and final layout plus
outgoing carrier during packing. These lifetimes do not measure provider ZIP handling, workspace baseline,
or verification's later registry-fetched disk snapshot. No hosted rerun has exercised this implementation,
and it establishes neither a measured hosted storage envelope nor independent FDE consumption.
