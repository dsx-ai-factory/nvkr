// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func writeRuntimeTrustedInput(t *testing.T, root, name string, value any) {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, raw, 0644); err != nil {
		t.Fatal(err)
	}
}

func runtimeInputsFixture(t *testing.T) (*Plan, *GeneratedBundle, string) {
	t.Helper()
	plan, root := githubRuntimePlanFixture(t), t.TempDir()
	writeRuntimeTrustedInput(t, root, plan.DefinitionPath, plan.Definition)
	writeRuntimeTrustedInput(t, root, plan.EnvironmentPath, plan.Environment)
	bundle := writeGitHubRuntimeBaseline(t, root, plan)
	return plan, bundle, root
}

func TestCheckGitHubRuntimeInputsChecksDeliveredBundleWithoutCatalogAccess(t *testing.T) {
	plan, _, root := runtimeInputsFixture(t)
	// Only the two authored documents and generated bundle exist. Catalog
	// providers are intentionally absent: this guard must not compile or fetch.
	prior := githubRuntimeTree(t, root)
	if err := CheckGitHubRuntimeInputs(t.Context(), plan, root); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prior, githubRuntimeTree(t, root)) {
		t.Fatal("runtime input check modified the checkout")
	}
}

func TestCheckGitHubRuntimeInputsRejectsIncompleteOrChangedBundle(t *testing.T) {
	for _, kind := range []string{"Plan", "action data", "manifest", "workflow", "action"} {
		for _, damage := range []string{"missing", "corrupt", "symlink"} {
			t.Run(kind+"/"+damage, func(t *testing.T) {
				plan, bundle, root := runtimeInputsFixture(t)
				member := bundle.PlanPath
				switch kind {
				case "action data":
					member = githubActionDataPathFor(plan)
				case "manifest":
					member = bundle.ManifestPath
				case "workflow":
					member = bundle.WorkflowPath
				case "action":
					_, member = githubTestBootstrap(t, bundle)
				}
				filename := filepath.Join(root, member)
				if damage == "corrupt" {
					if err := os.WriteFile(filename, []byte("corrupt delivered member"), 0644); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Remove(filename); err != nil {
						t.Fatal(err)
					}
					if damage == "symlink" {
						// Even a link to exact expected bytes is not an admitted
						// regular delivered member.
						target := filepath.Join(t.TempDir(), "member")
						raw := bundle.Files[member]
						if member == bundle.ManifestPath {
							_, manifest, err := ManifestFor(bundle)
							if err != nil {
								t.Fatal(err)
							}
							raw = manifest
						}
						if err := os.WriteFile(target, raw, 0644); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(target, filename); err != nil {
							t.Fatal(err)
						}
					}
				}
				prior := githubRuntimeTree(t, root)
				assertFactoryErrorCode(t, CheckGitHubRuntimeInputs(t.Context(), plan, root), e.CodePipelineDrift)
				if !reflect.DeepEqual(prior, githubRuntimeTree(t, root)) {
					t.Fatal("rejected delivery was silently repaired")
				}
			})
		}
	}
}

func TestCheckGitHubRuntimeInputsBindsCurrentAuthoredDocuments(t *testing.T) {
	for _, kind := range []string{"Definition", "Environment", "missing Definition", "missing Environment", "symlink parent"} {
		t.Run(kind, func(t *testing.T) {
			plan, _, root := runtimeInputsFixture(t)
			want := e.CodePipelineDrift
			switch kind {
			case "Definition":
				changed := plan.Definition.Clone()
				changed.Metadata.Name = "other-project"
				writeRuntimeTrustedInput(t, root, plan.DefinitionPath, changed)
			case "Environment":
				changed := plan.Environment.Clone()
				changed.Publication.RepositoryPrefix = "ghcr.io/example/other-tools"
				writeRuntimeTrustedInput(t, root, plan.EnvironmentPath, changed)
			case "missing Definition", "missing Environment":
				name := plan.DefinitionPath
				if kind == "missing Environment" {
					name = plan.EnvironmentPath
				}
				if err := os.Remove(filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
				want = e.CodeInvalidCatalog
			case "symlink parent":
				original := filepath.Join(root, filepath.Dir(plan.DefinitionPath))
				target := filepath.Join(t.TempDir(), "authored")
				if err := os.Rename(original, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, original); err != nil {
					t.Fatal(err)
				}
			}
			assertFactoryErrorCode(t, CheckGitHubRuntimeInputs(t.Context(), plan, root), want)
		})
	}
}

func TestCheckGitHubRuntimeInputsRejectsUnsupportedBeforeReading(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name   string
		ctx    context.Context
		change func(*Plan)
		want   e.Code
	}{
		{"missing context", nil, func(*Plan) {}, e.CodeUsage},
		{"canceled", ctx, func(*Plan) {}, e.CodeTimeout},
		{"legacy", t.Context(), func(p *Plan) { p.Environment.GitHubRuntimePlan = false }, e.CodeEngineCapability},
		{"default namespace", t.Context(), func(p *Plan) { p.Environment.GitHubBundle = "" }, e.CodeEngineCapability},
		{"development", t.Context(), func(p *Plan) { p.Environment.GitHubDevelopment = true }, e.CodeEngineCapability},
		{"noncanonical path", t.Context(), func(p *Plan) { p.DefinitionPath = "examples/../definition.yaml" }, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, root := githubRuntimePlanFixture(t), t.TempDir()
			test.change(plan)
			assertFactoryErrorCode(t, CheckGitHubRuntimeInputs(test.ctx, plan, root), test.want)
			if len(githubRuntimeTree(t, root)) != 0 {
				t.Fatal("runtime input check wrote before admission")
			}
		})
	}
}

func TestGitHubRuntimeInputPreparationRejectsPartialDeliveryBeforeMovingFiles(t *testing.T) {
	for _, member := range []string{"binary", "Plan", "action data", "manifest"} {
		for _, damage := range []string{"missing", "symlink"} {
			t.Run(member+"/"+damage, func(t *testing.T) {
				plan, bundle, root := runtimeInputsFixture(t)
				members := map[string]string{"binary": "nvkr-exec", "Plan": bundle.PlanPath, "action data": githubActionDataPathFor(plan), "manifest": bundle.ManifestPath}
				for label, name := range members {
					raw := bundle.Files[name]
					switch label {
					case "binary":
						raw = []byte("#!/bin/sh\n: > runtime-check-called\n")
					case "manifest":
						_, manifest, err := ManifestFor(bundle)
						if err != nil {
							t.Fatal(err)
						}
						raw = manifest
					}
					filename := filepath.Join(root, ".nvkr/runtime-runner", name)
					if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
						t.Fatal(err)
					}
					if label == member && damage == "missing" {
						continue
					}
					if label == member {
						target := filepath.Join(t.TempDir(), "delivered-member")
						if err := os.WriteFile(target, raw, 0644); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(target, filename); err != nil {
							t.Fatal(err)
						}
					} else if err := os.WriteFile(filename, raw, 0644); err != nil {
						t.Fatal(err)
					}
				}
				prior := githubRuntimeTree(t, root)
				command := exec.CommandContext(t.Context(), "bash", "-c", prepareHelperRunFor(plan))
				command.Dir = root
				output, err := command.CombinedOutput()
				if err == nil || command.ProcessState == nil || command.ProcessState.ExitCode() != e.ExitCode(e.New(e.CodePipelineDrift, "")) {
					t.Fatalf("partial delivery did not fail with pipeline drift before execution: %v %s", err, output)
				}
				if !reflect.DeepEqual(prior, githubRuntimeTree(t, root)) {
					t.Fatal("partial delivery moved files or executed the helper; stale checkout fallback is possible")
				}
			})
		}
	}
}
