package factory

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// TestExecutePackageHelmAppliesValueOverrides exercises the full PackageHelm
// execute path (mirroring the plain source-archive case, e.g. postgres-chart)
// with Node.HelmValues set, and confirms the packaged chart's values.yaml
// reflects them end to end.
func TestExecutePackageHelmAppliesValueOverrides(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	chart := filepath.Join(root, "postgresql")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("apiVersion: v2\nname: postgresql\nversion: 17.1.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "values.yaml"), []byte("image:\n  repository: old\n  tag: dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "source.tar")
	if err := transportarchive.Pack(ctx, archive, root, []string{"postgresql"}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "packaged")
	node := Node{ID: "package/postgres-chart", Kind: NodePackage, PackageKind: PackageHelm,
		Inputs: []string{"artifact/postgres"}, Outputs: []string{"package/postgres-chart"},
		HelmValues: []HelmValueOverride{
			{JSONPath: ".image.registry", Value: "ghcr.io/example"},
			{JSONPath: ".image.tag", Value: "17.1.2"},
		}}
	plannedArtifacts := []Artifact{
		{ID: "artifact/postgres", Name: "postgres", Kind: "tarball", BuildTarget: "helm-test", Component: "postgres", Path: archive},
		{ID: "package/postgres-chart", Name: "postgres-chart", Kind: PackageHelm,
			MediaType: packageMediaType(PackageHelm), Path: output, Producer: node.ID},
	}
	artifacts := map[string]Artifact{plannedArtifacts[0].ID: plannedArtifacts[0], plannedArtifacts[1].ID: plannedArtifacts[1]}
	plan := &Plan{Graph: ArtifactGraph{Nodes: []Node{node}, Artifacts: plannedArtifacts}}
	if err := executePackage(ctx, plan, node, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}

	_, layer, err := delivery.ReadArtifact(ctx, output)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var valuesRaw []byte
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "postgresql/values.yaml" {
			valuesRaw, err = io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if valuesRaw == nil {
		t.Fatal("packaged chart has no values.yaml")
	}
	var values map[string]any
	if err := yaml.Unmarshal(valuesRaw, &values); err != nil {
		t.Fatal(err)
	}
	image, ok := values["image"].(map[string]any)
	if !ok || image["registry"] != "ghcr.io/example" || image["tag"] != "17.1.2" || image["repository"] != "old" {
		t.Fatalf("packaged values = %#v", values)
	}
}

func TestApplyHelmValueOverridesMutatesExistingAndNewKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("image:\n  repository: old\n  tag: dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides := []HelmValueOverride{
		{JSONPath: ".image.registry", Value: "ghcr.io/example"},
		{JSONPath: ".image.tag", Value: "17.1.2"},
	}
	if err := applyHelmValueOverrides(context.Background(), dir, dir, overrides); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	image, ok := values["image"].(map[string]any)
	if !ok || image["registry"] != "ghcr.io/example" || image["tag"] != "17.1.2" || image["repository"] != "old" {
		t.Fatalf("values = %#v", values)
	}
	if _, err := os.Lstat(filepath.Join(dir, "checksums.txt")); !os.IsNotExist(err) {
		t.Fatalf("checksums.txt should not be created when absent: %v", err)
	}
}

func TestApplyHelmValueOverridesRegeneratesExistingChecksums(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("image:\n  tag: dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyHelmValueOverrides(context.Background(), dir, dir, []HelmValueOverride{{JSONPath: ".image.tag", Value: "17.1.2"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "stale\n" {
		t.Fatalf("checksums.txt was not regenerated")
	}
}

func TestApplyHelmValueOverridesFailsClosedOnNonMappingSegment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("image: not-a-mapping\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := applyHelmValueOverrides(context.Background(), dir, dir, []HelmValueOverride{{JSONPath: ".image.tag", Value: "17.1.2"}})
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
}

func TestApplyHelmValueOverridesNoopWithoutOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := applyHelmValueOverrides(context.Background(), dir, dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "values.yaml")); !os.IsNotExist(err) {
		t.Fatalf("should not touch values.yaml when there are no overrides: %v", err)
	}
}
