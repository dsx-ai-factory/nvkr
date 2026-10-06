package factory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// GitHubStepOptions supplies diagnostic destinations, never execution authority.
// Failure to write a log or optional summary cannot change the operation result.
type GitHubStepOptions struct {
	Stdout, Stderr io.Writer
	SummaryPath    string
}

type githubStepProgress struct {
	options GitHubStepOptions
	label   string
	details []string
	current string
	started time.Time
}

// githubProgress is entered only after the caller has admitted the complete Plan
// and exact operation. Its diagnostic values are descriptions, not evidence.
func githubProgress(ctx context.Context, options GitHubStepOptions, label string, details []string, work func(*githubStepProgress) error) error {
	if ctx == nil || work == nil {
		return e.New(e.CodeUsage, "GitHub progress requires context and operation")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "GitHub operation canceled")
	}
	lock := &sync.Mutex{}
	stdout, closeStdout := githubDiagnosticSink(options.Stdout)
	defer closeStdout()
	stderr, closeStderr := githubDiagnosticSink(options.Stderr)
	defer closeStderr()
	options.Stdout, options.Stderr = stdout, stderr
	options.Stdout = githubDiagnosticWriter{lock, options.Stdout}
	options.Stderr = githubDiagnosticWriter{lock, options.Stderr}
	progress := &githubStepProgress{options: options, label: label, details: append([]string(nil), details...), current: "prepare", started: time.Now()}
	progress.log("Starting: " + label)
	for _, detail := range details {
		progress.log(detail)
	}
	err := work(progress)
	// Cancellation must not turn a killed child or interrupted transport into an
	// ordinary byte/verification failure. A failed diagnostic never reaches here.
	if ctxErr := ctx.Err(); ctxErr != nil {
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
			err = e.Wrap(e.CodeTimeout, ctxErr, "GitHub operation canceled")
		}
	}
	progress.finish(err)
	return err
}

func (progress *githubStepProgress) phase(value string) { progress.current = value }
func (progress *githubStepProgress) log(value string) {
	_, _ = fmt.Fprintln(progress.options.Stdout, githubDisplayText(value))
}

func (progress *githubStepProgress) detail(value string) {
	progress.log(value)
	progress.details = append(progress.details, value)
}

func (progress *githubStepProgress) finish(err error) {
	elapsed := int64(time.Since(progress.started) / time.Second)
	status := GitHubStepExitCode(err)
	result := "completed"
	if err == nil {
		progress.log(fmt.Sprintf("Completed: %s (%ds)", progress.label, elapsed))
	} else {
		result = "failed"
		_, _ = fmt.Fprintf(progress.options.Stderr, "Failed: %s at %s (exit %d, %ds)\n", githubDisplayText(progress.label), githubDisplayText(progress.current), status, elapsed)
	}
	if progress.options.SummaryPath == "" {
		return
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "### %s\n\n| Result | Exit | Seconds | Last phase |\n| --- | --- | --- | --- |\n| %s | %d | %d | <code>%s</code> |\n\n", githubSummaryText(progress.label), result, status, elapsed, githubSummaryText(progress.current))
	for _, detail := range progress.details {
		fmt.Fprintf(&summary, "- <code>%s</code>\n", githubSummaryText(detail))
	}
	summary.WriteByte('\n')
	if err := appendGitHubSummary(progress.options.SummaryPath, summary.String()); err != nil {
		_, _ = fmt.Fprintln(progress.options.Stderr, "Unable to write diagnostic job summary.")
	}
}

// Child stdout/stderr may be copied concurrently. Closed diagnostics are ignored
// without exposing a write failure as the child's execution result.
type githubDiagnosticWriter struct {
	mu  *sync.Mutex
	out io.Writer
}

func (writer githubDiagnosticWriter) Write(raw []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.out != nil {
		_, _ = writer.out.Write(raw)
	}
	return len(raw), nil
}
