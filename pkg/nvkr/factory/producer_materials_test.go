// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func ordinaryMaterialPlan(t *testing.T) (*Plan, Node) {
	t.Helper()
	t.Chdir(t.TempDir())
	payload := []byte("runtime archive\n")
	components := map[string]catalog.Component{
		"composer": {
			Platforms: []string{"linux/amd64"}, FromSource: true,
			BuilderType: catalog.BuilderOCIHermetic, OutputType: "tarball",
			Repo: "https://example.test/composer", Tag: "v1.0.0",
			Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDate: 1,
			ToolchainImage: "example.test/composer@sha256:" + strings.Repeat("c", 64),
			BuildCommand:   []string{"sh", "compose.sh"}, WriteDir: "out",
			ArtifactPath: "out/image-inputs.tar.gz",
			Materials:    map[string]string{"runtime-archive": "runtime"},
		},
		"runtime": {
			Platforms: []string{"linux/amd64"}, OutputType: "tarball", Tag: "v1.0.0", SourceDate: 1,
			Prebuilt: &catalog.Prebuilt{
				URL:      "https://example.test/runtime-{{.Arch}}.tar.gz",
				Digests:  map[string]string{"amd64": pipeline.FileDigest(payload)},
				Evidence: &catalog.ExternalEvidence{None: true},
			},
		},
	}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: components})
	if err != nil {
		t.Fatal(err)
	}
	definition := authored.Definition{
		APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
		Metadata: authored.Metadata{Name: "ordinary-materials"}, Catalog: authored.Catalog{Base: "catalog"},
		Outputs: []authored.DefinitionOutput{{
			Name: "image-inputs", Platform: "linux/amd64", Format: authored.OutputExecutable,
			Components: []string{"composer", "runtime"},
		}},
	}
	environment := authored.Environment{
		APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind,
		Metadata: authored.Metadata{Name: "test"}, RunsOn: "ubuntu-24.04",
		UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/amd64",
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	var consumer Node
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeBuild && node.IRNode == "build-composer" {
			consumer = node
			break
		}
	}
	if consumer.ID == "" {
		t.Fatal("compiled Plan has no composer node")
	}
	_, artifacts := planIndex(plan)
	producer := ir.Node{}
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.ID == "pull-runtime" {
			producer = node
			break
		}
	}
	payloadArtifact := artifacts["artifact/image-inputs/runtime"]
	resultArtifact := artifacts["result/image-inputs/runtime"]
	if err := os.MkdirAll(filepath.Dir(payloadArtifact.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadArtifact.Path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(resultArtifact.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	record := pipeline.ArtifactResult{
		Component: producer.Component, OutputType: producer.OutputType,
		Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire,
		SHA256: producer.Digest, Size: int64(len(payload)),
	}
	raw, err := canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultArtifact.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return plan, consumer
}

func TestObserveOrdinaryProducerMaterialsUsesNativeContract(t *testing.T) {
	plan, consumer := ordinaryMaterialPlan(t)
	observed, err := observeBuildMaterials(t.Context(), plan, consumer)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	raw, err := observed.manifest.ReadAllContext(t.Context(), maxProducerManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	var manifest material.Manifest
	if err := canonical.UnmarshalExact(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != material.ManifestSchemaVersion || len(manifest.Materials) != 1 {
		t.Fatalf("ordinary producer manifest = %#v", manifest)
	}
	entry := manifest.Materials[0]
	if entry.Binding != "runtime-archive" || entry.Component != "runtime" || entry.Kind != "tarball" ||
		entry.Architecture != "amd64" || entry.Path != "materials/runtime-archive" {
		t.Fatalf("ordinary producer material = %#v", entry)
	}
}

func TestExecuteProducerPassesAuthenticatedOrdinaryMaterials(t *testing.T) {
	plan, consumer := ordinaryMaterialPlan(t)
	_, artifacts := planIndex(plan)
	_, instruction, err := plan.producerInstruction(consumer)
	if err != nil {
		t.Fatal(err)
	}
	planned := artifacts["artifact/"+consumer.BuildTarget+"/"+instruction.Component]
	called := false
	run := func(node ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
		called = true
		if options.BuildMaterials == nil || !options.Launch || len(node.Materials) != 1 {
			t.Fatalf("producer lost authenticated material handoff: node=%#v options=%#v", node, options)
		}
		return &pipeline.ArtifactResult{
			Component: node.Component, OutputType: node.OutputType, AbsPath: planned.Path,
			Qualification: pipeline.ArtifactUnqualified, SHA256: "sha256:" + strings.Repeat("f", 64),
		}, nil
	}
	if err := executeProducer(t.Context(), plan, consumer, "docker", 2, io.Discard, io.Discard, run, nil); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ordinary producer was not invoked")
	}
}

func TestObserveOrdinaryProducerMaterialsRejectsUnauthenticatedResult(t *testing.T) {
	plan, consumer := ordinaryMaterialPlan(t)
	_, artifacts := planIndex(plan)
	record := artifacts["result/"+consumer.BuildTarget+"/runtime"]
	payload := artifacts["artifact/"+consumer.BuildTarget+"/runtime"]
	forged := pipeline.ArtifactResult{
		Component: "runtime", OutputType: "tarball", Qualification: pipeline.ArtifactUnqualified,
		EngineClass: ir.EnginePrebuiltAcquire, SHA256: pipeline.FileDigest([]byte("runtime archive\n")),
		Size: int64(len([]byte("runtime archive\n"))),
	}
	raw, err := canonical.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(payload.Path); err != nil {
		t.Fatal(err)
	}
	observed, err := observeBuildMaterials(t.Context(), plan, consumer)
	if observed != nil {
		_ = observed.Close()
		t.Fatal("unauthenticated producer result returned material capabilities")
	}
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
		t.Fatalf("error = %v, want %s", err, e.CodeVerifyFailed)
	}
}
