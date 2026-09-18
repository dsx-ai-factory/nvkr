package lifecycle

import (
	"context"
	"testing"
)

func TestReleaseContentBindsExactOutputsAndBuilds(t *testing.T) {
	definition := testDefinition(t)
	plan, err := Compile(context.Background(), "definition.yaml", definition, "environment.yaml", testEnvironment(), "test")
	if err != nil {
		t.Fatal(err)
	}
	run := testRun()
	run.Status = RunComplete
	run.Observed = []Artifact{{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("6", 64),
		BuildDigest: plan.Builds[0].BuildDigest, Components: []string{"kubectl"}}}
	bound, err := bindReleaseContent(plan, run)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ContentSubject == "" || bound.Observed[0].Digest != run.Observed[0].Digest {
		t.Fatalf("release content = %+v", bound)
	}

	substituted := *run
	substituted.Observed = cloneArtifacts(run.Observed)
	substituted.Observed[0].BuildDigest = "sha256:" + repeat("9", 64)
	if _, err := bindReleaseContent(plan, &substituted); err == nil {
		t.Fatal("build substitution was accepted")
	}
}
