package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func TestGitHubFactoryCommandsRejectUnboundWorkBeforeOutputs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", factory.FactoryPlanPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"missing node", []string{"github", "run-node", "--plan", "plan.json"}, e.CodeUsage},
		{"unknown node", []string{"github", "run-node", "--plan", "plan.json", "--node", "unknown"}, e.CodeInvalidCatalog},
		{"unknown stage", []string{"github", "restore-inputs", "--plan", "plan.json", "--stage", "unknown"}, e.CodeInvalidCatalog},
		{"unknown pack node", []string{"github", "pack-outputs", "--plan", "plan.json", "--node", "unknown"}, e.CodeInvalidCatalog},
		{"missing recovery jobs", []string{"github", "recover", "--plan", "plan.json"}, e.CodeInvalidCatalog},
		{"positional arguments", []string{"github", "recover", "--plan", "plan.json", "unexpected"}, e.CodeUsage},
		{"extra authority flag", []string{"github", "run-node", "--plan", "plan.json", "--node", "unknown", "--workspace", "outside"}, e.CodeUsage},
		{"substituted plan", []string{"execute-node", "--plan", "plan.json", "--node", "unknown", "--plan-digest", "sha256:" + strings.Repeat("a", 64)}, e.CodePipelineDrift},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(root, "summary.md"))
			t.Setenv("NKD_JOB_RESULTS", "{}")
			if err := os.WriteFile("plan.json", raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := invokeLifecycleCLI(t, append([]string{"factory"}, test.args...)...)
			assertLifecycleCommandCode(t, err, test.code)
			for _, path := range []string{".nkd", "summary.md"} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("rejected operation touched %s: %v", path, err)
				}
			}
		})
	}
}

// Exercise main in a fresh process: writing to a reader-closed fd 2 otherwise
// exits with SIGPIPE before the selected coded status reaches os.Exit.
func TestCLIFinalDiagnosticPreservesStatusWithClosedPipe(t *testing.T) {
	if os.Getenv("NKD_TEST_FINAL_DIAGNOSTIC") == "1" {
		os.Args = []string{"nkdctl", "unknown-command"}
		main()
		return
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIFinalDiagnosticPreservesStatusWithClosedPipe$")
	command.Env = append(os.Environ(), "NKD_TEST_FINAL_DIAGNOSTIC=1")
	command.Stderr = writer
	err = command.Run()
	want := e.ExitCode(e.New(e.CodeUsage, ""))
	if command.ProcessState == nil || command.ProcessState.ExitCode() != want {
		t.Fatalf("closed final diagnostic changed exit: %v, state=%v, want %d", err, command.ProcessState, want)
	}
}
