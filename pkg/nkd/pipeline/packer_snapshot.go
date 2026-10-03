package pipeline

import (
	"context"
	"io"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// SnapshotHeldContext lends an immutable snapshot of the originally collected
// inode to a read-only inspector. It never reopens an artifact pathname. The
// caller owns the returned snapshot and must Close it; artifact ownership stays
// with the Run. Capacity admission must account for this additional exact copy.
func (artifact *PackerArtifact) SnapshotHeldContext(ctx context.Context, maxBytes int64) (*heldfile.File, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "snapshotting a Packer artifact requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "snapshotting a Packer artifact")
	}
	if artifact == nil || maxBytes <= 0 {
		return nil, e.New(e.CodeUsage, "snapshotting requires a collected artifact and positive byte budget")
	}
	if err := lockDiskInspection(ctx, &artifact.mu); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "waiting to snapshot the collected Packer artifact")
	}
	defer artifact.mu.Unlock()
	if artifact.file == nil {
		return nil, e.New(e.CodeVerifyFailed, "collected Packer artifact is closed")
	}
	if artifact.size <= 0 || artifact.size > maxBytes {
		return nil, e.New(e.CodeEngineCapability, "collected Packer artifact exceeds the inspection byte budget")
	}
	// SectionReader uses the held descriptor's ReaderAt, leaving its streaming
	// cursor unchanged. The existing held-file owner enforces bounds and digest.
	info, err := artifact.file.Stat()
	if err != nil || info.Size() != artifact.size {
		return nil, e.New(e.CodeVerifyFailed, "collected Packer artifact size changed")
	}
	snapshot, err := heldfile.FromReaderContext(ctx, io.NewSectionReader(artifact.file, 0, artifact.size),
		heldfile.Options{Label: "disk-inspection-subject", MaxBytes: maxBytes, ExpectedDigest: artifact.digest})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "snapshotting a Packer artifact")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding the collected Packer artifact for inspection")
	}
	return snapshot, nil
}
