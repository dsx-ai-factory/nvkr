package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
)

func TestHostedRestoresRequireEverySuccessfulCarrier(t *testing.T) {
	for _, tc := range []struct {
		name, status   string
		omit, canceled bool
		code           e.Code
	}{
		{name: "all restored", status: "restored:success\n"},
		{name: "provider failed", status: "job-failed\n", code: e.CodeProvenanceMissing},
		{name: "missing artifact", status: "missing\n", code: e.CodeProvenanceMissing},
		{name: "restore failed", status: "restore-failed\n", code: e.CodeProvenanceMissing},
		{name: "noncanonical status", status: "restored:success", code: e.CodeProvenanceMissing},
		{name: "omitted outcome", omit: true, code: e.CodeProvenanceMissing},
		{name: "canceled", canceled: true, code: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "factory")
			plan := &Plan{Graph: ArtifactGraph{Artifacts: []Artifact{{ID: "artifact/one"}, {ID: "artifact/two"}}}}
			for i, a := range plan.Graph.Artifacts {
				if i == 1 && tc.omit {
					continue
				}
				raw := "restored:success\n"
				if i == 1 {
					raw = tc.status
				}
				path := HostedRestoreOutcomePath(workspace, a.ID)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			err := ValidateHostedRestores(ctx, workspace, plan)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, tc.code)
			}
		})
	}
	assertFactoryErrorCode(t, ValidateHostedRestores(context.Background(), t.TempDir(), nil), e.CodeUsage)
}

func TestHostedObservationReopensExactInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*factoryObservation, *Plan, string)
		code   e.Code
	}{
		{name: "exact observation"},
		{name: "wrong schema", change: func(o *factoryObservation, _ *Plan, _ string) { o.SchemaVersion = "wrong" }, code: e.CodeProvenanceMissing},
		{name: "wrong recipe", change: func(o *factoryObservation, _ *Plan, _ string) { o.RecipeDigest = pipeline.FileDigest([]byte("other")) }, code: e.CodeProvenanceMissing},
		{name: "wrong tier", change: func(o *factoryObservation, _ *Plan, _ string) { o.Qualification = TierPipelineAttested }, code: e.CodeProvenanceMissing},
		{name: "missing input", change: func(o *factoryObservation, _ *Plan, _ string) { o.Inputs = nil }, code: e.CodeProvenanceMissing},
		{name: "unbound input", change: func(o *factoryObservation, _ *Plan, _ string) { o.Inputs[0].ArtifactID = "other" }, code: e.CodeProvenanceMissing},
		{name: "wrong kind", change: func(o *factoryObservation, _ *Plan, _ string) { o.Inputs[0].Kind = "other" }, code: e.CodeProvenanceMissing},
		{name: "changed bytes", change: func(_ *factoryObservation, _ *Plan, p string) {
			if err := os.WriteFile(p, []byte("changed"), 0600); err != nil {
				panic(err)
			}
		}, code: e.CodeVerifyFailed},
		{name: "missing recipe observation", change: func(_ *factoryObservation, p *Plan, _ string) {
			p.Recipes = append(p.Recipes, RecipePlan{Name: "missing", RecipeDigest: pipeline.FileDigest([]byte("missing"))})
		}, code: e.CodeProvenanceMissing},
		{name: "path escape", change: func(_ *factoryObservation, p *Plan, _ string) { p.Graph.Artifacts[0].Path = "outside" }, code: e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "input")
			if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
				t.Fatal(err)
			}
			digest, err := digestFactoryPath(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			recipeDigest := pipeline.FileDigest([]byte("recipe"))
			plan := &Plan{Recipes: []RecipePlan{{Name: "one", RecipeDigest: recipeDigest}}, Graph: ArtifactGraph{
				Nodes:     []Node{{ID: "observe", RecipeTarget: "one", Inputs: []string{"input"}}},
				Artifacts: []Artifact{{ID: "input", Kind: "executable", Path: ".nkd/factory/input"}, {ID: "observation", Kind: string(TierTerminalObservation), Producer: "observe", Path: ".nkd/factory/observation.json"}},
			}}
			observation := factoryObservation{SchemaVersion: "nkd.dev/factory-observation/v1", Qualification: TierStaticUnqualified, RecipeDigest: recipeDigest, Inputs: []factoryObservedInput{{ArtifactID: "input", Kind: "executable", Digest: digest}}}
			if tc.change != nil {
				tc.change(&observation, plan, path)
			}
			raw, err := canonical.Marshal(observation)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "observation.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			err = ValidateHostedOutcomes(context.Background(), workspace, plan)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, tc.code)
			}
		})
	}
	assertFactoryErrorCode(t, ValidateHostedOutcomes(context.Background(), t.TempDir(), nil), e.CodeUsage)
}

func TestHostedProductionPredicateRejectsPlanOrPolicySubstitution(t *testing.T) {
	signing := dryRunSigningProfile()
	plan := signedTestPlan(t, signing, ghcrTarget())
	run := hostedPredicateRun{builderID: "local", invocationID: "https://example.test/runs/1", startedOn: time.Unix(100, 0).UTC(), finishedOn: time.Unix(101, 0).UTC()}
	signing.Identity = "^local$"
	raw, err := productionPredicateForRun(plan, signing, run)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		change   func(*provenance.Predicate)
		identity string
		code     e.Code
	}{
		{name: "exact predicate", identity: "^local$"},
		{name: "wrong identity", identity: "^other$", code: e.CodeVerifyFailed},
		{name: "invalid identity regex", identity: "[", code: e.CodeVerifyFailed},
		{name: "changed external input", identity: "^local$", change: func(p *provenance.Predicate) {
			p.BuildDefinition.ExternalParameters["runPlanReference"] = "substituted"
		}, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var predicate provenance.Predicate
			if err := canonical.UnmarshalExact(raw, &predicate); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(&predicate)
			}
			candidate, err := canonical.Marshal(predicate)
			if err != nil {
				t.Fatal(err)
			}
			policy := *signing
			policy.Identity = tc.identity
			err = validateHostedProductionPredicate(candidate, plan, &policy)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, tc.code)
			}
		})
	}
}

func TestProductionBuildTypeUsesStrongestNetworkRequirement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nodes []ir.Node
		want  string
	}{
		{name: "acquired", nodes: []ir.Node{{Kind: ir.KindPull}}, want: provenance.BuildTypeAcquired},
		{name: "offline", nodes: []ir.Node{{Kind: ir.KindBuild, Network: ir.NetworkNone}}, want: provenance.BuildTypeHermeticBuild},
		{name: "wrapped", nodes: []ir.Node{{Kind: ir.KindBuild, BuilderType: ir.BuilderBuildKit, Network: ir.NetworkDefault}}, want: provenance.BuildTypeWrappedBuild},
		{name: "mixed", nodes: []ir.Node{{Kind: ir.KindBuild, Network: ir.NetworkNone}, {Kind: ir.KindBuild, BuilderType: ir.BuilderBuildKit, Network: ir.NetworkDefault}}, want: provenance.BuildTypeWrappedBuild},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &Plan{Recipes: []RecipePlan{{Graph: &ir.Graph{Nodes: tc.nodes}}}}
			if got := productionBuildType(plan); got != tc.want {
				t.Fatalf("build type=%s, want %s", got, tc.want)
			}
		})
	}
}
