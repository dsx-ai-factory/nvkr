// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func initOperatorArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	catalog, err := filepath.Abs(filepath.Join("..", "..", "inputs", "catalogs", "tools", "operator-arm64"))
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{"nvkr", "definition", "init", "--name", "my-tools", "--catalog", catalog, "--platform", "linux/arm64"}, extra...)
}

func TestDefinitionInitPrintsAValidatableDefinitionAndNothingElse(t *testing.T) {
	first, err := runCLIWithCapturedStdout(t, newApp(), initOperatorArgs(t, "--component", "kubectl", "--component", "helm")...)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runCLIWithCapturedStdout(t, newApp(), initOperatorArgs(t, "--component", "helm", "--component", "kubectl")...)
	if err != nil || string(first) != string(second) {
		t.Fatalf("output depends on flag order: %v\n%s\n%s", err, first, second)
	}
	path := filepath.Join(t.TempDir(), "definition.yaml")
	if err := os.WriteFile(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := lifecycle.LoadDefinition(t.Context(), path)
	if err != nil {
		t.Fatalf("emitted Definition does not load: %v\n%s", err, first)
	}
	if len(loaded.Outputs) != 1 || strings.Join(loaded.Outputs[0].Components, ",") != "helm,kubectl" {
		t.Fatalf("unexpected output selection: %+v", loaded.Outputs)
	}
	validated, err := runCLIWithCapturedStdout(t, newApp(), "nvkr", "definition", "validate", "--definition", path)
	if err != nil || !strings.Contains(string(validated), "outputs: 1") {
		t.Fatalf("definition validate: %v: %s", err, validated)
	}
}

func TestDefinitionInitRefusalsPrintNoYAML(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
		code  e.Code
	}{
		{"unknown component", []string{"--component", "nope"}, e.CodeInvalidCatalog},
		{"duplicate component", []string{"--component", "helm", "--component", "helm"}, e.CodeUsage},
		{"unsupported platform", []string{"--platform", "linux/amd64", "--component", "helm"}, e.CodeEngineCapability},
		{"unexpected argument", []string{"--component", "helm", "extra"}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runCLIWithCapturedStdout(t, newApp(), initOperatorArgs(t, tc.extra...)...)
			assertLifecycleCommandCode(t, err, tc.code)
			if len(output) != 0 {
				t.Fatalf("refused request printed %q", output)
			}
		})
	}
}

func TestDefinitionInitTUIPrintsNoYAML(t *testing.T) {
	output, err := runCLIWithCapturedStdout(t, newApp(), initOperatorArgs(t, "--component", "helm", "--ui=tui")...)
	assertLifecycleCommandCode(t, err, e.CodeUsage)
	if len(output) != 0 {
		t.Fatalf("rejected viewer printed %q", output)
	}
}
