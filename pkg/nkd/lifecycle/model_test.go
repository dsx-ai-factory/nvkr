package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/cluster"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func TestLifecycleOutputsLimitExecutionWithoutChangingRecipeIdentity(t *testing.T) {
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
	var gpu factory.RecipePlan
	for _, recipePlan := range plan.Recipes {
		if recipePlan.Name == "gpu-node" {
			gpu = recipePlan
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
	if graphDigest != gpu.RecipeDigest {
		t.Fatalf("full graph digest = %s, RecipeDigest = %s", graphDigest, gpu.RecipeDigest)
	}
	fullRecipeHasHelm := false
	for _, node := range gpu.Graph.Nodes {
		if node.Component == "helm" {
			fullRecipeHasHelm = true
		}
	}
	if !fullRecipeHasHelm {
		t.Fatal("full canonical Recipe identity lost unused Helm member")
	}
	nodes := make(map[string]factory.Node, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		nodes[node.ID] = node
	}
	if _, exists := nodes["gpu-node/build-helm"]; exists {
		t.Fatal("unused Helm producer remained in lifecycle execution graph")
	}
	containerd, exists := nodes["gpu-node/build-containerd"]
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
	definitionPath, err := filepath.Abs(filepath.Join("..", "..", "..", "definitions", "nke-v1", "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	environmentPath, err := filepath.Abs(filepath.Join("..", "..", "..", "definitions", "nke-v1", "environment-local.yaml"))
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
	base, err := catalog.LoadContext(context.Background(), catalog.Dir(filepath.Join(filepath.Dir(definitionPath), "catalog")))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	firstDefinition, firstEnvironment, err := lower(definition, environment)
	if err != nil {
		t.Fatal(err)
	}
	secondDefinition, secondEnvironment, err := lower(definition, changed)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan, err := factory.Compile(context.Background(), firstDefinition, firstEnvironment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := factory.Compile(context.Background(), secondDefinition, secondEnvironment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if firstPlan.Recipes[0].RecipeDigest != secondPlan.Recipes[0].RecipeDigest {
		t.Fatal("environment destination changed logical Recipe identity")
	}
	signedDefinition, signedDelivery, err := lower(definition, signingEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	signedPlan, err := factory.Compile(context.Background(), signedDefinition, signedDelivery, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if firstPlan.Recipes[0].RecipeDigest != signedPlan.Recipes[0].RecipeDigest {
		t.Fatal("Environment-owned signing tool changed logical Recipe identity")
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
	legacy, delivery, err := lower(definition, environment)
	if err != nil {
		t.Fatal(err)
	}
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(context.Background(), legacy, delivery, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionDigest = "sha256:" + repeat("7", 64)
	plan.EnvironmentDigest = ""
	if err := plan.Validate(); err == nil {
		t.Fatal("partial Definition/Environment identity pair accepted")
	} else {
		assertCode(t, err, nkderr.CodeUnverifiedRef)
	}
}

func TestLowerDerivesExecutableEnvelopeAndRoute(t *testing.T) {
	definition, delivery, err := lower(testDefinition(t), testEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Packages) != 2 || definition.Packages[0].Name != "cli" ||
		definition.Packages[0].Kind != factory.PackageBinaryBundle || definition.Packages[1].Name != "release" ||
		definition.Packages[1].Kind != factory.PackageOCI {
		t.Fatalf("derived packages = %#v", definition.Packages)
	}
	if len(delivery.Routes) != 1 || delivery.Routes[0] != (factory.Route{Name: "release", Input: "package/release", Target: "release"}) {
		t.Fatalf("derived routes = %#v", delivery.Routes)
	}
}

func TestClusterViewsReuseOneResolvedRecipeAcrossRoles(t *testing.T) {
	definition := testDefinition(t)
	definition.Clusters = []DefinitionCluster{
		{Name: "operator", Type: cluster.TypeOperator, Members: []ClusterMember{{Output: "cli", Role: "control-plane"}}},
		{Name: "tenant", Type: cluster.TypeTenant, Members: []ClusterMember{{Output: "cli", Role: "worker"}}},
	}
	legacy, _, err := lower(definition, testEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.Recipes) != 1 || legacy.Recipes[0].Criteria.Role != "any" || len(legacy.Clusters) != 2 {
		t.Fatalf("legacy lowering duplicated or role-specialized Recipe: %+v", legacy)
	}
	if legacy.Clusters[0].RoleBindings[0].Role != "control-plane" || legacy.Clusters[1].RoleBindings[0].Role != "worker" {
		t.Fatalf("cluster-local roles = %+v", legacy.Clusters)
	}
}

func TestDefinitionRejectsUnknownAndDuplicateSelections(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Definition)
	}{
		{name: "unknown output recipe", edit: func(d *Definition) { d.Outputs[0].Recipe = "missing" }},
		{name: "duplicate output", edit: func(d *Definition) { d.Outputs = append(d.Outputs, d.Outputs[0]) }},
		{name: "unknown cluster output", edit: func(d *Definition) { d.Clusters[0].Members = []ClusterMember{{Output: "missing", Role: "worker"}} }},
		{name: "recipe owns cluster role", edit: func(d *Definition) { d.Recipes[0].Criteria.Role = "worker" }},
		{name: "recipe owns cluster type", edit: func(d *Definition) { d.Recipes[0].Criteria.ClusterType = cluster.TypeOperator }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := testDefinition(t)
			test.edit(&definition)
			if err := definition.Validate(); err == nil {
				t.Fatal("invalid definition was accepted")
			} else {
				assertCode(t, err, nkderr.CodeInvalidCatalog)
			}
		})
	}
}

func assertCode(t *testing.T, err error, code nkderr.Code) {
	t.Helper()
	var coded *nkderr.Error
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
		Recipes: []RecipeSelection{{Name: "cli-recipe", Criteria: criteria.Criteria{Category: "kubernetes", Role: "any", K8sVersion: "1.36.0", OS: "ubuntu", Arch: "amd64"}}},
		Outputs: []DefinitionOutput{{Name: "cli", Recipe: "cli-recipe", Components: []string{"kubectl"}, Format: OutputExecutable}},
		Clusters: []DefinitionCluster{{Name: "tenant", Type: cluster.TypeTenant,
			Members: []ClusterMember{{Output: "cli", Role: "worker"}}}}}
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
