// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func cliTestPlan(t *testing.T, arch, mode string, outputs int) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["containerd"]
	component.Platforms = []string{"linux/amd64", "linux/arm64"}
	component.VersionStamp = map[string]string{"NKECTL_VERSION": "v1"}
	if mode == "missing-version" {
		component.VersionStamp = nil
	}
	if mode == "empty-version" {
		component.VersionStamp["NKECTL_VERSION"] = ""
	}
	image := "nvkr-toolchain-cli@sha256:" + strings.Repeat("a", 64)
	component.ToolchainImage = image
	if mode == "map" || mode == "precedence" || mode == "fallback" {
		component.ToolchainImages = map[string]string{arch: image}
		component.ToolchainImage = ""
		if mode == "fallback" {
			component.ToolchainImages = map[string]string{map[string]string{"amd64": "arm64", "arm64": "amd64"}[arch]: image}
			component.ToolchainImage = image
		}
		if mode == "precedence" {
			component.ToolchainImage = "fallback@sha256:" + strings.Repeat("b", 64)
		}
	}
	switch mode {
	case "alias", "equal-alias", "conflicting-alias":
		component.VersionStamp["NVKECTL_VERSION"] = "v1"
		if mode == "alias" {
			delete(component.VersionStamp, "NKECTL_VERSION")
		}
		if mode == "conflicting-alias" {
			component.VersionStamp["NVKECTL_VERSION"] = "v2"
		}
	case "nix":
		component.BuilderType = ir.BuilderNix
		component.Flake = &catalog.FlakePin{Ref: "github:example/tool?rev=" + component.Commit, Output: "packages.x86_64-linux.default", LockDigest: "sha256:" + strings.Repeat("b", 64), Image: "nixos/nix@sha256:" + strings.Repeat("c", 64)}
	case "acquired":
		component = catalog.Component{Platforms: []string{"linux/amd64"}, Tag: "v1.0.0", SourceDate: 1700000000, OutputType: "host-package", VersionStamp: map[string]string{"NKECTL_VERSION": "v1"}, Prebuilt: &catalog.Prebuilt{URL: "https://supplier.example/tool", Evidence: &catalog.ExternalEvidence{None: true}, Digests: map[string]string{"amd64": "sha256:" + strings.Repeat("d", 64)}}}
	case "image":
		component.OutputType = "oci-image"
	}
	base.Components["nvkectl"] = component
	definition, environment := fixtureInputs("cli-test", "renamed-cli", DefaultRunsOn)
	definition.Outputs[0].Components = []string{"nvkectl"}
	definition.Outputs[0].Platform = "linux/" + arch
	if mode == "image" {
		definition.Outputs[0].Format = authored.OutputOCI
	}
	if mode == "no-cli" {
		definition.Outputs[0].Components = []string{"containerd"}
	}
	if outputs == 2 {
		other := definition.Outputs[0]
		other.Name = "other-cli"
		definition.Outputs = append([]authored.DefinitionOutput{other}, definition.Outputs...)
	}
	if mode == "relocated" {
		environment.Runtime.ToolchainRegistry = "mirror.example/tools"
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func writeCLIPlan(t *testing.T, plan *Plan) string {
	t.Helper()
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIArtifactTestToolchains(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, mode := range []string{"scalar", "map", "precedence", "fallback", "relocated"} {
			t.Run(arch+"/"+mode, func(t *testing.T) {
				plan := cliTestPlan(t, arch, mode, 1)
				inputs, err := LoadCLIArtifactTestInputs(t.Context(), writeCLIPlan(t, plan), "")
				if err != nil {
					t.Fatal(err)
				}
				want := "nvkr-toolchain-cli@sha256:" + strings.Repeat("a", 64)
				if mode == "relocated" {
					want = "mirror.example/tools/" + want
				}
				if inputs.ToolchainImage != want || inputs.Platform != "linux/"+arch || inputs.Output != "renamed-cli" || inputs.ExpectedVersion != "v1" {
					t.Fatalf("unexpected inputs: %+v", inputs)
				}
			})
		}
	}
}

func TestCLIArtifactTestSelection(t *testing.T) {
	path := writeCLIPlan(t, cliTestPlan(t, "amd64", "scalar", 2))
	for _, output := range []string{"", "missing"} {
		_, err := LoadCLIArtifactTestInputs(t.Context(), path, output)
		assertFactoryErrorCode(t, err, e.CodeUsage)
	}
	inputs, err := LoadCLIArtifactTestInputs(t.Context(), path, "other-cli")
	if err != nil || inputs.Output != "other-cli" {
		t.Fatalf("explicit selection: %+v, %v", inputs, err)
	}
}

func TestCLIArtifactTestRejectsChangedTransport(t *testing.T) {
	plan := cliTestPlan(t, "amd64", "relocated", 1)
	for i := range plan.Builds[0].Graph.Nodes {
		node := &plan.Builds[0].Graph.Nodes[i]
		if node.Kind == ir.KindBuild {
			node.Toolchain = "unauthorized.example/cli@sha256:" + strings.Repeat("a", 64)
		}
	}
	inputs, err := LoadCLIArtifactTestInputs(t.Context(), writeCLIPlan(t, plan), "")
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	if inputs != nil {
		t.Fatal("changed transport emitted diagnostic inputs")
	}
}

func TestCLIArtifactTestRequiresVersion(t *testing.T) {
	for _, mode := range []string{"missing-version", "empty-version"} {
		t.Run(mode, func(t *testing.T) {
			plan := cliTestPlan(t, "amd64", mode, 1)
			inputs, err := LoadCLIArtifactTestInputs(t.Context(), writeCLIPlan(t, plan), "")
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			if inputs != nil {
				t.Fatal("missing expected version emitted inputs")
			}
		})
	}
}

func TestCLIArtifactTestUnsupportedProducers(t *testing.T) {
	for _, mode := range []string{"nix", "acquired", "image", "no-cli"} {
		t.Run(mode, func(t *testing.T) {
			plan := cliTestPlan(t, "amd64", mode, 1)
			inputs, err := LoadCLIArtifactTestInputs(t.Context(), writeCLIPlan(t, plan), "")
			code := e.CodeEngineCapability
			if mode == "no-cli" {
				code = e.CodeUsage
			}
			assertFactoryErrorCode(t, err, code)
			if inputs != nil {
				t.Fatal("unsupported producer emitted inputs")
			}
		})
	}
}

func TestCLIArtifactTestVersionAliasAdmission(t *testing.T) {
	for _, mode := range []string{"alias", "equal-alias", "conflicting-alias"} {
		t.Run(mode, func(t *testing.T) {
			// Both reviewed upstream spellings are valid build inputs. The
			// artifact-test owner requires an unambiguous expected version.
			plan := cliTestPlan(t, "amd64", mode, 1)
			inputs, err := LoadCLIArtifactTestInputs(t.Context(), writeCLIPlan(t, plan), "")
			if mode == "conflicting-alias" {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				if inputs != nil {
					t.Fatal("conflicting versions emitted test inputs")
				}
				return
			}
			if err != nil || inputs.ExpectedVersion != "v1" {
				t.Fatalf("reviewed version alias rejected: %+v, %v", inputs, err)
			}
		})
	}
}

func TestCLIArtifactTestValidatesUnselectedOutput(t *testing.T) {
	plan := cliTestPlan(t, "amd64", "scalar", 2)
	for i := range plan.Builds[0].Graph.Nodes {
		if plan.Builds[0].Graph.Nodes[i].Kind == ir.KindBuild {
			plan.Builds[0].Graph.Nodes[i].Toolchain = "tampered@sha256:" + strings.Repeat("b", 64)
		}
	}
	inputs, err := LoadCLIArtifactTestInputs(t.Context(), writeCLIPlan(t, plan), "renamed-cli")
	assertFactoryErrorCode(t, err, e.CodeUnverifiedRef)
	if inputs != nil {
		t.Fatal("invalid unselected output emitted inputs")
	}
}
