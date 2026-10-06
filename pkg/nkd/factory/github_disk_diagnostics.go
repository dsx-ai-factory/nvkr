package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/internal/diskdiagnostic"
	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

const maxGitHubInspectionLogBytes = diskdiagnostic.MaxRetainedBytes + len(inspectionLogTruncated)

const inspectionLogTruncated = "{\"truncated\":true}\n"

func githubInspectionLogPath(output string) string {
	return filepath.ToSlash(filepath.Join(".nkd", "diagnostics", "inspection", shortStableName(output)+".jsonl"))
}

// Only this exact non-evidence upload may tolerate a missing diagnostic file.
func githubInspectionDiagnosticStep(actions githubActionRefs, output string) githubStep {
	return githubStep{
		Name: "Retain inspection diagnostics", Uses: actions.Upload,
		If: "${{ always() }}", ContinueOnError: true,
		With: map[string]string{
			"name": "nkd-inspection-" + shortStableName(output) + "-${{ github.run_id }}-${{ github.run_attempt }}",
			"path": githubInspectionLogPath(output), "include-hidden-files": "true",
			"if-no-files-found": "warn", "retention-days": "1",
		},
	}
}

// retainGitHubInspectionLog persists a bounded sanitized Collector snapshot,
// never raw subprocess output. It creates one exclusive regular file beneath the original
// held workspace. Existing files and symlinked parents are never followed or
// overwritten. The caller reports retention failure without replacing the
// operation's primary result. Context checks bound admission between operations;
// already-entered regular-file syscalls, including fsync, cannot be interrupted.
func retainGitHubInspectionLog(ctx context.Context, workspace *os.File, output string, raw []byte, truncated bool) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "inspection diagnostic retention requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "inspection diagnostic retention canceled")
	}
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > diskdiagnostic.MaxRetainedBytes {
		return fmt.Errorf("inspection diagnostic snapshot exceeds its bounded collector")
	}
	parent, name, err := securepath.CreateParent(workspace, githubInspectionLogPath(output))
	if err != nil {
		return fmt.Errorf("hold inspection diagnostic parent: %w", err)
	}
	defer func() { _ = parent.Close() }()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "inspection diagnostic retention canceled before file creation")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create inspection diagnostic file: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "inspection diagnostic retention canceled before writing")
	}
	if truncated {
		if _, err := file.WriteString(inspectionLogTruncated); err != nil {
			return fmt.Errorf("write inspection truncation marker: %w", err)
		}
	}
	if _, err := file.Write(raw); err != nil {
		return fmt.Errorf("write inspection diagnostics: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "inspection diagnostic retention canceled before sync")
	}
	return file.Sync()
}
