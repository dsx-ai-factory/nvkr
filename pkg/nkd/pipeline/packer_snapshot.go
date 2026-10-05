package pipeline

import (
	"context"
	"errors"
	"io"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
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
	if !canonical.IsDigest(artifact.digest) {
		return nil, e.New(e.CodeVerifyFailed, "collected Packer artifact digest is not exact")
	}
	// SectionReader leaves the held descriptor's streaming cursor unchanged.
	// Compare the frozen bytes separately so allocation and read failures cannot
	// be mistaken for integrity failures.
	info, err := artifact.file.Stat()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "reading collected Packer artifact metadata")
	}
	if info.Size() != artifact.size {
		return nil, e.New(e.CodeVerifyFailed, "collected Packer artifact size changed")
	}
	snapshot, err := heldfile.FromReaderContext(ctx, io.NewSectionReader(artifact.file, 0, artifact.size),
		heldfile.Options{Label: "disk-inspection-subject", MaxBytes: maxBytes})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, errors.Join(ctx.Err(), err), "snapshotting a Packer artifact")
		}
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding the collected Packer artifact for inspection")
	}
	if snapshot.Digest() != artifact.digest || snapshot.Size() != artifact.size {
		return nil, e.Wrap(e.CodeVerifyFailed, snapshot.Close(), "collected Packer artifact bytes changed")
	}
	return snapshot, nil
}
