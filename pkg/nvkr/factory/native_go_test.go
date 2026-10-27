// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestNativeGoFactoryRequiresLocalToolchainBeforeWorkspace(t *testing.T) {
	t.Chdir(t.TempDir())
	identity := "sha256:" + strings.Repeat("a", 64)
	base := &catalog.Catalog{Components: map[string]catalog.Component{"tool": {
		Platforms: []string{"darwin/arm64"}, FromSource: true, CGO: true, OutputType: "host-package", Tag: "v1.0.0", SourceDate: 1,
		Repo: "https://example.test/tool", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		BuilderType: catalog.BuilderNativeGo, ToolchainImage: identity, ToolchainImages: map[string]string{"arm64": fixtureUtilityContainer},
		BuildNetwork: ir.NetworkDefault, BuildCommand: []string{"go", "build", "-o", "out/tool", "./cmd/tool"}, WriteDir: "out", ArtifactPath: "out/tool",
	}}}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("native-go", "tools", "")
	definition.Outputs = []authored.DefinitionOutput{{Name: "tools", Platform: "darwin/arm64", Format: authored.OutputExecutable, Components: []string{"tool"}}}
	environment.Runtime.BuildNetworkPolicy = authored.BuildNetworkProducerDeclared
	if _, err := Compile(t.Context(), definition, environment, stack, "test"); err == nil {
		t.Fatal("missing installed compiler binding admitted")
	}
	environment.Runtime.NativeToolchains = map[string]authored.NativeToolchain{identity: {GoManifest: "/absent/manifest.json", GoRoot: "/absent/sdk", Compiler: "/absent/clang"}}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RenderGitHub(t.Context(), plan); err == nil {
		t.Fatal("local compiler admitted for hosted execution")
	}
	_, err = RunLocal(t.Context(), plan, LocalRunOptions{Workspace: ".nvkr/forbidden"})
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if _, err := os.Lstat(".nvkr"); !os.IsNotExist(err) {
		t.Fatal("missing compiler created a workspace")
	}
}
