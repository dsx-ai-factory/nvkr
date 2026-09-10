package cluster

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/recipe"
)

func compositionDigest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }

func tenantAcceleratedComposition() *ClusterComposition {
	return &ClusterComposition{
		SchemaVersion: CompositionSchemaVersion, ClusterType: TypeTenant,
		KubernetesVersion: "1.32.0", OS: "ubuntu", Arch: "amd64", CNI: "vpc-cni",
		RoleBindings: []RoleBinding{{
			Role: "gpu-worker", NodeLevelCategories: []string{"kubernetes"}, Subset: []string{"kubelet"},
		}},
		ClusterCategories: []string{"accelerated-runtime"},
		Recipes: []RecipeBinding{
			{
				Category: "accelerated-runtime", RecipeID: "accelerated-runtime-1.32.0-ubuntu-amd64",
				RecipeDigest:        compositionDigest("a"),
				Criteria:            criteria.Criteria{Category: "accelerated-runtime", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"},
				AICRProfileIdentity: "h100-eks-ubuntu-training",
				AICR: &ir.AICRActivation{Service: "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training",
					RecipeDigest: compositionDigest("b"), GeneratorDigest: compositionDigest("c")},
				AICRBundle: &AICRBundleInputs{Deployer: "helm", StorageClass: "gp3",
					AcceleratedNodeSelectors:   []string{"nvidia.com/gpu.present=true"},
					AcceleratedNodeTolerations: []string{"nvidia.com/gpu=present:NoSchedule"},
					WorkloadSelectors:          []string{"nkd.nvidia.com/workload=training"}},
			},
			{
				Category: "kubernetes", RecipeID: "kubernetes-1.32.0-ubuntu-amd64",
				RecipeDigest: compositionDigest("d"),
				Criteria:     criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"},
				Components:   []string{"kubelet"},
			},
		},
	}
}

// TestVerifyRecipeIdentitiesRecomputesFromGraph is the regression for the identity
// seam: Validate accepts any well-formed recipeDigest, but VerifyRecipeIdentities must
// recompute it from the exact compiled graph and reject a fabricated one — the guarantee
// that a non-CLI caller cannot mint a composition whose identity the module accepts as a
// lie.
func TestVerifyRecipeIdentitiesRecomputesFromGraph(t *testing.T) {
	comp := func(roles ...string) catalog.Component {
		return catalog.Component{
			FromSource: true, Repo: "https://github.com/kubernetes/kubernetes",
			Commit: "70d3cc986aa8221cd1dfb1121852688902d3bf53", TreeHash: "8f82f106c65896250424161432791051e9f2cd99",
			ToolchainImage: "golang@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72",
			OutputType:     "host-package", BuildCommand: []string{"make"}, ArtifactPath: "out/artifact", WriteDir: "out",
			Tag: "v1.0.0", SourceDate: 1733939955, Roles: roles,
		}
	}
	cat := &catalog.Catalog{
		Recipes: []catalog.Recipe{{
			ID: "kubernetes", Criteria: criteria.Criteria{Category: "kubernetes"},
			Components: []string{"kubelet"},
		}},
		Components: map[string]catalog.Component{"kubelet": comp("worker")},
	}
	if err := cat.Validate(); err != nil {
		t.Fatal(err)
	}
	resolved, err := recipe.Resolve(cat, criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	composition := &ClusterComposition{
		SchemaVersion: CompositionSchemaVersion, ClusterType: TypeTenant,
		KubernetesVersion: "1.32.0", OS: "ubuntu", Arch: "amd64",
		RoleBindings: []RoleBinding{{Role: "worker", NodeLevelCategories: []string{"kubernetes"}, Subset: []string{"kubelet"}}},
		Recipes: []RecipeBinding{{
			Category: "kubernetes", RecipeID: resolved.RecipeID, RecipeDigest: digest,
			Criteria:   criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"},
			Components: []string{"kubelet"},
		}},
	}
	composition.Normalize()
	graphs := map[string]*ir.Graph{resolved.RecipeID: graph}
	if err := composition.VerifyRecipeIdentities(graphs); err != nil {
		t.Fatalf("authentic recipeDigest must verify against its compiled graph: %v", err)
	}
	// A fabricated but well-formed digest passes Validate yet must fail the recompute.
	fabricated := *composition
	fabricated.Recipes = []RecipeBinding{composition.Recipes[0]}
	fabricated.Recipes[0].RecipeDigest = compositionDigest("e")
	if err := fabricated.Validate(); err != nil {
		t.Fatalf("a well-formed fabricated digest still passes shape validation: %v", err)
	}
	if err := fabricated.VerifyRecipeIdentities(graphs); err == nil {
		t.Fatal("fabricated recipeDigest must be rejected by identity recompute")
	}
}

func TestClusterCompositionBindsExactRecipesWithoutReleaseAuthority(t *testing.T) {
	composition := tenantAcceleratedComposition()
	if err := composition.Validate(); err != nil {
		t.Fatal(err)
	}
	first, err := composition.Digest()
	if err != nil {
		t.Fatal(err)
	}
	changed := tenantAcceleratedComposition()
	changed.Recipes[0].AICR.Accelerator = "h200"
	second, err := changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("changed AICR activation did not move ClusterComposition identity")
	}
	changed = tenantAcceleratedComposition()
	changed.Recipes[0].AICRBundle.StorageClass = "io2"
	second, err = changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("changed AICR bundle input did not move ClusterComposition identity")
	}
}

func TestClusterCompositionRequiresEverySelectedCategoryRecipe(t *testing.T) {
	composition := tenantAcceleratedComposition()
	composition.Recipes = composition.Recipes[1:]
	if err := composition.Validate(); err == nil {
		t.Fatal("missing accelerated-runtime recipe was accepted")
	}
	composition = tenantAcceleratedComposition()
	composition.Recipes[0].AICR = nil
	if err := composition.Validate(); err == nil {
		t.Fatal("accelerated-runtime recipe without AICR activation was accepted")
	}
	composition = tenantAcceleratedComposition()
	composition.Recipes[0].AICRBundle = nil
	if err := composition.Validate(); err == nil {
		t.Fatal("accelerated-runtime recipe without AICR bundle inputs was accepted")
	}
}
