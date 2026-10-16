// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func githubRuntimePlanFixture(t *testing.T) *Plan {
	t.Helper()
	plan := namedGitHubTestPlan(t, "runtime-project")
	plan.Environment.GitHubRuntimePlan = true
	var err error
	plan.EnvironmentDigest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func githubRuntimeTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		files[name] = string(raw)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func writeGitHubRuntimeBaseline(t *testing.T, root string, plan *Plan) *GeneratedBundle {
	t.Helper()
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestPrepareGitHubRuntimeBundleUpdatesPinsWithoutChangingStaticContract(t *testing.T) {
	for _, check := range []bool{true, false} {
		t.Run(map[bool]string{true: "check", false: "write"}[check], func(t *testing.T) {
			root := t.TempDir()
			before := githubRuntimePlanFixture(t)
			baseline := writeGitHubRuntimeBaseline(t, root, before)
			after := recompileGitHubStabilityPlan(t, before, func(inputs *githubStabilityInputs) {
				for name, component := range inputs.catalog.Components {
					if component.FromSource {
						previous := component.Commit
						component.Commit = nextGitHubStabilityPin(t, component.Commit)
						component.TreeHash = nextGitHubStabilityPin(t, component.TreeHash)
						component.SourceDate++
						for key, value := range component.VersionStamp {
							component.VersionStamp[key] = strings.ReplaceAll(value, previous, component.Commit)
						}
						inputs.catalog.Components[name] = component
					}
				}
			})
			fresh, err := RenderGitHub(t.Context(), after)
			if err != nil || fresh.PlanDigest == baseline.PlanDigest {
				t.Fatal("pin update did not create a distinct compiled Plan", err)
			}
			prior := githubRuntimeTree(t, root)
			if err := PrepareGitHubRuntimeBundle(t.Context(), after, root, check); err != nil {
				t.Fatal(err)
			}
			if check {
				if !reflect.DeepEqual(prior, githubRuntimeTree(t, root)) {
					t.Fatal("check mode changed committed bundle bytes")
				}
				if err := CheckGeneratedBundle(t.Context(), root, baseline); err != nil {
					t.Fatal(err)
				}
			} else if err := CheckGeneratedBundle(t.Context(), root, fresh); err != nil {
				t.Fatal("prepared bundle does not bind the fresh Plan", err)
			}
		})
	}
}

func TestPrepareGitHubRuntimeBundleRejectsChangedAuthorityWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*githubStabilityInputs)
	}{
		{"Environment policy", func(in *githubStabilityInputs) {
			in.environment.Publication.RepositoryPrefix = "ghcr.io/example/other-tools"
		}},
		{"runner policy", func(in *githubStabilityInputs) { in.environment.RunsOn = "ubuntu-22.04" }},
		{"action template", func(in *githubStabilityInputs) {
			pin := in.catalog.Actions["setup-go"]
			pin.SHA = nextGitHubStabilityPin(t, pin.SHA)
			in.catalog.Actions["setup-go"] = pin
		}},
		{"workflow action", func(in *githubStabilityInputs) {
			pin := in.catalog.Actions["upload-artifact"]
			pin.SHA = nextGitHubStabilityPin(t, pin.SHA)
			in.catalog.Actions["upload-artifact"] = pin
		}},
		{"topology and membership", func(in *githubStabilityInputs) {
			output := in.definition.Outputs[0]
			output.Name = "worker-two"
			in.definition.Outputs = append(in.definition.Outputs, output)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, baseline := t.TempDir(), githubRuntimePlanFixture(t)
			writeGitHubRuntimeBaseline(t, root, baseline)
			fresh := recompileGitHubStabilityPlan(t, baseline, test.change)
			prior := githubRuntimeTree(t, root)
			assertFactoryErrorCode(t, PrepareGitHubRuntimeBundle(t.Context(), fresh, root, false), e.CodePipelineDrift)
			if !reflect.DeepEqual(prior, githubRuntimeTree(t, root)) {
				t.Fatal("rejected runtime bundle changed existing files")
			}
		})
	}
}

func TestPrepareGitHubRuntimeBundleRejectsBaselineDriftWithoutRepair(t *testing.T) {
	for _, kind := range []string{"workflow", "action", "data", "manifest", "extra member", "input path"} {
		t.Run(kind, func(t *testing.T) {
			root, plan := t.TempDir(), githubRuntimePlanFixture(t)
			baseline := writeGitHubRuntimeBaseline(t, root, plan)
			member := baseline.WorkflowPath
			switch kind {
			case "action":
				for _, name := range sortedFileNames(baseline.Files) {
					if strings.HasSuffix(name, "/action.yml") {
						member = name
						break
					}
				}
			case "data":
				member = githubActionDataPathFor(plan)
			case "manifest":
				member = baseline.ManifestPath
			case "extra member":
				member = filepath.ToSlash(filepath.Join(filepath.Dir(baseline.PlanPath), "unowned.json"))
			case "input path":
				plan.DefinitionPath = "other/definition.yaml"
			}
			if kind != "input path" {
				if err := os.WriteFile(filepath.Join(root, member), []byte("unreviewed change"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			prior := githubRuntimeTree(t, root)
			assertFactoryErrorCode(t, PrepareGitHubRuntimeBundle(t.Context(), plan, root, false), e.CodePipelineDrift)
			if !reflect.DeepEqual(prior, githubRuntimeTree(t, root)) {
				t.Fatal("runtime admission repaired or overwrote rejected baseline drift")
			}
		})
	}
}

func TestPrepareGitHubRuntimeBundleRequiresExplicitAdmissionBeforeIO(t *testing.T) {
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
			root := t.TempDir()
			plan := githubRuntimePlanFixture(t)
			test.change(plan)
			assertFactoryErrorCode(t, PrepareGitHubRuntimeBundle(test.ctx, plan, root, false), test.code)
			if len(githubRuntimeTree(t, root)) != 0 {
				t.Fatal("unsupported runtime preparation performed output work")
			}
		})
	}
}
