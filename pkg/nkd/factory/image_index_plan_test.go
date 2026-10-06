package factory

import (
	"reflect"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func imageIndexTestPlan(t *testing.T) *Plan {
	t.Helper()
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nkd-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	return compileImageIndexFixture(t, plan, plan.Environment)
}

// Compile from checked-in resolved snapshots, without fetching private pin sets.
func compileImageIndexFixture(t *testing.T, original *Plan, environment authored.Environment) *Plan {
	t.Helper()
	stack, err := catalog.ApplyStack(original.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	targetStacks := make(map[string]*catalog.Stack)
	for _, output := range original.Definition.Outputs {
		if output.Catalog == nil {
			continue
		}
		for _, build := range original.Builds {
			if build.Name == output.Name {
				targetStacks[output.Name], err = catalog.ApplyStack(build.CatalogSnapshot)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if targetStacks[output.Name] == nil {
			t.Fatalf("missing catalog for %s", output.Name)
		}
	}
	plan, err := compileWithTargetStacks(t.Context(), original.Definition, environment, stack, targetStacks,
		original.GeneratorVersion, original.DefinitionPath, original.EnvironmentPath)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestImageIndexPlanFreezesNativeMembershipAndEvidence(t *testing.T) {
	plan := imageIndexTestPlan(t)
	var output LifecycleOutputPlan
	for _, candidate := range plan.LifecycleOutputs {
		if candidate.Name == "cwe" {
			output = candidate
		}
	}
	if output.ImageIndex == nil || len(output.ImageIndex.Children) != 2 || len(plan.LifecycleOutputs) != 8 || len(plan.Builds) != 9 {
		t.Fatalf("incorrect aggregate/native membership: %+v", output)
	}
	children := make([]ir.ImageIndexBuildChild, 0, 2)
	nodes, artifacts := planIndex(plan)
	for _, child := range output.ImageIndex.Children {
		if child.BuildTarget != "cwe/"+child.Platform {
			t.Fatal("noninjective child target")
		}
		for _, id := range []string{child.ArtifactID, child.ResultArtifactID, child.SBOMArtifactID, "observation/" + child.BuildTarget} {
			if !containsString(nodes[output.ArtifactID].Inputs, id) {
				t.Fatalf("aggregate omitted %s", id)
			}
		}
		route, err := plan.GitHubExecutionFor(nodes[artifacts[child.ArtifactID].Producer])
		if err != nil {
			t.Fatal(err)
		}
		if route.Platform != child.Platform || route.RunsOn != plan.Environment.GitHubRunners[child.Platform] {
			t.Fatalf("wrong native route: %+v", route)
		}
		children = append(children, ir.ImageIndexBuildChild{Platform: child.Platform, BuildDigest: child.BuildDigest, Component: child.Component})
	}
	digest, err := ir.ImageIndexBuildDigest(output.ImageIndex.RequiredPlatforms, children)
	if err != nil || digest != output.BuildDigest {
		t.Fatalf("aggregate identity=%s,%v", digest, err)
	}
	clone := output.ImageIndex.Clone()
	clone.RequiredPlatforms[0] = "changed"
	clone.Children[0].Component = "changed"
	if reflect.DeepEqual(clone, output.ImageIndex) {
		t.Fatal("requirements not independently owned")
	}
	if _, err := RenderGitHub(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
}

func TestImageIndexPlanRejectsIncompleteOrSubstitutedMembership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Plan, int)
		code   e.Code
	}{
		{"missing child", func(p *Plan, i int) {
			p.LifecycleOutputs[i].ImageIndex.Children = p.LifecycleOutputs[i].ImageIndex.Children[:1]
		}, e.CodeUnverifiedRef},
		{"changed required set", func(p *Plan, i int) {
			p.LifecycleOutputs[i].ImageIndex.RequiredPlatforms = p.LifecycleOutputs[i].ImageIndex.RequiredPlatforms[:1]
		}, e.CodeUnverifiedRef},
		{"sibling result", func(p *Plan, i int) {
			p.LifecycleOutputs[i].ImageIndex.Children[1].ResultArtifactID = p.LifecycleOutputs[i].ImageIndex.Children[0].ResultArtifactID
		}, e.CodeUnverifiedRef},
		{"missing native build", func(p *Plan, _ int) { p.Builds = p.Builds[:len(p.Builds)-1] }, e.CodeUnverifiedRef},
		{"missing evidence edge", func(p *Plan, i int) {
			for n := range p.Graph.Nodes {
				if p.Graph.Nodes[n].ID == p.LifecycleOutputs[i].ArtifactID {
					ids := p.Graph.Nodes[n].Inputs
					for j, id := range ids {
						if id == p.LifecycleOutputs[i].ImageIndex.Children[0].SBOMArtifactID {
							p.Graph.Nodes[n].Inputs = append(ids[:j:j], ids[j+1:]...)
							break
						}
					}
				}
			}
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := imageIndexTestPlan(t)
			index := -1
			for i, o := range p.LifecycleOutputs {
				if o.Name == "cwe" {
					index = i
				}
			}
			tc.mutate(p, index)
			assertFactoryErrorCode(t, p.Validate(), tc.code)
		})
	}
}
