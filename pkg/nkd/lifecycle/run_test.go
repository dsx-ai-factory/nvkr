package lifecycle

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestBindObservedPreservesFrozenRequiredOutputs(t *testing.T) {
	run := testRun()
	artifact := Artifact{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64),
		BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}}
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
		{name: "unexpected", artifacts: []Artifact{{Name: "other", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}}}, want: nkderr.CodeReleaseIncomplete},
		{name: "wrong build", artifacts: []Artifact{{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), BuildDigest: "sha256:" + repeat("6", 64), Components: []string{"kubectl"}}}, want: nkderr.CodeVerifyFailed},
		{name: "wrong components", artifacts: []Artifact{{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"other"}}}, want: nkderr.CodeVerifyFailed},
		{name: "duplicate", artifacts: []Artifact{
			{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}},
			{Name: "cli", Kind: OutputExecutable, Digest: "sha256:" + repeat("4", 64), BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}},
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

func TestBindObservedDiskRequiresExactPlanBoundObservation(t *testing.T) {
	run := testRun()
	run.Expected[0].Kind = OutputDiskImage
	run.Expected[0].Components = []string{"agent", "image"}
	run.Expected[0].ComponentResults = []string{"result/node-image/agent", "result/node-image/image"}
	artifact := Artifact{
		Name: "cli", Kind: OutputDiskImage, Digest: "sha256:" + strings.Repeat("4", 64),
		BuildDigest: run.Expected[0].BuildDigest, Components: append([]string(nil), run.Expected[0].Components...),
		Disk: &delivery.DiskOutput{
			PayloadDigest: "sha256:" + strings.Repeat("4", 64),
			Producer:      "build-image", Component: "image", Format: "qcow2", Encryption: "none",
			Size: 4096, PlanDigest: run.PlanDigest, Outcome: "succeeded",
		},
	}
	bound, err := bindObserved(run, []Artifact{artifact})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Status != RunComplete || bound.Observed[0].Disk == nil || bound.Observed[0].Disk.PlanDigest != run.PlanDigest {
		t.Fatalf("bound disk observation = %+v", bound)
	}
	artifact.Disk.Size++
	if bound.Observed[0].Disk.Size != 4096 {
		t.Fatal("bound disk observation retained caller-mutable metadata")
	}

	changed := artifact
	changed.Disk = &delivery.DiskOutput{}
	*changed.Disk = *artifact.Disk
	changed.Disk.PlanDigest = "sha256:" + strings.Repeat("9", 64)
	assertCode(t, bindObservedError(run, changed), nkderr.CodeVerifyFailed)
}

func bindObservedError(run *Run, artifact Artifact) error {
	_, err := bindObserved(run, []Artifact{artifact})
	return err
}

func testRun() *Run {
	return &Run{SchemaVersion: RunSchemaVersion, Attempt: "attempt-1", DefinitionDigest: "sha256:" + repeat("1", 64),
		EnvironmentDigest: "sha256:" + repeat("2", 64), PlanDigest: "sha256:" + repeat("3", 64),
		Lane: LaneUnqualified, Status: RunPending,
		Expected: []requiredOutput{{Name: "cli", Kind: OutputExecutable, ArtifactID: "package/cli", MemberArtifactID: "package/cli-bundle", BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}, ComponentResults: []string{"result/cli/kubectl"}}}}
}
