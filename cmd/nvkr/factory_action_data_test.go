// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// Compile one executable producer: these CLI contracts need real shared action
// data, not the maintained product's complete graph and publication pipeline.
// The fixture does not execute producers, restore carriers, or contact providers.
func githubActionDataCommandFixture(t *testing.T) (planBytes []byte, selector, output string) {
	t.Helper()
	definition := authored.Definition{
		APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
		Metadata: authored.Metadata{Name: "action-data"},
		Catalog:  authored.Catalog{Base: filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")},
		Outputs: []authored.DefinitionOutput{{
			Name: "containerd", Platform: "linux/amd64", Format: authored.OutputExecutable,
			Components: []string{"containerd"},
		}},
	}
	environment := authored.Environment{
		APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind,
		Metadata: authored.Metadata{Name: "action-data"}, RunsOn: "ubuntu-24.04",
		UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161",
		UtilityPlatform:  "linux/amd64",
	}
	plan, err := factory.CompileDocuments(t.Context(), "definition.yaml", definition,
		"environment.yaml", environment, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}
	planBytes, err = canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := factory.RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Entries map[string]struct {
			Template string            `json:"template"`
			Values   map[string]string `json:"values"`
		} `json:"entries"`
	}
	if err := canonical.UnmarshalExact(bundle.Files[factory.GitHubActionDataPath], &data); err != nil {
		t.Fatal(err)
	}
	var selectors []string
	for key, entry := range data.Entries {
		if entry.Template != "" && len(entry.Values) > 0 {
			selectors = append(selectors, key)
		}
	}
	if len(selectors) == 0 {
		t.Fatal("fixture Plan has no shared action descriptor")
	}
	sort.Strings(selectors)
	selector = selectors[0]
	values, err := canonical.Marshal(data.Entries[selector].Values)
	if err != nil {
		t.Fatal(err)
	}
	return planBytes, selector, "data=" + string(values) + "\n"
}

func TestDescribeActionCommandUsesGitHubOutputEnvironment(t *testing.T) {
	planBytes, selector, want := githubActionDataCommandFixture(t)
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile("plan.json", planBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "selected-output")
	const retained = "existing=value\n"
	if err := os.WriteFile(output, []byte(retained), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_OUTPUT", output)
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(root, "summary.md"))
	stdout, err := invokeLifecycleCLI(t, "factory", "github", "describe-action", "--plan", "plan.json", "--action", selector)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != retained+want {
		t.Fatalf("descriptor did not append exact Plan-derived data to GITHUB_OUTPUT: %v", err)
	}
	if stdout != "" {
		t.Fatal("descriptor printed its provider-output payload to stdout")
	}
	for _, path := range []string{"summary.md", ".nvkr", ".github", "GITHUB_OUTPUT"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("descriptor touched unrelated path %s: %v", path, err)
		}
	}
}

func TestDescribeActionCommandRejectsBeforeChangingOutput(t *testing.T) {
	planBytes, selector, _ := githubActionDataCommandFixture(t)
	var invalidPlan factory.Plan
	if err := canonical.UnmarshalExact(planBytes, &invalidPlan); err != nil {
		t.Fatal(err)
	}
	invalidPlan.Name = ""
	invalidBytes, err := canonical.Marshal(invalidPlan)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		plan          []byte
		selector      string
		missingOutput bool
		canceled      bool
		code          e.Code
	}{
		{name: "missing GITHUB_OUTPUT", plan: planBytes, selector: selector, missingOutput: true, code: e.CodeUsage},
		{name: "missing action", plan: planBytes, code: e.CodeUsage},
		{name: "unknown action", plan: planBytes, selector: "not-an-admitted-action", code: e.CodePipelineDrift},
		{name: "invalid Plan", plan: invalidBytes, selector: selector, code: e.CodeInvalidCatalog},
		{name: "noncanonical Plan", plan: append(bytes.Clone(planBytes), '\n'), selector: selector, code: e.CodePipelineDrift},
		{name: "canceled Plan load", plan: planBytes, selector: selector, canceled: true, code: e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, existing := range []bool{false, true} {
				name := "absent output"
				if existing {
					name = "existing output"
				}
				t.Run(name, func(t *testing.T) {
					root := t.TempDir()
					t.Chdir(root)
					if err := os.WriteFile("plan.json", test.plan, 0o600); err != nil {
						t.Fatal(err)
					}
					output := filepath.Join(root, "github-output")
					const retained = "retained=value\n"
					if existing {
						if err := os.WriteFile(output, []byte(retained), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					t.Setenv("GITHUB_OUTPUT", output)
					if test.missingOutput {
						t.Setenv("GITHUB_OUTPUT", "")
					}
					t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(root, "summary.md"))
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					if test.canceled {
						cancel()
					}
					app := newApp()
					var diagnostic bytes.Buffer
					app.Writer, app.ErrWriter = &diagnostic, &diagnostic
					args := []string{"nvkr", "factory", "github", "describe-action", "--plan", "plan.json"}
					if test.selector != "" {
						args = append(args, "--action", test.selector)
					}
					err := app.RunContext(ctx, args)
					assertLifecycleCommandCode(t, err, test.code)
					if diagnostic.Len() != 0 {
						t.Fatal("rejected descriptor emitted a partial command result")
					}
					if test.canceled && !errors.Is(err, context.Canceled) {
						t.Fatal("descriptor lost the cancellation cause")
					}
					got, readErr := os.ReadFile(output)
					if existing {
						if readErr != nil || string(got) != retained {
							t.Fatalf("rejected descriptor changed existing output: %v", readErr)
						}
					} else if !os.IsNotExist(readErr) {
						t.Fatalf("rejected descriptor created output: %v", readErr)
					}
					for _, path := range []string{"summary.md", ".nvkr", ".github"} {
						if _, err := os.Lstat(path); !os.IsNotExist(err) {
							t.Fatalf("rejected descriptor touched %s: %v", path, err)
						}
					}
				})
			}
		})
	}
}

func TestDescribeActionCommandRejectsMalformedOutputSink(t *testing.T) {
	planBytes, selector, _ := githubActionDataCommandFixture(t)
	for _, kind := range []string{"symlink", "directory", "missing parent"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if err := os.WriteFile("plan.json", planBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			const retained = "retained=value\n"
			target := filepath.Join(root, "retained-output")
			if err := os.WriteFile(target, []byte(retained), 0o600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "github-output")
			switch kind {
			case "symlink":
				if err := os.Symlink(target, output); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(output, 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing parent":
				output = filepath.Join(root, "missing", "github-output")
			}
			before, err := os.Lstat(output)
			if kind == "missing parent" {
				if !os.IsNotExist(err) {
					t.Fatalf("fixture output unexpectedly exists: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			t.Setenv("GITHUB_OUTPUT", output)
			t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(root, "summary.md"))
			stdout, err := invokeLifecycleCLI(t, "factory", "github", "describe-action", "--plan", "plan.json", "--action", selector)
			assertLifecycleCommandCode(t, err, e.CodeInternal)
			if stdout != "" {
				t.Fatal("refused output sink emitted a partial descriptor on stdout")
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != retained {
				t.Fatalf("malformed sink changed another output file: %v", err)
			}
			after, err := os.Lstat(output)
			if before == nil {
				if !os.IsNotExist(err) {
					t.Fatalf("malformed sink created an output file: %v", err)
				}
				if _, err := os.Lstat(filepath.Dir(output)); !os.IsNotExist(err) {
					t.Fatalf("malformed sink created its missing parent: %v", err)
				}
			} else if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
				t.Fatalf("malformed sink was replaced or changed: %v", err)
			}
			for _, path := range []string{"summary.md", ".nvkr", ".github"} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("refused descriptor touched %s: %v", path, err)
				}
			}
		})
	}
}
