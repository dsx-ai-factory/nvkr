// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
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
	if err := transportarchive.Pack(ctx, archive, layout, []string{"blobs", "index.json", "oci-layout"}); err != nil {
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
	record := pipeline.ArtifactResult{Component: "cwe", OutputType: "oci-image", SHA256: digest, Size: stat.Size()}
	raw, err := canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "result.json")
	if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts := map[string]Artifact{
		"artifact/build/cwe": {ID: "artifact/build/cwe", Name: "cwe", Kind: "oci-image", Component: "cwe", BuildTarget: "cwe", Path: archive},
		"result/build/cwe":   {ID: "result/build/cwe", Name: "cwe-result", Kind: "producer-result", Component: "cwe", BuildTarget: "cwe", Path: recordPath},
		"package/cwe":        {ID: "package/cwe", Name: "cwe", Kind: PackageImage, Producer: "package/cwe", MediaType: packageMediaType(PackageImage), Path: filepath.Join(root, "packaged")},
		"package/root":       {ID: "package/root", Name: "root", Kind: PackageOCI, Producer: "package/root", Path: filepath.Join(root, "publication")},
	}
	imageNode := Node{ID: "package/cwe", Kind: NodePackage, PackageKind: PackageImage,
		Inputs: []string{"artifact/build/cwe"}, Outputs: []string{"package/cwe"}}
	rootNode := Node{ID: "package/root", Kind: NodePackage, PackageKind: PackageOCI,
		Inputs: []string{"package/cwe"}, Outputs: []string{"package/root"}}
	buildDigest := "sha256:" + strings.Repeat("a", 64)
	plan := &Plan{Builds: []BuildPlan{{Name: "cwe", BuildDigest: buildDigest, Platform: "linux/amd64",
		Graph: &ir.Graph{Nodes: []ir.Node{{Kind: ir.KindBuild, Component: "cwe", OutputType: "oci-image"}}}}},
		Graph: ArtifactGraph{Nodes: []Node{imageNode, rootNode}}}
	for _, a := range artifacts {
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, a)
	}
	if err := executePackage(ctx, plan, imageNode, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	expected := LifecycleOutputPlan{Name: "cwe", Kind: "oci", ArtifactID: "package/cwe", BuildDigest: buildDigest, Components: []string{"cwe"}}
	observed, err := ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if err := executePackage(ctx, plan, rootNode, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	publication, err := delivery.Inspect(ctx, artifacts["package/root"].Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(publication.Root.Objects) != 1 {
		t.Fatalf("publication changed image membership: %#v", publication.Root.Objects)
	}
	image := publication.Root.Objects[0]
	if image.Name != "cwe" || image.SemanticDigest != observed.Digest || image.OS != "linux" || image.Arch != "amd64" {
		t.Fatalf("publication changed image binding: %#v", image)
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
