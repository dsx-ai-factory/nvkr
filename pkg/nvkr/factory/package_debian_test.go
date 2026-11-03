// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestRawDebianOriginalProducerThroughNormalPublicationPackage(t *testing.T) {
	d, environment, stack := rawDebianInputs(t)
	environment.Publication = &authored.Publication{RepositoryPrefix: "registry.example.test/packages", Anonymous: true}
	plan, err := Compile(t.Context(), d, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	nodes, artifacts := planIndex(plan)
	expected := plan.LifecycleOutputs[0]
	payload := artifacts[expected.ArtifactID]
	instruction, err := producerInstructionFor(plan, payload)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("original producer-created Debian carrier fixture; no installation claim")
	if err := os.MkdirAll(filepath.Dir(payload.Path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{Component: instruction.Component, OutputType: instruction.OutputType, Qualification: pipeline.ArtifactUnqualified, Toolchain: instruction.Toolchain, BuilderType: instruction.BuilderType, InputDigest: pipeline.FileDigest([]byte("original prepared source")), Path: instruction.ArtifactPath, SHA256: pipeline.FileDigest(raw), Size: int64(len(raw))}
	writeRawResult(t, artifacts["result/"+expected.Name+"/"+payload.Component].Path, result)
	if err := executeSBOM(t.Context(), plan, nodes[expected.Name+"/sbom"], artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	wrapper := nodes["package/"+expected.Name]
	if err := executePackage(t.Context(), plan, wrapper, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.InspectDebianArtifact(t.Context(), artifacts["package/"+expected.Name].Path, result.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Digest == result.SHA256 {
		t.Fatal("carrier digest replaced raw output identity")
	}
	publication := nodes["package/release"]
	if publication.ID == "" {
		t.Fatal("normal publication graph omitted aggregate package")
	}
	if err := executePackage(t.Context(), plan, publication, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.Inspect(t.Context(), artifacts["package/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := delivery.OpenPublication(t.Context(), artifacts["package/release"].Path, inspection.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	outputs, err := owner.Outputs()
	if err != nil || len(outputs) != 1 || outputs[0].Kind != PackageDebian || outputs[0].Digest != result.SHA256 {
		t.Fatalf("normal publication changed raw Debian subject: %+v %v", outputs, err)
	}
	// Package/result join is reauthenticated before output staging; an already
	// built sibling carrier cannot authorize substituted original producer bytes.
	if err := os.WriteFile(payload.Path, []byte("substituted producer"), 0600); err != nil {
		t.Fatal(err)
	}
	refused := artifacts["package/"+expected.Name]
	refused.Path = filepath.Join(t.TempDir(), "refused")
	artifacts[refused.ID] = refused
	err = executePackage(t.Context(), plan, wrapper, artifacts, io.Discard)
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
	if _, err := os.Lstat(refused.Path); !os.IsNotExist(err) {
		t.Fatal("changed original producer exposed a new carrier")
	}
}
