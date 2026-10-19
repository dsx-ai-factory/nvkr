// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCatalogMaintenanceRejectsBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"refresh timeout", []string{"refresh", "--config", "absent", "--timeout", "0s"}, e.CodeUsage},
		{"refresh positional", []string{"refresh", "--config", "absent", "extra"}, e.CodeUsage},
		{"refresh missing input", []string{"refresh", "--config", "absent"}, e.CodeInvalidCatalog},
		{"workflow engine", []string{"update-ci", "--engine", "gitlab"}, e.CodeEngineCapability},
		{"workflow timeout", []string{"update-ci", "--timeout", "-1s"}, e.CodeUsage},
		{"workflow positional", []string{"update-ci", "extra"}, e.CodeUsage},
		{"workflow schedule", []string{"update-ci", "--schedule", "* * * * *"}, e.CodeUsage},
		{"workflow source without secret", []string{"update-ci", "--source-checkout", "owner/repo"}, e.CodeUsage},
		{"workflow App secrets without a key", []string{"update-ci", "--app-secrets", "BOT_CLIENT"}, e.CodeUsage},
		{"workflow App secrets with an empty key", []string{"update-ci", "--app-secrets", "BOT_CLIENT:"}, e.CodeUsage},
		{"workflow no policies", []string{"update-ci"}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runCatalogCommand(t, append([]string{"catalog"}, tc.args...)...)
			if got := cliErrorCode(err); got != tc.code {
				t.Fatalf("code %s, want %s: %v", got, tc.code, err)
			}
			if output != "" {
				t.Fatalf("failure emitted output: %q", output)
			}
		})
	}
}

func TestCatalogMaintenanceRejectsTrailingPolicyBeforeWork(t *testing.T) {
	const valid = `apiVersion: nvkr.dev/catalog-maintenance/v1
name: test
catalog: unopened-catalog
sources: [source]
definition: unopened-definition.yaml
environment: unopened-environment.yaml
validation: static
`
	for _, command := range []string{"refresh", "update-ci"} {
		policy := "maintenance.yaml"
		args := []string{"catalog", command, "--config", policy}
		if command == "update-ci" {
			policy = "inputs/policies/test-catalog-maintenance.yaml"
			args = []string{"catalog", command}
		}
		for _, trailing := range []struct {
			name string
			raw  string
		}{
			{"unknown", "---\nunknownPolicy: true\n"},
			{"conflicting", "---\n" + strings.Replace(valid, "name: test", "name: other", 1)},
			{"malformed", "---\ninvalid: [\n"},
		} {
			t.Run(command+"/"+trailing.name, func(t *testing.T) {
				t.Chdir(t.TempDir())
				raw := valid + trailing.raw
				if err := os.MkdirAll(filepath.Dir(policy), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(policy, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
				output, err := runCatalogCommand(t, args...)
				if cliErrorCode(err) != e.CodeInvalidCatalog || !strings.Contains(err.Error(), "decoding maintenance config") {
					t.Fatalf("expected policy decoding rejection before opening selected inputs: %v", err)
				}
				if output != "" {
					t.Fatalf("invalid policy emitted output: %q", output)
				}
				entries, err := os.ReadDir(".")
				if err != nil || len(entries) != 1 || entries[0].Name() != strings.Split(policy, "/")[0] {
					t.Fatalf("invalid policy changed workspace: entries=%v error=%v", entries, err)
				}
				retained, err := os.ReadFile(policy)
				if err != nil || string(retained) != raw {
					t.Fatalf("invalid policy file changed: error=%v", err)
				}
			})
		}
	}
}

// A binary whose stamp differs from the committed Plans would render a workflow
// that can never pass its baseline drift check, so update-ci refuses before output.
func TestCatalogUpdateCIRefusesAGeneratorStampThatDiffersFromTheCommittedPlan(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	oldVersion, oldCommit := version, commit
	version, commit = "mismatched", "cafef00d"
	t.Cleanup(func() { version, commit = oldVersion, oldCommit })
	output, err := runCatalogCommand(t, "catalog", "update-ci", "--source-checkout", "dsx-ai-factory/nvke:NKE_SOURCE_TOKEN")
	if got := cliErrorCode(err); got != e.CodePipelineDrift {
		t.Fatalf("code %s, want %s: %v", got, e.CodePipelineDrift, err)
	}
	if want := 51; e.ExitCode(err) != want {
		t.Fatalf("exit code %d, want %d", e.ExitCode(err), want)
	}
	if output != "" {
		t.Fatalf("generator drift emitted output: %q", output)
	}
}

// transportFixture writes a minimal maintenance repository whose catalog selects one root on example/nvke
// and one role on a repository that shares that URL prefix.
func transportFixture(t *testing.T, siblingRepo string) {
	t.Helper()
	t.Chdir(t.TempDir())
	image := "busybox@sha256:" + strings.Repeat("a", 64)
	sources := map[string]catalog.Source{
		"root": {Repo: "https://github.com/example/nvke", Constraint: "1.x", TagPrefix: "release/", ToolchainImage: image},
		"role": {Repo: siblingRepo, Constraint: "1.x", TagPrefix: "release/", ToolchainImage: image},
	}
	registry := map[string]any{"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "sources": sources, "components": map[string]any{
		"tool": map[string]any{"source": "root", "fromSource": true, "artifactPath": "bin/tool", "writeDir": "bin", "buildCommand": []string{"sh", "build.sh"}, "outputType": "host-package", "platforms": []string{"linux/amd64"}}}}
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"catalog/registry.yaml": string(raw),
		"definition.yaml":       "apiVersion: nvkr.dev/definition/v5\nkind: Definition\nmetadata: {name: fixture}\ncatalog: {base: catalog}\noutputs:\n- {name: tools, format: executable, platform: linux/amd64, components: [tool]}\n",
		"environment.yaml":      "apiVersion: nvkr.dev/environment/v1\nkind: Environment\nmetadata: {name: fixture}\nutilityContainer: " + image + "\nutilityPlatform: linux/amd64\n",
		"maintenance.yaml": "apiVersion: nvkr.dev/catalog-maintenance/v1\nname: fixture\ncatalog: catalog\nsources: [root, role]\ndefinition: definition.yaml\n" +
			"environment: environment.yaml\nvalidation: static\n",
	}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The workflow passes --source-repository so the run-time admission matches the one made at render time. Each
// case fails if the flag stops reaching the owner: an empty value would skip admission and fail differently.
func TestCatalogRefreshAdmitsTheSourceRepositoryBeforeAnyWork(t *testing.T) {
	for _, tc := range []struct {
		name, sibling string
		args          []string
		code          e.Code
	}{
		{"invalid spelling", "https://github.com/example/other", []string{"--source-repository", "example/nvke.git"}, e.CodeUsage},
		{"not an owner and repository", "https://github.com/example/other", []string{"--source-repository", "nvke"}, e.CodeUsage},
		{"repository that transports no selected root", "https://github.com/example/other", []string{"--source-repository", "example/elsewhere"}, e.CodeUsage},
		{"prefix collision", "https://github.com/example/nvke-tools", []string{"--source-repository", "example/nvke"}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transportFixture(t, tc.sibling)
			before, err := os.ReadFile("catalog/registry.yaml")
			if err != nil {
				t.Fatal(err)
			}
			output, err := runCatalogCommand(t, append([]string{"catalog", "refresh", "--config", "maintenance.yaml", "--check", "--json"}, tc.args...)...)
			if got := cliErrorCode(err); got != tc.code {
				t.Fatalf("code %s, want %s: %v", got, tc.code, err)
			}
			if output != "" {
				t.Fatalf("failure emitted output: %q", output)
			}
			if after, readErr := os.ReadFile("catalog/registry.yaml"); readErr != nil || string(after) != string(before) {
				t.Fatalf("a rejected admission changed the catalog: %v", readErr)
			}
			if _, statErr := os.Stat("catalog/pins.lock.yaml"); statErr == nil {
				t.Fatal("a rejected admission wrote a lock")
			}
		})
	}
}
