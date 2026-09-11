package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func TestCombinedBuildKitRuntimeAdmission(t *testing.T) {
	ctx := context.Background()
	t.Chdir(filepath.Join("..", "..", ".."))
	root := filepath.Join("definitions", "nke-v1")
	definitionPath := filepath.Join(root, "definition.yaml")
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(root, "environment-github.yaml")
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, runtime := range []string{"docker", "podman", ""} {
		t.Run("runtime="+runtime, func(t *testing.T) {
			environment.Runtime.Runtime = runtime
			plan, err := Compile(ctx, definitionPath, definition, environmentPath, environment, "v0.0.0 (test)")
			if runtime != "docker" {
				assertCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			seenReceipts := map[string]bool{}
			for _, artifact := range plan.Graph.Artifacts {
				if artifact.Kind != "source-receipt" {
					continue
				}
				if seenReceipts[artifact.Path] || !strings.HasSuffix(artifact.Path, ".json") {
					t.Fatalf("source receipt carrier overlaps another fetch: %s", artifact.Path)
				}
				seenReceipts[artifact.Path] = true
			}
			if len(seenReceipts) < 3 {
				t.Fatal("combined fixture must exercise independent fetches within one recipe")
			}
			bundle, err := factory.Render(ctx, plan, factory.EngineGitHub)
			if err != nil {
				t.Fatal(err)
			}
			workflow := string(bundle.Files[factory.GitHubWorkflowPath])
			if !strings.Contains(workflow, "exec-node --launch --runtime docker --node build-cwe") {
				t.Fatal("CWE launch does not select admitted Docker runtime")
			}
		})
	}
}

func TestLowerDerivesImageEnvelope(t *testing.T) {
	definition := testDefinition(t)
	definition.Outputs[0].Format = OutputOCI
	spec, _, err := lower(definition, testEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Packages) != 2 || spec.Packages[0].Kind != factory.PackageImage || spec.Packages[1].Kind != factory.PackageOCI {
		t.Fatalf("image packages: %#v", spec.Packages)
	}
	definition.Outputs[0].Components = []string{"first", "second"}
	assertCode(t, definition.Validate(), e.CodeInvalidCatalog)
}

func TestConsumerImageBinding(t *testing.T) {
	for _, tc := range []struct {
		name, arch string
		reject     bool
	}{
		{"exact platform", "amd64", false}, {"substituted platform", "arm64", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			binary := filepath.Join(root, "worker")
			if err := os.WriteFile(binary, []byte("fixture worker"), 0o755); err != nil {
				t.Fatal(err)
			}
			imagePath := filepath.Join(root, "image")
			if _, err := assemble.OCIImage(binary, "worker", 1, "amd64", imagePath); err != nil {
				t.Fatal(err)
			}
			image, err := delivery.InspectContainerImage(ctx, imagePath, "linux/amd64")
			if err != nil {
				t.Fatal(err)
			}
			artifact := Artifact{Name: "cwe", Kind: OutputOCI, Digest: image.ManifestDigest, RecipeDigest: "sha256:" + repeat("a", 64), Components: []string{"cwe"}}
			content, err := canonical.Digest(canonical.KindReleaseContent, releaseContent{Artifacts: []Artifact{artifact}})
			if err != nil {
				t.Fatal(err)
			}
			layout := filepath.Join(root, "release")
			request := testReleaseRequest(t, []Artifact{artifact}, []delivery.ObjectInput{{Name: "cwe", Kind: factory.PackageImage, Path: imagePath, Layout: true, ArtifactType: delivery.MediaOCIManifest, SemanticDigest: image.ManifestDigest, BlobDigest: image.RootDigest, OS: "linux", Arch: tc.arch}}, map[string]string{"cwe": image.RootDigest})
			_, err = delivery.BuildPublicationRoot(ctx, layout, request)
			if tc.reject {
				assertCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := delivery.Inspect(ctx, layout)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, subject, _, err := artifactsFromBinding(ctx, layout, inspection)
			if err != nil {
				t.Fatal(err)
			}
			if subject != content || len(artifacts) != 1 || artifacts[0].Digest != image.ManifestDigest || artifacts[0].Kind != OutputOCI {
				t.Fatalf("image projection: %#v %s", artifacts, subject)
			}
		})
	}
}
