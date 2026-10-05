//go:build !linux && !darwin

package factory

import (
	"context"
	"errors"
	"os"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestGitHubRecoveryUnsupportedPlatform(t *testing.T) {
	for _, test := range []struct {
		name string
		call func() error
	}{
		{"admission", admitGitHubRecoveryPlatform},
		{"outcome", func() error { return writeGitHubRestoreOutcome(context.Background(), "artifact", "missing") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			err := test.call()
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("unsupported recovery error = %v", err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("unsupported recovery changed workspace: entries=%v error=%v", entries, err)
			}
		})
	}
}
