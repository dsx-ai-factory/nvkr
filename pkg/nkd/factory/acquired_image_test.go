package factory

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func acquiredImagePlanFixture(t *testing.T, platform string) (*Plan, map[string]Artifact, pipeline.ArtifactResult) {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "worker")
	if err := os.WriteFile(binary, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	arch := "amd64"
	if platform == "linux/arm64" {
		arch = "arm64"
	}
	if _, err := assemble.OCIImage(binary, "worker", 1, arch, source); err != nil {
		t.Fatal(err)
	}
	image, err := delivery.InspectContainerImage(t.Context(), source, platform)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"worker": {
		Platforms: []string{platform}, Tag: "1.2.3", OutputType: "oci-image", Prebuilt: &catalog.Prebuilt{
			Image: "example.test/worker:1.2.3", Digests: map[string]string{arch: image.ManifestDigest}, Evidence: &catalog.ExternalEvidence{None: true},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	definition := authored.Definition{APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
		Metadata: authored.Metadata{Name: "acquired-image"}, Catalog: authored.Catalog{Base: "catalog"},
		Outputs: []authored.DefinitionOutput{{Name: "worker", Platform: platform, Format: authored.OutputOCI, Components: []string{"worker"}}}}
	environment := authored.Environment{APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind,
		Metadata: authored.Metadata{Name: "test"}, UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/amd64"}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(plan)
	input := artifacts["artifact/worker/worker"]
	if _, err := delivery.ImportAcquiredContainerImage(t.Context(), source, image.ManifestDigest, platform, input.Path); err != nil {
		t.Fatal(err)
	}
	var size int64
	if err := filepath.WalkDir(input.Path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			size += info.Size()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ref := "example.test/worker:1.2.3@" + image.ManifestDigest
	result := pipeline.ArtifactResult{Component: "worker", OutputType: "oci-image", SHA256: image.ManifestDigest, Size: size,
		Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire, Ref: ref, Path: ref, Packaged: true}
	writeAcquiredImageResult(t, artifacts["result/worker/worker"].Path, result)
	return plan, artifacts, result
}

func writeAcquiredImageResult(t *testing.T, path string, result pipeline.ArtifactResult) {
	t.Helper()
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryFile(t.Context(), path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAcquiredImagePackagePreservesNativeSubjectAndLifecycle(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan, artifacts, result := acquiredImagePlanFixture(t, platform)
			node := planNode(plan, "package/worker")
			if !containsString(node.Inputs, "result/worker/worker") {
				t.Fatal("compiler omitted acquired image's exact producer record")
			}
			if err := executePackage(t.Context(), plan, node, artifacts, io.Discard); err != nil {
				t.Fatal(err)
			}
			observed, err := ObserveLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], artifacts)
			if err != nil || observed.Digest != result.SHA256 {
				t.Fatalf("lifecycle relabeled acquired image: %#v, %v", observed, err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAcquiredImagePackageRejectsForgedProducerWithoutOutput(t *testing.T) {
	for _, testcase := range []struct {
		name   string
		change func(*pipeline.ArtifactResult)
	}{
		{name: "wrong bytes", change: func(result *pipeline.ArtifactResult) { result.Size++ }},
		{name: "wrong component", change: func(result *pipeline.ArtifactResult) { result.Component = "sibling" }},
		{name: "invented source lane", change: func(result *pipeline.ArtifactResult) { result.BuilderType = ir.BuilderVMImage }},
		{name: "invented qualification", change: func(result *pipeline.ArtifactResult) { result.Qualification = pipeline.ArtifactQualifiedExecution }},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan, artifacts, result := acquiredImagePlanFixture(t, "linux/amd64")
			testcase.change(&result)
			writeAcquiredImageResult(t, artifacts["result/worker/worker"].Path, result)
			node := planNode(plan, "package/worker")
			if err := executePackage(context.Background(), plan, node, artifacts, io.Discard); factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("error = %v, want %s", err, e.CodeVerifyFailed)
			}
			if _, err := os.Lstat(artifacts["package/worker"].Path); !os.IsNotExist(err) {
				t.Fatalf("forged producer exposed output: %v", err)
			}
		})
	}
}
