// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// These contracts compile the maintained inputs through the public loaders and
// compiler. They establish selection integrity, not live
// rendering, publication, deployment compatibility or hardware acceptance.
func TestMaintainedAICRBundleInputs(t *testing.T) {
	aicrInputTestRoot(t)
	for _, test := range []struct {
		file      string
		recipe    []string // positional recipe coordinate preceding the renderer
		renderers []string
		contract  []string // selection-specific rendering contract
	}{
		{
			file:      "definition.yaml",
			recipe:    []string{"generic", "gb300"},
			renderers: []string{"argocd", "argocd-helm", "flux", "helm", "helmfile", "terraform"},
			contract:  []string{"--os ubuntu --intent training", `--deployer "$3"`},
		},
		{
			file:      "nvke-gb200.yaml",
			recipe:    []string{"nvke", "gb200", "kubeflow"},
			renderers: []string{"argocd-helm", "flux", "helm"},
			contract: []string{
				`--service "$1" --accelerator "$2" --platform "$3"`, "--os ubuntu --intent training",
				`leaf="$2-$1-ubuntu-training-$3"`, `[ "$applied" = "$leaf" ] ||`, `--deployer "$4"`,
			},
		},
	} {
		t.Run(test.file, func(t *testing.T) {
			plan := compileAICRInput(t, test.file, "aicr-local-arm64.yaml")
			count := len(test.renderers)
			if len(plan.Definition.Outputs) != count || len(plan.Builds) != count || len(plan.LifecycleOutputs) != count {
				t.Fatalf("AICR selection must retain all %d independent renderer outputs", count)
			}
			for _, output := range plan.Definition.Outputs {
				if output.Format != definition.OutputBundle || output.Platform != "linux/arm64" ||
					!slices.Equal(output.Components, []string{output.Name}) || output.Helm != nil {
					t.Fatalf("renderer lost its exact generic archive output: %+v", output)
				}
			}
			renderers, digests := make(map[string]bool), make(map[string]bool)
			for _, build := range plan.Builds {
				var producer, fetch ir.Node
				for _, node := range build.Graph.Nodes {
					switch node.Kind {
					case ir.KindBuild:
						if producer.ID != "" {
							t.Fatal("bundle gained a second source producer")
						}
						producer = node
					case ir.KindFetch:
						fetch = node
					case ir.KindPull:
						t.Fatal("maintained AICR producer became an acquired artifact")
					}
				}
				assertAICRSourceProducer(t, producer, fetch, test.contract)
				command := producer.Command
				coordinate := len(command) - 1 - len(test.recipe)
				if coordinate < 0 || !slices.Equal(command[coordinate:len(command)-1], test.recipe) {
					t.Fatalf("recipe coordinate changed: %q", command)
				}
				renderer := command[len(command)-1]
				if renderers[renderer] || digests[build.BuildDigest] || !strings.HasSuffix(build.Name, "-"+renderer) {
					t.Fatalf("renderer lost its independent named build: %s/%s", build.Name, renderer)
				}
				renderers[renderer], digests[build.BuildDigest] = true, true
			}
			for _, renderer := range test.renderers {
				if !renderers[renderer] {
					t.Fatalf("missing maintained renderer %q", renderer)
				}
			}
			if plan.Environment.Publication != nil || plan.Environment.Signing != nil {
				t.Fatal("example must remain build-only")
			}
		})
	}
}

// The NVKE producer admits a recipe only when its most specific applied overlay is
// the coordinate's own leaf. This runs the catalog's exact parser on rendered shapes.
func TestAICRNVKELeafAdmissionParser(t *testing.T) {
	aicrInputTestRoot(t)
	plan := compileAICRInput(t, "nvke-gb200.yaml", "aicr-local-arm64.yaml")
	var script string
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindBuild {
			script = node.Command[4]
		}
	}
	const open, closing = "applied=\"$(awk '\n", "\n' \"$out/recipe.yaml\")\""
	start := strings.Index(script, open)
	end := strings.Index(script, closing)
	if start < 0 || end < start {
		t.Fatalf("leaf admission parser not found in producer script:\n%s", script)
	}
	program := script[start+len(open) : end]
	const leaf = "gb200-nvke-ubuntu-training-kubeflow"
	for _, test := range []struct {
		name, recipe, want string
	}{
		{"curated leaf last", "kind: RecipeResult\nmetadata:\n  appliedOverlays:\n    - base\n    - nvke\n" +
			"    - gb200-nvke-ubuntu-training\n    - " + leaf + "\n  version: v1.2.0\n", leaf},
		{"wildcard merge without the leaf", "metadata:\n  appliedOverlays:\n    - base\n    - nvke\n" +
			"    - gb200-nvke-ubuntu-training\n  excludedOverlays:\n    - " + leaf + "\n", "gb200-nvke-ubuntu-training"},
		{"flow sequence", "metadata:\n  appliedOverlays: [base, " + leaf + "]\n", ""},
		{"duplicate lists", "metadata:\n  appliedOverlays:\n    - " + leaf + "\nother:\n  appliedOverlays:\n    - " + leaf + "\n", ""},
		{"absent list", "metadata:\n  version: v1.2.0\n", ""},
		{"quoted item", "metadata:\n  appliedOverlays:\n    - \"" + leaf + "\"\n", "\"" + leaf + "\""},
	} {
		t.Run(test.name, func(t *testing.T) {
			recipe := filepath.Join(t.TempDir(), "recipe.yaml")
			if err := os.WriteFile(recipe, []byte(test.recipe), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := exec.CommandContext(t.Context(), "awk", program, recipe).Output()
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSuffix(string(got), "\n") != test.want {
				t.Fatalf("parser printed %q, want %q", got, test.want)
			}
		})
	}
}

func assertAICRSourceProducer(t *testing.T, producer, fetch ir.Node, contract []string) {
	t.Helper()
	const commit = "935e0746dc54da890a23c1ecca998244b3a25de7"
	const tree = "1d4bfa6d824159dbb4c8077303fe8de154946405"
	const image = "golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c"
	if fetch.Repo != "https://github.com/NVIDIA/aicr" || fetch.Commit != commit || fetch.TreeHash != tree ||
		fetch.VendorImage != image || fetch.VendorSpecDigest == "" || fetch.Network != ir.NetworkDefault {
		t.Fatalf("AICR fetch lost its pinned source/vendor identity: %+v", fetch)
	}
	if producer.SourceCommit != commit || producer.SourceTreeHash != tree || producer.Toolchain != image ||
		producer.OutputType != "tarball" || producer.Network != ir.NetworkNone || !producer.SourceReadOnly ||
		producer.WriteDir == "" || !strings.HasPrefix(producer.ArtifactPath, producer.WriteDir+"/") {
		t.Fatalf("AICR build lost its offline archive/private-output contract: %+v", producer)
	}
	environment := make(map[string]string)
	for _, value := range producer.Env {
		environment[value.Name] = value.Value
	}
	if environment["GIT_VERSION"] != commit || environment["BUILD_VERSION"] != "1.2.0" || environment["SOURCE_DATE_EPOCH"] != "1793648938" {
		t.Fatalf("AICR build stamps do not bind the frozen source: %+v", environment)
	}
	for _, required := range []string{"go mod download", "go mod verify", "go mod vendor", "sha256sum -c -"} {
		if !strings.Contains(strings.Join(fetch.VendorCmd, "\n"), required) {
			t.Fatalf("AICR dependency fetch omits %q", required)
		}
	}
	for _, required := range append([]string{"-mod=vendor", "GOPROXY=off", `--output "oci-layout:$out/layout"`, `"$out/aicr" verify`, `cp "$layer" "$out/bundle.tar.gz"`}, contract...) {
		if !strings.Contains(strings.Join(producer.Command, "\n"), required) {
			t.Fatalf("AICR build omits its offline rendering/layer contract %q", required)
		}
	}
}

func aicrInputTestRoot(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
}

func compileAICRInput(t *testing.T, file, environment string) *Plan {
	t.Helper()
	definitionPath := filepath.Join("inputs/definitions/aicr", file)
	environmentPath := filepath.Join("inputs/environments", environment)
	authored, err := definition.LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := definition.LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := CompileDocuments(t.Context(), definitionPath, authored, environmentPath, selected, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
