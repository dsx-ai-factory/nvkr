package factory

import (
	"context"
	"os"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestLocalDiskPublicationRejectsBeforeExecution(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan := diskPublicationPlanFixture(t, platform)
			t.Chdir(t.TempDir())
			options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/unwired-disk-publication", Runtime: "must-not-execute"})
			assertRefusal := func(err error) {
				t.Helper()
				if factoryErrorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "disk publication") {
					t.Fatalf("admission error = %v, want explicit disk publication capability refusal", err)
				}
				if _, err := os.Lstat(".nkd"); !os.IsNotExist(err) {
					t.Fatalf("admission touched workspace: %v", err)
				}
			}
			assertRefusal(AdmitLocal(t.Context(), plan, options))
			calls := 0
			deps := localRunDeps{makeDisk: func(*Plan, *LocalRunOptions) diskProducerCallback {
				calls++
				return nil
			}, prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
				calls++
				return nil, nil
			}}
			result, err := runLocal(t.Context(), plan, options, deps)
			assertRefusal(err)
			if result != nil || calls != 0 {
				t.Fatalf("refused plan reached execution: result=%v callbacks=%d", result, calls)
			}
			options.DryRun = true
			result, err = RunLocal(t.Context(), plan, options)
			if err != nil || result == nil {
				t.Fatalf("static disk publication plan unavailable: %v", err)
			}
			defer result.Close()
			if _, err := os.Lstat(".nkd"); !os.IsNotExist(err) {
				t.Fatalf("dry-run touched workspace: %v", err)
			}
		})
	}
}
