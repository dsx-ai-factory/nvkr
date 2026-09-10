package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/assemble"
	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/factory"
)

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
			artifact := Artifact{Name: "cwe", Kind: OutputOCI, Digest: image.RootDigest, RecipeDigest: "sha256:" + repeat("a", 64), Components: []string{"cwe"}}
			content, err := canonical.Digest(canonical.KindReleaseContent, releaseContent{Artifacts: []Artifact{artifact}})
			if err != nil {
				t.Fatal(err)
			}
			binding := factory.LifecycleBinding{SchemaVersion: factory.LifecycleBindingSchema,
				DefinitionDigest: "sha256:" + repeat("b", 64), EnvironmentDigest: "sha256:" + repeat("c", 64), ContentSubject: content,
				Outputs: []factory.LifecycleBoundOutput{{Name: artifact.Name, Kind: artifact.Kind, Digest: artifact.Digest, RecipeDigest: artifact.RecipeDigest, Components: artifact.Components}}}
			raw, err := canonical.Marshal(binding)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := canonical.Digest(canonical.KindLifecycleBinding, binding)
			if err != nil {
				t.Fatal(err)
			}
			layout := filepath.Join(root, "release")
			_, err = delivery.BuildPublicationRoot(ctx, layout, delivery.PublicationRequest{Objects: []delivery.ObjectInput{
				{Name: "cwe", Kind: factory.PackageImage, Path: imagePath, Layout: true, ArtifactType: delivery.MediaOCIManifest, SemanticDigest: image.RootDigest, BlobDigest: image.RootDigest, OS: "linux", Arch: tc.arch},
				{Name: "nkd-lifecycle", Kind: "lifecycle-binding", ArtifactType: "application/vnd.nkd.lifecycle-binding.v1+json", LayerMediaType: "application/vnd.nkd.lifecycle-binding.v1+json", SemanticDigest: digest, Bytes: raw},
			}})
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := delivery.Inspect(ctx, layout)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, subject, _, err := artifactsFromBinding(ctx, layout, inspection)
			if tc.reject {
				assertCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if subject != content || len(artifacts) != 1 || artifacts[0].Digest != image.RootDigest || artifacts[0].Kind != OutputOCI {
				t.Fatalf("image projection: %#v %s", artifacts, subject)
			}
		})
	}
}
