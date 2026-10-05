//go:build !linux && !darwin

package factory

import (
	"fmt"
	"io"
)

func githubDiagnosticSink(writer io.Writer) (io.Writer, func()) {
	return writer, func() {}
}

func appendGitHubSummary(_, _ string) error {
	return fmt.Errorf("safe diagnostic summary append is unsupported on this platform")
}
