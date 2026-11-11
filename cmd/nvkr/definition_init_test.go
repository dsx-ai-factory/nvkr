// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func initOperatorArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	catalog, err := filepath.Abs(filepath.Join("..", "..", "inputs", "catalogs", "tools"))
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{"nvkr", "definition", "init", "--name", "my-tools", "--catalog", catalog, "--platform", "linux/arm64"}, extra...)
}

func TestDefinitionInitPrintsAValidatableDefinitionAndNothingElse(t *testing.T) {
	first, err := runCLIWithCapturedStdout(t, newApp(), initOperatorArgs(t, "--component", "kubectl-operator", "--component", "helm-operator")...)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runCLIWithCapturedStdout(t, newApp(), initOperatorArgs(t, "--component", "helm-operator", "--component", "kubectl-operator")...)
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
	if len(loaded.Outputs) != 1 || strings.Join(loaded.Outputs[0].Components, ",") != "helm-operator,kubectl-operator" {
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
		{"duplicate component", []string{"--component", "helm-operator", "--component", "helm-operator"}, e.CodeUsage},
		{"unsupported platform", []string{"--platform", "darwin/arm64", "--component", "helm-operator"}, e.CodeEngineCapability},
		{"unexpected argument", []string{"--component", "helm-operator", "extra"}, e.CodeUsage},
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

func TestDefinitionInitMissingFlagIsCodedAndPrintsNothing(t *testing.T) {
	full := initOperatorArgs(t, "--component", "helm-operator")[3:]
	for _, flag := range []string{"--name", "--catalog", "--platform", "--component"} {
		t.Run(flag, func(t *testing.T) {
			args := []string{"nvkr", "definition", "init"}
			for i := 0; i < len(full); i += 2 {
				if full[i] != flag {
					args = append(args, full[i], full[i+1])
				}
			}
			output, err := runCLIWithCapturedStdout(t, newApp(), args...)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if len(output) != 0 {
				t.Fatalf("missing %s printed %q", flag, output)
			}
		})
	}
}

func TestDefinitionInitTUIPrintsNoYAML(t *testing.T) {
	output, err := runCLIWithCapturedStdout(t, newApp(), initOperatorArgs(t, "--component", "helm-operator", "--ui=tui")...)
	assertLifecycleCommandCode(t, err, e.CodeUsage)
	if len(output) != 0 {
		t.Fatalf("rejected viewer printed %q", output)
	}
}

func definitionInitPresentationApp(show func(progress.Snapshot) runpresenter.Result) *cli.App {
	return &cli.App{Commands: []*cli.Command{cmdDefinition()}, Metadata: map[string]interface{}{
		commandPresentationServicesKey: runPresentationServices{
			eligible: func(io.Writer) bool { return true },
			show: func(_ context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				if work != nil || options.Live {
					panic("definition init presentation attempted execution")
				}
				return show(source.Snapshot())
			},
		},
	}}
}

func TestDefinitionInitTUIShowsTheSelectionAndPrintsNoYAML(t *testing.T) {
	for _, tc := range []struct {
		name      string
		component string
		status    string
		code      e.Code
	}{
		{"admitted", "helm-operator", "complete", ""},
		{"refused", "nope", "failed", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var shown *progress.Snapshot
			app := definitionInitPresentationApp(func(snapshot progress.Snapshot) runpresenter.Result {
				shown = &snapshot
				return runpresenter.Result{}
			})
			output, err := runCLIWithCapturedStdout(t, app, initOperatorArgs(t, "--component", tc.component, "--ui=tui")...)
			if tc.code == "" && err != nil {
				t.Fatal(err)
			}
			if tc.code != "" {
				assertLifecycleCommandCode(t, err, tc.code)
			}
			if len(output) != 0 {
				t.Fatalf("TUI printed %q", output)
			}
			if shown == nil || shown.Status != tc.status || shown.FailureCode != string(tc.code) {
				t.Fatalf("unexpected viewer snapshot: %+v", shown)
			}
			if tc.code == "" {
				fields := map[string]string{}
				for _, field := range shown.Fields {
					fields[field.Label] = field.Value
				}
				if fields["Name"] != "my-tools" || fields["Platform"] != "linux/arm64" || fields["Components"] != "helm-operator" || !filepath.IsAbs(fields["Catalog"]) {
					t.Fatalf("unexpected fields: %+v", shown.Fields)
				}
			}
		})
	}
}
