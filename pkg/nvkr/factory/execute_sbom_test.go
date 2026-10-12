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

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// sbomTestPlan compiles the explicit node-worker catalog used by the other compile tests
// use, so every resolved component carries real, SBOM-valid source identity.
func sbomTestPlan(t *testing.T) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, delivery := fixtureInputs("ai-factory", "node-worker", "ubuntu-24.04")
	delivery.Publication = &authored.Publication{RepositoryPrefix: "localhost:5000/nvkr", Anonymous: true, PlainHTTP: true}
	plan, err := Compile(context.Background(), definition, delivery, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestCompileEmitsPerBuildSBOMNodeGatedOnObservation(t *testing.T) {
	plan := sbomTestPlan(t)
	nodes, artifacts := planIndex(plan)

	node, ok := nodes["node-worker/sbom"]
	if !ok || node.Kind != NodeSBOM || node.Network != "none" {
		t.Fatalf("missing isolated per-build sbom node: %#v", node)
	}
	if !contains(node.Inputs, "observation/node-worker") {
		t.Fatalf("sbom node is not gated on the terminal observation: %#v", node.Inputs)
	}
	if !containsPrefix(node.Inputs, "result/node-worker/") {
		t.Fatalf("sbom node bills nothing it observed: %#v", node.Inputs)
	}
	out := artifacts[node.Outputs[0]]
	if out.ID != "sbom/node-worker" || out.Kind != "sbom-set" {
		t.Fatalf("sbom node output is not the per-build SBOM set: %#v", out)
	}

	// The plan must persist the resolved composition so the container-isolated
	// executor can build without the catalog (ADR-13).
	var billed bool
	for _, build := range plan.Builds {
		if build.Name == "node-worker" {
			billed = len(build.ResolvedComponents) > 0
		}
	}
	if !billed {
		t.Fatal("build plan persisted no resolved components for the sbom node")
	}
	if _, err := plan.Digest(); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteSBOMBillsEachBuiltComponentAgainstItsObservedDigest(t *testing.T) {
	plan := sbomTestPlan(t)
	t.Chdir(t.TempDir())
	nodes, artifacts := planIndex(plan)
	node := nodes["node-worker/sbom"]

	observed := "sha256:" + strings.Repeat("ab", 32)
	var expectComponents []string
	for _, input := range node.Inputs {
		artifact := artifacts[input]
		if artifact.Kind != "producer-result" {
			// The observation is only a scheduling gate; any admissible bytes suffice.
			if err := writeFactoryFile(context.Background(), artifact.Path, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		result := pipeline.ArtifactResult{Component: artifact.Component, OutputType: "binary",
			Path: artifact.Component, SHA256: observed}
		raw, err := canonical.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(context.Background(), artifact.Path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		expectComponents = append(expectComponents, artifact.Component)
	}
	if len(expectComponents) == 0 {
		t.Fatal("test fixture built no producer results")
	}

	if err := executeSBOM(context.Background(), plan, node, artifacts, io.Discard); err != nil {
		t.Fatalf("executeSBOM: %v", err)
	}

	dir := artifacts[node.Outputs[0]].Path
	rawIndex, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatalf("sbom index not written: %v", err)
	}
	var index sbom.Index
	if err := canonical.UnmarshalExact([]byte(strings.TrimRight(string(rawIndex), "\n")), &index); err != nil {
		t.Fatalf("decoding sbom index: %v", err)
	}
	if err := index.Validate(); err != nil {
		t.Fatalf("emitted sbom index is invalid: %v", err)
	}
	if index.BuildDigest == "" {
		t.Fatal("sbom index carries no build identity")
	}
	for _, entry := range index.Entries {
		if entry.ArtifactSHA != observed {
			t.Fatalf("component %q billed %q, want the observed digest %q", entry.Artifact, entry.ArtifactSHA, observed)
		}
		if _, err := os.Stat(filepath.Join(dir, entry.Artifact+".cdx.json")); err != nil {
			t.Fatalf("per-artifact SBOM missing for %q: %v", entry.Artifact, err)
		}
	}
	// A subject the run never observed must never acquire an SBOM.
	if len(index.Entries) > len(expectComponents) {
		t.Fatalf("index billed %d subjects but only %d were observed", len(index.Entries), len(expectComponents))
	}
}
