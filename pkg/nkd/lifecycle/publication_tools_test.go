package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCatalogToolsAndDerivedPublication(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	definitionPath := "definitions/nke-v1/definition.yaml"
	environmentPath := "definitions/nke-v1/environment-local.yaml"
	definition, err := LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Definition, *Environment)
		want   e.Code
	}{
		{"valid", func(_ *Definition, _ *Environment) {}, ""},
		{"unknown scanner", func(d *Definition, _ *Environment) { d.SBOMTool = "missing-scanner" }, e.CodeUnpinnedSource},
		{"wrong platform", func(_ *Definition, v *Environment) { v.UtilityPlatform = "linux/arm64" }, e.CodeEngineCapability},
		{"missing signer selection", func(d *Definition, v *Environment) {
			d.CosignTool = ""
			v.Signing = &Signing{Mode: "cosign", Identity: "builder", Issuer: "issuer"}
		}, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, v := definition, environment
			tc.change(&d, &v)
			plan, err := Compile(context.Background(), definitionPath, d, environmentPath, v, "test")
			if tc.want != "" {
				assertCode(t, err, tc.want)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.ExecutionSettings.Targets) != 3 || len(plan.ExecutionSettings.Routes) != 3 {
				t.Fatal("not all outputs routed")
			}
			for _, name := range []string{"cwe", "nke-helm", "nkectl"} {
				if plan.ExecutionSettings.Targets[name].Ref != "127.0.0.1:5000/nke/"+name {
					t.Fatalf("wrong target for %s", name)
				}
			}
			found := false
			for _, node := range plan.Graph.Nodes {
				if node.ID == "tool/cosign" {
					t.Fatal("dry-run acquired unused signer")
				}
				if node.ID == "tool/syft" {
					found = true
				}
			}
			if !found {
				t.Fatal("catalog scanner was not acquired")
			}
		})
	}
}

func TestEnvironmentRejectsRemovedAuthoringFields(t *testing.T) {
	for _, field := range []string{"tools: {}", "destinations: {}", "publish: {}", "sbomTool: syft"} {
		t.Run(field, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "environment.yaml")
			if err := os.WriteFile(p, []byte("apiVersion: nkd.dev/environment/v1\nkind: Environment\n"+field+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadEnvironment(t.Context(), p)
			assertCode(t, err, e.CodeInvalidCatalog)
		})
	}
}
