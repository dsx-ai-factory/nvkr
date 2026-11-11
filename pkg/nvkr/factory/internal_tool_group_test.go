// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubDiskHelperLifecycleFailures(t *testing.T) {
	openFailure := e.New(e.CodeEngineCapability, "probe admission failed")
	workFailure := e.New(e.CodeBuildFailed, "operation failed")
	closeFailure := errors.New("inspector cleanup failed")
	for _, phase := range []struct {
		name            string
		run             func(context.Context, func(context.Context) (func() error, error), func(context.Context) error) error
		closeBeforeWork bool
	}{
		{"probe", probeGitHubDiskHelper, true},
		{"completion", completeGitHubDiskWithHelper, false},
	} {
		for _, tc := range []struct {
			name                       string
			openErr, workErr, closeErr error
		}{
			{"success", nil, nil, nil},
			{"open failure", openFailure, nil, nil},
			{"close failure", nil, nil, closeFailure},
			{"operation failure", nil, workFailure, nil},
			{"operation and close failures", nil, workFailure, closeFailure},
		} {
			t.Run(phase.name+"/"+tc.name, func(t *testing.T) {
				var events []string
				err := phase.run(t.Context(), func(context.Context) (func() error, error) {
					events = append(events, "open")
					if tc.openErr != nil {
						return nil, tc.openErr
					}
					return func() error { events = append(events, "close"); return tc.closeErr }, nil
				}, func(context.Context) error { events = append(events, "work"); return tc.workErr })
				want := []string{"open"}
				workRan := tc.openErr == nil && (!phase.closeBeforeWork || tc.closeErr == nil)
				if tc.openErr == nil {
					if phase.closeBeforeWork {
						want = append(want, "close")
					}
					if workRan {
						want = append(want, "work")
					}
					if !phase.closeBeforeWork {
						want = append(want, "close")
					}
				}
				if !reflect.DeepEqual(events, want) {
					t.Fatalf("events=%v want=%v", events, want)
				}
				for _, cause := range []struct {
					err     error
					present bool
				}{
					{openFailure, tc.openErr != nil},
					{workFailure, workRan && tc.workErr != nil},
					{closeFailure, tc.openErr == nil && tc.closeErr != nil},
				} {
					if errors.Is(err, cause.err) != cause.present {
						t.Fatalf("error %v lost or invented cause %v", err, cause.err)
					}
				}
				switch {
				case tc.openErr != nil:
					assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				case workRan && tc.workErr != nil:
					assertFactoryErrorCode(t, err, e.CodeBuildFailed)
				case tc.closeErr != nil:
					assertFactoryErrorCode(t, err, e.CodeInternal)
				case err != nil:
					t.Fatal(err)
				}
			})
		}
	}
}

func TestInternalToolNativeHostedPlanGeneration(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			base := arcTestPlan(t, platform)
			definition, environment := base.Definition.Clone(), base.Environment.Clone()
			definition.InternalTools = []authored.InternalTool{{Name: "inspector", Platform: platform,
				Format: authored.OutputExecutable, Components: []string{"helper"}, Result: "helper", SBOMTool: "helper-scanner"}}
			profile := environment.DiskInspectionProfiles[platform]
			profile.HelperTool = "inspector"
			environment.DiskInspectionProfiles[platform] = profile
			scanner := base.CatalogSnapshot.Tools["cosign"]
			scanner.Platform, scanner.ExecutablePath, scanner.ExecutableDigest = platform, "syft", profile.ScannerDigest
			base.CatalogSnapshot.Tools["helper-scanner"] = scanner
			component := base.CatalogSnapshot.Components["image"]
			component.BuilderType, component.OutputType = catalog.BuilderOCIHermetic, "host-package"
			component.Disk, component.Packer, component.Materials, component.BuildRequirements = nil, nil, nil, nil
			component.BuildCommand, component.ArtifactPath, component.WriteDir = []string{"make", "helper"}, "bin/helper", "bin"
			base.CatalogSnapshot.Components["helper"] = component
			stack, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), definition, environment, stack, base.GeneratorVersion)
			if err != nil {
				t.Fatal(err)
			}
			plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
				t.Fatal(err)
			}
			stages, jobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			groups, helperNodes, fetches := 0, 0, 0
			for _, stage := range stages {
				if stage.DiskOutput != "" {
					groups++
				}
				for _, node := range stage.Nodes {
					if node.BuildTarget != plan.InternalTools[0].BuildTarget {
						continue
					}
					if node.Kind == NodeFetch {
						fetches++
						if stage.DiskOutput != "" || node.RunsOn != githubBootstrapRunner(githubARCRunner(platform)) {
							t.Fatal("helper source fetch entered native disk group")
						}
						if _, err := admitExecutionNode(t.Context(), plan, node.ID); err != nil {
							t.Fatal(err)
						}
					} else {
						helperNodes++
						if stage.DiskOutput != "node-image" || node.RunsOn != githubARCRunner(platform) || jobs[node.ID] != stage.ID {
							t.Fatal("helper evidence escaped original native owner")
						}
						_, err := admitOperation(t.Context(), plan, node.ID)
						assertFactoryErrorCode(t, err, e.CodeEngineCapability)
					}
				}
			}
			if groups != 1 || helperNodes < 3 || fetches == 0 {
				t.Fatalf("groups=%d helper operations=%d fetches=%d", groups, helperNodes, fetches)
			}
			if !reflect.DeepEqual(plan.LifecycleOutputs, base.LifecycleOutputs) || !reflect.DeepEqual(plan.Routes, base.Routes) {
				t.Fatal("helper changed consumer delivery membership")
			}
		})
	}
}

func TestInternalToolNativeGroupKeepsFetchOutside(t *testing.T) {
	_, plan := internalToolPlanFixture(t)
	plan.Environment.GitHubARC = "native-kvm-v1"
	tool := plan.InternalTools[0]
	output, err := internalToolDiskOutput(plan, tool)
	if err != nil || output != "node-image" {
		t.Fatalf("group=%s error=%v", output, err)
	}
	fetches, grouped := 0, 0
	for _, node := range plan.Graph.Nodes {
		if node.BuildTarget != tool.BuildTarget {
			continue
		}
		want := output
		if node.Kind == NodeFetch {
			want = ""
			fetches++
		} else {
			grouped++
		}
		if got := githubDiskOutput(plan, node); got != want {
			t.Fatalf("%s group=%q want=%q", node.ID, got, want)
		}
	}
	if fetches == 0 || grouped < 3 {
		t.Fatal("fixture did not cover source fetch, production and both evidence operations")
	}
	nodes, _ := planIndex(plan)
	duplicate := nodes["node-image/sbom"]
	duplicate.ID = "second-inspection"
	plan.Graph.Nodes = append(plan.Graph.Nodes, duplicate)
	_, err = internalToolDiskOutput(plan, tool)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
}

func TestInternalToolSBOMExecutesBeforeDiskCompletion(t *testing.T) {
	_, plan := internalToolPlanFixture(t)
	nodes, _ := planIndex(plan)
	stage := githubStage{DiskOutput: "node-image", Nodes: []Node{
		nodes["internal/inspector/terminal-observe"], nodes["internal/inspector/sbom"], nodes["node-image/sbom"],
	}}
	owner := &LocalRunResult{}
	var seen []string
	completed := false
	err := executeGitHubDiskNodes(t.Context(), plan, stage, owner, GitHubStepOptions{}, func(_ context.Context, node Node) error {
		seen = append(seen, node.ID)
		return nil
	}, func(context.Context) error { completed = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !completed || !reflect.DeepEqual(seen, []string{"internal/inspector/terminal-observe", "internal/inspector/sbom"}) {
		t.Fatalf("dispatch=%v completed=%v", seen, completed)
	}
}
