// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeExecutableFactoryKeepsLinuxUtilityExecution(t *testing.T) {
	tool := catalog.ToolPin{Format: "raw", URL: "https://example.test/tool", Platform: "darwin/arm64", ExecutablePath: "tool", ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("a", 64)}
	base := &catalog.Catalog{Components: map[string]catalog.Component{"tool": {Platforms: []string{tool.Platform}, OutputType: "host-package", Tag: "v1.2.3", SourceDate: 1, Prebuilt: &catalog.Prebuilt{URL: tool.URL, Digests: map[string]string{"arm64": tool.ExecutableDigest}, Executable: &tool, Evidence: &catalog.ExternalEvidence{None: true}}}}}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("native-tools", "native-tools", "")
	definition.Outputs = []authored.DefinitionOutput{{Name: "native-tools", Format: authored.OutputExecutable, Platform: tool.Platform, Components: []string{"tool"}}}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, node := range plan.Graph.Nodes {
		operation, err := plan.ExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		if operation.Container != "" && operation.Platform != environment.UtilityPlatform {
			t.Fatalf("native payload changed utility execution: %+v", operation)
		}
	}
	if err := admitGitHubPlatforms(plan); err == nil {
		t.Fatal("native acquisition admitted an unsupported hosted runner")
	}
	for _, mutation := range []func(*authored.Environment){
		func(env *authored.Environment) {
			env.Publication = &authored.Publication{RepositoryPrefix: "example.test/native"}
		},
		func(env *authored.Environment) { env.Signing = &authored.Signing{} },
	} {
		mutated := environment
		mutation(&mutated)
		if got, err := Compile(t.Context(), definition, mutated, stack, "test"); got != nil || err == nil {
			t.Fatal("native tool acquisition admitted unsupported publication/signing")
		}
	}
}

func TestNativeSourceFactorySeparatesPayloadAndContainer(t *testing.T) {
	component := catalog.Component{Platforms: []string{"darwin/arm64"}, FromSource: true,
		Repo: "https://example.test/source", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImage: "example.test/go@sha256:" + strings.Repeat("c", 64), OutputType: "host-package",
		BuildCommand: []string{"go", "build", "-o", "bin/tool", "./cmd/tool"}, WriteDir: "bin", ArtifactPath: "bin/tool", SourceDate: 1,
		VendorCommand: []string{"go", "mod", "vendor"}}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"tool": component}})
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("native-tools", "native-tools", "")
	definition.Outputs = []authored.DefinitionOutput{{Name: "native-tools", Format: authored.OutputExecutable, Platform: "darwin/arm64", Components: []string{"tool"}}}
	environment.RunsOn, environment.UtilityPlatform = "ubuntu-24.04-arm", "linux/arm64"
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(plan.Builds) != 1 || plan.Builds[0].Platform != "darwin/arm64" {
		t.Fatal("payload target lost")
	}
	for _, node := range plan.Graph.Nodes {
		operation, err := plan.ExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		if operation.Container == "" {
			continue
		}
		want := environment.UtilityPlatform
		if node.Kind == NodeBuild || node.Kind == NodeFetch {
			want = "linux/arm64"
		}
		if operation.Platform != want {
			t.Fatalf("operation %s platform %q, want %q", node.ID, operation.Platform, want)
		}
	}
	if err := admitGitHubPlatforms(plan); err == nil {
		t.Fatal("Darwin payload admitted by legacy ARM64 RunsOn")
	} else {
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	}
	for _, node := range plan.Graph.Nodes {
		assertFactoryErrorCode(t, admitGitHubNodePlatform(plan, node, "linux/arm64"), e.CodeEngineCapability)
	}
	if rendered, err := RenderGitHub(t.Context(), plan); rendered != nil || err == nil {
		t.Fatal("Mac source build admitted ARM64 hosted execution")
	} else {
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	}
}

func TestBundleObservationBindsTargetPlatform(t *testing.T) {
	plan, artifacts := lifecyclePackageFixture(t, authored.OutputExecutable)
	plan.Builds[0].Platform = "darwin/arm64"
	_, err := ObserveLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], artifacts)
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
}

func TestBinaryPackageRequiresOneExactTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		builds []BuildPlan
		inputs []Artifact
		want   string
		code   e.Code
	}{
		{name: "Mac", builds: []BuildPlan{{Name: "tools", Platform: "darwin/arm64"}}, inputs: []Artifact{{BuildTarget: "tools"}}, want: "darwin/arm64"},
		{name: "Linux", builds: []BuildPlan{{Name: "tools", Platform: "linux/amd64"}}, inputs: []Artifact{{BuildTarget: "tools"}}, want: "linux/amd64"},
		{name: "missing", inputs: []Artifact{{BuildTarget: "missing"}}, code: e.CodeInvalidCatalog},
		{name: "empty", code: e.CodeReleaseIncomplete},
		{name: "mixed", builds: []BuildPlan{{Name: "mac", Platform: "darwin/arm64"}, {Name: "linux", Platform: "linux/arm64"}}, inputs: []Artifact{{BuildTarget: "mac"}, {BuildTarget: "linux"}}, code: e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := binaryPackagePlatform(&Plan{Builds: tc.builds}, tc.inputs)
			if got != tc.want {
				t.Fatalf("platform %q, want %q", got, tc.want)
			}
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("error %v, want %s", err, tc.code)
			}
		})
	}
}
