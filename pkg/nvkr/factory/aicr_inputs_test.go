// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// These contracts compile the maintained inputs through the public loaders and
// renderer. They establish selection and generated-workflow integrity, not live
// rendering, publication, deployment compatibility or hardware acceptance.
func TestMaintainedAICRBundleInputs(t *testing.T) {
	aicrInputTestRoot(t)
	for _, test := range []struct {
		file, service, accelerator string
	}{
		{"definition.yaml", "generic", "gb300"},
		{"definition-vr200-preview.yaml", "rke2", "vr200"},
		{"definition-gb200-eks-reference.yaml", "eks", "gb200"},
	} {
		t.Run(test.file, func(t *testing.T) {
			plan := compileAICRInput(t, test.file, "environment-github.yaml")
			if len(plan.Definition.Outputs) != 6 || len(plan.Builds) != 6 || len(plan.LifecycleOutputs) != 6 {
				t.Fatal("maintained AICR selection must retain all six independent renderer outputs")
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
				assertAICRSourceProducer(t, producer, fetch)
				command := producer.Command
				if len(command) < 4 || command[len(command)-3] != test.service || command[len(command)-2] != test.accelerator {
					t.Fatalf("recipe coordinate changed: %q", command)
				}
				renderer := command[len(command)-1]
				if renderers[renderer] || digests[build.BuildDigest] || !strings.HasSuffix(build.Name, "-"+renderer) {
					t.Fatalf("renderer lost its independent named build: %s/%s", build.Name, renderer)
				}
				renderers[renderer], digests[build.BuildDigest] = true, true
			}
			for _, renderer := range []string{"argocd", "argocd-helm", "flux", "helm", "helmfile", "terraform"} {
				if !renderers[renderer] {
					t.Fatalf("missing maintained renderer %q", renderer)
				}
			}
			for _, node := range plan.Graph.Nodes {
				if node.Kind != NodeBuild {
					continue
				}
				execution, err := plan.ExecutionFor(node)
				if err != nil || execution.Platform != "linux/arm64" || execution.RunsOn != "ubuntu-24.04-arm" {
					t.Fatalf("AICR native hosted route changed: %+v, %v", execution, err)
				}
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if bundle.WorkflowPath != ".github/workflows/nvkr-run-aicr.yml" || len(bundle.Files[bundle.WorkflowPath]) == 0 ||
				len(bundle.Files[".github/nvkr-run-aicr/plan.json"]) == 0 {
				t.Fatal("AICR generation lost its named workflow namespace")
			}
		})
	}
}

func assertAICRSourceProducer(t *testing.T, producer, fetch ir.Node) {
	t.Helper()
	const commit = "fa705ec263a54eeb0ca25cb24c9f20c5d8504dca"
	const tree = "10c8c6832fa5d6b37c431dc5aa5bb44c52b3d1a7"
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
	if environment["GIT_VERSION"] != commit || environment["BUILD_VERSION"] != "0.0.0-nvkr."+commit[:12] || environment["SOURCE_DATE_EPOCH"] != "1792385421" {
		t.Fatalf("AICR build stamps do not bind the frozen source: %+v", environment)
	}
	for _, required := range []string{"go mod download", "go mod verify", "go mod vendor", "sha256sum -c -"} {
		if !strings.Contains(strings.Join(fetch.VendorCmd, "\n"), required) {
			t.Fatalf("AICR dependency fetch omits %q", required)
		}
	}
	for _, required := range []string{"-mod=vendor", "GOPROXY=off", "--os ubuntu --intent training", `--deployer "$3"`, "gzip -n"} {
		if !strings.Contains(strings.Join(producer.Command, "\n"), required) {
			t.Fatalf("AICR build omits its offline rendering/archive contract %q", required)
		}
	}
}

func TestMaintainedAICRBuildOnlyEnvironments(t *testing.T) {
	aicrInputTestRoot(t)
	for _, environment := range []string{"environment-local-arm64.yaml", "environment-github-dev.yaml"} {
		t.Run(environment, func(t *testing.T) {
			plan := compileAICRInput(t, "definition.yaml", environment)
			if plan.Environment.Publication != nil || plan.Environment.Signing != nil || len(plan.LifecycleOutputs) != 6 {
				t.Fatal("build-only AICR environment changed its lifecycle scope")
			}
			if environment == "environment-github-dev.yaml" {
				if _, err := RenderGitHub(t.Context(), plan); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMaintainedAICRNativeHelmInput(t *testing.T) {
	aicrInputTestRoot(t)
	plan := compileAICRInput(t, "definition-native-helm.yaml", "environment-github.yaml")
	if len(plan.Definition.Outputs) != 1 || len(plan.Builds) != 1 {
		t.Fatal("native Helm selection must carry one genuine Argo CD chart")
	}
	output := plan.Definition.Outputs[0]
	if output.Name != "aicr-bundle" || output.Format != definition.OutputHelm || output.Platform != "linux/arm64" ||
		!slices.Equal(output.Components, []string{"aicr-gb300-generic-training-native-helm"}) {
		t.Fatalf("native Helm output must retain its upstream chart name and exact producer: %+v", output)
	}
	var producer, fetch ir.Node
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindBuild {
			producer = node
		}
		if node.Kind == ir.KindFetch {
			fetch = node
		}
	}
	assertAICRSourceProducer(t, producer, fetch)
	if len(producer.Command) < 3 || !slices.Equal(producer.Command[len(producer.Command)-3:], []string{"generic", "gb300", "argocd-helm"}) ||
		!strings.Contains(strings.Join(producer.Command, "\n"), `-cf "$out/bundle.tar" bundle`) {
		t.Fatal("native Helm producer must render the chart deployer and retain its complete archive root")
	}
	if _, err := RenderGitHub(t.Context(), plan); err != nil {
		t.Fatal(err)
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
	definitionPath := filepath.Join("definitions/aicr", file)
	environmentPath := filepath.Join("definitions/aicr", environment)
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
