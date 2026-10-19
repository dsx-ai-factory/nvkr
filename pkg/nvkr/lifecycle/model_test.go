// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestLifecycleOutputsCompileOnlySelectedProducers(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoRoot)
	plan, err := CompileFiles(context.Background(), "testdata/examples/ai-factory/definition-arm64.yaml",
		"testdata/examples/ai-factory/environment-local.yaml", "test (test)")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"containerd", "crictl", "kubeadm", "kubectl", "kubelet", "runc"}
	var gpu factory.BuildPlan
	for _, buildPlan := range plan.Builds {
		if buildPlan.Name == "worker-binaries" {
			gpu = buildPlan
			break
		}
	}
	if !slices.Equal(gpu.Components, want) {
		t.Fatalf("executed components = %v, want %v", gpu.Components, want)
	}
	graphDigest, err := gpu.Graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if graphDigest != gpu.BuildDigest {
		t.Fatalf("full graph digest = %s, BuildDigest = %s", graphDigest, gpu.BuildDigest)
	}
	fullBuildHasHelm := false
	for _, node := range gpu.Graph.Nodes {
		if node.Component == "helm" {
			fullBuildHasHelm = true
		}
	}
	if fullBuildHasHelm {
		t.Fatal("unselected Helm producer contaminated build identity")
	}
	nodes := make(map[string]factory.Node, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		nodes[node.ID] = node
	}
	if _, exists := nodes["worker-binaries/build-helm"]; exists {
		t.Fatal("unused Helm producer remained in lifecycle execution graph")
	}
	containerd, exists := nodes["worker-binaries/build-containerd"]
	if !exists || len(containerd.Inputs) == 0 {
		t.Fatal("selected containerd producer lost its source dependency")
	}
	for _, input := range containerd.Inputs {
		producer := ""
		for _, artifact := range plan.Graph.Artifacts {
			if artifact.ID == input {
				producer = artifact.Producer
				break
			}
		}
		if producer == "" || nodes[producer].Kind != factory.NodeFetch {
			t.Fatalf("containerd input %q is not produced by a retained pinned fetch", input)
		}
	}
}

func TestDefinitionAndEnvironmentHaveIndependentIdentity(t *testing.T) {
	definitionPath, err := filepath.Abs(filepath.Join("..", "..", "..", "docs/examples/nvke-tutorial/definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	environmentPath, err := filepath.Abs(filepath.Join("..", "..", "..", "docs/examples/nvke-tutorial/environment-local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := LoadDefinition(context.Background(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(context.Background(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	definitionDigest, err := definition.Digest()
	if err != nil {
		t.Fatal(err)
	}
	environmentDigest, err := environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	changed := environment
	changed.Publication = &Publication{RepositoryPrefix: "registry.example.test/other", Anonymous: true}
	changedDigest, err := changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if definitionDigest == environmentDigest || environmentDigest == changedDigest {
		t.Fatal("definition and environment identities were not independently domain-separated")
	}
	signingEnvironment := environment
	signingEnvironment.Signing = &Signing{Mode: factory.SigningModeCosign, Identity: "^builder$", Issuer: "issuer"}
	signingDigest, err := signingEnvironment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	changedSigner := *signingEnvironment.Signing
	changedSigner.Identity = "^another-builder$"
	signingEnvironment.Signing = &changedSigner
	changedSigningDigest, err := signingEnvironment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if signingDigest == changedSigningDigest {
		t.Fatal("signer policy did not change Environment identity")
	}
	definitionDigestAgain, err := definition.Digest()
	if err != nil || definitionDigestAgain != definitionDigest {
		t.Fatal("changing an environment perturbed definition identity")
	}
	base, err := catalog.LoadContext(context.Background(), catalog.Dir(filepath.Join(filepath.Dir(definitionPath), definition.Catalog.Base)))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan, err := factory.Compile(context.Background(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := factory.Compile(context.Background(), definition, changed, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if firstPlan.DefinitionDigest != definitionDigest || firstPlan.EnvironmentDigest != environmentDigest || secondPlan.EnvironmentDigest != changedDigest {
		t.Fatal("complete plan lost the exact authored input identities")
	}
	if len(firstPlan.Builds) != len(secondPlan.Builds) {
		t.Fatal("environment destination changed selected builds")
	}
	for i, build := range firstPlan.Builds {
		if build.Name != secondPlan.Builds[i].Name || build.BuildDigest != secondPlan.Builds[i].BuildDigest {
			t.Fatal("environment destination changed logical Build identity")
		}
	}
	signedPlan, err := factory.Compile(context.Background(), definition, signingEnvironment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPlan.Builds) != len(signedPlan.Builds) {
		t.Fatal("signing policy changed selected builds")
	}
	for i, build := range firstPlan.Builds {
		if build.Name != signedPlan.Builds[i].Name || build.BuildDigest != signedPlan.Builds[i].BuildDigest {
			t.Fatal("Environment-owned signing tool changed logical Build identity")
		}
	}
	firstPlanDigest, err := firstPlan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	secondPlanDigest, err := secondPlan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if firstPlanDigest == secondPlanDigest {
		t.Fatal("environment destination did not change prepared plan identity")
	}

}

func TestLifecyclePlanRejectsPartialPublicIdentityPair(t *testing.T) {
	definition, environment := testDefinition(t), testEnvironment()
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(context.Background(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionDigest = "sha256:" + repeat("7", 64)
	plan.EnvironmentDigest = ""
	if err := plan.Validate(); err == nil {
		t.Fatal("partial Definition/Environment identity pair accepted")
	} else {
		assertCode(t, err, nvkrerr.CodeUnverifiedRef)
	}
}

func TestCompileDerivesExecutableEnvelopeAndRoute(t *testing.T) {
	plan, err := Compile(t.Context(), "definition.yaml", testDefinition(t), "environment.yaml", testEnvironment(), "test")
	if err != nil {
		t.Fatal(err)
	}
	packages := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == factory.NodePackage {
			packages[node.ID] = node.PackageKind
		}
	}
	if len(packages) != 2 || packages["package/cli"] != factory.PackageBinaryBundle || packages["package/release"] != factory.PackageOCI {
		t.Fatalf("derived packages = %#v", packages)
	}
	if len(plan.Routes) != 1 || plan.Routes[0] != (factory.Route{Name: "release", Input: "package/release", Target: "release"}) {
		t.Fatalf("derived routes = %#v", plan.Routes)
	}
	if len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "cli" || plan.LifecycleOutputs[0].ArtifactID != "package/cli" {
		t.Fatalf("complete plan lost its compiled output join: %#v", plan.LifecycleOutputs)
	}
}

func TestDefinitionRejectsUnknownAndDuplicateSelections(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Definition)
	}{
		{name: "unknown output build", edit: func(d *Definition) { d.Outputs[0].Platform = "missing" }},
		{name: "duplicate output", edit: func(d *Definition) { d.Outputs = append(d.Outputs, d.Outputs[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition(t)
			test.edit(&definition)
			if err := definition.Validate(); err == nil {
				t.Fatal("invalid definition was accepted")
			} else {
				assertCode(t, err, nvkrerr.CodeInvalidCatalog)
			}
		})
	}
}

func assertCode(t *testing.T, err error, code nvkrerr.Code) {
	t.Helper()
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func testDefinition(t *testing.T) Definition {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate lifecycle test catalog")
	}
	catalogPath := filepath.Join(filepath.Dir(source), "..", "..", "..", "testdata", "catalogs", "kubernetes-layered")
	return Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind,
		Metadata: Metadata{Name: "product"}, Catalog: Catalog{Base: catalogPath},
		Outputs: []DefinitionOutput{{Name: "cli", Platform: "linux/amd64", Components: []string{"kubectl"}, Format: OutputExecutable}}}
}

func testEnvironment() Environment {
	return Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "test"},
		UtilityContainer: "golang@sha256:" + repeat("1", 64), UtilityPlatform: "linux/amd64",
		Publication: &Publication{RepositoryPrefix: "registry.example.test/product", Anonymous: true}}
}

func repeat(value string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += value
	}
	return result
}

func TestOutputsWithIdenticalProducersKeepSeparateExecutionUses(t *testing.T) {
	definition := testDefinition(t)
	duplicate := definition.Outputs[0]
	duplicate.Name = "cli-copy"
	definition.Outputs = append(definition.Outputs, duplicate)
	environment := testEnvironment()
	environment.Publication = nil
	plan, err := Compile(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Builds) != 2 || plan.Builds[0].BuildDigest != plan.Builds[1].BuildDigest {
		t.Fatal("identical producers should have one content identity across distinct output uses")
	}
	run, err := PrepareRun(definition, environment, plan, "same-content")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Expected) != 2 || run.Expected[0].ComponentResults[0] == run.Expected[1].ComponentResults[0] {
		t.Fatal("equal content collapsed required execution evidence")
	}
	if plan.Builds[0].Name == plan.Builds[1].Name {
		t.Fatal("output names collapsed")
	}
}
