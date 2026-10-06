package factory

import (
	"context"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestGitHubReporterPreservesInspectionTimeoutSummary(t *testing.T) {
	for _, test := range []struct {
		name     string
		primary  error
		preserve bool
	}{
		{"nil result", nil, false},
		{"other coded result", e.New(e.CodeVerifyFailed, "verification failed"), false},
		{"inspection timeout", e.Wrap(e.CodeTimeout, context.Canceled, "inspecting held disk: phase=nested-scan elapsedMillis=123"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := githubProgress(ctx, GitHubStepOptions{}, "Inspect disk", nil, func(*githubStepProgress) error {
				cancel()
				return test.primary
			})
			assertFactoryErrorCode(t, err, e.CodeTimeout)
			if test.preserve {
				if err != test.primary || !strings.Contains(err.Error(), "phase=nested-scan") || !strings.Contains(err.Error(), "elapsedMillis=123") {
					t.Fatalf("lost inspection timeout summary: %v", err)
				}
			} else if !strings.Contains(err.Error(), "GitHub operation canceled") {
				t.Fatalf("cancellation did not take precedence: %v", err)
			}
		})
	}
}
