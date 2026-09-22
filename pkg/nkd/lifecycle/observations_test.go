package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

type binaryObservationFixture struct {
	plan       *factory.Plan
	slot       requiredOutput
	artifacts  map[string]factory.Artifact
	workspace  string
	payload    string
	resultPath string
	result     pipeline.ArtifactResult
	digest     string
	semantic   string
}

func prepareBinaryObservationFixture(t *testing.T) *binaryObservationFixture {
	t.Helper()
	definition, environment := testDefinition(t), testEnvironment()
	plan, err := Compile(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	run, err := PrepareRun(definition, environment, plan, "observation")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &binaryObservationFixture{plan: plan, slot: run.Expected[0], workspace: t.TempDir(), artifacts: map[string]factory.Artifact{}}
	for _, artifact := range plan.Graph.Artifacts {
		fixture.artifacts[artifact.ID] = artifact
	}
	input := fixture.artifacts["artifact/cli/kubectl"]
	fixture.payload = fixture.path(t, input.Path)
	if err := os.MkdirAll(filepath.Dir(fixture.payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.payload, []byte("exact component bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.result = fixtureProducerResult(t, plan, input, fixture.payload)
	fixture.resultPath = fixture.path(t, fixture.artifacts[fixture.slot.ComponentResults[0]].Path)
	writeCanonicalTestFile(t, fixture.resultPath, fixture.result)
	member := fixture.artifacts[fixture.slot.MemberArtifactID]
	bundlePath := fixture.path(t, member.Path)
	bundle, err := delivery.BuildBinaryBundle(t.Context(), bundlePath, delivery.BinaryBundleRequest{
		OS: "linux", Arch: "amd64", Purpose: member.Name,
		Files: []delivery.BinaryFileInput{{Name: "kubectl", Path: fixture.payload, InstallPath: "/usr/local/bin/kubectl", Mode: 0o755}},
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.PayloadDescriptor(t.Context(), bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.digest, fixture.semantic = descriptor.Digest, bundle.SemanticDigest
	request := testReleaseRequest(t,
		[]Artifact{{Name: member.Name, Kind: fixture.slot.Kind, Digest: fixture.digest, BuildDigest: fixture.slot.BuildDigest, Components: fixture.slot.Components}},
		[]delivery.ObjectInput{{Name: member.Name, Kind: member.Kind, Path: bundlePath, Layout: true, ArtifactType: member.MediaType, SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest}},
		map[string]string{"kubectl": fixture.result.SHA256})
	if _, err := delivery.BuildPublicationRoot(t.Context(), fixture.path(t, fixture.artifacts[fixture.slot.ArtifactID].Path), request); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *binaryObservationFixture) path(t *testing.T, planned string) string {
	t.Helper()
	path, err := lifecycleArtifactPath(planned, fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestObserveOutputReconstructsExactBundleAndPublicationMembership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *binaryObservationFixture)
		code   e.Code
	}{
		{name: "exact"},
		{name: "relocated workspace", mutate: func(t *testing.T, f *binaryObservationFixture) {
			moved := filepath.Join(t.TempDir(), "relocated")
			if err := os.Rename(f.workspace, moved); err != nil {
				t.Fatal(err)
			}
			f.workspace = moved
		}},
		{name: "nil plan", mutate: func(_ *testing.T, f *binaryObservationFixture) { f.plan = nil }, code: e.CodeInternal},
		{name: "raw payload absent", mutate: func(t *testing.T, f *binaryObservationFixture) {
			if err := os.Rename(f.payload, f.payload+".absent"); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
		{name: "missing frozen result", mutate: func(_ *testing.T, f *binaryObservationFixture) { delete(f.artifacts, f.slot.ComponentResults[0]) }, code: e.CodeReleaseIncomplete},
		{name: "incomplete frozen result mapping", mutate: func(_ *testing.T, f *binaryObservationFixture) { f.slot.ComponentResults = nil }, code: e.CodeReleaseIncomplete},
		{name: "sibling frozen result", mutate: func(_ *testing.T, f *binaryObservationFixture) {
			result := f.artifacts[f.slot.ComponentResults[0]]
			result.ID, result.BuildTarget = "result/sibling/kubectl", "sibling"
			f.artifacts[result.ID] = result
			f.slot.ComponentResults = []string{result.ID}
		}, code: e.CodeVerifyFailed},
		{name: "wrong frozen build", mutate: func(_ *testing.T, f *binaryObservationFixture) { f.slot.BuildDigest = "sha256:" + repeat("8", 64) }, code: e.CodeReleaseIncomplete},
		{name: "missing publication root", mutate: func(_ *testing.T, f *binaryObservationFixture) { delete(f.artifacts, f.slot.ArtifactID) }, code: e.CodeReleaseIncomplete},
		{name: "wrong publication member name", mutate: func(_ *testing.T, f *binaryObservationFixture) {
			member := f.artifacts[f.slot.MemberArtifactID]
			member.Name = "sibling"
			f.artifacts[f.slot.MemberArtifactID] = member
		}, code: e.CodeReleaseIncomplete},
		{name: "wrong publication member media type", mutate: func(_ *testing.T, f *binaryObservationFixture) {
			member := f.artifacts[f.slot.MemberArtifactID]
			member.MediaType = "application/x-substituted"
			f.artifacts[f.slot.MemberArtifactID] = member
		}, code: e.CodeVerifyFailed},
		{name: "changed packaged bytes", mutate: func(t *testing.T, f *binaryObservationFixture) {
			member := f.artifacts[f.slot.MemberArtifactID]
			member.Path = ".nkd/factory/outputs/repackaged"
			_, err := delivery.BuildBinaryBundle(t.Context(), f.path(t, member.Path), delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: member.Name,
				Files: []delivery.BinaryFileInput{{Name: "kubectl", Path: f.payload, InstallPath: "/different/kubectl", Mode: 0o755}}})
			if err != nil {
				t.Fatal(err)
			}
			f.artifacts[f.slot.MemberArtifactID] = member
		}, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := prepareBinaryObservationFixture(t)
			if tc.mutate != nil {
				tc.mutate(t, fixture)
			}
			observed, err := observeFixtureOutput(t.Context(), fixture.workspace, fixture.slot, fixture.artifacts, fixture.plan)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if observed.Name != "" || observed.Digest != "" {
					t.Fatal("failed observation exposed partial output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if observed.Name != fixture.slot.Name || observed.Digest != fixture.digest || observed.BuildDigest != fixture.slot.BuildDigest || !equalStrings(observed.Components, fixture.slot.Components) {
				t.Fatalf("observed=%+v", observed)
			}
			localized := make(map[string]factory.Artifact, len(fixture.artifacts))
			for id, artifact := range fixture.artifacts {
				if artifact.Path != "" {
					artifact.Path = fixture.path(t, artifact.Path)
				}
				localized[id] = artifact
			}
			payload, err := factory.ObserveLifecyclePayload(t.Context(), fixture.plan, fixture.plan.LifecycleOutputs[0], localized)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Digest != fixture.digest || payload.SemanticDigest != fixture.semantic {
				t.Fatalf("payload digest=%q semantic=%q", payload.Digest, payload.SemanticDigest)
			}
		})
	}
}

func TestBinaryObservationRechecksExactProducerMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pipeline.ArtifactResult)
	}{
		{"toolchain", func(r *pipeline.ArtifactResult) { r.Toolchain = "other@sha256:" + repeat("9", 64) }},
		{"builder", func(r *pipeline.ArtifactResult) { r.BuilderType = "nix" }},
		{"path", func(r *pipeline.ArtifactResult) { r.Path = "other/kubectl" }},
		{"size", func(r *pipeline.ArtifactResult) { r.Size++ }},
		{"negative size", func(r *pipeline.ArtifactResult) { r.Size = -1 }},
		{"digest", func(r *pipeline.ArtifactResult) { r.SHA256 = "sha256:" + repeat("9", 64) }},
		{"malformed digest", func(r *pipeline.ArtifactResult) { r.SHA256 = "mutable" }},
		{"component", func(r *pipeline.ArtifactResult) { r.Component = "sibling" }},
		{"output type", func(r *pipeline.ArtifactResult) { r.OutputType = "oci-image" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := prepareBinaryObservationFixture(t)
			tc.mutate(&fixture.result)
			writeCanonicalTestFile(t, fixture.resultPath, fixture.result)
			_, err := observeFixtureOutput(t.Context(), fixture.workspace, fixture.slot, fixture.artifacts, fixture.plan)
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestObserveDiskOutputRejectsPathDescriptionWithoutHeldExecutionResult(t *testing.T) {
	slot := requiredOutput{Name: "node-image", Kind: OutputDiskImage,
		ArtifactID: "artifact/node-image/image", MemberArtifactID: "artifact/node-image/image",
		BuildDigest: "sha256:" + strings.Repeat("b", 64), Components: []string{"image"},
		ComponentResults: []string{"result/node-image/image"}}
	artifacts := map[string]factory.Artifact{
		slot.ArtifactID: {ID: slot.ArtifactID, Name: "image", Kind: OutputDiskImage,
			BuildTarget: slot.Name, Component: "image", Path: ".nkd/factory/caller-supplied.qcow2"},
	}
	_, err := observeOutput(t.Context(), slot, artifacts, nil, nil)
	assertCode(t, err, e.CodeEngineCapability)
}

func TestObserveDiskOutputRecordsHeldFactoryObservation(t *testing.T) {
	run := testRun()
	run.Expected[0] = requiredOutput{Name: "node-image", Kind: OutputDiskImage,
		ArtifactID: "artifact/node-image/image", MemberArtifactID: "artifact/node-image/image",
		BuildDigest: run.Expected[0].BuildDigest, Components: []string{"image"},
		ComponentResults: []string{"result/node-image/image"}}
	plan := &factory.Plan{}
	observation := factory.DiskPayloadObservation{
		Digest: "sha256:" + strings.Repeat("4", 64), Size: 4096,
		BuildDigest: run.Expected[0].BuildDigest, Components: []string{"image"},
		Producer: "build-image", Component: "image", Format: "qcow2", Encryption: "none",
		Outcome: "succeeded", PlanDigest: run.PlanDigest,
	}
	observed, err := observeHeldDiskOutput(t.Context(), run.Expected[0], plan,
		func(ctx context.Context, gotPlan *factory.Plan, expected factory.LifecycleOutputPlan) (factory.DiskPayloadObservation, error) {
			if ctx != t.Context() || gotPlan != plan || expected.Name != run.Expected[0].Name ||
				expected.ArtifactID != run.Expected[0].MemberArtifactID ||
				expected.BuildDigest != run.Expected[0].BuildDigest ||
				!equalStrings(expected.Components, run.Expected[0].Components) {
				t.Fatalf("held observation request: plan=%p expected=%+v", gotPlan, expected)
			}
			return observation, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindObserved(run, []Artifact{observed})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Status != RunComplete || len(bound.Observed) != 1 || bound.Observed[0].Disk == nil ||
		bound.Observed[0].Digest != observation.Digest || bound.Observed[0].Disk.PlanDigest != run.PlanDigest ||
		bound.Observed[0].Disk.Size != observation.Size {
		t.Fatalf("recorded held disk observation = %+v", bound)
	}
}

func TestObserveDiskOutputsRejectMissingOrClosedExecutionBeforePathDescriptions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		closed bool
		code   e.Code
	}{
		{name: "missing execution", code: e.CodeEngineCapability},
		{name: "closed execution", closed: true, code: e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var execution *factory.LocalRunResult
			if tc.closed {
				execution = &factory.LocalRunResult{}
				if err := execution.Close(); err != nil {
					t.Fatal(err)
				}
			}
			slot := requiredOutput{Name: "node-image", Kind: OutputDiskImage,
				MemberArtifactID: "artifact/node-image/image"}
			artifacts := map[string]factory.Artifact{
				slot.MemberArtifactID: {ID: slot.MemberArtifactID, Kind: OutputDiskImage,
					Path: "/caller-supplied/outside-workspace.qcow2"},
			}
			// Neither a missing Plan nor an invalid path may mask the owning
			// factory result's capability and lifetime checks for a disk output.
			observed, err := observeOutputs(t.Context(), t.TempDir(), []requiredOutput{slot}, artifacts, nil, execution)
			assertCode(t, err, tc.code)
			if observed != nil {
				t.Fatal("failed disk observation exposed partial output")
			}
		})
	}
}

func TestPreparedRunDescriptionOwnsDiskMetadata(t *testing.T) {
	run := testRun()
	run.Observed = []Artifact{{Name: "node-image", Kind: OutputDiskImage,
		Components: []string{"image"},
		Disk:       &delivery.DiskOutput{PlanDigest: run.PlanDigest, Size: 4096}}}
	prepared := &PreparedRun{run: run}
	first := prepared.Description()
	first.Observed[0].Components[0] = "substituted"
	first.Observed[0].Disk.PlanDigest = "substituted"
	first.Observed[0].Disk.Size = 1
	second := prepared.Description()
	if second.Observed[0].Components[0] != "image" || second.Observed[0].Disk.PlanDigest != run.PlanDigest ||
		second.Observed[0].Disk.Size != 4096 {
		t.Fatalf("description mutation changed prepared disk observation: %+v", second.Observed[0])
	}
	if second.Observed[0].Disk == run.Observed[0].Disk || second.Observed[0].Disk == first.Observed[0].Disk {
		t.Fatal("disk descriptions share mutable metadata")
	}
}
