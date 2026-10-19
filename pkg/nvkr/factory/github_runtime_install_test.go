// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func stageGitHubRuntimeInputs(t *testing.T, root string, plan *Plan) *GeneratedBundle {
	t.Helper()
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := ManifestFor(bundle)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, githubRuntimeInputDirectory)
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"plan.json": bundle.Files[bundle.PlanPath], "action-data.json": bundle.Files[githubActionDataPathFor(plan)], "manifest.json": manifest} {
		if err := os.WriteFile(filepath.Join(directory, name), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return bundle
}

func freshGitHubRuntimePins(t *testing.T, before *Plan) *Plan {
	t.Helper()
	return recompileGitHubStabilityPlan(t, before, func(in *githubStabilityInputs) {
		for name, component := range in.catalog.Components {
			if component.FromSource {
				prior := component.Commit
				component.Commit = nextGitHubStabilityPin(t, component.Commit)
				component.TreeHash = nextGitHubStabilityPin(t, component.TreeHash)
				component.SourceDate++
				for key, value := range component.VersionStamp {
					component.VersionStamp[key] = strings.ReplaceAll(value, prior, component.Commit)
				}
				in.catalog.Components[name] = component
			}
		}
	})
}

func TestInstallGitHubRuntimeInputsUsesGeneratedFileOwner(t *testing.T) {
	before, baseline, original := runtimeInputsFixture(t)
	for _, refreshed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "updated source pins"}[refreshed], func(t *testing.T) {
			root := copyGitHubRuntimeRoot(t, original)
			fresh := before
			if refreshed {
				fresh = freshGitHubRuntimePins(t, before)
			}
			bundle := stageGitHubRuntimeInputs(t, root, fresh)
			if refreshed && bundle.PlanDigest == baseline.PlanDigest {
				t.Fatal("fixture did not refresh the Plan")
			}
			staged := githubRuntimeTree(t, filepath.Join(root, githubRuntimeInputDirectory))
			if err := InstallGitHubRuntimeInputs(t.Context(), before, root); err != nil {
				t.Fatal(err)
			}
			if err := CheckGitHubRuntimeInputs(t.Context(), fresh, root); err != nil {
				t.Fatal(err)
			}
			for name, prior := range baseline.Files {
				if name == baseline.PlanPath || name == githubActionDataPathFor(before) {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || !bytes.Equal(raw, prior) {
					t.Fatalf("runtime install changed static member %s: %v", name, err)
				}
			}
			if !reflect.DeepEqual(staged, githubRuntimeTree(t, filepath.Join(root, githubRuntimeInputDirectory))) {
				t.Fatal("runtime installation moved or changed the authenticated staging files")
			}
		})
	}
}

func TestInstallGitHubRuntimeInputsRejectsIncompleteOrMixedHandoff(t *testing.T) {
	before, baseline, original := runtimeInputsFixture(t)
	fresh := freshGitHubRuntimePins(t, before)
	stageGitHubRuntimeInputs(t, original, fresh)
	// Old bytes exercise every same-Run binding. Missing/type admission is a
	// common directory reader; corrupt Plan bytes additionally cover decoding.
	for _, test := range []struct{ member, damage string }{
		{"plan.json", "old bytes"}, {"action-data.json", "old bytes"}, {"manifest.json", "old bytes"},
		{"plan.json", "corrupt"}, {"action-data.json", "missing"}, {"manifest.json", "symlink"},
	} {
		member, damage := test.member, test.damage
		t.Run(member+"/"+damage, func(t *testing.T) {
			root := copyGitHubRuntimeRoot(t, original)
			filename := filepath.Join(root, githubRuntimeInputDirectory, member)
			switch damage {
			case "missing":
				if err := os.Remove(filename); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(filename, []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "member")
				if err := os.Rename(filename, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filename); err != nil {
					t.Fatal(err)
				}
			case "old bytes":
				name := baseline.PlanPath
				switch member {
				case "action-data.json":
					name = githubActionDataPathFor(before)
				case "manifest.json":
					name = baseline.ManifestPath
				}
				raw, err := os.ReadFile(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, raw, 0644); err != nil {
					t.Fatal(err)
				}
			}
			prior := githubRuntimeTree(t, root)
			assertFactoryErrorCode(t, InstallGitHubRuntimeInputs(t.Context(), before, root), e.CodePipelineDrift)
			if !reflect.DeepEqual(prior, githubRuntimeTree(t, root)) {
				t.Fatal("rejected handoff changed existing files")
			}
		})
	}
}

func TestInstallGitHubRuntimeInputsRejectsChangedAuthority(t *testing.T) {
	before, baseline, original := runtimeInputsFixture(t)
	updated := freshGitHubRuntimePins(t, before)
	stageGitHubRuntimeInputs(t, original, updated)
	for _, kind := range []string{"Environment", "namespace", "input path", "workflow", "authored input", "baseline drift", "extra staged member", "oversized Plan"} {
		t.Run(kind, func(t *testing.T) {
			root := copyGitHubRuntimeRoot(t, original)
			switch kind {
			case "Environment", "namespace", "workflow":
				fresh := recompileGitHubStabilityPlan(t, before, func(in *githubStabilityInputs) {
					switch kind {
					case "Environment":
						in.environment.Publication.RepositoryPrefix = "ghcr.io/example/other-tools"
					case "namespace":
						in.environment.GitHubBundle = "other-runtime"
					case "workflow":
						pin := in.catalog.Actions["upload-artifact"]
						pin.SHA = nextGitHubStabilityPin(t, pin.SHA)
						in.catalog.Actions["upload-artifact"] = pin
					}
				})
				stageGitHubRuntimeInputs(t, root, fresh)
			case "input path":
				fresh := *updated
				fresh.DefinitionPath = "other/definition.yaml"
				stageGitHubRuntimeInputs(t, root, &fresh)
			case "authored input":
				definition := before.Definition.Clone()
				definition.Metadata.Name = "other-project"
				writeRuntimeTrustedInput(t, root, before.DefinitionPath, definition)
			case "baseline drift":
				if err := os.WriteFile(filepath.Join(root, baseline.WorkflowPath), []byte("changed"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "extra staged member":
				if err := os.WriteFile(filepath.Join(root, githubRuntimeInputDirectory, "extra.json"), []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			case "oversized Plan":
				if err := os.Truncate(filepath.Join(root, githubRuntimeInputDirectory, "plan.json"), MaxPlanBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			prior := githubRuntimeTree(t, root)
			assertFactoryErrorCode(t, InstallGitHubRuntimeInputs(t.Context(), before, root), e.CodePipelineDrift)
			if !reflect.DeepEqual(prior, githubRuntimeTree(t, root)) {
				t.Fatal("rejected authority change modified the checkout")
			}
		})
	}
}

func TestInstallGitHubRuntimeInputsRejectsLinkedStagingDirectory(t *testing.T) {
	plan, baseline, root := runtimeInputsFixture(t)
	stageGitHubRuntimeInputs(t, root, plan)
	name := filepath.Join(root, githubRuntimeInputDirectory)
	target := filepath.Join(t.TempDir(), "staging")
	if err := os.Rename(name, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
	prior := githubRuntimeTree(t, target)
	assertFactoryErrorCode(t, InstallGitHubRuntimeInputs(t.Context(), plan, root), e.CodePipelineDrift)
	if err := CheckGeneratedBundle(t.Context(), root, baseline); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prior, githubRuntimeTree(t, target)) {
		t.Fatal("linked staging target changed")
	}
}

func TestInstallGitHubRuntimeInputsAdmitsBeforeIO(t *testing.T) {
	baseline := githubRuntimePlanFixture(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name   string
		ctx    context.Context
		change func(*Plan)
		code   e.Code
	}{
		{"missing context", nil, func(*Plan) {}, e.CodeUsage},
		{"canceled", canceled, func(*Plan) {}, e.CodeTimeout},
		{"not opted in", t.Context(), func(p *Plan) { p.Environment.GitHubRuntimePlan = false }, e.CodeEngineCapability},
		{"default namespace", t.Context(), func(p *Plan) { p.Environment.GitHubBundle = "" }, e.CodeEngineCapability},
		{"development", t.Context(), func(p *Plan) { p.Environment.GitHubDevelopment = true }, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, root := *baseline, t.TempDir()
			test.change(&plan)
			assertFactoryErrorCode(t, InstallGitHubRuntimeInputs(test.ctx, &plan, root), test.code)
			if len(githubRuntimeTree(t, root)) != 0 {
				t.Fatal("runtime installer wrote before capability admission")
			}
		})
	}
}
