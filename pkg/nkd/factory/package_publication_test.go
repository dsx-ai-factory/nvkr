package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestPublicationObjectsPropagateDigestFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		missing  bool
		canceled bool
		wantErr  error
	}{
		{name: "regular payload"},
		{name: "missing payload", missing: true, wantErr: os.ErrNotExist},
		{name: "canceled directory digest", canceled: true, wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "payload")
			payload := []byte("publication payload")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				path = dir
				cancel()
			} else if !tc.missing {
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			output := Artifact{Path: filepath.Join(dir, "publication")}
			objects, cleanup, err := collectPublicationObjects(ctx, &Plan{}, output,
				[]Artifact{{Name: "payload", Kind: "record", Path: path}}, nil)
			if cleanup != nil {
				defer cleanup()
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("digest error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if objects != nil || cleanup != nil {
					t.Fatal("failed digest transferred publication objects")
				}
			} else if len(objects) != 1 || objects[0].SemanticDigest != pipeline.FileDigest(payload) {
				t.Fatalf("publication objects = %+v", objects)
			}
			if _, err := os.Stat(output.Path); !os.IsNotExist(err) {
				t.Fatalf("collection created output: %v", err)
			}
		})
	}
}

func TestPublicationMetadataUsesHelmRepository(t *testing.T) {
	for _, tc := range []struct {
		name       string
		override   string
		repository string
	}{
		{name: "chart name", repository: "controller"},
		{name: "repository override", override: "custom-controller", repository: "custom-controller"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			workspace := t.TempDir()
			canonicalTree := filepath.Join(".nkd", "factory", "aicr", "chart-output")
			treePath := filepath.Join(workspace, canonicalTree)
			if err := os.MkdirAll(treePath, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(treePath, "Chart.yaml"),
				[]byte("apiVersion: v2\nname: controller\nversion: 1.2.3\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			tree := Artifact{ID: "tree/chart-output", Name: "chart-output", Kind: "aicr-bundle", Path: treePath}
			outputPath := filepath.Join(workspace, ".nkd", "factory", "outputs", "chart-output")
			if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
				t.Fatal(err)
			}
			output := Artifact{ID: "package/chart-output", Name: "chart-output", Kind: PackageHelm,
				Producer: "package/chart-output", MediaType: packageMediaType(PackageHelm), Path: outputPath}
			packageNode := Node{ID: output.Producer, Kind: NodePackage, PackageKind: PackageHelm,
				HelmRepository: tc.override, Inputs: []string{tree.ID}, Outputs: []string{output.ID}}
			publicationNode := Node{ID: "package/release", Kind: NodePackage, PackageKind: PackageOCI,
				Inputs: []string{tree.ID, output.ID}}
			buildDigest := pipeline.FileDigest([]byte("chart build"))
			plannedTree := tree
			plannedTree.Path = canonicalTree
			plan := &Plan{
				Builds: []BuildPlan{{Name: output.Name, BuildDigest: buildDigest}},
				Graph:  ArtifactGraph{Nodes: []Node{packageNode, publicationNode}, Artifacts: []Artifact{plannedTree, output}},
				LifecycleOutputs: []LifecycleOutputPlan{{Name: output.Name, Kind: PackageHelm,
					ArtifactID: output.ID, BuildDigest: buildDigest}},
			}
			artifacts := map[string]Artifact{tree.ID: tree, output.ID: output}
			if err := executePackage(ctx, plan, packageNode, artifacts, io.Discard); err != nil {
				t.Fatal(err)
			}
			objects := []delivery.ObjectInput{{Name: output.Name, Kind: output.Kind, Path: output.Path}}
			metadata, err := bindPublicationMetadata(ctx, plan, publicationNode, artifacts, objects)
			if err != nil {
				t.Fatal(err)
			}
			if metadata == nil || objects[0].Output == nil {
				t.Fatal("Helm output has no publication binding")
			}
			if got := objects[0].Output; got.Repository != tc.repository || got.Format != PackageHelm || got.BuildDigest != buildDigest {
				t.Fatalf("Helm publication binding = %+v, want repository %q and build %q", got, tc.repository, buildDigest)
			}
		})
	}
}
