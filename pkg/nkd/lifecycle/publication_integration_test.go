package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/factory"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
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
	bundle, err := delivery.BuildBinaryBundle(context.Background(), bundleArtifact.Path, delivery.BinaryBundleRequest{
		OS: "linux", Arch: "amd64", Purpose: "cli", Files: []delivery.BinaryFileInput{{Name: "kubectl", Path: component.Path,
			InstallPath: "/usr/local/bin/kubectl", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.ExecuteNode(context.Background(), plan, "composition/tenant", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := factory.ExecuteNode(context.Background(), plan, "package/cli-release", nil, nil); err != nil {
		t.Fatal(err)
	}
	root := artifacts["package/cli-release"].Path
	inspection, err := delivery.Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	gotArtifacts, subject, clusters, err := artifactsFromBinding(context.Background(), root, inspection)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArtifacts) != 1 || gotArtifacts[0].Name != "cli" || gotArtifacts[0].Digest != bundle.RootDigest ||
		len(clusters) != 1 || clusters[0].Members[0].Role != "worker" || clusters[0].Members[0].ArtifactDigest != bundle.RootDigest || subject == "" {
		t.Fatalf("artifacts=%+v subject=%s clusters=%+v", gotArtifacts, subject, clusters)
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
	if err := factory.ExecuteNode(context.Background(), plan, "package/cli-release", nil, nil); err == nil {
		t.Fatal("publication accepted a binary payload that differed from its producer result")
	} else {
		assertCode(t, err, nkderr.CodeVerifyFailed)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("failed publication left output: %v", err)
	}
}
