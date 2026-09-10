package factory

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestGitHubActionPinsBindRenderingAndPlanIdentity(t *testing.T) {
	plan := githubTestPlan(t)
	before, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	original := plan.Actions["checkout"]
	changed := original
	changed.SHA = strings.Repeat("b", 40)
	changed.Version = "v7.0.2"
	plan.Actions["checkout"] = changed
	after, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if before.PlanDigest == after.PlanDigest {
		t.Fatal("action change did not move Run identity")
	}
	if !bytes.Contains(after.Files[GitHubWorkflowPath], []byte(changed.Uses())) || bytes.Contains(after.Files[GitHubWorkflowPath], []byte(original.Uses())) {
		t.Fatal("renderer did not consume new pin")
	}
	if err := ValidateGitHubBundle(context.Background(), plan, before); err == nil {
		t.Fatal("old workflow accepted under new action pins")
	}
	delete(plan.Actions, "checkout")
	bundle, err := RenderGitHub(context.Background(), plan)
	var coded *e.Error
	if bundle != nil || !errors.As(err, &coded) || coded.Code != e.CodeUnpinnedSource {
		t.Fatalf("missing action: %v, %v", bundle, err)
	}
}
