package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestImagePackageObservesProducerAndPreservesPublishedImage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	binary := filepath.Join(root, "worker")
	if err := os.WriteFile(binary, []byte("worker fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(root, "input")
	if _, err := assemble.OCIImage(binary, "worker", 1, "amd64", layout); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(layout, "image.tar")
	if err := pipeline.PackTransport(ctx, archive, layout, []string{"blobs", "index.json", "oci-layout"}); err != nil {
		t.Fatal(err)
	}
	digest, err := digestFactoryPath(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	record := pipeline.ArtifactResult{Component: "cwe", SHA256: digest, Size: stat.Size()}
	raw, err := canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "result.json")
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts := map[string]Artifact{
		"artifact/recipe/cwe": {ID: "artifact/recipe/cwe", Name: "cwe", Kind: "oci-image", Component: "cwe", RecipeTarget: "recipe", Path: archive},
		"result/recipe/cwe":   {ID: "result/recipe/cwe", Name: "cwe-result", Kind: "producer-result", Component: "cwe", RecipeTarget: "recipe", Path: recordPath},
		"package/cwe":         {ID: "package/cwe", Name: "cwe", Kind: PackageImage, Producer: "package/cwe", MediaType: packageMediaType(PackageImage), Path: filepath.Join(root, "packaged")},
		"package/root":        {ID: "package/root", Name: "root", Kind: PackageOCI, Producer: "package/root", Path: filepath.Join(root, "publication")},
	}
	imageNode := Node{ID: "package/cwe", Kind: NodePackage, PackageKind: PackageImage,
		Inputs: []string{"artifact/recipe/cwe"}, Outputs: []string{"package/cwe"}}
	rootNode := Node{ID: "package/root", Kind: NodePackage, PackageKind: PackageOCI,
		Inputs: []string{"package/cwe"}, Outputs: []string{"package/root"}}
	recipeDigest := "sha256:" + strings.Repeat("a", 64)
	plan := &Plan{Recipes: []RecipePlan{{Name: "recipe", RecipeDigest: recipeDigest, Criteria: criteria.Criteria{Arch: "amd64"}}},
		Graph: ArtifactGraph{Nodes: []Node{imageNode, rootNode}}}
	for _, a := range artifacts {
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, a)
	}
	if err := executePackage(ctx, plan, imageNode, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	expected := LifecycleOutputPlan{Name: "cwe", Kind: "oci", ArtifactID: "package/cwe", RecipeDigest: recipeDigest, Components: []string{"cwe"}}
	observed, err := ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if err := executePackage(ctx, plan, rootNode, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	image, err := delivery.InspectPublishedContainerImage(ctx, artifacts["package/root"].Path, "cwe")
	if err != nil {
		t.Fatal(err)
	}
	if image.ManifestDigest != observed.Digest || len(image.Entrypoint) != 1 || image.Entrypoint[0] != "/usr/local/bin/worker" {
		t.Fatalf("publication changed image: %#v", image)
	}
	record.SHA256 = "sha256:" + strings.Repeat("b", 64)
	raw, err = canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
		t.Fatalf("producer substitution error: %v", err)
	}
}
