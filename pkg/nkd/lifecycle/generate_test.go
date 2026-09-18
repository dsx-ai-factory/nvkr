package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func TestGenerateFilesUsesLifecycleInputsForSupportedHostedEngines(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	definition := "definitions/aicr/definition.yaml"
	environments := map[string]string{
		factory.EngineGitHub: "definitions/aicr/environment-github.yaml",
	}
	for engine, environment := range environments {
		t.Run(engine, func(t *testing.T) {
			root := t.TempDir()
			digest, err := GenerateFiles(context.Background(), definition, environment, "test (deadbeef)", engine, root, false)
			if err != nil {
				t.Fatal(err)
			}
			if !canonical.IsDigest(digest) {
				t.Fatalf("plan digest = %q", digest)
			}
			checked, err := GenerateFiles(context.Background(), definition, environment, "test (deadbeef)", engine, root, true)
			if err != nil {
				t.Fatal(err)
			}
			if checked != digest {
				t.Fatalf("checked digest %s != generated digest %s", checked, digest)
			}
		})
	}
}

func TestGenerateFilesRejectsUnsupportedEngineBeforeReadingInputsOrOutput(t *testing.T) {
	for _, engine := range []string{factory.EngineGitLab, factory.EngineTekton, factory.EngineArgo} {
		t.Run(engine, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "must-not-exist")
			_, err := GenerateFiles(context.Background(), "missing-definition.yaml", "missing-environment.yaml", "test", engine, root, false)
			assertCode(t, err, nkderr.CodeEngineCapability)
			if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unsupported generation touched output root: %v", statErr)
			}
		})
	}
}
