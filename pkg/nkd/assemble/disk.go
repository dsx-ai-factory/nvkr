package assemble

import (
	"context"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

// This file implements NKD v1 §8.3.1's canonical-disk boundary — the reconciliation
// of nkdctl's disk-image reproducibility toward NKD v1. RAW is the ONLY canonical OS
// disk artifact: its byte boundary is unambiguous — a digest over the complete
// logical LBA stream [0, virtual_size), with no container header, allocation
// metadata, or writer-tool version in the payload. QCOW2/VMDK/VHD are DERIVED
// ENVELOPES: a deterministic, pinned conversion of the reproduced RAW image, verified
// by the decoded logical-disk digest, never by the container-file digest (QCOW2
// embeds a writer-tool version, header padding, image UUID, refcount ordering,
// cluster size, and compression state, all of which vary across otherwise-identical
// hosts). The reproducibility assertion therefore attaches HERE, to the RAW stream.

// LogicalDiskDigestContext computes diagnostic logical identity from one held
// regular-file capability while observing cancellation.
func LogicalDiskDigestContext(ctx context.Context, rawPath string) (string, int64, error) {
	snapshot, err := openRegularFileCapabilityContext(ctx, rawPath, "raw disk "+rawPath)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = snapshot.Close() }()
	return snapshot.logicalDigestContext(ctx)
}

// BlockManifest is a per-block digest series over the canonical RAW LBA stream at a
// fixed block size — the diagnosable granularity NKD v1 §8.4.2 requires so a mismatch
// is locatable without the reference bytes (it approaches a Merkle tree over the
// image). Aggregate is LogicalDiskDigest of the same stream. Deterministic.
type BlockManifest struct {
	BlockSizeBytes int      `json:"blockSizeBytes"`
	Aggregate      string   `json:"aggregate"`
	Blocks         []string `json:"blocks"` // sha256 per block, in LBA order
}

// BlockManifestOfContext computes a bounded diagnostic block manifest from one
// held regular-file capability while observing cancellation.
func BlockManifestOfContext(ctx context.Context, rawPath string, blockSize int) (BlockManifest, error) {
	// Reject an attacker-sized working allocation before touching rawPath.
	if _, err := boundedDiskBlockCount(0, blockSize, e.CodeInvalidCatalog); err != nil {
		return BlockManifest{}, err
	}
	if err := checkContext(ctx, "building a raw-disk block manifest"); err != nil {
		return BlockManifest{}, err
	}
	snapshot, err := openRegularFileCapabilityContext(ctx, rawPath, "raw disk "+rawPath)
	if err != nil {
		return BlockManifest{}, err
	}
	defer func() { _ = snapshot.Close() }()
	return snapshot.blockManifestContext(ctx, blockSize)
}
