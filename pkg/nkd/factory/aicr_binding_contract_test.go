package factory

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/cluster"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestAICRExecutorAdmitsExactActivationBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Plan, *Node)
		code e.Code
	}{
		{"missing recipe", func(p *Plan, _ *Node) { p.Recipes = nil }, e.CodePipelineDrift},
		{"missing activation", func(p *Plan, _ *Node) { p.Recipes[0].Graph.AICR = nil }, e.CodePipelineDrift},
		{"missing generator input", func(_ *Plan, n *Node) { n.Inputs = nil }, e.CodePipelineDrift},
		{"multiple generator inputs", func(_ *Plan, n *Node) { n.Inputs = []string{"generator", "other"} }, e.CodePipelineDrift},
		{"invalid profile", func(p *Plan, _ *Node) { p.Recipes[0].AICRProfileIdentity = "" }, e.CodeInvalidCatalog},
		{"missing executable", func(*Plan, *Node) {}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pin := "sha256:" + strings.Repeat("a", 64)
			bundle := &cluster.AICRBundleInputs{Deployer: "helm", StorageClass: "gp3", AcceleratedNodeSelectors: []string{"gpu=true"}, AcceleratedNodeTolerations: []string{"gpu=true:NoSchedule"}, WorkloadSelectors: []string{"workload=training"}}
			plan := &Plan{Recipes: []RecipePlan{{Name: "accelerated", RecipeDigest: pin, AICRProfileIdentity: "h100-eks",
				Graph: &ir.Graph{AICR: &ir.AICRActivation{RecipeDigest: pin, GeneratorDigest: pin, Service: "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training"}}}},
				Compositions: []CompositionPlan{{Composition: cluster.ClusterComposition{Recipes: []cluster.RecipeBinding{{RecipeDigest: pin, AICRBundle: bundle}}}}}}
			node := Node{ID: "aicr/accelerated", RecipeTarget: "accelerated", Inputs: []string{"generator"}, Outputs: []string{"bundle"}}
			tc.edit(plan, &node)
			parent := t.TempDir()
			err := executeAICR(t.Context(), plan, node, map[string]Artifact{"generator": {Path: filepath.Join(parent, "absent-generator")}, "bundle": {Path: filepath.Join(parent, "output", "bundle")}}, io.Discard, io.Discard)
			want := tc.code
			if want == "" {
				want = e.CodeEngineCapability
				if runtime.GOOS == "linux" {
					want = e.CodeUnsupported
				}
			}
			assertFactoryErrorCode(t, err, want)
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed admission produced output: %v, %v", entries, err)
			}
		})
	}
}

func TestAICRBundleLookupRequiresMatchingRecipeIdentity(t *testing.T) {
	want := &cluster.AICRBundleInputs{Deployer: "helm", StorageClass: "gp3"}
	plan := &Plan{Compositions: []CompositionPlan{
		{Composition: cluster.ClusterComposition{Recipes: []cluster.RecipeBinding{{RecipeDigest: "other", AICRBundle: &cluster.AICRBundleInputs{Deployer: "flux"}}}}},
		{Composition: cluster.ClusterComposition{Recipes: []cluster.RecipeBinding{{RecipeDigest: "wanted"}, {RecipeDigest: "wanted", AICRBundle: want}}}},
	}}
	for _, tc := range []struct {
		digest string
		want   *cluster.AICRBundleInputs
	}{{"wanted", want}, {"absent", nil}} {
		t.Run(tc.digest, func(t *testing.T) {
			if got := aicrBundleForRecipe(plan, tc.digest); got != tc.want {
				t.Fatalf("bundle = %+v; want %+v", got, tc.want)
			}
		})
	}
}
