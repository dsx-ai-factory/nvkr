package lifecycle

import (
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func indexRunFixture(t *testing.T) (*Run, Artifact) {
	t.Helper()
	run := testRun()
	required := &factory.ImageIndexPlan{RequiredPlatforms: []string{"linux/amd64", "linux/arm64"}}
	observed := &delivery.ImageIndexBinding{RequiredPlatforms: []string{"linux/amd64", "linux/arm64"}}
	logical := []ir.ImageIndexBuildChild{}
	results := []string{}
	for i, platform := range required.RequiredPlatforms {
		target := "cwe/" + platform
		build := "sha256:" + repeat(string(rune('a'+i)), 64)
		required.Children = append(required.Children, factory.ImageIndexChildPlan{Platform: platform, BuildTarget: target, BuildDigest: build, Component: "controller", ArtifactID: "artifact/" + target + "/controller", ResultArtifactID: "result/" + target + "/controller", SBOMArtifactID: "sbom/" + target})
		observed.Children = append(observed.Children, delivery.ImageIndexChildBinding{Platform: platform, BuildDigest: build, Component: "controller", ManifestDigest: "sha256:" + repeat(string(rune('c'+i)), 64), Size: 500, BuildSubject: "sha256:" + repeat(string(rune('e'+i)), 64)})
		logical = append(logical, ir.ImageIndexBuildChild{Platform: platform, BuildDigest: build, Component: "controller"})
		results = append(results, "result/"+target+"/controller")
	}
	digest, err := ir.ImageIndexBuildDigest(required.RequiredPlatforms, logical)
	if err != nil {
		t.Fatal(err)
	}
	run.Expected = []requiredOutput{{Name: "cwe", Kind: OutputOCI, ArtifactID: "package/cwe", MemberArtifactID: "package/cwe", BuildDigest: digest, Components: []string{"controller"}, ComponentResults: results, ImageIndex: required}}
	return run, Artifact{Name: "cwe", Kind: OutputOCI, Digest: "sha256:" + repeat("9", 64), BuildDigest: digest, Components: []string{"controller"}, ImageIndex: observed}
}

func TestImageIndexRunCompletenessAndDefensiveCopies(t *testing.T) {
	run, artifact := indexRunFixture(t)
	bound, err := bindObserved(run, []Artifact{artifact})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Status != RunComplete {
		t.Fatal("aggregate did not complete")
	}
	artifact.ImageIndex.Children[0].BuildSubject = "mutated"
	run.Expected[0].ImageIndex.RequiredPlatforms[0] = "mutated"
	if bound.Observed[0].ImageIndex.Children[0].BuildSubject == "mutated" || bound.Expected[0].ImageIndex.RequiredPlatforms[0] == "mutated" {
		t.Fatal("Run aliases aggregate metadata")
	}
	prepared := &PreparedRun{run: bound}
	description := prepared.Description()
	description.Observed[0].ImageIndex.Children[0].Component = "mutated"
	description.Expected[0].ImageIndex.Children[0].Component = "mutated"
	if prepared.Description().Observed[0].ImageIndex.Children[0].Component != "controller" || prepared.Description().Expected[0].ImageIndex.Children[0].Component != "controller" {
		t.Fatal("description aliases aggregate metadata")
	}
}

func TestImageIndexRunRejectsMissingAndSiblingBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Run, *Artifact)
	}{
		{"omitted aggregate", func(_ *Run, a *Artifact) { a.ImageIndex = nil }},
		{"missing child", func(_ *Run, a *Artifact) { a.ImageIndex.Children = a.ImageIndex.Children[:1] }},
		{"shrunk successful subset", func(_ *Run, a *Artifact) {
			a.ImageIndex.Children = a.ImageIndex.Children[:1]
			a.ImageIndex.RequiredPlatforms = a.ImageIndex.RequiredPlatforms[:1]
		}},
		{"foreign child build", func(_ *Run, a *Artifact) { a.ImageIndex.Children[1].BuildDigest = a.ImageIndex.Children[0].BuildDigest }},
		{"missing frozen result", func(r *Run, _ *Artifact) { r.Expected[0].ComponentResults = r.Expected[0].ComponentResults[:1] }},
		{"sibling frozen result", func(r *Run, _ *Artifact) { r.Expected[0].ComponentResults[1] = r.Expected[0].ComponentResults[0] }},
		{"legacy run", func(r *Run, _ *Artifact) { r.SchemaVersion = "nkd.dev/run/v3" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, a := indexRunFixture(t)
			tc.mutate(r, &a)
			_, err := bindObserved(r, []Artifact{a})
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestPrepareRunFreezesExplicitNativeImageUses(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	definitionPath, environmentPath := "definitions/nke-deploy/definition.yaml", "definitions/nke-deploy/environment-github.yaml"
	definition, err := LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range definition.Outputs {
		if definition.Outputs[i].Name == "cwe" {
			definition.Outputs[i].Platform = ""
			definition.Outputs[i].Platforms = []string{"linux/amd64", "linux/arm64"}
		}
	}
	plan, err := factory.CompileDocuments(t.Context(), definitionPath, definition, environmentPath, environment, "test (test)")
	if err != nil {
		t.Fatal(err)
	}
	run, err := PrepareRun(definition, environment, plan, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRun(run); err != nil {
		t.Fatal(err)
	}
	index := -1
	for i, slot := range run.Expected {
		if slot.Name == "cwe" {
			index = i
		}
	}
	if index < 0 || run.Expected[index].ImageIndex == nil || len(run.Expected[index].ComponentResults) != 2 {
		t.Fatal("Run lost native requirements")
	}
	plan.LifecycleOutputs[index].ImageIndex.Children[0].Component = "mutated"
	if run.Expected[index].ImageIndex.Children[0].Component != "nke-controller" {
		t.Fatal("Run aliases compiled requirements")
	}
}
