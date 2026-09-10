package lifecycle

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPreparedFilesDryRunPreservesFrozenRequirements(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	prepared, err := PrepareFiles(t.Context(), "definitions/nke-v1/definition.yaml", "definitions/nke-v1/environment-local.yaml", "test", "attempt")
	if err != nil {
		t.Fatal(err)
	}
	before := prepared.Description()
	if before.Attempt != "attempt" || before.Status != RunPending || len(before.Expected) == 0 {
		t.Fatalf("invalid prepared run: %+v", before)
	}
	root := filepath.Join(".nkd", "runs", filepath.Base(t.TempDir()))
	got, err := ExecuteLocal(t.Context(), prepared, LocalOptions{AttemptsRoot: root, DryRun: true, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, before) {
		t.Fatalf("dry run changed frozen state: %+v", got)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("dry run reserved attempt storage: %v", err)
	}
	got.Expected[0].Name = "changed"
	got.Expected[0].Components[0] = "changed"
	got.Expected[0].ComponentResults[0] = "changed"
	if !reflect.DeepEqual(prepared.Description(), before) {
		t.Fatal("description exposed mutable prepared requirements")
	}
	if !reflect.DeepEqual((*PreparedRun)(nil).Description(), Run{}) {
		t.Fatal("nil description must be empty")
	}
}

func TestPrepareFilesRejectsInputsWithoutIssuingCapability(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	for _, tc := range []struct{ name, definition, environment, attempt string }{
		{"missing definition", "missing.yaml", "definitions/nke-v1/environment-local.yaml", "attempt"},
		{"missing environment", "definitions/nke-v1/definition.yaml", "missing.yaml", "attempt"},
		{"invalid attempt", "definitions/nke-v1/definition.yaml", "definitions/nke-v1/environment-local.yaml", "../escape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepared, err := PrepareFiles(t.Context(), tc.definition, tc.environment, "test", tc.attempt)
			if err == nil || prepared != nil {
				t.Fatalf("invalid input issued capability: %+v, %v", prepared, err)
			}
			if tc.name == "invalid attempt" {
				assertCode(t, err, e.CodeUsage)
			}
		})
	}
	for _, prepared := range []*PreparedRun{nil, {}} {
		_, err := ExecuteLocal(t.Context(), prepared, LocalOptions{DryRun: true})
		assertCode(t, err, e.CodeUsage)
	}
}
