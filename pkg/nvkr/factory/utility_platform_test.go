// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func nativeUtilityFixture(t *testing.T) (*Plan, *Plan) {
	t.Helper()
	base := referenceTestPlan(t)
	def, env, cat := base.Definition, base.Environment, base.CatalogSnapshot
	env.RunsOn = ""
	def.Outputs[1].Platform = "linux/arm64"
	for i := range def.Outputs {
		def.Outputs[i].Components = []string{"containerd"}
	}
	def.SBOMTool = "syft"
	pins := []catalog.ToolPin{}
	for _, arch := range []string{"amd64", "arm64"} {
		pins = append(pins, catalog.ToolPin{Format: "raw", Platform: "linux/" + arch, URL: "https://example.test/syft-" + arch, ExecutablePath: "syft", ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("a", 64)})
	}
	pins[1].ArchiveDigest, pins[1].ExecutableDigest = "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("b", 64)
	cat.Tools["syft"] = pins[0]
	stack, err := catalog.ApplyStack(cat)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Compile(t.Context(), def, env, stack, "dev (unknown)")
	if err != nil {
		t.Fatal(err)
	}
	delete(cat.Tools, "syft")
	cat.Sources["syft"] = catalog.Source{Repo: "https://example.test/syft", Tool: &catalog.ToolSource{Format: "raw", URL: "https://example.test/syft-{{.Arch}}", ExecutablePath: "syft", Platforms: []string{"linux/amd64", "linux/arm64"}}}
	cat.ToolPlatforms = map[string][]catalog.ToolPin{"syft": pins}
	env.UtilityContainers = map[string]string{"linux/amd64": env.UtilityContainer, "linux/arm64": "golang@sha256:" + strings.Repeat("b", 64)}
	stack, err = catalog.ApplyStack(cat)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Compile(t.Context(), def, env, stack, "dev (unknown)")
	if err != nil {
		t.Fatal(err)
	}
	after.DefinitionPath, after.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
	return before, after
}

func TestNativeOutputUtilitiesKeepBuildIdentityAndJoinProducer(t *testing.T) {
	before, after := nativeUtilityFixture(t)
	for i, build := range before.Builds {
		if build.BuildDigest != after.Builds[i].BuildDigest || !reflect.DeepEqual(build.Graph, after.Builds[i].Graph) {
			t.Fatal("native utility selection changed producer identity or instructions")
		}
	}
	if before.EnvironmentDigest == after.EnvironmentDigest || before.CatalogDigest == after.CatalogDigest {
		t.Fatal("changed inputs retained identity")
	}
	if err := after.Validate(); err != nil {
		t.Fatal(err)
	}
	stages, jobs, err := githubStages(after)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := planIndex(after)
	for _, tc := range []struct{ output, platform, runner, tool string }{
		{"worker", "linux/amd64", "ubuntu-latest", "syft"},
		{"worker-two", "linux/arm64", "ubuntu-24.04-arm", "syft/linux/arm64"},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			producer := jobs[tc.output+"/build-containerd"]
			for _, id := range []string{tc.output + "/terminal-observe", tc.output + "/sbom", "package/" + tc.output, "package/publication-" + tc.output} {
				node, exists := nodes[id]
				if !exists {
					t.Fatalf("missing %s", id)
				}
				if node.Platform != tc.platform || node.RunsOn != tc.runner || jobs[id] != producer {
					t.Fatalf("%s platform/runner/job = %s/%s/%s, want %s/%s/%s", id, node.Platform, node.RunsOn, jobs[id], tc.platform, tc.runner, producer)
				}
			}
			if !containsString(nodes[tc.output+"/sbom"].Inputs, "tool/"+tc.tool) {
				t.Fatal("wrong scanner edge")
			}
			if jobs["publish/output-"+tc.output] == producer {
				t.Fatal("publication joined producer authority")
			}
		})
	}
	for _, stage := range stages {
		for _, node := range stage.Nodes {
			if node.RunsOn != stage.Nodes[0].RunsOn {
				t.Fatal("mixed runner job")
			}
		}
	}
	if _, err := RenderGitHub(t.Context(), after); err != nil {
		t.Fatal(err)
	}
}

func TestNativeOutputUtilitiesWithARMDefault(t *testing.T) {
	_, base := nativeUtilityFixture(t)
	env := base.Environment.Clone()
	env.UtilityPlatform = "linux/arm64"
	env.UtilityContainer = env.UtilityContainers[env.UtilityPlatform]
	env.Signing, env.Publication = nil, nil
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), base.Definition, env, stack, "dev (unknown)")
	if err != nil {
		t.Fatal(err)
	}
	for i, build := range base.Builds {
		if build.BuildDigest != plan.Builds[i].BuildDigest || !reflect.DeepEqual(build.Graph, plan.Builds[i].Graph) {
			t.Fatal("default utility platform changed producer identity or instructions")
		}
	}
	nodes, _ := planIndex(plan)
	for _, tc := range []struct{ output, platform, runner, tool string }{
		{"worker", "linux/amd64", DefaultRunsOn, "syft/linux/amd64"},
		{"worker-two", "linux/arm64", "ubuntu-24.04-arm", "syft"},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			for _, id := range []string{tc.output + "/terminal-observe", tc.output + "/sbom", "package/" + tc.output, "tool/" + tc.tool} {
				node, exists := nodes[id]
				if !exists || node.Platform != tc.platform || node.RunsOn != tc.runner {
					t.Fatalf("%s platform/runner = %s/%s, want %s/%s", id, node.Platform, node.RunsOn, tc.platform, tc.runner)
				}
			}
			if !containsString(nodes[tc.output+"/sbom"].Inputs, "tool/"+tc.tool) {
				t.Fatal("wrong scanner edge")
			}
		})
	}
}

func TestNativeUtilitySelectionRefusesMissingPinsAndGraphSubstitution(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Plan)
		code e.Code
	}{
		{"missing container", func(p *Plan) { delete(p.Environment.UtilityContainers, "linux/arm64") }, e.CodeEngineCapability},
		{"missing scanner", func(p *Plan) { p.CatalogSnapshot.ToolPlatforms["syft"] = p.CatalogSnapshot.ToolPlatforms["syft"][:1] }, e.CodeUnpinnedSource},
		{"substituted scanner edge", func(p *Plan) {
			for i := range p.Graph.Nodes {
				n := &p.Graph.Nodes[i]
				if n.ID == "worker-two/sbom" {
					for j := range n.Inputs {
						if n.Inputs[j] == "tool/syft/linux/arm64" {
							n.Inputs[j] = "tool/syft"
						}
					}
				}
			}
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p := nativeUtilityFixture(t)
			tc.edit(p)
			if tc.name == "substituted scanner edge" {
				packages, _, _, err := derivePublication(p.Definition, p.Environment, p.Builds)
				if err != nil {
					t.Fatal(err)
				}
				assertFactoryErrorCode(t, p.validateCompiledGraph(packages), tc.code)
				return
			}
			stack, err := catalog.ApplyStack(p.CatalogSnapshot)
			if err == nil {
				_, err = Compile(t.Context(), p.Definition, p.Environment, stack, "dev (unknown)")
			}
			assertFactoryErrorCode(t, err, tc.code)
		})
	}
}

func TestNativeDiskScannerSelectionUsesExactPlatformPin(t *testing.T) {
	_, plan := nativeUtilityFixture(t)
	for _, tc := range []struct {
		name, platform, digest string
		code                   e.Code
	}{
		{"amd64 exact", "linux/amd64", "sha256:" + strings.Repeat("a", 64), ""},
		{"arm64 exact", "linux/arm64", "sha256:" + strings.Repeat("b", 64), ""},
		{"arm64 substituted", "linux/arm64", "sha256:" + strings.Repeat("a", 64), e.CodeVerifyFailed},
		{"missing platform", "darwin/arm64", "sha256:" + strings.Repeat("a", 64), e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDiskScannerSelection(plan, tc.platform, tc.digest)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
