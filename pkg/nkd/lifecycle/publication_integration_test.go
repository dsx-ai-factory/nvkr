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
)

func TestPublicationEmbedsAuthenticatedOutputsAndContent(t *testing.T) {
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
	component := artifacts["artifact/cli/kubectl"]
	if err := os.MkdirAll(filepath.Dir(component.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("exact kubectl bytes")
	if err := os.WriteFile(component.Path, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	resultArtifact := artifacts["result/cli/kubectl"]
	if err := os.MkdirAll(filepath.Dir(resultArtifact.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	result := fixtureProducerResult(t, plan, component, component.Path)
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
	if err := factory.ExecuteNode(context.Background(), plan, "cli/sbom", nil, nil); err != nil {
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
	gotArtifacts, subject, err := inspectTestPublication(t, context.Background(), root, inspection.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.PayloadDescriptor(context.Background(), bundleArtifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArtifacts) != 1 || gotArtifacts[0].Name != "cli" || gotArtifacts[0].Digest != descriptor.Digest ||
		subject == "" {
		t.Fatalf("artifacts=%+v subject=%s", gotArtifacts, subject)
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
