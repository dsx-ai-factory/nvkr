//go:build linux

package pipeline

import (
	"context"
	"os"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
)

const maxQualifiedRunnerBytes int64 = 1 << 30

// snapshotMappedRunnerExecutable copies the executable inode mapped by the current
// process, not the mutable pathname returned by os.Executable. Linux's proc magic
// link retains access to the mapped inode even if the original path is renamed,
// replaced, or unlinked. The returned descriptor remains held through stage two.
func snapshotMappedRunnerExecutable() (*heldfile.File, func(), error) {
	return snapshotMappedRunnerExecutableContext(context.Background())
}

func snapshotMappedRunnerExecutableContext(ctx context.Context) (*heldfile.File, func(), error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "holding the mapped runner requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "holding the mapped runner executable")
	}
	in, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, nil, e.Wrap(e.CodeEngineCapability, err,
			"opening the mapped runner executable through /proc/self/exe")
	}
	defer in.Close()
	before, err := in.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 {
		return nil, nil, e.New(e.CodeEngineCapability,
			"mapped runner executable is not a regular executable")
	}
	held, err := heldfile.FromReaderContext(ctx, in, heldfile.Options{
		Label: "qualified-runner", Executable: true, MaxBytes: maxQualifiedRunnerBytes,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, e.Wrap(e.CodeTimeout, ctx.Err(), "holding mapped runner executable")
		}
		return nil, nil, e.Wrap(e.CodeEngineCapability, err,
			"holding mapped runner executable")
	}
	after, statErr := in.Stat()
	if statErr != nil || held.Size() != before.Size() ||
		!os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		_ = held.Close()
		return nil, nil, e.New(e.CodeEngineCapability,
			"mapped runner executable changed while being snapshotted")
	}
	cleanup := func() { _ = held.Close() }
	return held, cleanup, nil
}
