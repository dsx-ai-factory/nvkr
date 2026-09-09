package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalFactoryDemoDefaultsToAICRLifecycle(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	logPath := filepath.Join(temp, "calls.log")
	fakeNKD := filepath.Join(temp, "nkdctl")
	if err := os.WriteFile(fakeNKD, []byte("#!/usr/bin/env bash\nprintf '<%s>' \"$*\" >> \"$DEMO_LOG\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "demo-ai-factory-local.sh"), "--plan")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NKDCTL_BIN="+fakeNKD, "DEMO_LOG="+logPath)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("AICR plan failed: %v\n%s", runErr, output)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	if !strings.Contains(calls, "definition validate --definition definitions/aicr/definition.yaml") ||
		!strings.Contains(calls, "run plan --definition definitions/aicr/definition.yaml --environment definitions/aicr/environment-local.yaml") {
		t.Fatalf("local demo did not select the maintained AICR lifecycle:\n%s", calls)
	}
}

func TestLocalFactoryDemoAllowsExplicitAdvancedDefinition(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	logPath := filepath.Join(temp, "calls.log")
	fakeNKD := filepath.Join(temp, "nkdctl")
	if err := os.WriteFile(fakeNKD, []byte("#!/usr/bin/env bash\nprintf '<%s>' \"$*\" >> \"$DEMO_LOG\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "demo-ai-factory-local.sh"), "--plan",
		"--definition", "testdata/examples/ai-factory/definition.yaml",
		"--environment", "testdata/examples/ai-factory/environment-local.yaml")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NKDCTL_BIN="+fakeNKD, "DEMO_LOG="+logPath)
	output, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("advanced fixture plan failed: %v\n%s", runErr, output)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "definition validate --definition testdata/examples/ai-factory/definition.yaml") ||
		!strings.Contains(string(raw), "run plan --definition testdata/examples/ai-factory/definition.yaml --environment testdata/examples/ai-factory/environment-local.yaml") {
		t.Fatalf("explicit advanced fixture was not honored:\n%s", raw)
	}
}
