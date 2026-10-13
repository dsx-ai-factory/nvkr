// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
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
		{"legacy run", func(r *Run, _ *Artifact) { r.SchemaVersion = "nvkr.dev/run/v3" }},
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
	// Preparation consumes the validated frozen plan and does not need source credentials.
	plan, err := factory.LoadPlanContext(t.Context(), "../../../testdata/plans/nvke-m2.json")
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := plan.Definition, plan.Environment
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
	if run.Expected[index].ImageIndex.Children[0].Component != "nvke-controller" {
		t.Fatal("Run aliases compiled requirements")
	}
}

func TestPrepareExactInputsFreezesImageIndexRequirements(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*factory.ImageIndexPlan)
	}{
		{"child producer", func(p *factory.ImageIndexPlan) { p.Children[0].Component = "changed" }},
		{"required platform", func(p *factory.ImageIndexPlan) { p.RequiredPlatforms[0] = "changed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := factory.LoadPlanContext(t.Context(), "../../../testdata/plans/nvke-m2.json")
			if err != nil {
				t.Fatal(err)
			}
			expectation, err := PrepareExactInputs(plan.Definition, plan.Environment, plan)
			if err != nil {
				t.Fatal(err)
			}
			index := -1
			for i, output := range plan.LifecycleOutputs {
				if output.ImageIndex != nil {
					index = i
					break
				}
			}
			if index < 0 {
				t.Fatal("fixture lacks aggregate")
			}
			output := expectation.outputs[index]
			binding := &delivery.ImageIndexBinding{RequiredPlatforms: append([]string(nil), output.ImageIndex.RequiredPlatforms...)}
			for i, child := range output.ImageIndex.Children {
				binding.Children = append(binding.Children, delivery.ImageIndexChildBinding{Platform: child.Platform, BuildDigest: child.BuildDigest, Component: child.Component, ManifestDigest: "sha256:" + repeat(string(rune('a'+i)), 64), Size: 500, BuildSubject: "sha256:" + repeat(string(rune('c'+i)), 64)})
			}
			artifact := Artifact{Name: output.Name, Kind: OutputOCI, Digest: "sha256:" + repeat("9", 64), BuildDigest: output.BuildDigest, Components: append([]string(nil), output.Components...), ImageIndex: binding}
			check := func() {
				t.Helper()
				if err := factory.ValidateLifecycleOutputs([]factory.LifecycleOutputPlan{expectation.outputs[index]}, []Artifact{artifact}); err != nil {
					t.Fatal(err)
				}
			}
			check()
			tc.mutate(plan.LifecycleOutputs[index].ImageIndex)
			check()
			// A second expectation from the untouched prepared inputs must not
			// share either nested slice with the first expectation.
			fresh, err := factory.LoadPlanContext(t.Context(), "../../../testdata/plans/nvke-m2.json")
			if err != nil {
				t.Fatal(err)
			}
			other, err := PrepareExactInputs(fresh.Definition, fresh.Environment, fresh)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(other.outputs[index].ImageIndex)
			if err := factory.ValidateLifecycleOutputs([]factory.LifecycleOutputPlan{fresh.LifecycleOutputs[index]}, []Artifact{artifact}); err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}
