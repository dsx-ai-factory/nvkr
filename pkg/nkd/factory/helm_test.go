package factory

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestSourceHelmArchiveBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []tar.Header
	}{
		{"extra root", []tar.Header{{Name: "other", Typeflag: tar.TypeDir, Mode: 0o755}}},
		{"root is file", []tar.Header{{Name: "helm", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"escaping symlink", []tar.Header{{Name: "helm", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "helm/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}}},
		{"duplicate directory", []tar.Header{{Name: "helm", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "helm", Typeflag: tar.TypeDir, Mode: 0o755}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "source.tar")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			writer := tar.NewWriter(file)
			for _, header := range tc.headers {
				if err := writer.WriteHeader(&header); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			root, _, cleanup, err := helmPackageTree(context.Background(), &Plan{}, Artifact{ID: "charts", Kind: "tarball", BuildTarget: "charts", Path: archive})
			defer cleanup()
			if root != "" || e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
				t.Fatalf("unsafe archive accepted: %q %v", root, err)
			}
		})
	}
}

func TestSourceHelmGNUTarDirectory(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "source.tar")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err := writer.WriteHeader(&tar.Header{Name: "helm/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	root, relative, cleanup, err := helmPackageTree(context.Background(), &Plan{}, Artifact{ID: "charts", Kind: "tarball", BuildTarget: "charts", Path: archive})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if info, err := os.Stat(filepath.Join(root, relative)); err != nil || !info.IsDir() {
		t.Fatalf("GNU directory missing: %v", err)
	}
}

func TestSourceHelmProducerObservation(t *testing.T) {
	for _, binding := range []bool{false, true} {
		name := "source only"
		if binding {
			name = "bound image"
		}
		t.Run(name, func(t *testing.T) { testSourceHelmProducerObservation(t, binding) })
	}
}

func testSourceHelmProducerObservation(t *testing.T, binding bool) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	chart := filepath.Join(root, "helm", "controller")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("apiVersion: v2\nname: controller\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "templates", "controller.yaml"), []byte(helmDeploymentFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "values.yaml"), []byte("image:\n  repository: old\n  tag: dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "source.tar")
	if err := pipeline.PackTransport(ctx, archive, root, []string{"helm"}); err != nil {
		t.Fatal(err)
	}
	digest, err := digestFactoryPath(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	record := pipeline.ArtifactResult{Component: "charts", SHA256: digest, Size: info.Size()}
	recordPath := filepath.Join(root, "result.json")
	writeRecord := func() {
		raw, marshalErr := canonical.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRecord()
	artifacts := map[string]Artifact{
		"source":         {ID: "source", Name: "charts", Kind: "tarball", Component: "charts", BuildTarget: "charts", Path: archive},
		"result":         {ID: "result", Name: "charts-result", Kind: "producer-result", Component: "charts", BuildTarget: "charts", Path: recordPath},
		"package/charts": {ID: "package/charts", Name: "charts", Kind: PackageHelm, Producer: "package/charts", MediaType: packageMediaType(PackageHelm), Path: filepath.Join(root, "packaged")},
	}
	node := Node{ID: "package/charts", Kind: NodePackage, PackageKind: PackageHelm, Inputs: []string{"source"}, Outputs: []string{"package/charts"}}
	buildDigest := "sha256:" + strings.Repeat("a", 64)
	plan := &Plan{Builds: []BuildPlan{{Name: "charts", BuildDigest: buildDigest, Platform: "linux/amd64"}}}
	if binding {
		binary := filepath.Join(root, "worker")
		if err := os.WriteFile(binary, []byte("fixture worker"), 0o755); err != nil {
			t.Fatal(err)
		}
		imagePath := filepath.Join(root, "image")
		if _, err := assemble.OCIImage(binary, "worker", 1, "amd64", imagePath); err != nil {
			t.Fatal(err)
		}
		artifacts["package/image"] = Artifact{ID: "package/image", Name: "image", Kind: PackageImage, Producer: "package/image", Path: imagePath}
		artifacts["rawimage"] = Artifact{ID: "rawimage", Kind: "oci-image", BuildTarget: "charts", Component: "image"}
		plan.Graph.Nodes = append(plan.Graph.Nodes, Node{ID: "package/image", Kind: NodePackage, PackageKind: PackageImage, Inputs: []string{"rawimage"}, Outputs: []string{"package/image"}})
		node.HelmImage = &HelmImageBinding{Image: "package/image", Chart: "controller"}
		node.Inputs = append(node.Inputs, "package/image")
	}
	plan.Graph.Nodes = append(plan.Graph.Nodes, node)
	for _, artifact := range artifacts {
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, artifact)
	}
	var log bytes.Buffer
	if err := executePackage(ctx, plan, node, artifacts, &log); err != nil {
		t.Fatal(err)
	}
	if binding {
		image, err := inspectFactoryImage(ctx, plan, artifacts["package/image"])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(log.String(), image.ManifestDigest) || !strings.Contains(log.String(), "checksums refreshed") {
			t.Fatalf("missing exact binding log: %s", &log)
		}
	} else if strings.Contains(log.String(), "Bound Helm") {
		t.Fatalf("claimed an image binding without an image: %s", &log)
	}
	expected := LifecycleOutputPlan{Name: "charts", Kind: "helm-oci", ArtifactID: "package/charts", BuildDigest: buildDigest, Components: []string{"charts"}}
	observed, err := ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.PayloadDescriptor(ctx, artifacts["package/charts"].Path)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Digest != inspection.Digest || len(observed.Components) != 1 || observed.Components[0] != "charts" {
		t.Fatalf("observation: %#v", observed)
	}
	record.SHA256 = "sha256:" + strings.Repeat("b", 64)
	writeRecord()
	_, err = ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
		t.Fatalf("substituted producer: %v", err)
	}
}
