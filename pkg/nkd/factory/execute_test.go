// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestPublicationRootEnclosesAPTRepositoryAsOCIArtifact(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("inputs", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("inputs/tool", []byte("tool-bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := &Plan{Builds: []BuildPlan{
		{
			Name: "node",
			Graph: &ir.Graph{Nodes: []ir.Node{
				{
					ID: "build-tool", Kind: ir.KindBuild, Component: "tool", Version: "1.0.0",
					Platform: "linux/amd64", Env: []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1733939955"}},
				},
			}},
		},
	}}
	artifacts := map[string]Artifact{
		"artifact/node/tool": {ID: "artifact/node/tool", Name: "tool", Kind: "host-package", Path: "inputs/tool", BuildTarget: "node", Component: "tool"},
		"package/worker-apt": {ID: "package/worker-apt", Name: "worker-apt", Kind: PackageAPT, MediaType: packageMediaType(PackageAPT), Path: "outputs/apt"},
		"package/root":       {ID: "package/root", Name: "root", Kind: PackageOCI, MediaType: packageMediaType(PackageOCI), Path: "outputs/root"},
	}
	aptNode := Node{ID: "package/worker-apt", Kind: NodePackage, PackageKind: PackageAPT,
		Distribution: "stable", PackageComponent: "main", Inputs: []string{"artifact/node/tool"}, Outputs: []string{"package/worker-apt"}}
	rootNode := Node{ID: "package/root", Kind: NodePackage, PackageKind: PackageOCI,
		Inputs: []string{"package/worker-apt"}, Outputs: []string{"package/root"}}
	plan.Graph = ArtifactGraph{Nodes: []Node{aptNode, rootNode}, Artifacts: []Artifact{
		artifacts["artifact/node/tool"], artifacts["package/worker-apt"], artifacts["package/root"],
	}}
	if err := executePackage(context.Background(), plan, aptNode, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := executePackage(context.Background(), plan, rootNode, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.Inspect(context.Background(), filepath.FromSlash("outputs/root"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Root.Objects) != 1 || inspection.Root.Objects[0].Kind != PackageAPT ||
		inspection.Root.Objects[0].Name != "worker-apt" {
		t.Fatalf("publication objects = %+v", inspection.Root.Objects)
	}
}

func TestHelmPackageIsStableAcrossWorkspaceRelocationAndReclaimsCarrier(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	chartStage := filepath.FromSlash(".nkd/factory/gpu/out")
	canonicalInput := filepath.FromSlash(".nkd/factory/gpu/out/chart.tar")
	canonicalOutput := filepath.FromSlash(".nkd/factory/packages/gpu-runtime")
	if err := os.MkdirAll(filepath.Join(chartStage, "helm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartStage, "helm", "Chart.yaml"), []byte("apiVersion: v2\nname: gpu\nversion: 1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := transportarchive.Pack(context.Background(), canonicalInput, chartStage, []string{"helm"}); err != nil {
		t.Fatal(err)
	}
	node := Node{ID: "package/gpu", Kind: NodePackage, PackageKind: PackageHelm,
		Inputs: []string{"artifact/chart"}, Outputs: []string{"package/gpu"}}
	plannedArtifacts := []Artifact{
		{ID: "artifact/chart", Name: "chart", Kind: "tarball", BuildTarget: "gpu", Path: filepath.ToSlash(canonicalInput)},
		{ID: "package/gpu", Name: "gpu-runtime", Kind: PackageHelm,
			MediaType: packageMediaType(PackageHelm), Path: filepath.ToSlash(canonicalOutput), Producer: node.ID},
	}
	plan := &Plan{Graph: ArtifactGraph{Nodes: []Node{node}, Artifacts: plannedArtifacts}}
	artifacts := map[string]Artifact{"artifact/chart": plannedArtifacts[0], "package/gpu": plannedArtifacts[1]}
	carrier := filepath.FromSlash(".nkd/factory/packages/.gpu-runtime.helm.tar")
	if err := os.MkdirAll(filepath.Dir(canonicalOutput), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(carrier, []byte("interrupted prior run"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executePackage(context.Background(), plan, node, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	first, err := delivery.PayloadDescriptor(context.Background(), canonicalOutput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(carrier); !os.IsNotExist(err) {
		t.Fatalf("retained internal carrier: %v", err)
	}

	relocatedRoot := filepath.FromSlash(".nkd/runs/attempt")
	if err := os.MkdirAll(filepath.Dir(relocatedRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.FromSlash(".nkd/factory"), relocatedRoot); err != nil {
		t.Fatal(err)
	}
	relocatedInput := filepath.Join(relocatedRoot, "gpu", "out", "chart.tar")
	relocatedOutput := filepath.Join(relocatedRoot, "packages", "gpu-runtime")
	if err := os.RemoveAll(relocatedOutput); err != nil {
		t.Fatal(err)
	}
	artifacts["artifact/chart"] = Artifact{ID: "artifact/chart", Name: "chart", Kind: "tarball", BuildTarget: "gpu", Path: relocatedInput}
	artifacts["package/gpu"] = Artifact{ID: "package/gpu", Name: "gpu-runtime", Kind: PackageHelm,
		MediaType: packageMediaType(PackageHelm), Path: relocatedOutput, Producer: node.ID}
	if err := executePackage(context.Background(), plan, node, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	second, err := delivery.PayloadDescriptor(context.Background(), relocatedOutput)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("Helm payload changed across workspace relocation: %s != %s", first.Digest, second.Digest)
	}
}
