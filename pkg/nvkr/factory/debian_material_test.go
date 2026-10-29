// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// This checks the existing material transport with controlled bytes and producer
// descriptions. It does not build a package, run Packer or establish installation.
func TestDebianMaterialPlanRetainsOrdinaryAuthenticatedHandoff(t *testing.T) {
	t.Chdir(t.TempDir())
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	root := stack.Catalog.Components["image"]
	root.BuildCommand = nil
	root.Packer = &catalog.PackerConfig{ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory"}
	root.Materials = map[string]string{"package": "agent"}
	_, _, packages := rawDebianInputs(t)
	producer := packages.Catalog.Components["agent"]
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"agent": producer, "image": root}})
	if err != nil {
		t.Fatal(err)
	}
	definition.Outputs[0].Components = []string{"agent", "image"}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err = LoadPlanContext(t.Context(), planPath)
	if err != nil {
		t.Fatal("material Plan did not survive exact rederivation", err)
	}
	var consumer Node
	var instruction ir.Node
	count := 0
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.ID == "build-agent" {
			instruction = node
			count++
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.IRNode == "build-image" {
			consumer = node
		}
	}
	if count != 1 || instruction.OutputType != "host-package" || instruction.ArtifactPath != "out/agent.deb" {
		t.Fatal("ordinary image input gained a duplicate producer or changed artifact contract")
	}

	_, artifacts := planIndex(plan)
	payloadArtifact, resultArtifact := artifacts["artifact/node-image/agent"], artifacts["result/node-image/agent"]
	payload := []byte("controlled package material")
	for _, path := range []string{payloadArtifact.Path, resultArtifact.Path} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(payloadArtifact.Path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{Component: instruction.Component, OutputType: instruction.OutputType,
		Qualification: pipeline.ArtifactUnqualified, BuilderType: instruction.BuilderType, EngineClass: instruction.BuilderType,
		Toolchain: instruction.Toolchain, Path: instruction.ArtifactPath, InputDigest: "sha256:" + strings.Repeat("e", 64),
		SHA256: pipeline.FileDigest(payload), Size: int64(len(payload))}
	raw, err = canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultArtifact.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := observeVMPackerMaterials(t.Context(), plan, consumer)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.inputs) != 1 {
		t.Fatal("material handoff changed cardinality")
	}
	var copied bytes.Buffer
	if _, err := observed.inputs[0].Content.WriteToContext(t.Context(), &copied); err != nil || !bytes.Equal(copied.Bytes(), payload) {
		t.Fatal("material handoff changed package bytes", err)
	}
	if err := observed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadArtifact.Path, []byte("changed package"), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := observeVMPackerMaterials(t.Context(), plan, consumer); changed != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatal("changed package bytes bypassed existing producer-result authentication", err)
	}
}
