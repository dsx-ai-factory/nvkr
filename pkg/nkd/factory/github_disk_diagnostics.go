package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/diskdiagnostic"
	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

const maxGitHubInspectionLogBytes = diskdiagnostic.MaxRetainedBytes + len(inspectionLogTruncated)

const inspectionLogTruncated = "{\"truncated\":true}\n"

// Called after the owned inspection stops and before the held workspace closes.
// Retention never replaces the operation result and survives caller cancellation.
func finishGitHubInspectionDiagnostics(ctx context.Context, workspace *os.File, output string, collector *diskdiagnostic.Collector, options GitHubStepOptions) {
	if collector == nil {
		return
	}
	raw, truncated := collector.Snapshot()
	retentionCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	if err := retainGitHubInspectionLog(retentionCtx, workspace, output, raw, truncated); err != nil && options.Stderr != nil {
		message := err.Error()
		if len(message) > 4096 {
			message = message[:4096]
		}
		_, _ = fmt.Fprintf(options.Stderr, "Unable to retain inspection diagnostics: %q\n", message)
	}
}

func reportGitHubInspectionDiagnostics(progress *githubStepProgress, collector *diskdiagnostic.Collector, err error) {
	if collector == nil {
		return
	}
	if phase := collector.LastPhase(); phase != "" {
		progress.phase("Inspection: " + phase)
	}
	if err == nil {
		return
	}
	raw, truncated := collector.Snapshot()
	if len(raw) == 0 {
		return
	}
	_, _ = fmt.Fprintln(progress.options.Stdout, "::group::Inspection diagnostics")
	if truncated {
		_, _ = fmt.Fprint(progress.options.Stdout, inspectionLogTruncated)
	}
	_, _ = progress.options.Stdout.Write(raw)
	_, _ = fmt.Fprintln(progress.options.Stdout, "::endgroup::")
}

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
			"if-no-files-found": "ignore", "retention-days": "1",
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
		return e.New(e.CodeInternal, "inspection diagnostic snapshot exceeds its bounded collector")
	}
	parent, name, err := securepath.CreateParent(workspace, githubInspectionLogPath(output))
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "hold inspection diagnostic parent")
	}
	defer func() { _ = parent.Close() }()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "inspection diagnostic retention canceled before file creation")
	}
	file, temporary, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "create inspection diagnostic file")
	}
	defer func() { _ = file.Close() }()
	defer func() { _ = unix.Unlinkat(int(parent.Fd()), temporary, 0) }()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "inspection diagnostic retention canceled before writing")
	}
	if truncated {
		if _, err := file.WriteString(inspectionLogTruncated); err != nil {
			return e.Wrap(e.CodeInternal, err, "write inspection truncation marker")
		}
	}
	if _, err := file.Write(raw); err != nil {
		return e.Wrap(e.CodeInternal, err, "write inspection diagnostics")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "inspection diagnostic retention canceled before sync")
	}
	if err := file.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "sync inspection diagnostics")
	}
	if err := file.Close(); err != nil {
		return e.Wrap(e.CodeInternal, err, "close inspection diagnostics")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "inspection diagnostic retention canceled before publication")
	}
	// Linkat publishes only a complete synchronized file, atomically and without
	// replacing any existing destination. Temporary siblings are never uploaded.
	if err := unix.Linkat(int(parent.Fd()), temporary, int(parent.Fd()), name, 0); err != nil {
		return e.Wrap(e.CodeInternal, err, "publish inspection diagnostic file")
	}
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "sync inspection diagnostic parent")
	}
	return nil
}
