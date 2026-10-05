package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// TestAuthoringLabCLIProcess runs the real CLI adapter in a subprocess so the
// executable documentation exercises argv, output and coded exit contracts.
func TestAuthoringLabCLIProcess(t *testing.T) {
	if os.Getenv("NKD_AUTHORING_TEST_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		t.Fatal("helper invocation has no argument separator")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	err := newApp().RunContext(ctx, append([]string{"nkdctl"}, os.Args[separator+1:]...))
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(e.ExitCode(err))
}

func TestAuthoringLabReplacementJourney(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("authoring lab requires Python 3: ", err)
	}
	fixture, err := filepath.Abs(filepath.Join("..", "..", "docs", "examples", "authoring-lab", "run.py"))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	wrapper := filepath.Join(root, "nkdctl")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$NKD_AUTHORING_TEST_EXE\" -test.run='^TestAuthoringLabCLIProcess$' -- \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "lab")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, fixture, "--nkdctl", wrapper, "--work", work)
	cmd.Env = append(os.Environ(), "NKD_AUTHORING_TEST_HELPER=1", "NKD_AUTHORING_TEST_EXE="+executable)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("authoring journey: %v\n%s", err, output)
	}
	var report struct {
		Scope     string `json:"scope"`
		Scenarios map[string]struct {
			ChangedOutputs []string `json:"changedOutputs"`
		} `json:"scenarios"`
	}
	raw, err := os.ReadFile(filepath.Join(work, "identity-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Scenarios) != 4 || !strings.Contains(report.Scope, "no artifact execution") {
		t.Fatalf("incomplete or misleading authoring report: %+v", report)
	}
	// The script must refuse reusing a workspace instead of deleting or silently
	// modifying retained inputs and the evidence of the preceding journey.
	cmd = exec.CommandContext(ctx, python, fixture, "--nkdctl", wrapper, "--work", work)
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "FileExistsError") {
		t.Fatalf("existing workspace was not rejected: %v\n%s", err, output)
	}
	after, err := os.ReadFile(filepath.Join(work, "identity-report.json"))
	if err != nil || string(after) != string(raw) {
		t.Fatal("refused rerun modified the identity report")
	}
}
