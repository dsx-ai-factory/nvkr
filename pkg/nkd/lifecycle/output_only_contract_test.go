package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func TestDefinitionRejectsRetiredDeploymentFieldsBeforeCompilation(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		output      bool
	}{
		{"clusters", "clusters", false}, {"context", "context", false},
		{"output role", "role", true}, {"output category", "category", true},
		{"output context", "context", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := canonical.Marshal(testDefinition(t))
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			target := document
			if tc.output {
				target = document["outputs"].([]any)[0].(map[string]any)
			}
			target[tc.field] = "retired"
			raw, err = canonical.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "definition.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			plan, err := CompileFiles(t.Context(), path, "absent-environment.yaml", "test")
			assertCode(t, err, e.CodeInvalidCatalog)
			if plan != nil {
				t.Fatal("retired field reached compiler")
			}
		})
	}
}

func TestAICROutputBundleIsIsolatedAndBoundToBuildIdentity(t *testing.T) {
	t.Chdir(filepath.Join("..", "..", ".."))
	definitionPath := "definitions/aicr/definition.yaml"
	definition, err := LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environmentPath := "definitions/aicr/environment-local.yaml"
	environment, err := LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	environment.Publication = nil
	first := definition.Outputs[0]
	first.Name = "first"
	firstAICR := *first.AICR
	firstAICR.Bundle = &aicr.BundleInputs{StorageClass: "fast", AcceleratedNodeSelectors: []string{"gpu=true"}}
	first.AICR = &firstAICR
	second := first
	second.Name = "second"
	secondAICR := firstAICR
	secondAICR.Bundle = &aicr.BundleInputs{Deployer: "flux", StorageClass: "durable"}
	second.AICR = &secondAICR
	definition.Outputs = []DefinitionOutput{first, second}
	plan, err := Compile(t.Context(), definitionPath, definition, environmentPath, environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Builds) != 2 || plan.Builds[0].BuildDigest == plan.Builds[1].BuildDigest {
		t.Fatal("different renderer settings collapsed build identities")
	}
	if plan.Builds[0].Graph.AICR.Bundle.Deployer != "helm" || plan.Builds[1].Graph.AICR.Bundle.Deployer != "flux" {
		t.Fatal("effective renderer settings were not compiled per output")
	}
	definition.Outputs[0].AICR.Bundle.AcceleratedNodeSelectors[0] = "mutated=true"
	if plan.Builds[0].Graph.AICR.Bundle.AcceleratedNodeSelectors[0] != "gpu=true" {
		t.Fatal("compiled graph aliases authored settings")
	}
	for _, tc := range []struct {
		name   string
		change func(*factory.Plan)
		code   e.Code
	}{
		{"tampered settings", func(p *factory.Plan) { p.Builds[0].Graph.AICR.Bundle.StorageClass = "tampered" }, e.CodeUnverifiedRef},
		{"omitted effective deployer", func(p *factory.Plan) { p.Builds[0].Graph.AICR.Bundle.Deployer = "" }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := canonical.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			var changed factory.Plan
			if err := canonical.UnmarshalExact(raw, &changed); err != nil {
				t.Fatal(err)
			}
			tc.change(&changed)
			assertCode(t, changed.Validate(), tc.code)
		})
	}
}
