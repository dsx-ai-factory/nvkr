// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestRunLocalDefaultEnvironmentAndExplicitOverride(t *testing.T) {
	lifecycleCommandRepo(t)
	repository, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"inputs/catalogs/tools/operator-arm64", "inputs/catalogs/shared"} {
		if err := os.CopyFS(filepath.Join(root, name), os.DirFS(filepath.Join(repository, name))); err != nil {
			t.Fatal(err)
		}
	}
	environmentBytes, err := os.ReadFile(filepath.Join(repository, "inputs/environments/local-conformance-tools-arm64.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "environment.yaml"), environmentBytes, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	destination := filepath.Join(root, "definition.yaml")
	authored, err := lifecycle.InitDefinition(t.Context(), lifecycle.DefinitionInit{Root: root, Destination: destination, Name: "starter", Catalog: "inputs/catalogs/tools/operator-arm64", Platform: "linux/arm64", Components: []string{"helm", "kubectl"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := lifecycle.RenderDefinition(authored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, raw, 0600); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(root, destination)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, environment string
		flags             []string
		code              e.Code
	}{
		{name: "built-in"},
		{name: "captured built-in", flags: []string{"--capture-inputs-root", root}},
		{name: "empty capture root", flags: []string{"--capture-inputs-root="}, code: e.CodeUsage},
		{name: "explicit", environment: "environment.yaml"},
		{name: "no implicit publication", flags: []string{"--publish"}, code: e.CodeEngineCapability},
		{name: "outside default root", flags: []string{"--definition", "../outside.yaml"}, code: e.CodeUsage},
		{name: "explicit empty", flags: []string{"--environment="}, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			called := false
			execute := func(_ context.Context, prepared *lifecycle.PreparedRun, _ lifecycle.LocalOptions) (*lifecycle.Run, error) {
				called = true
				environment, err := factory.DefaultLocalEnvironment()
				if tc.environment != "" {
					environment, err = lifecycle.LoadEnvironment(t.Context(), tc.environment)
				}
				if err != nil {
					t.Fatal(err)
				}
				digest, err := environment.Digest()
				if err != nil {
					t.Fatal(err)
				}
				if prepared.Description().EnvironmentDigest != digest {
					t.Fatal("wrong selected policy")
				}
				return completedPresentationRun(prepared), nil
			}
			app := localPresentationTestApp(execute, &output, defaultRunPresentationServices())
			args := []string{"nvkr", "run", "local", "--definition", relative, "--attempt", "starter", "--json"}
			if tc.environment != "" {
				args = append(args, "--environment", tc.environment)
			}
			err := app.RunContext(t.Context(), append(args, tc.flags...))
			if tc.code != "" {
				assertLifecycleCommandCode(t, err, tc.code)
				var result localRunResult
				if err := json.Unmarshal(output.Bytes(), &result); err != nil {
					t.Fatalf("missing JSON refusal: %v: %s", err, output.String())
				}
				if result.FailureCode != string(tc.code) || result.Status != "not-started" || result.RecordAvailable {
					t.Fatalf("wrong refusal result: %+v", result)
				}

				if called {
					t.Fatal("refused input executed")
				}
				return
			}
			if err != nil || !called {
				t.Fatalf("local default routing: called=%v err=%v", called, err)
			}
		})
	}
}
