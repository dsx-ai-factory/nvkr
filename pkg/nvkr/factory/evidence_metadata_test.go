// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func TestMetadataEvidenceGraphSeparatesPayloadAndSigningInputs(t *testing.T) {
	plan := referenceTestPlan(t)
	nodes, artifacts := planIndex(plan)
	for _, route := range plan.Routes {
		if route.Name == "release" {
			continue
		}
		t.Run(route.Name, func(t *testing.T) {
			payload := artifacts[route.Input]
			producer := nodes[payload.Producer]
			attester := nodes[NodeAttestEvidence+"/"+route.Name]
			publisher := nodes[NodePublish+"/"+route.Name]
			if payload.Kind != PackageOCI || producer.Kind != NodePackage ||
				!slices.Equal(producer.Outputs, uniqueSorted([]string{route.Input, preparedEvidenceMetadataID(route.Name)})) {
				t.Fatal("preparation must produce its original payload and bounded controls")
			}
			if !slices.Equal(attester.Inputs, uniqueSorted([]string{preparedEvidenceMetadataID(route.Name), "tool/" + plan.Definition.CosignTool})) ||
				!slices.Equal(attester.Outputs, []string{attestedEvidenceMetadataID(route.Name)}) {
				t.Fatal("SBOM signing still transports a prepared payload")
			}
			if !slices.Equal(publisher.Inputs, uniqueSorted([]string{route.Input, attestedEvidenceMetadataID(route.Name), "tool/" + plan.Definition.CosignTool})) {
				t.Fatal("publisher omits the original payload, signed controls, or pinned verifier")
			}
		})
	}
}

func TestMetadataEvidenceGraphRejectsMissingAndPayloadEdges(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(Route, map[string]Node, map[string]Artifact)
	}{
		{name: "payload sent to attester", edit: func(route Route, nodes map[string]Node, _ map[string]Artifact) {
			node := nodes[NodeAttestEvidence+"/"+route.Name]
			node.Inputs = uniqueSorted(append(node.Inputs, route.Input))
			nodes[node.ID] = node
		}},
		{name: "publisher omits controls", edit: func(route Route, nodes map[string]Node, _ map[string]Artifact) {
			node := nodes[NodePublish+"/"+route.Name]
			node.Inputs = []string{route.Input}
			nodes[node.ID] = node
		}},
		{name: "control from another producer", edit: func(route Route, _ map[string]Node, artifacts map[string]Artifact) {
			id := preparedEvidenceMetadataID(route.Name)
			artifact := artifacts[id]
			artifact.Producer = "foreign-producer"
			artifacts[id] = artifact
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := referenceTestPlan(t)
			nodes, artifacts := planIndex(plan)
			for _, route := range plan.Routes {
				if route.Name != "release" {
					tc.edit(route, nodes, artifacts)
					assertFactoryErrorCode(t, validateMetadataEvidenceGraph(plan, route, nodes, artifacts), e.CodePipelineDrift)
					break
				}
			}
		})
	}
}

func TestMetadataEvidenceAttestationNeedsNoPayloadPath(t *testing.T) {
	plan, original, signer := referenceMetadataFixture(t)
	nodes, _ := planIndex(plan)
	for _, route := range plan.Routes {
		if route.Name == "release" {
			continue
		}
		t.Run(route.Name, func(t *testing.T) {
			artifacts := maps.Clone(original)
			payload := artifacts[route.Input]
			payload.Path = filepath.Join(t.TempDir(), "absent-payload")
			artifacts[payload.ID] = payload
			out := artifacts[attestedEvidenceMetadataID(route.Name)]
			out.Path = filepath.Join(t.TempDir(), "attested-controls")
			artifacts[out.ID] = out
			if err := executeAttestEvidence(t.Context(), plan, nodes[NodeAttestEvidence+"/"+route.Name], artifacts, referenceSignerFactory(signer), io.Discard); err != nil {
				t.Fatal(err)
			}
			inspection, err := delivery.InspectMetadata(t.Context(), out.Path)
			if err != nil {
				t.Fatal(err)
			}
			if err := delivery.RequireAttestedEvidence(inspection); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMetadataEvidenceRejectsForeignControlsBeforeSigner(t *testing.T) {
	plan, original, signer := referenceMetadataFixture(t)
	for _, tc := range []struct {
		name        string
		foreignRun  bool
		foreignName bool
	}{
		{name: "other output", foreignName: true},
		{name: "other run", foreignRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifacts := maps.Clone(original)
			routeName := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
			node := planNode(plan, NodeAttestEvidence+"/"+routeName)
			if tc.foreignName {
				input := artifacts[preparedEvidenceMetadataID(routeName)]
				other := artifacts[preparedEvidenceMetadataID(outputPublicationRoute(plan.LifecycleOutputs[1].Name))]
				input.Path = other.Path
				artifacts[input.ID] = input
			}
			if tc.foreignRun {
				t.Setenv("GITHUB_RUN_ID", "99999")
			}
			output := artifacts[attestedEvidenceMetadataID(routeName)]
			output.Path = filepath.Join(t.TempDir(), "rejected-controls")
			artifacts[output.ID] = output
			called := false
			factory := referenceSignerFactory(signer)
			newSigner := func(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) (sign.OCISigner, error) {
				called = true
				return factory(ctx, plan, node, artifacts)
			}
			assertFactoryErrorCode(t, executeAttestEvidence(t.Context(), plan, node, artifacts, newSigner, io.Discard), e.CodeVerifyFailed)
			if called {
				t.Fatal("foreign controls reached signer construction")
			}
			if _, err := os.Lstat(output.Path); !os.IsNotExist(err) {
				t.Fatalf("rejected controls created output: %v", err)
			}
		})
	}
}
