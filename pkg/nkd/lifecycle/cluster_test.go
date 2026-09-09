package lifecycle

import (
	"context"
	"testing"
)

func TestClusterRealizationBindsExactCompositionContentRoleAndMembers(t *testing.T) {
	definition := testDefinition(t)
	plan, err := Compile(context.Background(), "definition.yaml", definition, "environment.yaml", testEnvironment(), "test")
	if err != nil {
		t.Fatal(err)
	}
	run := testRun()
	run.Status = RunComplete
	run.Observed = []Artifact{{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("6", 64),
		RecipeDigest: plan.Recipes[0].RecipeDigest, Components: []string{"kubectl"}}}
	bound, err := bindClusterRealizations(definition, plan, run)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ContentSubject == "" || len(bound.Clusters) != 1 || bound.Clusters[0].CompositionDigest != plan.Compositions[0].Digest ||
		bound.Clusters[0].Members[0].ArtifactDigest != run.Observed[0].Digest || bound.Clusters[0].Members[0].Role != "worker" {
		t.Fatalf("cluster realization = %+v", bound)
	}

	substituted := *run
	substituted.Observed = cloneArtifacts(run.Observed)
	substituted.Observed[0].RecipeDigest = "sha256:" + repeat("9", 64)
	if _, err := bindClusterRealizations(definition, plan, &substituted); err == nil {
		t.Fatal("recipe substitution was accepted")
	}
}
