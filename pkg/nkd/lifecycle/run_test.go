package lifecycle

import (
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestBindObservedPreservesFrozenRequiredOutputs(t *testing.T) {
	run := testRun()
	artifact := Artifact{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64),
		RecipeDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}}
	bound, err := bindObserved(run, []Artifact{artifact})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Status != RunComplete || len(bound.Expected) != 1 || len(run.Observed) != 0 || run.Status != RunPending {
		t.Fatalf("binding mutated requirements or input attempt: bound=%+v input=%+v", bound, run)
	}
	if _, err := bindObserved(bound, []Artifact{artifact}); err == nil {
		t.Fatal("completed attempt accepted replacement observations")
	} else {
		assertCode(t, err, nkderr.CodeImmutableViolation)
	}
}

func TestBindObservedFailsClosedOnIncompleteOrSubstitutedOutput(t *testing.T) {
	tests := []struct {
		name      string
		artifacts []Artifact
		want      nkderr.Code
		status    string
	}{
		{name: "missing", status: RunFailed},
		{name: "unexpected", artifacts: []Artifact{{Name: "other", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), RecipeDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}}}, want: nkderr.CodeReleaseIncomplete},
		{name: "wrong recipe", artifacts: []Artifact{{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), RecipeDigest: "sha256:" + repeat("6", 64), Components: []string{"kubectl"}}}, want: nkderr.CodeVerifyFailed},
		{name: "wrong components", artifacts: []Artifact{{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), RecipeDigest: "sha256:" + repeat("5", 64), Components: []string{"other"}}}, want: nkderr.CodeVerifyFailed},
		{name: "duplicate", artifacts: []Artifact{
			{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), RecipeDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}},
			{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), RecipeDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}},
		}, want: nkderr.CodeReleaseIncomplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bound, err := bindObserved(testRun(), test.artifacts)
			if test.want != "" {
				if err == nil {
					t.Fatal("invalid observations accepted")
				}
				assertCode(t, err, test.want)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bound.Status != test.status || len(bound.Expected) != 1 {
				t.Fatalf("bound run = %+v", bound)
			}
		})
	}
}

func testRun() *Run {
	return &Run{SchemaVersion: RunSchemaVersion, Attempt: "attempt-1", DefinitionDigest: "sha256:" + repeat("1", 64),
		EnvironmentDigest: "sha256:" + repeat("2", 64), PlanDigest: "sha256:" + repeat("3", 64),
		Lane: LaneUnqualified, Status: RunPending,
		Expected: []requiredOutput{{Name: "cli", Kind: OutputExecutable, ArtifactID: "package/cli", MemberArtifactID: "package/cli-bundle", RecipeDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}, ComponentResults: []string{"result/cli-recipe/kubectl"}}}}
}
