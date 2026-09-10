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
	"sigs.k8s.io/yaml"
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
		factory.EngineGitLab: writeGitLabEnvironmentFixture(t, repo),
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

func writeGitLabEnvironmentFixture(t *testing.T, repo string) string {
	t.Helper()
	directory, err := os.MkdirTemp(repo, ".nkd-gitlab-environment-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove GitLab environment fixture: %v", err)
		}
	})
	environment := Environment{
		APIVersion:       EnvironmentAPIVersion,
		Kind:             EnvironmentKind,
		Metadata:         Metadata{Name: "gitlab-build-only"},
		RunsOn:           "generic",
		UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161",
		UtilityPlatform:  "linux/amd64",
		Runtime: Runtime{
			Runtime:           "docker",
			ToolchainRegistry: "registry.test/toolchains",
			BuilderImage:      "golang:1.27-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452",
			RuntimeImage:      "docker:27.3.1@sha256:6ca9a6811085e2cf769cbac04bc47daf66629102990391caab7cf37426e939da",
			Services:          []string{"docker:27.3.1-dind@sha256:6ca9a6811085e2cf769cbac04bc47daf66629102990391caab7cf37426e939da"},
			Variables: map[string]string{
				"DOCKER_HOST":        "tcp://docker:2375",
				"DOCKER_TLS_CERTDIR": "",
			},
		},
	}
	raw, err := yaml.Marshal(environment)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "environment.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(repo, path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(relative)
}

func TestGenerateFilesRejectsUnsupportedEngineBeforeReadingInputsOrOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist")
	_, err := GenerateFiles(context.Background(), "missing-definition.yaml", "missing-environment.yaml", "test", factory.EngineTekton, root, false)
	assertCode(t, err, nkderr.CodeEngineCapability)
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsupported generation touched output root: %v", statErr)
	}
}
