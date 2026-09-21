package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func recordRunFailure(ctx context.Context, path string, run *Run, cause error, operation string) (*Run, error) {
	failed := failedRun(run, cause)
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	commitCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	if err := writeRunRecord(commitCtx, path, failed); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, errors.Join(cause, err), "%s", operation)
	}
	return failed, cause
}

func writeRunRecord(ctx context.Context, path string, run *Run) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "committing run record canceled")
	}
	raw, err := canonical.Marshal(run)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".lifecycle-*.json")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "staging immutable run record")
	}
	stage := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(stage) }()
	if _, err := file.Write(raw); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "writing run record")
	}
	if err := file.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "syncing run record")
	}
	if err := file.Close(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "closing run record")
	}
	if err := os.Chmod(stage, 0o444); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "protecting run record")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "committing run record canceled")
	}
	if err := os.Link(stage, path); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing immutable run record")
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening run directory")
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "syncing run directory")
	}
	return nil
}
