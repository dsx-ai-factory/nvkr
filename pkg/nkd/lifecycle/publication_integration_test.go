package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestPublicationEmbedsAuthenticatedOutputsAndClusterRealization(t *testing.T) {
	plan, err := Compile(context.Background(), "definition.yaml", testDefinition(t), "environment.yaml", testEnvironment(), "test")
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	artifacts := map[string]factory.Artifact{}
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	component := artifacts["artifact/cli-recipe/kubectl"]
	if err := os.MkdirAll(filepath.Dir(component.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("exact kubectl bytes")
	if err := os.WriteFile(component.Path, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	resultArtifact := artifacts["result/cli-recipe/kubectl"]
	if err := os.MkdirAll(filepath.Dir(resultArtifact.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{Component: "kubectl", OutputType: "binary", SHA256: pipeline.FileDigest(payload), Size: int64(len(payload))}
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultArtifact.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	bundleArtifact := artifacts["package/cli"]
	_, err = delivery.BuildBinaryBundle(context.Background(), bundleArtifact.Path, delivery.BinaryBundleRequest{
		OS: "linux", Arch: "amd64", Purpose: "cli", Files: []delivery.BinaryFileInput{{Name: "kubectl", Path: component.Path,
			InstallPath: "/usr/local/bin/kubectl", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.ExecuteNode(context.Background(), plan, "composition/tenant", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := factory.ExecuteNode(context.Background(), plan, "cli-recipe/sbom", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := factory.ExecuteNode(context.Background(), plan, "package/release", nil, nil); err != nil {
		t.Fatal(err)
	}
	root := artifacts["package/release"].Path
	inspection, err := delivery.Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	gotArtifacts, subject, clusters, err := artifactsFromBinding(context.Background(), root, inspection)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.PayloadDescriptor(context.Background(), bundleArtifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArtifacts) != 1 || gotArtifacts[0].Name != "cli" || gotArtifacts[0].Digest != descriptor.Digest ||
		len(clusters) != 1 || clusters[0].Members[0].Role != "worker" || clusters[0].Members[0].ArtifactDigest != descriptor.Digest || subject == "" {
		t.Fatalf("artifacts=%+v subject=%s clusters=%+v", gotArtifacts, subject, clusters)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*delivery.Inspection)
	}{
		{"missing document", func(i *delivery.Inspection) {
			for n, o := range i.Root.Objects {
				if o.Kind == "sbom" {
					i.Root.Objects = append(i.Root.Objects[:n], i.Root.Objects[n+1:]...)
					return
				}
			}
		}},
		{"unbound evidence", func(i *delivery.Inspection) {
			for n, o := range i.Root.Objects {
				if o.Kind == "sbom" {
					i.Root.Objects[n].Evidence = nil
					return
				}
			}
		}},
		{"wrong evidence type", func(i *delivery.Inspection) {
			for n, o := range i.Root.Objects {
				if o.Kind == "sbom" {
					i.Root.Objects[n].ArtifactType = "application/json"
					return
				}
			}
		}},
		{"wrong semantic identity", func(i *delivery.Inspection) {
			for n, o := range i.Root.Objects {
				if o.Kind == "sbom" {
					i.Root.Objects[n].SemanticDigest = "sha256:" + repeat("7", 64)
					return
				}
			}
		}},
		{"duplicate document", func(i *delivery.Inspection) {
			for _, o := range i.Root.Objects {
				if o.Kind == "sbom" {
					i.Root.Objects = append(i.Root.Objects, o)
					return
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := inspection
			changed.Root.Objects = append([]delivery.PublishedObject(nil), inspection.Root.Objects...)
			tc.mutate(&changed)
			_, _, _, err := artifactsFromBinding(t.Context(), root, changed)
			assertCode(t, err, nkderr.CodeVerifyFailed)
		})
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	result.SHA256 = "sha256:" + repeat("9", 64)
	raw, err = canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultArtifact.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := factory.ExecuteNode(context.Background(), plan, "package/release", nil, nil); err == nil {
		t.Fatal("publication accepted a binary payload that differed from its producer result")
	} else {
		assertCode(t, err, nkderr.CodeVerifyFailed)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("failed publication left output: %v", err)
	}
}
