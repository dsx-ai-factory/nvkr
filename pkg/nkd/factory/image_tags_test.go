package factory

import (
	"path/filepath"
	"strings"
	"testing"

	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestImageTagsRequireExactAggregateBeforeCatalogIO(t *testing.T) {
	for _, tc := range []struct {
		name, output, format string
		aggregate            bool
	}{
		{"unknown", "missing", authored.OutputOCI, true},
		{"native image", "cwe", authored.OutputOCI, false},
		{"binary", "cwe", authored.OutputExecutable, false},
		{"chart", "cwe", authored.OutputHelm, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := authored.DefinitionOutput{Name: "cwe", Platform: "linux/amd64", Components: []string{"controller"}, Format: tc.format}
			if tc.aggregate {
				output.Platform = ""
				output.Platforms = []string{"linux/amd64", "linux/arm64"}
			}
			definition := authored.Definition{APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind, Metadata: authored.Metadata{Name: "fixture"}, Catalog: authored.Catalog{Base: "absent-catalog"}, Outputs: []authored.DefinitionOutput{output}}
			environment := authored.Environment{APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind, Metadata: authored.Metadata{Name: "fixture"}, UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64", Publication: &authored.Publication{RepositoryPrefix: "registry.example/team", Anonymous: true, ImageTags: map[string]string{tc.output: "candidate"}}}
			_, err := CompileDocuments(t.Context(), filepath.Join(t.TempDir(), "definition.yaml"), definition, "environment.yaml", environment, "v0.0.0 (test)")
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			if !strings.Contains(err.Error(), "imageTags") {
				t.Fatalf("tag admission did not precede catalog IO: %v", err)
			}
		})
	}
}

func TestImageTagsRemainTransportOnlyAndIndependentlyOwned(t *testing.T) {
	original := imageIndexTestPlan(t)
	environment := original.Environment.Clone()
	environment.Publication.ImageTags = map[string]string{"cwe": "candidate-two"}
	changed, err := CompileDocuments(t.Context(), original.DefinitionPath, original.Definition, original.EnvironmentPath, environment, original.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	if changed.EnvironmentDigest == original.EnvironmentDigest {
		t.Fatal("image tags absent from transport identity")
	}
	if changed.DefinitionDigest != original.DefinitionDigest {
		t.Fatal("image tags changed Definition identity")
	}
	for i, build := range original.Builds {
		if build.BuildDigest != changed.Builds[i].BuildDigest {
			t.Fatal("image tags changed native Build identity")
		}
	}
	for i, output := range original.LifecycleOutputs {
		if output.BuildDigest != changed.LifecycleOutputs[i].BuildDigest {
			t.Fatal("image tags changed output Build identity")
		}
	}
	environment.Publication.ImageTags["cwe"] = "caller-changed"
	if changed.Environment.Publication.ImageTags["cwe"] != "candidate-two" || changed.Targets["release"].ImageTags["cwe"] != "candidate-two" {
		t.Fatal("compiler retained caller map")
	}
	changed.Targets["release"].ImageTags["cwe"] = "target-changed"
	if changed.Environment.Publication.ImageTags["cwe"] != "candidate-two" {
		t.Fatal("target shares Environment map")
	}
	assertFactoryErrorCode(t, changed.Validate(), e.CodeUnverifiedRef)
}
